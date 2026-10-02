package soulfactory

import (
	"context"
	"sync"
)

// Per-soul operation serialization (bahia-irsry.38).
//
// Lifecycle actions (kind:1950) and fleet config reloads (kind:31953) both
// drive a soul's runtime and republish its kind:31951 read model. They run on
// different reactor handler shards, so without a shared gate a fleet reload
// and a lifecycle update for one soul could interleave: two runtime requests in
// flight at once, and the later read-modify-write of the soul overwriting the
// earlier one. soulOperationGate admits one operation per soul at a time. An
// operation keeps the soul for as long as it is parked on a runtime terminal
// result (runtimeResultWaiters), so a timeout does not let later work overtake
// it. Work that arrives while the soul is held is deferred and runs in arrival
// order once the holder finishes.
//
// The holder count is bounded by the number of souls: each soul has at most one
// holder, and a parked holder is one runtimeResultWaiters entry.

// soulOperation is one unit of work on a soul.
type soulOperation struct {
	// key identifies the work. A soul held by key, or with key already
	// deferred, does not take the same work again (replays and duplicate
	// deliveries collapse).
	key string
	// run performs the work while the soul is held and reports whether it
	// parked on a runtime terminal result. A parked operation keeps the soul;
	// its continuation calls soulOperationGate.release when it finishes.
	run func(ctx context.Context, hold *soulHold) (parked bool)
}

// soulHold is an operation's hold on one soul.
type soulHold struct {
	agentID string
	key     string
}

type soulOperationOutcome int

const (
	// soulOperationStarted: the soul was free and the operation now holds it.
	soulOperationStarted soulOperationOutcome = iota
	// soulOperationDeferred: the soul is held; the operation runs once the
	// work ahead of it finishes.
	soulOperationDeferred
	// soulOperationDuplicate: the same work already holds the soul or is
	// already deferred.
	soulOperationDuplicate
)

type soulOperationQueue struct {
	holder   *soulHold
	deferred []soulOperation
	// running is set while a goroutine is inside an operation's run for this
	// soul (do, runAcquired or a release). A parked operation's continuation
	// can release the soul on another goroutine before that run returns; new
	// work then queues, and the running goroutine starts it once its run has
	// returned, so two runs never overlap and nothing is left unstarted.
	running bool
}

// soulOperationGate serializes operations per soul. Deferred work runs inline
// on the goroutine that frees the soul, with that goroutine's context; the
// reactor runs every operation under its handler context.
type soulOperationGate struct {
	mu    sync.Mutex
	souls map[string]*soulOperationQueue
}

func newSoulOperationGate() *soulOperationGate {
	return &soulOperationGate{souls: make(map[string]*soulOperationQueue)}
}

// acquire takes agentID for now when the soul is free. Otherwise it defers
// later (which may differ from now: a fleet reload defers a re-drive to the
// latest revision, not the revision that found the soul busy). A started hold
// must be run with runAcquired.
func (g *soulOperationGate) acquire(agentID string, now, later soulOperation) (*soulHold, soulOperationOutcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	queue := g.souls[agentID]
	if queue == nil {
		queue = &soulOperationQueue{}
		g.souls[agentID] = queue
	}
	if queue.holder == nil && len(queue.deferred) == 0 && !queue.running {
		hold := &soulHold{agentID: agentID, key: now.key}
		queue.holder = hold
		queue.running = true
		return hold, soulOperationStarted
	}
	// Only the exact work holding the soul is a duplicate: a running fleet
	// re-drive may have read an older latest revision, so a newer one still
	// defers another re-drive behind it.
	if queue.holder != nil && queue.holder.key == now.key {
		return nil, soulOperationDuplicate
	}
	for _, queued := range queue.deferred {
		if queued.key == later.key {
			return nil, soulOperationDuplicate
		}
	}
	queue.deferred = append(queue.deferred, later)
	return nil, soulOperationDeferred
}

// do runs now on agentID when the soul is free, otherwise defers later.
func (g *soulOperationGate) do(ctx context.Context, agentID string, now, later soulOperation) soulOperationOutcome {
	hold, outcome := g.acquire(agentID, now, later)
	if outcome == soulOperationStarted {
		g.runAcquired(ctx, hold, now)
	}
	return outcome
}

// runAcquired runs op, which acquire started under hold, then the soul's
// deferred work in order, inline, until one parks (and keeps the soul) or none
// is left.
func (g *soulOperationGate) runAcquired(ctx context.Context, hold *soulHold, op soulOperation) {
	agentID := hold.agentID
	for {
		parked := op.run(ctx, hold)
		g.mu.Lock()
		queue := g.souls[agentID]
		if !parked && queue.holder == hold {
			queue.holder = nil
		}
		if queue.holder != nil || len(queue.deferred) == 0 {
			queue.running = false
			if queue.holder == nil {
				delete(g.souls, agentID)
			}
			g.mu.Unlock()
			return
		}
		op = queue.deferred[0]
		queue.deferred = queue.deferred[1:]
		hold = &soulHold{agentID: agentID, key: op.key}
		queue.holder = hold
		g.mu.Unlock()
	}
}

// release ends a parked operation's hold and runs the soul's deferred work,
// unless a goroutine is still inside a run for the soul, which then does. A
// stale or repeated release is a no-op.
func (g *soulOperationGate) release(ctx context.Context, hold *soulHold) {
	if hold == nil {
		return
	}
	g.mu.Lock()
	queue := g.souls[hold.agentID]
	if queue == nil || queue.holder != hold {
		g.mu.Unlock()
		return
	}
	queue.holder = nil
	if queue.running {
		g.mu.Unlock()
		return
	}
	if len(queue.deferred) == 0 {
		delete(g.souls, hold.agentID)
		g.mu.Unlock()
		return
	}
	next := queue.deferred[0]
	queue.deferred = queue.deferred[1:]
	nextHold := &soulHold{agentID: hold.agentID, key: next.key}
	queue.holder = nextHold
	queue.running = true
	g.mu.Unlock()
	g.runAcquired(ctx, nextHold, next)
}

// held reports whether agentID is held and how many operations wait behind it.
func (g *soulOperationGate) held(agentID string) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	queue := g.souls[agentID]
	if queue == nil {
		return false, 0
	}
	return queue.holder != nil, len(queue.deferred)
}

func lifecycleOperationKey(actionEventID string) string { return "action:" + actionEventID }

func fleetOperationKey(revisionEventID string) string { return "fleet:" + revisionEventID }

// fleetRedriveOperationKey names a deferred re-drive of a soul to the latest
// fleet revision; one pending re-drive covers every revision deferred behind it.
const fleetRedriveOperationKey = "fleet"

// forceRelease releases the soul held by agentID regardless of which
// operation holds it, and runs the soul's deferred work. It is the operator
// abandon mechanism: an authorized operator breaks the serialization hold a
// stuck awaiting_terminal operation keeps on the soul. A soul that is not
// held or has no holder is a no-op.
func (g *soulOperationGate) forceRelease(ctx context.Context, agentID string) bool {
	g.mu.Lock()
	queue := g.souls[agentID]
	if queue == nil || queue.holder == nil {
		g.mu.Unlock()
		return false
	}
	hold := queue.holder
	queue.holder = nil
	if queue.running {
		// A goroutine is still inside a run for this soul; it will start
		// deferred work when it returns. We cleared the holder; that is
		// enough.
		g.mu.Unlock()
		return true
	}
	if len(queue.deferred) == 0 {
		delete(g.souls, agentID)
		g.mu.Unlock()
		return true
	}
	next := queue.deferred[0]
	queue.deferred = queue.deferred[1:]
	nextHold := &soulHold{agentID: hold.agentID, key: next.key}
	queue.holder = nextHold
	queue.running = true
	g.mu.Unlock()
	g.runAcquired(ctx, nextHold, next)
	return true
}
