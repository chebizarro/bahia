package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// assistantHealRelay wraps the test relay as the checkpoint store's publisher.
// While down it answers checkpoints of one revision like a lost connection,
// records every signed attempt, and can hold an attempt at a gate so a test
// observes what happens while a heal is in flight.
type assistantHealRelay struct {
	relay    *assistantTestRelay
	revision string
	mu       sync.Mutex
	down     bool
	gate     chan struct{}
	entered  chan struct{}
	attempts []string
}

func (h *assistantHealRelay) watches(ev nostr.Event) bool {
	return ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) && tagValue(ev.Tags, domain.AssistantCheckpointTagRevision) == h.revision
}

func (h *assistantHealRelay) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if h.watches(ev) {
		h.mu.Lock()
		h.attempts = append(h.attempts, ev.ID.Hex())
		gate, entered := h.gate, h.entered
		h.mu.Unlock()
		h.relay.touch()
		if gate != nil {
			entered <- struct{}{}
			<-gate
		}
		h.mu.Lock()
		down := h.down
		h.mu.Unlock()
		if down {
			return 0, errors.New("publishing to wss://relay.test: websocket: close 1006 (abnormal closure)")
		}
	}
	return h.relay.Publish(ctx, ev)
}

func (h *assistantHealRelay) setDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down = down
}

// hold makes the next watched publications block until the returned release
// is called; entered receives once per held publication.
func (h *assistantHealRelay) hold() (entered <-chan struct{}, release func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	gate := make(chan struct{})
	h.gate, h.entered = gate, make(chan struct{}, 64)
	var once sync.Once
	return h.entered, func() {
		once.Do(func() {
			h.mu.Lock()
			h.gate = nil
			h.mu.Unlock()
			close(gate)
		})
	}
}

func (h *assistantHealRelay) attemptIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.attempts...)
}

// assistantCheckpointIDsByRevision maps each revision of a run to the set of
// distinct checkpoint event IDs any relay accepted for it.
func assistantCheckpointIDsByRevision(relay *assistantTestRelay, runID string) map[uint64]map[string]bool {
	out := map[uint64]map[string]bool{}
	for _, ev := range relay.published(nostr.Kind(domain.AssistantExecutionCheckpointKind)) {
		if tagValue(ev.Tags, domain.AssistantCheckpointTagRun) != runID {
			continue
		}
		rev, _ := strconv.ParseUint(tagValue(ev.Tags, domain.AssistantCheckpointTagRevision), 10, 64)
		if out[rev] == nil {
			out[rev] = map[string]bool{}
		}
		out[rev][ev.ID.Hex()] = true
	}
	return out
}

// assertAssistantHealedChain proves the journal never forked and that the
// fenced revision was healed with the event signed before the outage.
func assertAssistantHealedChain(t *testing.T, st *assistantStack, relay *assistantTestRelay, heal *assistantHealRelay, sessionID, runID string) AssistantCheckpointHead {
	t.Helper()
	attempts := heal.attemptIDs()
	if len(attempts) == 0 {
		t.Fatal("fenced revision was never attempted")
	}
	for _, id := range attempts {
		if id != attempts[0] {
			t.Fatalf("fenced checkpoint was re-signed: attempts %v", attempts)
		}
	}
	byRevision := assistantCheckpointIDsByRevision(relay, runID)
	fencedRev, _ := strconv.ParseUint(heal.revision, 10, 64)
	if ids := byRevision[fencedRev]; len(ids) != 1 || !ids[attempts[0]] {
		t.Fatalf("revision %d accepted %v, want only the pending event %s", fencedRev, ids, attempts[0])
	}
	for rev, ids := range byRevision {
		if len(ids) != 1 {
			t.Fatalf("revision %d has %d distinct checkpoints", rev, len(ids))
		}
	}
	head, err := st.store.Load(context.Background(), sessionID, runID)
	if err != nil {
		t.Fatalf("healed chain does not load: %v", err)
	}
	if !head.Contains(attempts[0]) {
		t.Fatal("healed chain does not contain the pending checkpoint")
	}
	return head
}

func assistantOneReadPlan() domain.AssistantPlan {
	return domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
}

// fenceAssistantReservation approves a one-step batch whose dispatching
// reservation (revision 4) is lost with the relay, fencing the session.
func fenceAssistantReservation(t *testing.T, sessionID string) (*assistantStack, *assistantTestRelay, *assistantHealRelay, *assistantTestToolServer, string) {
	t.Helper()
	relay := newAssistantTestRelay()
	heal := &assistantHealRelay{relay: relay, revision: "4", down: true}
	server := newAssistantTestToolServer(relay.touch)
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantOneReadPlan()}, checkpointPublisher: heal})
	start := st.startBatch(t, sessionID)
	st.approve(t, sessionID, start)
	relay.waitFor(t, "fenced", func() bool { return st.faulted(sessionID) })
	if server.total() != 0 {
		t.Fatal("tool dispatched after its reservation was lost")
	}
	return st, relay, heal, server, start.Session.CurrentRunID
}

// Done-when: a relay reconnect re-publishes the identical pending signed
// checkpoint of a fenced session and resumes the run through Recover. While
// the relay is still down a reconnect signal changes nothing.
func TestAssistantExecutionReconnectHealsFencedSessionWithIdenticalCheckpoint(t *testing.T) {
	st, relay, heal, server, runID := fenceAssistantReservation(t, "s-heal")
	ctx := context.Background()

	if n := st.engine.HealFenced(ctx); n != 0 || !st.faulted("s-heal") || server.total() != 0 {
		t.Fatalf("heal while down: healed=%d fenced=%v calls=%d", n, st.faulted("s-heal"), server.total())
	}
	heal.setDown(false)
	if n := st.engine.HealFenced(ctx); n != 1 {
		t.Fatalf("healed %d sessions, want 1", n)
	}
	relay.waitFor(t, "healed run completes", func() bool { return st.snapshot("s-heal").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
	head := assertAssistantHealedChain(t, st, relay, heal, "s-heal", runID)
	if head.Execution.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("journal head phase %s", head.Execution.Phase)
	}
	if n := st.engine.HealFenced(ctx); n != 0 {
		t.Fatalf("a healthy session was healed again: %d", n)
	}
}

// A flapping relay and a reconnect storm: while one heal attempt is in
// flight every other reconnect returns at once instead of queueing on the
// session; the storm costs exactly one trailing attempt; nothing dispatches
// while fenced; the fence lifts only with the originally signed checkpoint.
func TestAssistantExecutionReconnectStormOnFlappingRelayHealsSingleFlight(t *testing.T) {
	st, relay, heal, server, runID := fenceAssistantReservation(t, "s-flap")
	ctx := context.Background()
	before := len(heal.attemptIDs())

	entered, release := heal.hold()
	first := make(chan int, 1)
	go func() { first <- st.engine.HealFenced(ctx) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("heal attempt did not reach the relay")
	}
	var storm sync.WaitGroup
	for range 32 {
		storm.Add(1)
		go func() {
			defer storm.Done()
			if n := st.engine.HealFenced(ctx); n != 0 {
				t.Errorf("a concurrent reconnect healed %d sessions", n)
			}
		}()
	}
	stormDone := make(chan struct{})
	go func() { storm.Wait(); close(stormDone) }()
	select {
	case <-stormDone:
	case <-time.After(10 * time.Second):
		t.Fatal("reconnects queued behind the in-flight heal")
	}
	release() // the relay drops again: the attempt and its one trailing retry fail
	if n := <-first; n != 0 || !st.faulted("s-flap") {
		t.Fatalf("heal against a dropped relay: healed=%d fenced=%v", n, st.faulted("s-flap"))
	}
	if got := len(heal.attemptIDs()) - before; got != 2 {
		t.Fatalf("storm of 33 reconnects made %d publication attempts, want 2 (one plus one trailing)", got)
	}
	if server.total() != 0 {
		t.Fatal("tool dispatched while fenced")
	}

	// Reconnects keep arriving while the relay keeps dropping; only the one
	// that finds it up heals.
	for _, down := range []bool{true, true, false} {
		heal.setDown(down)
		want := 0
		if !down {
			want = 1
		}
		if n := st.engine.HealFenced(ctx); n != want {
			t.Fatalf("down=%v healed %d sessions, want %d", down, n, want)
		}
		if !down {
			break
		}
		if server.total() != 0 || !st.faulted("s-flap") {
			t.Fatal("fence lifted or tool dispatched while the relay was down")
		}
	}
	relay.waitFor(t, "healed run completes", func() bool { return st.snapshot("s-flap").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
	assertAssistantHealedChain(t, st, relay, heal, "s-flap", runID)
}

// A reconnect racing an operator cancellation: whichever lifts the fence,
// the pending checkpoint is confirmed once, the cancellation follows it on
// the same chain, and the reserved tool runs at most once.
func TestAssistantExecutionReconnectRacingUserOperationKeepsOneChain(t *testing.T) {
	for i := range 12 {
		t.Run(fmt.Sprintf("race-%02d", i), func(t *testing.T) {
			sessionID := fmt.Sprintf("s-race-%02d", i)
			st, relay, heal, server, runID := fenceAssistantReservation(t, sessionID)
			heal.setDown(false)
			ctx := context.Background()
			barrier := make(chan struct{})
			var wg sync.WaitGroup
			var cancelErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-barrier
				st.engine.HealFenced(ctx)
			}()
			go func() {
				defer wg.Done()
				<-barrier
				_, cancelErr = st.engine.Cancel(ctx, assistantCancelRequest(sessionID, runID, "cancel-"+sessionID))
			}()
			close(barrier)
			wg.Wait()
			if cancelErr != nil {
				t.Fatalf("cancel racing reconnect: %v", cancelErr)
			}
			relay.waitFor(t, "cancelled", func() bool { return st.snapshot(sessionID).Phase == domain.AssistantExecutionCancelled })
			if n := server.count("read-one"); n > 1 {
				t.Fatalf("read-one dispatched %d times", n)
			}
			head := assertAssistantHealedChain(t, st, relay, heal, sessionID, runID)
			if head.Execution.Cancellation == nil || head.Execution.Phase != domain.AssistantExecutionCancelled {
				t.Fatalf("journal head %+v", head.Execution)
			}
		})
	}
}
