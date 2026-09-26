package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// assistantDecodedCheckpoints decrypts every checkpoint of a run, in revision
// order, through the store's own validation.
func assistantDecodedCheckpoints(t *testing.T, st *assistantStack, relay *assistantTestRelay, sessionID, runID string) []domain.AssistantExecution {
	t.Helper()
	author, err := nostr.PubKeyFromHex(st.pubkey)
	if err != nil {
		t.Fatal(err)
	}
	byRevision := map[uint64]domain.AssistantExecution{}
	for _, ev := range relay.eventsOfKind(nostr.Kind(domain.AssistantExecutionCheckpointKind)) {
		if tagValue(ev.Tags, domain.AssistantCheckpointTagRun) != runID {
			continue
		}
		cp, err := st.store.decode(context.Background(), &ev, author, sessionID, runID)
		if err != nil {
			t.Fatalf("checkpoint %s: %v", ev.ID.Hex(), err)
		}
		byRevision[cp.Execution.Revision] = cp.Execution
	}
	revisions := make([]uint64, 0, len(byRevision))
	for rev := range byRevision {
		revisions = append(revisions, rev)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	out := make([]domain.AssistantExecution, 0, len(revisions))
	for i, rev := range revisions {
		if rev != uint64(i+1) {
			t.Fatalf("checkpoint revisions %v have a gap", revisions)
		}
		out = append(out, byRevision[rev])
	}
	return out
}

func assistantAbandonRequest(sessionID, runID, workID, requestID, reason string) AssistantTurnReconciliationRequest {
	return AssistantTurnReconciliationRequest{Reconciliation: domain.AssistantReconciliationRequest{ContractVersion: 2, SessionID: sessionID, RunID: runID, WorkID: workID, Resolution: domain.AssistantReconciliationAbandon, Reason: reason, Attestation: domain.AssistantAbandonmentAttestation}, OperatorPubkey: "operator", RequestEventID: requestID}
}

// assistantSideEffectRuntime adds a synchronous mutation to the test catalog.
func assistantSideEffectRuntime(server AssistantToolRuntimeMCPServer) *AssistantToolRuntime {
	registry := assistantTestRegistry()
	registry["sync-mutate"] = AssistantToolRuntimeToolDescriptor{Name: "sync-mutate", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}
	return NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: registry, Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil)})
}

// crashInsideDispatch approves a batch, lets its first step reach the
// provider and kills the process there, so the chain holds a dispatching
// reservation with no outcome.
func crashInsideDispatch(t *testing.T, relay *assistantTestRelay, signer nostr.Signer, server *assistantTestToolServer, sessionID, tool string, plan domain.AssistantPlan) (runID string) {
	t.Helper()
	gate := server.gate(tool)
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}, runtime: assistantSideEffectRuntime(server)})
	start := first.startBatch(t, sessionID)
	first.approve(t, sessionID, start)
	assistantWait(t, server.entered, tool)
	first.crash()
	close(gate)
	return start.Session.CurrentRunID
}

// Done-when (a): after a restart, a read-only synchronous item whose dispatch
// outcome was lost is re-dispatched automatically, and the release is a
// checkpoint in the chain between the two reservations.
func TestAssistantExecutionRestartRedispatchesReadOnlySyncWorkAndRecordsIt(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	runID := crashInsideDispatch(t, relay, signer, server, "s-replay", "read-one", plan)

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	second.recover(t, relay)
	relay.waitFor(t, "re-dispatched run completes", func() bool { return second.snapshot("s-replay").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 2 || server.count("read-two") != 1 {
		t.Fatalf("read-one=%d read-two=%d, want the lost read re-run once", server.count("read-one"), server.count("read-two"))
	}
	chain := assistantDecodedCheckpoints(t, second, relay, "s-replay", runID)
	released := -1
	for i, x := range chain {
		if len(x.Work) > 0 && len(x.Work[0].Redispatches) > 0 {
			released = i
			break
		}
	}
	if released < 1 || chain[released-1].Work[0].State != domain.AssistantWorkDispatching || chain[released].Work[0].State != domain.AssistantWorkReady {
		t.Fatalf("release checkpoint not found between reservations (index %d)", released)
	}
	record := chain[released].Work[0].Redispatches
	if len(record) != 1 || record[0].Attempt != 1 || record[0].Action != domain.AssistantWorkRedispatchReleased || record[0].Reason == "" || record[0].RecordedAt.IsZero() {
		t.Fatalf("re-dispatch record = %+v", record)
	}
	if next := chain[released+1].Work[0]; next.State != domain.AssistantWorkDispatching {
		t.Fatalf("release not followed by a fresh reservation: %s", next.State)
	}
	head := chain[len(chain)-1]
	if head.Work[0].State != domain.AssistantWorkSucceeded || len(head.Work[0].Redispatches) != 1 || len(head.Work[1].Redispatches) != 0 {
		t.Fatalf("head work = %+v", head.Work)
	}
}

// Done-when (a): anything outside the read-only synchronous classification
// (async or synchronous mutation, service-owned internal tools) stays
// uncertain after the same crash and is never re-dispatched.
func TestAssistantExecutionRestartKeepsSideEffectingAndInternalWorkUncertain(t *testing.T) {
	for _, tool := range []string{"mutate", "sync-mutate"} {
		t.Run(tool, func(t *testing.T) {
			relay := newAssistantTestRelay()
			signer := testAssistantSigner(t)
			server := newAssistantTestToolServer(relay.touch)
			plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: tool, ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}}}}
			crashInsideDispatch(t, relay, signer, server, "s-effect", tool, plan)
			second := newAssistantStack(t, relay, signer, server, assistantStackOptions{runtime: assistantSideEffectRuntime(server)})
			second.recover(t, relay)
			x := second.snapshot("s-effect")
			if x.Phase != domain.AssistantExecutionBlocked || x.Work[0].State != domain.AssistantWorkUncertain || len(x.Work[0].Redispatches) != 0 {
				t.Fatalf("phase=%s work=%+v", x.Phase, x.Work[0])
			}
			second.crash()
			if server.count(tool) != 1 || server.count("read-two") != 0 {
				t.Fatalf("%s=%d read-two=%d", tool, server.count(tool), server.count("read-two"))
			}
		})
	}
	t.Run("internal read tool", func(t *testing.T) {
		relay := newAssistantTestRelay()
		signer := testAssistantSigner(t)
		server := newAssistantTestToolServer(relay.touch)
		entered := make(chan struct{}, 4)
		calls := 0
		internal := func() *AssistantToolRuntime {
			runtime := assistantTestRuntime(server, nil, nil)
			if err := runtime.RegisterInternalTools(AssistantInternalTool{Name: "internal-read", InputSchema: map[string]any{"type": "object"}, Effect: domain.AssistantToolEffectRead, Risk: domain.AssistantToolRiskLow, Handler: func(ctx context.Context, _ AssistantInternalToolCall) (*domain.AssistantToolObservation, error) {
				calls++
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}}); err != nil {
				t.Fatal(err)
			}
			return runtime
		}
		proposer := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "c1", Name: "internal-read", Arguments: map[string]any{}}}}}}
		first := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer, runtime: internal()})
		first.startIterative(t, "s-internal")
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("internal tool not dispatched")
		}
		first.crash()
		second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer, runtime: internal()})
		second.recover(t, relay)
		x := second.snapshot("s-internal")
		second.crash()
		if x.Work[0].State != domain.AssistantWorkUncertain || calls != 1 || proposer.calls() != 1 {
			t.Fatalf("internal tool state=%s calls=%d model=%d", x.Work[0].State, calls, proposer.calls())
		}
	})
}

// A cancelled run does not re-run a lost read: the item is discarded with a
// recorded reason and the run finishes cancelled without operator help.
func TestAssistantExecutionRestartDiscardsLostReadInCancelledRun(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	gate := server.gate("read-one")
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantOneReadPlan()}})
	start := first.startBatch(t, "s-discard")
	first.approve(t, "s-discard", start)
	assistantWait(t, server.entered, "read-one")
	if res, err := first.engine.Cancel(context.Background(), assistantCancelRequest("s-discard", start.Session.CurrentRunID, "cancel")); err != nil || res.Session.Phase != domain.AssistantExecutionCancelling {
		t.Fatalf("cancel phase=%s err=%v", res.Session.Phase, err)
	}
	first.crash()
	close(gate)
	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	second.recover(t, relay)
	x := second.snapshot("s-discard")
	if x.Phase != domain.AssistantExecutionCancelled || x.Work[0].State != domain.AssistantWorkSkipped || len(x.Work[0].Redispatches) != 1 || x.Work[0].Redispatches[0].Action != domain.AssistantWorkRedispatchDiscarded {
		t.Fatalf("phase=%s work=%+v", x.Phase, x.Work[0])
	}
	second.crash()
	if server.count("read-one") != 1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
}

// A read that keeps dying with the process is re-dispatched a bounded number
// of times, then becomes uncertain; abandoning it ends the run failed with
// its successor skipped and never run.
func TestAssistantExecutionRedispatchIsBoundedThenAbandonable(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	gate := server.gate("read-one")
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	start := first.startBatch(t, "s-bound")
	runID := start.Session.CurrentRunID
	first.approve(t, "s-bound", start)
	assistantWait(t, server.entered, "read-one")
	first.crash()
	for attempt := 1; attempt <= assistantMaxAutomaticRedispatches; attempt++ {
		next := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
		next.recover(t, relay)
		assistantWait(t, server.entered, "read-one")
		next.crash()
	}
	last := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	last.recover(t, relay)
	x := last.snapshot("s-bound")
	if x.Phase != domain.AssistantExecutionBlocked || x.Work[0].State != domain.AssistantWorkUncertain || len(x.Work[0].Redispatches) != assistantMaxAutomaticRedispatches {
		t.Fatalf("after the bound: phase=%s work=%+v", x.Phase, x.Work[0])
	}
	if server.count("read-one") != assistantMaxAutomaticRedispatches+1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
	close(gate)
	res, err := last.engine.Reconcile(context.Background(), assistantAbandonRequest("s-bound", runID, x.Work[0].WorkID, "abandon-1", "read keeps crashing the service"))
	if err != nil || res.Acknowledgment != "abandoned" || res.Session.Phase != domain.AssistantExecutionFailed || res.Session.AbandonedEffects != 1 || res.Session.UncertainEffects != 0 {
		t.Fatalf("abandon = %+v err=%v", res, err)
	}
	last.crash()
	after := last.snapshot("s-bound")
	if after.Work[1].State != domain.AssistantWorkSkipped || server.count("read-two") != 0 {
		t.Fatalf("successor after abandoned work: %+v read-two=%d", after.Work[1], server.count("read-two"))
	}
}

// Done-when (b): an attested abandonment lets a run stuck in cancelling
// finish; the checkpoint carries who/when/why; the item is abandoned, never
// succeeded or failed; misuse is refused with a stable error and records
// nothing; redelivery is idempotent; a restart preserves it.
func TestAssistantExecutionAbandonFinishesCancellingRunWithAttestation(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	runID := crashInsideDispatch(t, relay, signer, server, "s-abandon", "mutate", assistantAsyncThenSyncPlan())
	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{evidence: &assistantTestEvidence{}})
	second.recover(t, relay)
	x := second.snapshot("s-abandon")
	workID := x.Work[0].WorkID
	if res, err := second.engine.Cancel(context.Background(), assistantCancelRequest("s-abandon", runID, "cancel")); err != nil || res.Session.Phase != domain.AssistantExecutionCancelling {
		t.Fatalf("cancel phase=%s err=%v", res.Session.Phase, err)
	}
	cancelling := second.snapshot("s-abandon").Revision

	misuse := map[string]AssistantTurnReconciliationRequest{}
	noReason := assistantAbandonRequest("s-abandon", runID, workID, "abandon-x", "  ")
	misuse["no reason"] = noReason
	noAttestation := assistantAbandonRequest("s-abandon", runID, workID, "abandon-x", "lost")
	noAttestation.Reconciliation.Attestation = "done"
	misuse["no attestation"] = noAttestation
	withEvidence := assistantAbandonRequest("s-abandon", runID, workID, "abandon-x", "lost")
	withEvidence.Reconciliation.RequestEventID = "downstream"
	misuse["with a request event"] = withEvidence
	for name, req := range misuse {
		if _, err := second.engine.Reconcile(context.Background(), req); !errors.Is(err, ErrAssistantAbandonmentRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	stranger := assistantAbandonRequest("s-abandon", runID, workID, "abandon-x", "lost")
	stranger.OperatorPubkey = "someone-else"
	if _, err := second.engine.Reconcile(context.Background(), stranger); !errors.Is(err, ErrAssistantOperatorMismatch) {
		t.Fatalf("non-participant abandonment: %v", err)
	}
	if _, err := second.engine.Reconcile(context.Background(), assistantAbandonRequest("s-abandon", "older-run", workID, "abandon-x", "lost")); !errors.Is(err, ErrAssistantStaleTarget) {
		t.Fatalf("stale run abandonment: %v", err)
	}
	if got := second.snapshot("s-abandon"); got.Revision != cancelling || got.Work[0].State != domain.AssistantWorkUncertain {
		t.Fatalf("refused abandonment changed state: rev %d->%d", cancelling, got.Revision)
	}

	res, err := second.engine.Reconcile(context.Background(), assistantAbandonRequest("s-abandon", runID, workID, "abandon-1", "downstream relay history was pruned"))
	if err != nil || res.Acknowledgment != "abandoned" || res.Session.Phase != domain.AssistantExecutionCancelled || res.PendingEffects != 0 || res.Session.AbandonedEffects != 1 {
		t.Fatalf("abandon = %+v err=%v", res, err)
	}
	head, err := second.store.Load(context.Background(), "s-abandon", runID)
	if err != nil {
		t.Fatal(err)
	}
	w := head.Execution.Work[0]
	a := w.Abandonment
	if w.State != domain.AssistantWorkAbandoned || w.Observation != nil || w.Receipt != nil || a == nil || a.OperatorPubkey != "operator" || a.RequestID != "abandon-1" || a.Reason != "downstream relay history was pruned" || a.Attestation != domain.AssistantAbandonmentAttestation || !a.RecordedAt.Equal(assistantTestClock()) {
		t.Fatalf("checkpointed abandonment = %+v record=%+v", w, a)
	}
	if head.Execution.Phase != domain.AssistantExecutionCancelled || head.Execution.Work[1].State != domain.AssistantWorkSkipped {
		t.Fatalf("journal head phase=%s work=%+v", head.Execution.Phase, head.Execution.Work)
	}
	if again, err := second.engine.Reconcile(context.Background(), assistantAbandonRequest("s-abandon", runID, workID, "abandon-1", "downstream relay history was pruned")); err != nil || again.Acknowledgment != "already_abandoned" {
		t.Fatalf("redelivered abandonment = %+v err=%v", again, err)
	}
	if _, err := second.engine.Reconcile(context.Background(), assistantAbandonRequest("s-abandon", runID, workID, "abandon-2", "again")); !errors.Is(err, ErrAssistantAbandonmentRefused) {
		t.Fatalf("second abandonment of abandoned work: %v", err)
	}
	evidence := AssistantTurnReconciliationRequest{Reconciliation: domain.AssistantReconciliationRequest{ContractVersion: 2, SessionID: "s-abandon", RunID: runID, WorkID: workID, RequestEventID: "downstream"}, OperatorPubkey: "operator", RequestEventID: "reconcile-late"}
	if _, err := second.engine.Reconcile(context.Background(), evidence); !errors.Is(err, ErrAssistantStaleTarget) {
		t.Fatalf("evidence after abandonment: %v", err)
	}
	second.crash()

	third := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	third.recover(t, relay)
	if x := third.snapshot("s-abandon"); x.Phase != domain.AssistantExecutionCancelled || x.Work[0].State != domain.AssistantWorkAbandoned {
		t.Fatalf("recovered phase=%s work=%+v", x.Phase, x.Work[0])
	}
	third.crash()
	if server.count("mutate") != 1 || server.count("read-two") != 0 {
		t.Fatalf("mutate=%d read-two=%d", server.count("mutate"), server.count("read-two"))
	}
}

// Done-when (b): abandonment is not a generic completion control. It is
// refused for pending, ready, in-flight dispatching and succeeded work, and
// for work of a completed run; nothing is recorded.
func TestAssistantExecutionAbandonIsRefusedForWorkThatIsNotUncertain(t *testing.T) {
	ctx := context.Background()
	t.Run("pending ready and succeeded", func(t *testing.T) {
		relay := newAssistantTestRelay()
		signer := testAssistantSigner(t)
		server := newAssistantTestToolServer(relay.touch)
		st := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
		digest, err := domain.ComputeAssistantArgumentsDigest(map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		item := func(id string, ordinal int, state domain.AssistantWorkState) domain.AssistantWorkItem {
			return domain.AssistantWorkItem{WorkID: id, OriginID: id, Ordinal: ordinal, ToolName: "read-one", Arguments: map[string]any{}, ArgumentsDigest: digest, State: state}
		}
		x := domain.AssistantExecution{Version: 2, SessionID: "s-states", RunID: "run-states", TurnID: "t", RequestID: "r", Workflow: domain.AssistantWorkflowIterative, Revision: 1, Phase: domain.AssistantExecutionBlocked,
			Work: []domain.AssistantWorkItem{item("succeeded", 0, domain.AssistantWorkSucceeded), item("uncertain", 1, domain.AssistantWorkUncertain), item("pending", 2, domain.AssistantWorkPending), item("ready", 3, domain.AssistantWorkReady)}}
		if _, err := st.store.Append(ctx, x, ""); err != nil {
			t.Fatal(err)
		}
		st.engine.HydrateProjection(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: "s-states", OperatorPubkey: "operator", CurrentRunID: "run-states"}, 0)
		if err := st.engine.Recover(ctx, AssistantExecutionReference{SessionID: "s-states", RunID: "run-states"}); err != nil {
			t.Fatal(err)
		}
		before := st.snapshot("s-states").Revision
		for _, workID := range []string{"succeeded", "pending", "ready", "missing"} {
			if _, err := st.engine.Reconcile(ctx, assistantAbandonRequest("s-states", "run-states", workID, "abandon-"+workID, "operator gave up")); !errors.Is(err, ErrAssistantAbandonmentRefused) {
				t.Fatalf("abandon %s: %v", workID, err)
			}
		}
		if after := st.snapshot("s-states"); after.Revision != before {
			t.Fatalf("refused abandonments recorded checkpoints: %d -> %d", before, after.Revision)
		}
		if server.total() != 0 {
			t.Fatal("refused abandonment dispatched work")
		}
	})
	t.Run("in-flight dispatching", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		gate := server.gate("mutate")
		st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}})
		start := st.startBatch(t, "s-dispatching")
		st.approve(t, "s-dispatching", start)
		assistantWait(t, server.entered, "mutate")
		x := st.snapshot("s-dispatching")
		if x.Work[0].State != domain.AssistantWorkDispatching {
			t.Fatalf("state=%s", x.Work[0].State)
		}
		if _, err := st.engine.Reconcile(ctx, assistantAbandonRequest("s-dispatching", x.RunID, x.Work[0].WorkID, "abandon", "impatient")); !errors.Is(err, ErrAssistantAbandonmentRefused) {
			t.Fatalf("abandon dispatching: %v", err)
		}
		close(gate)
		relay.waitFor(t, "receipt recorded", func() bool {
			return assistantWorkState(st.snapshot("s-dispatching"), 0) == domain.AssistantWorkWaitingAsync
		})
		if _, err := st.engine.Reconcile(ctx, assistantAbandonRequest("s-dispatching", x.RunID, x.Work[0].WorkID, "abandon-2", "impatient")); !errors.Is(err, ErrAssistantAbandonmentRefused) {
			t.Fatalf("abandon waiting_async: %v", err)
		}
	})
	t.Run("completed run", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantOneReadPlan()}})
		start := st.startBatch(t, "s-done")
		st.approve(t, "s-done", start)
		relay.waitFor(t, "completed", func() bool { return st.snapshot("s-done").Phase == domain.AssistantExecutionCompleted })
		x := st.snapshot("s-done")
		if _, err := st.engine.Reconcile(ctx, assistantAbandonRequest("s-done", x.RunID, x.Work[0].WorkID, "abandon", "tidy up")); !errors.Is(err, ErrAssistantAbandonmentRefused) {
			t.Fatalf("abandon succeeded work: %v", err)
		}
		if after := st.snapshot("s-done"); after.Revision != x.Revision || after.Work[0].State != domain.AssistantWorkSucceeded {
			t.Fatal("refused abandonment changed a completed run")
		}
	})
}

// The journal validator refuses abandonment and re-dispatch records that do
// not explain their transition, and any edit to recorded ones.
func TestAssistantExecutionWorkRecordTransitionsAreValidated(t *testing.T) {
	at := assistantTestClock()
	attested := &domain.AssistantWorkAbandonment{OperatorPubkey: "operator", RequestID: "r", Reason: "lost", Attestation: domain.AssistantAbandonmentAttestation, RecordedAt: at}
	redispatch := domain.AssistantWorkRedispatch{Attempt: 1, Action: domain.AssistantWorkRedispatchReleased, Reason: "lost", RecordedAt: at}
	base := func(w domain.AssistantWorkItem) domain.AssistantExecution {
		w.WorkID, w.OriginID, w.ToolName = "w", "w", "read-one"
		return domain.AssistantExecution{SessionID: "s", RunID: "r", TurnID: "t", RequestID: "q", Workflow: domain.AssistantWorkflowIterative, Phase: domain.AssistantExecutionBlocked, Work: []domain.AssistantWorkItem{w}}
	}
	cases := []struct {
		name     string
		prev     domain.AssistantWorkItem
		next     domain.AssistantWorkItem
		accepted bool
	}{
		{"attested abandonment", domain.AssistantWorkItem{State: domain.AssistantWorkUncertain}, domain.AssistantWorkItem{State: domain.AssistantWorkAbandoned, Abandonment: attested}, true},
		{"unattested abandonment", domain.AssistantWorkItem{State: domain.AssistantWorkUncertain}, domain.AssistantWorkItem{State: domain.AssistantWorkAbandoned}, false},
		{"abandon pending", domain.AssistantWorkItem{State: domain.AssistantWorkPending}, domain.AssistantWorkItem{State: domain.AssistantWorkAbandoned, Abandonment: attested}, false},
		{"abandoned is terminal", domain.AssistantWorkItem{State: domain.AssistantWorkAbandoned, Abandonment: attested}, domain.AssistantWorkItem{State: domain.AssistantWorkSucceeded, Abandonment: attested}, false},
		{"record on other work", domain.AssistantWorkItem{State: domain.AssistantWorkReady}, domain.AssistantWorkItem{State: domain.AssistantWorkReady, Abandonment: attested}, false},
		{"release reservation", domain.AssistantWorkItem{State: domain.AssistantWorkDispatching}, domain.AssistantWorkItem{State: domain.AssistantWorkReady, Redispatches: []domain.AssistantWorkRedispatch{redispatch}}, true},
		{"record without release", domain.AssistantWorkItem{State: domain.AssistantWorkReady}, domain.AssistantWorkItem{State: domain.AssistantWorkReady, Redispatches: []domain.AssistantWorkRedispatch{redispatch}}, false},
		{"record removed", domain.AssistantWorkItem{State: domain.AssistantWorkReady, Redispatches: []domain.AssistantWorkRedispatch{redispatch}}, domain.AssistantWorkItem{State: domain.AssistantWorkReady}, false},
	}
	for _, tc := range cases {
		err := validateAssistantExecutionTransition(base(tc.prev), base(tc.next))
		if (err == nil) != tc.accepted {
			t.Fatalf("%s: err=%v", tc.name, err)
		}
	}
	if got := strconv.Quote(string(domain.AssistantWorkAbandoned)); got != `"abandoned"` {
		t.Fatalf("state token %s", got)
	}
}
