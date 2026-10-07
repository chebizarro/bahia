package soulfactory

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Rebuilding parked operations after a restart.
//
// A parked continuation lives in memory, but the operation it continues is on
// the relays: parking publishes a kind:6950 awaiting_terminal progress event
// tagged t=awaitingTerminalTopic, and the operation ends with a kind:7950 (or
// compatibility 1951) terminal result for the same request. An awaiting_terminal
// event without a matching terminal result is an operation still outstanding.
//
// At startup the reactor finds those, holds their souls again (in the order
// they parked) and re-drives them with the same runtime idempotency keys, so
// the runtime replays a result it already produced. Because the souls are held
// before the backlog is handled, later actions and fleet revisions for them
// wait behind the re-driven operation, as they did before the restart, instead
// of overtaking it. Without the rebuild the backlog (newest first) could run a
// soul's later action first, and an action outside the backlog's limit would
// never be re-driven.
//
// Re-driving is safe on a partial view: a lifecycle action that already has a
// terminal result is skipped by its own terminal-result check, and a fleet
// re-drive re-checks the soul's applied revision.

// awaitingTerminalTopic is the "t" tag of every awaiting_terminal progress
// event, so one #t REQ finds them (relays index single-letter tags only).
const awaitingTerminalTopic = "soulfactory-awaiting-terminal"

const (
	// reactorParkedRebuildLimit bounds the awaiting_terminal events read at
	// startup (newest first).
	reactorParkedRebuildLimit = 500
	// parkedRebuildIDsPerFilter bounds the event ids in one rebuild REQ.
	parkedRebuildIDsPerFilter = 100
)

func awaitingTerminalTags() nostr.Tags {
	return nostr.Tags{{tagTopic, awaitingTerminalTopic}}
}

// outstandingOperation is one awaiting_terminal operation read from relays.
type outstandingOperation struct {
	requestKind int
	// request is the kind:1950 action id, or the fleet revision id.
	request   string
	agentID   string
	createdAt nostr.Timestamp
}

func (o outstandingOperation) fleet() bool { return o.requestKind == domain.KindSoulFleetConfig }

// rebuiltOperation is an outstanding operation ready to hold its soul again.
type rebuiltOperation struct {
	agentID string
	op      soulOperation
}

// rebuildParkedOperations reads the operations a previous run left awaiting a
// runtime terminal result, oldest first. It never fails startup: an incomplete
// read is logged and rebuilt from what arrived, and the backlog still re-drives
// anything missed, as before.
func (r *Reactor) rebuildParkedOperations(ctx context.Context) []rebuiltOperation {
	relayClient := r.relayClient
	factoryHex := strings.TrimSpace(r.config.SoulFactoryPubkey)
	if relayClient == nil || factoryHex == "" {
		return nil
	}
	factory, err := nostr.PubKeyFromHex(factoryHex)
	if err != nil {
		return nil
	}
	logger := r.logger.With("caller", "reactor.rebuild_parked")
	query := func(filters []nostr.Filter) ([]*nostr.Event, bool) {
		events, err := relayClient.Query(ctx, filters)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			logger.Warn("parked operation rebuild read incomplete; rebuilding from the events received", "error", err)
		}
		return events, true
	}

	awaiting, ok := query([]nostr.Filter{{
		Kinds:   []nostr.Kind{lifecycleProgressKind},
		Authors: []nostr.PubKey{factory},
		Tags:    nostr.TagMap{tagTopic: []string{awaitingTerminalTopic}},
		Limit:   reactorParkedRebuildLimit,
	}})
	if !ok {
		return nil
	}
	outstanding := outstandingFromAwaiting(awaiting, factory)
	if len(outstanding) == 0 {
		return nil
	}

	requests := make([]string, 0, len(outstanding))
	for _, op := range outstanding {
		if !slices.Contains(requests, op.request) {
			requests = append(requests, op.request)
		}
	}
	var results []*nostr.Event
	for chunk := range slices.Chunk(requests, parkedRebuildIDsPerFilter) {
		events, ok := query([]nostr.Filter{{
			Kinds:   lifecycleTerminalResultKinds,
			Authors: []nostr.PubKey{factory},
			Tags:    nostr.TagMap{tagEvent: chunk},
		}})
		if !ok {
			return nil
		}
		results = append(results, events...)
	}
	outstanding = withoutTerminalResults(outstanding, results, factory)

	var actionIDs []nostr.ID
	for _, op := range outstanding {
		if op.fleet() {
			continue
		}
		if id, err := nostr.IDFromHex(op.request); err == nil {
			actionIDs = append(actionIDs, id)
		}
	}
	actions := make(map[string]*nostr.Event, len(actionIDs))
	for chunk := range slices.Chunk(actionIDs, parkedRebuildIDsPerFilter) {
		events, ok := query([]nostr.Filter{{IDs: chunk, Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulAction)}}})
		if !ok {
			return nil
		}
		for _, event := range events {
			if event != nil && event.Kind == nostr.Kind(domain.KindSoulAction) && slices.Contains(chunk, event.ID) && validSignedEvent(event) {
				actions[event.ID.Hex()] = event
			}
		}
	}

	var rebuilt []rebuiltOperation
	fleetSouls := map[string]bool{}
	for _, op := range outstanding {
		if op.fleet() {
			if !r.config.FleetConfigEnabled || fleetSouls[op.agentID] {
				continue
			}
			fleetSouls[op.agentID] = true
			rebuilt = append(rebuilt, rebuiltOperation{agentID: op.agentID, op: r.fleetReconciler().redriveOperation(op.agentID)})
			continue
		}
		event := actions[op.request]
		if event == nil {
			logger.Warn("awaiting_terminal lifecycle action not found on the relays; it is not re-driven", "event_id", op.request, "agent_id", op.agentID)
			continue
		}
		action, err := ParseSoulActionEvent(event)
		if err != nil {
			logger.Warn("awaiting_terminal lifecycle action does not parse; it is not re-driven", "event_id", op.request, "error", err)
			continue
		}
		agentID := normalizeSoulLookupRef(action.SoulRef)
		if agentID == "" {
			continue
		}
		rebuilt = append(rebuilt, rebuiltOperation{agentID: agentID, op: r.lifecycle().deferredAction(event, action)})
	}
	if len(rebuilt) > 0 {
		logger.Info("rebuilt operations left awaiting a runtime terminal result; re-driving them", "operations", len(rebuilt))
	}
	return rebuilt
}

// outstandingFromAwaiting returns the distinct operations named by
// awaiting_terminal progress events, oldest first.
func outstandingFromAwaiting(events []*nostr.Event, factory nostr.PubKey) []outstandingOperation {
	byKey := map[string]outstandingOperation{}
	for _, event := range events {
		if event == nil || event.PubKey != factory || event.Kind != lifecycleProgressKind ||
			tagValue(event.Tags, tagStatus) != actionStatusAwaitingTerminal {
			continue
		}
		requestKind, err := strconv.Atoi(tagValue(event.Tags, tagRequestKind))
		if err != nil || (requestKind != domain.KindSoulAction && requestKind != domain.KindSoulFleetConfig) {
			continue
		}
		op := outstandingOperation{
			requestKind: requestKind,
			request:     tagValue(event.Tags, tagEvent),
			agentID:     tagValue(event.Tags, tagAgentID),
			createdAt:   event.CreatedAt,
		}
		if op.request == "" || (op.fleet() && op.agentID == "") {
			continue
		}
		key := op.key()
		if existing, seen := byKey[key]; !seen || op.createdAt < existing.createdAt {
			byKey[key] = op
		}
	}
	out := make([]outstandingOperation, 0, len(byKey))
	for _, op := range byKey {
		out = append(out, op)
	}
	slices.SortFunc(out, func(a, b outstandingOperation) int {
		return cmp.Or(cmp.Compare(a.createdAt, b.createdAt), strings.Compare(a.key(), b.key()))
	})
	return out
}

// key identifies the operation: a lifecycle action by its event, a fleet
// apply by its revision and soul (one revision fans out to many souls).
func (o outstandingOperation) key() string {
	if o.fleet() {
		return "fleet:" + o.request + ":" + o.agentID
	}
	return "action:" + o.request
}

// withoutTerminalResults drops the operations that have a terminal result.
func withoutTerminalResults(ops []outstandingOperation, results []*nostr.Event, factory nostr.PubKey) []outstandingOperation {
	terminal := map[string]bool{}
	for _, result := range results {
		if result == nil || result.PubKey != factory || !domain.IsLifecycleResultKind(int(result.Kind)) {
			continue
		}
		requestKind, _ := strconv.Atoi(tagValue(result.Tags, tagRequestKind))
		done := outstandingOperation{requestKind: requestKind, request: tagValue(result.Tags, tagEvent), agentID: tagValue(result.Tags, tagAgentID)}
		terminal[done.key()] = true
	}
	return slices.DeleteFunc(ops, func(op outstandingOperation) bool { return terminal[op.key()] })
}

// startRebuiltOperations holds each rebuilt operation's soul, in order, and
// re-drives the ones that got their soul on a bounded set of goroutines
// tracked by wg; an operation for a soul already held by an earlier one is
// deferred behind it. Holds are taken before this returns, so the backlog
// handled afterwards defers behind them.
func (r *Reactor) startRebuiltOperations(ctx context.Context, wg *sync.WaitGroup, rebuilt []rebuiltOperation) {
	type started struct {
		hold *soulHold
		op   soulOperation
	}
	gate := r.soulOperations()
	var ready []started
	for _, item := range rebuilt {
		if hold, outcome := gate.acquire(item.agentID, item.op, item.op); outcome == soulOperationStarted {
			ready = append(ready, started{hold: hold, op: item.op})
		}
	}
	if len(ready) == 0 {
		return
	}
	work := make(chan started, len(ready))
	for _, item := range ready {
		work <- item
	}
	close(work)
	for range min(reactorHandlerWorkers, len(ready)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				gate.runAcquired(ctx, item.hold, item.op)
			}
		}()
	}
}
