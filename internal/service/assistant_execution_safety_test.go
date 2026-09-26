package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Checkpoint failure matrix

type assistantEffectEntry struct {
	kind        string // checkpoint | call | reject | heal
	tool        string
	dispatching []string
}

type assistantEffectLog struct {
	mu      sync.Mutex
	entries []assistantEffectEntry
}

func (l *assistantEffectLog) add(e assistantEffectEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

// verify proves every provider call follows an accepted checkpoint that holds
// that tool's work in dispatching (one call per reservation), and that no call
// happens while a rejected checkpoint fences the session.
func (l *assistantEffectLog) verify(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	reserved := map[string]bool{}
	fenced := false
	for i, e := range l.entries {
		switch e.kind {
		case "checkpoint":
			for _, tool := range e.dispatching {
				reserved[tool] = true
			}
		case "reject":
			fenced = true
		case "heal":
			fenced = false
		case "call":
			if fenced {
				t.Fatalf("entry %d: %s called while the session was fenced", i, e.tool)
			}
			if !reserved[e.tool] {
				t.Fatalf("entry %d: %s called without an accepted dispatching checkpoint", i, e.tool)
			}
			reserved[e.tool] = false
		}
	}
}

type assistantFlakyStore struct {
	mu       sync.Mutex
	failRev  uint64
	failing  bool
	hit      bool
	accepted int
	heads    map[string]string
	chains   map[string][]string
	latest   map[string]domain.AssistantExecution
	log      *assistantEffectLog
	changed  chan struct{}
}

func newAssistantFlakyStore(failRev uint64, log *assistantEffectLog) *assistantFlakyStore {
	return &assistantFlakyStore{failRev: failRev, failing: failRev > 0, heads: map[string]string{}, chains: map[string][]string{}, latest: map[string]domain.AssistantExecution{}, log: log, changed: make(chan struct{})}
}

func (s *assistantFlakyStore) touchLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *assistantFlakyStore) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchLocked()
}

func (s *assistantFlakyStore) Append(_ context.Context, x domain.AssistantExecution, previous string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.touchLocked()
	if s.failing && x.Revision == s.failRev {
		if !s.hit {
			s.log.add(assistantEffectEntry{kind: "reject"})
		}
		s.hit = true
		return "", errors.New("relay rejected checkpoint: rate-limited")
	}
	key := x.SessionID + "/" + x.RunID
	if previous != s.heads[key] {
		return "", fmt.Errorf("predecessor mismatch: %q != %q", previous, s.heads[key])
	}
	clone, err := x.Clone()
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s-%d", x.RunID, x.Revision)
	s.heads[key] = id
	s.chains[key] = append(s.chains[key], id)
	s.latest[key] = clone
	s.accepted++
	entry := assistantEffectEntry{kind: "checkpoint"}
	for _, w := range clone.Work {
		if w.State == domain.AssistantWorkDispatching {
			entry.dispatching = append(entry.dispatching, w.ToolName)
		}
	}
	s.log.add(entry)
	return id, nil
}

func (s *assistantFlakyStore) Load(_ context.Context, sessionID, runID string) (AssistantCheckpointHead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + "/" + runID
	x, ok := s.latest[key]
	if !ok {
		return AssistantCheckpointHead{}, ErrAssistantCheckpointNotFound
	}
	clone, err := x.Clone()
	return AssistantCheckpointHead{Execution: clone, EventID: s.heads[key], Chain: append([]string(nil), s.chains[key]...)}, err
}

func (s *assistantFlakyStore) state() (hit, failing bool, accepted int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hit, s.failing, s.accepted
}

func (s *assistantFlakyStore) stopFailing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = false
	s.touchLocked()
}

func (s *assistantFlakyStore) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		if cond() {
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// assistantGateObserver returns a terminal outcome only when the test releases
// it. Each parked ObserveWork call owns a one-slot channel; release removes a
// slot and fills it in one critical section, so pending reports exactly the
// calls that can still take an outcome, the send never waits, and no call is
// released twice.
type assistantGateObserver struct {
	mu     sync.Mutex
	parked []chan AssistantAsyncObservationOutcome
	notify func()
}

func (o *assistantGateObserver) ObserveWork(ctx context.Context, _ AssistantWorkObservationRequest) (AssistantAsyncObservationOutcome, error) {
	slot := make(chan AssistantAsyncObservationOutcome, 1)
	o.mu.Lock()
	o.parked = append(o.parked, slot)
	o.mu.Unlock()
	o.notify()
	select {
	case out := <-slot:
		return out, nil
	case <-ctx.Done():
		o.mu.Lock()
		for i, parked := range o.parked {
			if parked == slot {
				o.parked = append(o.parked[:i], o.parked[i+1:]...)
				break
			}
		}
		o.mu.Unlock()
		o.notify()
		return AssistantAsyncObservationOutcome{Status: "blocked"}, ctx.Err()
	}
}

func (o *assistantGateObserver) pending() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.parked) > 0
}

// release hands out to the oldest parked observation.
func (o *assistantGateObserver) release(t *testing.T, out AssistantAsyncObservationOutcome) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.parked) == 0 {
		t.Fatal("release with no parked observation")
	}
	slot := o.parked[0]
	o.parked = o.parked[1:]
	slot <- out
}

func runAssistantCheckpointBoundary(t *testing.T, failRev uint64) int {
	t.Helper()
	log := &assistantEffectLog{}
	store := newAssistantFlakyStore(failRev, log)
	server := newAssistantTestToolServer(store.touch)
	server.onCall = func(tool string) { log.add(assistantEffectEntry{kind: "call", tool: tool}) }
	observer := &assistantGateObserver{notify: store.touch}
	lifecycle, cancel := context.WithCancel(context.Background())
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantTestRuntime(server, nil, nil), Observer: observer, Batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}, Lifecycle: lifecycle, Now: assistantTestClock, NewID: func(prefix string) string { return prefix + "-fixed" }})
	defer func() { cancel(); engine.Wait() }()
	ctx := context.Background()
	ref := AssistantExecutionReference{SessionID: "s", RunID: "run-fixed"}
	start := func(requestID string) error {
		_, err := engine.StartTurn(ctx, AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s", TurnID: "turn", Prompt: "go"}, OperatorPubkey: "operator", RequestEventID: requestID, DefaultWorkflow: domain.AssistantWorkflowBatch})
		return err
	}
	healed := false
	heal := func() {
		t.Helper()
		if failRev > 1 {
			if err := engine.Recover(ctx, ref); !errors.Is(err, ErrAssistantCheckpointUnconfirmed) {
				t.Fatalf("fence lifted while the relay still rejects: %v", err)
			}
			if _, err := engine.Cancel(ctx, assistantCancelRequest("s", "run-fixed", "cancel-probe")); !errors.Is(err, ErrAssistantCheckpointUnconfirmed) {
				t.Fatalf("cancellation recorded without a confirmed checkpoint: %v", err)
			}
		}
		log.add(assistantEffectEntry{kind: "heal"})
		store.stopFailing()
		healed = true
		if failRev > 1 {
			if err := engine.Recover(ctx, ref); err != nil {
				t.Fatalf("heal: %v", err)
			}
		}
	}
	if err := start("prompt-1"); err != nil {
		if !errors.Is(err, ErrAssistantCheckpointUnconfirmed) {
			t.Fatal(err)
		}
		heal()
		if failRev == 1 {
			if err := start("prompt-2"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if x, _ := engine.Snapshot("s"); x.Phase == domain.AssistantExecutionAwaitingApproval {
		p := x.Proposal
		_, err := engine.Decide(ctx, AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s", RunID: x.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approve"})
		if err != nil {
			if !errors.Is(err, ErrAssistantCheckpointUnconfirmed) {
				t.Fatal(err)
			}
			heal()
		}
	}
	for {
		store.waitFor(t, "progress", func() bool {
			x, _ := engine.Snapshot("s")
			hit, _, _ := store.state()
			return x.Phase == domain.AssistantExecutionCompleted || (hit && !healed) || observer.pending()
		})
		if x, _ := engine.Snapshot("s"); x.Phase == domain.AssistantExecutionCompleted {
			break
		}
		if hit, _, _ := store.state(); hit && !healed {
			heal()
			continue
		}
		// Reached only when the wait observed a parked call; only release
		// removes one before shutdown, so the call is still parked.
		observer.release(t, AssistantAsyncObservationOutcome{Status: "completed"})
	}
	for _, tool := range []string{"read-one", "mutate", "read-two"} {
		if n := server.count(tool); n != 1 {
			t.Fatalf("%s called %d times", tool, n)
		}
	}
	if observer.pending() {
		t.Fatal("completed run left an observation parked")
	}
	log.verify(t)
	if hit, _, _ := store.state(); failRev > 0 && !hit {
		t.Fatalf("revision %d was never attempted", failRev)
	}
	_, _, accepted := store.state()
	return accepted
}

// Done-when: failure at every checkpoint boundary blocks safely. For each
// revision of a sync -> async -> sync batch the checkpoint is rejected once:
// no tool runs without an accepted reservation, nothing runs while fenced,
// the fence only lifts when the identical checkpoint is confirmed, and the
// healed run still executes each item exactly once.
func TestAssistantExecutionCheckpointFailureAtEveryBoundaryBlocksSafely(t *testing.T) {
	total := runAssistantCheckpointBoundary(t, 0)
	if total < 13 {
		t.Fatalf("clean run produced only %d checkpoints", total)
	}
	for rev := uint64(1); rev <= uint64(total); rev++ {
		t.Run(fmt.Sprintf("revision-%02d", rev), func(t *testing.T) { runAssistantCheckpointBoundary(t, rev) })
	}
}

// A relay that refuses the dispatching reservation (no OK) prevents the tool
// call; the real encrypted store and relay publish path are exercised.
func TestAssistantExecutionRelayRejectedReservationPreventsDispatch(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}}})
	start := st.startBatch(t, "s-reject")
	relay.setReject(func(ev nostr.Event) error {
		if ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) && tagValue(ev.Tags, domain.AssistantCheckpointTagRevision) == "4" {
			return errors.New("blocked: policy")
		}
		return nil
	})
	st.approve(t, "s-reject", start)
	relay.waitFor(t, "fenced", func() bool { return st.faulted("s-reject") })
	if server.total() != 0 {
		t.Fatal("tool dispatched after its reservation was rejected")
	}
	if _, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-reject", start.Session.CurrentRunID, "cancel")); !errors.Is(err, ErrAssistantCheckpointUnconfirmed) {
		t.Fatalf("cancel while fenced: %v", err)
	}
	relay.setReject(nil)
	if err := st.engine.Recover(context.Background(), AssistantExecutionReference{SessionID: "s-reject", RunID: start.Session.CurrentRunID}); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "completed", func() bool { return st.snapshot("s-reject").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
}

// ---------------------------------------------------------------------------
// Cancellation (Decision 5)

func assistantAsyncThenSyncPlan() domain.AssistantPlan {
	return domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "async", ToolName: "mutate", ToolArgs: map[string]any{}}, {StepID: "successor", ToolName: "read-two", ToolArgs: map[string]any{}}}}
}

func TestAssistantExecutionCancellationDuringModelWorkDiscardsLateProposal(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	proposer := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "c", Name: "read-one", Arguments: map[string]any{}}}}}, gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{iterative: proposer})
	type startResult struct {
		res AssistantTurnResult
		err error
	}
	done := make(chan startResult, 1)
	go func() {
		res, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-model", TurnID: "t", Prompt: "act"}, OperatorPubkey: "operator", RequestEventID: "prompt", DefaultWorkflow: domain.AssistantWorkflowIterative})
		done <- startResult{res, err}
	}()
	select {
	case <-proposer.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model call not started")
	}
	x := st.snapshot("s-model")
	res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-model", x.RunID, "cancel"))
	if err != nil || res.Session.Phase != domain.AssistantExecutionCancelled {
		t.Fatalf("cancel=%+v err=%v", res.Session.Phase, err)
	}
	close(proposer.gate)
	var got startResult
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartTurn did not return")
	}
	if got.err != nil || got.res.Acknowledgment != "proposal_discarded" {
		t.Fatalf("late proposal: %+v %v", got.res.Acknowledgment, got.err)
	}
	st.crash()
	if server.total() != 0 || len(st.snapshot("s-model").Work) != 0 {
		t.Fatal("late model response scheduled work")
	}
}

func TestAssistantExecutionCancellationDuringDispatchKeepsAccounting(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	gate := server.gate("mutate")
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}})
	start := st.startBatch(t, "s-dispatch")
	st.approve(t, "s-dispatch", start)
	assistantWait(t, server.entered, "mutate")
	res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-dispatch", start.Session.CurrentRunID, "cancel"))
	if err != nil || res.Session.Phase != domain.AssistantExecutionCancelling || res.PendingEffects != 1 {
		t.Fatalf("cancel during dispatch phase=%s pending=%d err=%v", res.Session.Phase, res.PendingEffects, err)
	}
	close(gate)
	relay.waitFor(t, "receipt retained for accounting", func() bool {
		return assistantWorkState(st.snapshot("s-dispatch"), 0) == domain.AssistantWorkWaitingAsync && relay.liveSubs(7961) == 1
	})
	if st.snapshot("s-dispatch").Phase != domain.AssistantExecutionCancelling {
		t.Fatal("late receipt restarted the cancelled run")
	}
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
	relay.waitFor(t, "cancelled", func() bool { return st.snapshot("s-dispatch").Phase == domain.AssistantExecutionCancelled })
	x := st.snapshot("s-dispatch")
	if x.Work[0].State != domain.AssistantWorkSucceeded || x.Work[1].State != domain.AssistantWorkSkipped || server.count("read-two") != 0 || server.count("mutate") != 1 {
		t.Fatalf("work=%+v", x.Work)
	}
}

func TestAssistantExecutionCancellationDuringObservationSurvivesRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}})
	start := first.startBatch(t, "s-observe")
	first.approve(t, "s-observe", start)
	relay.waitFor(t, "waiting", func() bool { return relay.liveSubs(7961) == 1 })
	res, err := first.engine.Cancel(context.Background(), assistantCancelRequest("s-observe", start.Session.CurrentRunID, "cancel"))
	if err != nil || res.Session.Phase != domain.AssistantExecutionCancelling {
		t.Fatalf("phase=%s err=%v", res.Session.Phase, err)
	}
	first.crash()
	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	second.recover(t, relay)
	relay.waitFor(t, "accounting observer restored", func() bool { return relay.liveSubs(7961) == 1 })
	if second.snapshot("s-observe").Phase != domain.AssistantExecutionCancelling {
		t.Fatal("cancellation not recovered")
	}
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "failed")
	relay.waitFor(t, "cancelled", func() bool { return second.snapshot("s-observe").Phase == domain.AssistantExecutionCancelled })
	x := second.snapshot("s-observe")
	if x.Work[0].State != domain.AssistantWorkFailed || x.Work[1].State != domain.AssistantWorkSkipped || server.count("read-two") != 0 {
		t.Fatalf("work=%+v", x.Work)
	}
}

func TestAssistantExecutionCancellationScopesAndStaleTargets(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}})
	start := st.startBatch(t, "s-scope")
	if _, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-scope", "older-run", "cancel-stale")); !errors.Is(err, ErrAssistantStaleTarget) {
		t.Fatalf("stale cancellation: %v", err)
	}
	res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-scope", start.Session.CurrentRunID, "cancel-draft"))
	if err != nil || res.Session.Phase != domain.AssistantExecutionCancelled {
		t.Fatalf("draft cancel phase=%s err=%v", res.Session.Phase, err)
	}
	if _, err = st.engine.Decide(context.Background(), assistantApproveUnchanged("s-scope", start, "late-approval")); !errors.Is(err, ErrAssistantNotAwaitingApproval) {
		t.Fatalf("approval after cancel: %v", err)
	}
	// Run scope leaves the conversation open; session scope closes it.
	next, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-scope", TurnID: "t2", Prompt: "again"}, OperatorPubkey: "operator", RequestEventID: "prompt-2", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	close := assistantCancelRequest("s-scope", next.Session.CurrentRunID, "close")
	close.Cancellation.Scope = "session"
	if _, err = st.engine.Cancel(context.Background(), close); err != nil {
		t.Fatal(err)
	}
	if _, err = st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-scope", TurnID: "t3", Prompt: "more"}, OperatorPubkey: "operator", RequestEventID: "prompt-3", DefaultWorkflow: domain.AssistantWorkflowBatch}); !errors.Is(err, ErrAssistantSessionClosed) {
		t.Fatalf("closed session accepted a turn: %v", err)
	}
	if server.total() != 0 {
		t.Fatal("cancelled drafts executed")
	}
}

// ---------------------------------------------------------------------------
// Ambiguous dispatch

type assistantTestEvidence struct {
	mu      sync.Mutex
	receipt *domain.AsyncToolReceipt
	err     error
	calls   int
}

func (e *assistantTestEvidence) set(receipt *domain.AsyncToolReceipt, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.receipt, e.err = receipt, err
}

func (e *assistantTestEvidence) ResolveAssistantRequestEvidence(_ context.Context, _ string, _ domain.AssistantExecution, _ domain.AssistantWorkItem) (*domain.AsyncToolReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.receipt, e.err
}

// Done-when: a recovered dispatching item without a durable receipt becomes
// uncertain, is never redispatched, blocks successors, and resolves only via
// Reconcile with exact downstream evidence. Absence is not proof.
func TestAssistantExecutionMissingReceiptBecomesUncertainAndReconcilesWithoutReplay(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	evidence := &assistantTestEvidence{}
	gate := server.gate("mutate")
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}})
	start := first.startBatch(t, "s-uncertain")
	first.approve(t, "s-uncertain", start)
	assistantWait(t, server.entered, "mutate")
	first.crash() // the process dies inside the provider call
	close(gate)

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{evidence: evidence})
	second.recover(t, relay)
	x := second.snapshot("s-uncertain")
	if x.Phase != domain.AssistantExecutionBlocked || x.Work[0].State != domain.AssistantWorkUncertain || x.Work[1].State != domain.AssistantWorkReady {
		t.Fatalf("recovered phase=%s work=%+v", x.Phase, x.Work)
	}
	if p := second.projection("s-uncertain"); p.UncertainEffects != 1 {
		t.Fatalf("projection uncertain=%d", p.UncertainEffects)
	}
	reconcile := func(requestID string) error {
		_, err := second.engine.Reconcile(context.Background(), AssistantTurnReconciliationRequest{Reconciliation: domain.AssistantReconciliationRequest{ContractVersion: 2, SessionID: "s-uncertain", RunID: x.RunID, WorkID: x.Work[0].WorkID, RequestEventID: requestID}, OperatorPubkey: "operator", RequestEventID: "reconcile-" + requestID})
		return err
	}
	evidence.set(nil, errors.New("request event not found at EOSE"))
	if err := reconcile("absent"); !errors.Is(err, ErrAssistantReconciliationRejected) {
		t.Fatalf("absence reconciled: %v", err)
	}
	key := x.Work[0].IdempotencyKey
	evidence.set(&domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "downstream", RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: "someone-else"}, nil)
	if err := reconcile("downstream"); !errors.Is(err, ErrAssistantReconciliationRejected) {
		t.Fatalf("mismatched evidence accepted: %v", err)
	}
	if st := second.snapshot("s-uncertain"); st.Work[0].State != domain.AssistantWorkUncertain || server.count("mutate") != 1 || server.count("read-two") != 0 {
		t.Fatal("rejected reconciliation changed state or dispatched")
	}
	evidence.set(&domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "downstream", RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: key}, nil)
	if err := reconcile("downstream"); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "observing reconciled request", func() bool { return relay.liveSubs(7961) == 1 })
	publishAssistantResult(t, relay, "downstream", "completed")
	relay.waitFor(t, "completed", func() bool { return second.snapshot("s-uncertain").Phase == domain.AssistantExecutionCompleted })
	if server.count("mutate") != 1 || server.count("read-two") != 1 {
		t.Fatalf("mutate=%d read-two=%d", server.count("mutate"), server.count("read-two"))
	}
}

// ---------------------------------------------------------------------------
// Authorization (Decision 4)

func TestAssistantExecutionHookChangingApprovedInputNeverExecutesUnreviewedContent(t *testing.T) {
	t.Run("at approval a replacement draft is published", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		evaluator := &assistantMutableHookEvaluator{}
		plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "a", ToolName: "mutate", ToolArgs: map[string]any{"zone": "z"}}}}
		st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}, hooks: newAssistantTestHooks(t, evaluator)})
		start := st.startBatch(t, "s-hook")
		evaluator.set(AssistantHookOutcome{UpdatedInput: map[string]any{"zone": "rewritten"}})
		if _, err := st.engine.Decide(context.Background(), assistantApproveUnchanged("s-hook", start, "approve-1")); !errors.Is(err, ErrAssistantProposalChangedRequiresReview) {
			t.Fatalf("hook-changed approval: %v", err)
		}
		x := st.snapshot("s-hook")
		if x.Phase != domain.AssistantExecutionAwaitingApproval || x.Proposal.Revision != 2 || x.Proposal.Plan.Steps[0].ToolArgs["zone"] != "rewritten" || server.total() != 0 {
			t.Fatalf("replacement=%+v calls=%d", x.Proposal, server.total())
		}
		replacement := AssistantTurnResult{Session: st.projection("s-hook")}
		st.approve(t, "s-hook", replacement)
		relay.waitFor(t, "reviewed input dispatched", func() bool { return server.count("mutate") == 1 })
	})
	t.Run("after approval the item is blocked with approved_input_changed", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		// Approval prepares both steps with the hook leaving input unchanged;
		// every later evaluation (dispatch) rewrites it.
		evaluator := &assistantMutableHookEvaluator{sequence: []AssistantHookOutcome{{}, {}}, outcome: AssistantHookOutcome{UpdatedInput: map[string]any{"zone": "changed-later"}}}
		plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "a", ToolName: "mutate", ToolArgs: map[string]any{"zone": "z"}}, {StepID: "b", ToolName: "read-two", ToolArgs: map[string]any{}}}}
		st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}, hooks: newAssistantTestHooks(t, evaluator)})
		start := st.startBatch(t, "s-hook-late")
		st.approve(t, "s-hook-late", start)
		relay.waitFor(t, "batch stopped", func() bool { return st.snapshot("s-hook-late").Phase == domain.AssistantExecutionFailed })
		x := st.snapshot("s-hook-late")
		if x.Work[0].State != domain.AssistantWorkDenied || x.Work[0].Observation.Error != ErrAssistantApprovedInputChanged.Error() || x.Work[1].State != domain.AssistantWorkSkipped || server.total() != 0 {
			t.Fatalf("work=%+v calls=%d", x.Work, server.total())
		}
	})
}

func TestAssistantExecutionAllowedToolsNilVersusEmptyAcrossWorkflowsAndRestart(t *testing.T) {
	scope := func(allowed []string) AssistantExecutionScopeResolver {
		return assistantScopeResolverFunc(func(context.Context, AssistantTurnStartRequest) (domain.AssistantCommandScope, error) {
			return domain.AssistantCommandScope{CommandName: "cmd", AllowedTools: allowed}, nil
		})
	}
	t.Run("iterative empty permits nothing", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		proposer := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "c", Name: "read-one", Arguments: map[string]any{}}}}}}
		st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{iterative: proposer, scope: scope([]string{})})
		st.startIterative(t, "s-empty")
		relay.waitFor(t, "completed", func() bool { return st.snapshot("s-empty").Phase == domain.AssistantExecutionCompleted })
		x := st.snapshot("s-empty")
		if x.Scope.AllowedTools == nil || x.Work[0].State != domain.AssistantWorkDenied || server.total() != 0 || proposer.calls() != 2 {
			t.Fatalf("scope=%#v work=%+v calls=%d", x.Scope.AllowedTools, x.Work, server.total())
		}
	})
	t.Run("batch empty rejects the whole approval; nil permits", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
		empty := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}, scope: scope([]string{})})
		start := empty.startBatch(t, "s-batch-empty")
		var denial *AssistantWorkDenial
		if _, err := empty.engine.Decide(context.Background(), assistantApproveUnchanged("s-batch-empty", start, "approve")); !errors.As(err, &denial) {
			t.Fatalf("out-of-scope approval: %v", err)
		}
		if empty.snapshot("s-batch-empty").Phase != domain.AssistantExecutionAwaitingApproval || server.total() != 0 {
			t.Fatal("rejected approval changed state or dispatched")
		}
		unrestricted := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}, scope: scope(nil)})
		start = unrestricted.startBatch(t, "s-batch-nil")
		unrestricted.approve(t, "s-batch-nil", start)
		relay.waitFor(t, "completed", func() bool { return unrestricted.snapshot("s-batch-nil").Phase == domain.AssistantExecutionCompleted })
		if server.count("read-one") != 1 {
			t.Fatal("nil scope did not permit the tool")
		}
	})
	t.Run("persisted scope survives restart", func(t *testing.T) {
		relay := newAssistantTestRelay()
		signer := testAssistantSigner(t)
		server := newAssistantTestToolServer(relay.touch)
		proposer := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "m", Name: "mutate", Arguments: map[string]any{}}, {ID: "r", Name: "read-two", Arguments: map[string]any{}}}}}}
		first := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer, scope: scope([]string{"mutate"})})
		first.startIterative(t, "s-scope-restart")
		relay.waitFor(t, "waiting", func() bool { return relay.liveSubs(7961) == 1 })
		first.crash()
		second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer})
		second.recover(t, relay)
		relay.waitFor(t, "resubscribed", func() bool { return relay.liveSubs(7961) == 1 })
		publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
		relay.waitFor(t, "completed", func() bool { return second.snapshot("s-scope-restart").Phase == domain.AssistantExecutionCompleted })
		x := second.snapshot("s-scope-restart")
		if len(x.Scope.AllowedTools) != 1 || x.Work[1].State != domain.AssistantWorkDenied || server.count("read-two") != 0 {
			t.Fatalf("scope=%v work=%+v", x.Scope.AllowedTools, x.Work)
		}
	})
}

// An action approval binds one work item and its exact digest; it cannot
// authorize another call that uses the same tool.
func TestAssistantExecutionIterativeActionApprovalBindsExactWork(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	rules := []AssistantPermissionRule{{ID: "ask-mutate", Decision: domain.AssistantPermissionDecisionAsk, ToolNames: []string{"mutate"}}}
	proposer := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "a", Name: "mutate", Arguments: map[string]any{"zone": "a"}}, {ID: "b", Name: "mutate", Arguments: map[string]any{"zone": "b"}}}}}}
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{iterative: proposer, rules: rules})
	st.startIterative(t, "s-action")
	relay.waitFor(t, "first action pending", func() bool { return st.snapshot("s-action").Phase == domain.AssistantExecutionAwaitingApproval })
	x := st.snapshot("s-action")
	decide := func(actionID, decision, requestID string) (AssistantTurnResult, error) {
		return st.engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-action", RunID: x.RunID, Workflow: domain.AssistantWorkflowIterative, ActionID: actionID, Decision: decision}, OperatorPubkey: "operator", RequestEventID: requestID})
	}
	if _, err := decide(x.Work[1].WorkID, "approve", "wrong-action"); !errors.Is(err, ErrAssistantStaleTarget) {
		t.Fatalf("approval of a non-pending same-tool call: %v", err)
	}
	if _, err := decide(x.Work[0].WorkID, "approve", "approve-a"); err != nil {
		t.Fatal(err)
	}
	if res, err := decide(x.Work[0].WorkID, "approve", "approve-a"); err != nil || res.Acknowledgment != "duplicate_decision" {
		t.Fatalf("redelivered decision: %+v %v", res.Acknowledgment, err)
	}
	relay.waitFor(t, "a submitted", func() bool { return relay.liveSubs(7961) == 1 })
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
	relay.waitFor(t, "second action pending", func() bool {
		return assistantWorkState(st.snapshot("s-action"), 1) == domain.AssistantWorkAwaitingApproval
	})
	if server.count("mutate") != 1 {
		t.Fatal("approval of one call authorized another")
	}
	if _, err := decide(x.Work[1].WorkID, "reject", "reject-b"); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "completed", func() bool { return st.snapshot("s-action").Phase == domain.AssistantExecutionCompleted })
	if y := st.snapshot("s-action"); y.Work[1].State != domain.AssistantWorkDenied || server.count("mutate") != 1 {
		t.Fatalf("work=%+v", y.Work)
	}
}

// Loss of relay visibility is volatile: the projection shows blocked while
// the journal is untouched, and the reissued subscription restores live
// observation.
func TestAssistantExecutionObservationBlockedIsVolatileAndReissues(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "a", ToolName: "mutate", ToolArgs: map[string]any{}}}}}})
	start := st.startBatch(t, "s-blind")
	st.approve(t, "s-blind", start)
	relay.waitFor(t, "waiting", func() bool {
		return relay.liveSubs(7961) == 1 && st.projection("s-blind").Phase == domain.AssistantExecutionWaitingAsync
	})
	revisions := len(assistantCheckpointRevisions(relay, start.Session.CurrentRunID))
	relay.closeMatching(7961, "auth-required: authenticate")
	relay.waitFor(t, "projection blocked", func() bool { return st.projection("s-blind").Phase == domain.AssistantExecutionBlocked })
	if got := len(assistantCheckpointRevisions(relay, start.Session.CurrentRunID)); got != revisions || st.snapshot("s-blind").Phase != domain.AssistantExecutionWaitingAsync {
		t.Fatal("relay visibility loss was written to the execution journal")
	}
	st.reissue <- struct{}{}
	relay.waitFor(t, "live again", func() bool { return st.projection("s-blind").Phase == domain.AssistantExecutionWaitingAsync })
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
	relay.waitFor(t, "completed", func() bool { return st.snapshot("s-blind").Phase == domain.AssistantExecutionCompleted })
}
