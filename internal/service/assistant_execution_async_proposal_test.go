package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// assistantHeldProposer holds every model call until released. A call honours
// cancellation unless its index is listed in ignoreCancel (a model client that
// only returns late).
type assistantHeldProposer struct {
	mu           sync.Mutex
	calls        int
	ignoreCancel map[int]bool
	// untilCancel calls are not released by release; only cancellation ends them.
	untilCancel map[int]bool
	err         error
	plan        *domain.AssistantPlan
	entered     chan string
	interrupted chan string
	release     chan struct{}
}

func newAssistantHeldProposer() *assistantHeldProposer {
	return &assistantHeldProposer{entered: make(chan string, 16), interrupted: make(chan string, 16), release: make(chan struct{}), ignoreCancel: map[int]bool{}, untilCancel: map[int]bool{}}
}

func (p *assistantHeldProposer) propose(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	ignore, untilCancel := p.ignoreCancel[idx], p.untilCancel[idx]
	p.mu.Unlock()
	p.entered <- req.RunID
	switch {
	case ignore:
		<-p.release
	case untilCancel:
		<-ctx.Done()
		p.interrupted <- req.RunID
		return AssistantProposal{}, ctx.Err()
	default:
		select {
		case <-p.release:
		case <-ctx.Done():
			p.interrupted <- req.RunID
			return AssistantProposal{}, ctx.Err()
		}
	}
	if p.err != nil {
		return AssistantProposal{}, p.err
	}
	if p.plan != nil {
		plan := *p.plan
		return AssistantProposal{Kind: AssistantProposalBatch, Batch: &plan}, nil
	}
	return AssistantProposal{Kind: AssistantProposalFinal, Text: "done"}, nil
}

func (p *assistantHeldProposer) ProposeBatch(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	return p.propose(ctx, req)
}

func (p *assistantHeldProposer) ProposeIterative(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	return p.propose(ctx, req)
}

func assistantReceive(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// within fails the test if fn does not return while the model call is held.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s blocked behind a held model call", what)
	}
}

func assistantStart(st *assistantStack, sessionID, requestID string, workflow domain.AssistantWorkflow) (AssistantTurnResult, error) {
	return st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: sessionID, TurnID: requestID, Prompt: "act"}, OperatorPubkey: "operator", RequestEventID: requestID, DefaultWorkflow: workflow})
}

// Done-when: a held proposal does not delay a concurrent cancel or an
// unrelated request. StartTurn returns at the checkpointed turn start.
func TestAssistantExecutionHeldProposalDoesNotBlockOtherRequests(t *testing.T) {
	relay := newAssistantTestRelay()
	held := newAssistantHeldProposer()
	st := newAssistantStack(t, relay, testAssistantSigner(t), newAssistantTestToolServer(relay.touch), assistantStackOptions{iterative: held, batch: assistantTestBatchProposer{plan: assistantOneReadPlan()}})

	var started AssistantTurnResult
	within(t, "StartTurn", func() {
		var err error
		started, err = assistantStart(st, "s-held", "prompt-held", domain.AssistantWorkflowIterative)
		if err != nil || started.Session.Phase != domain.AssistantExecutionProposing || started.Acknowledgment != "proposing" || started.ExecutionRevision != 1 {
			t.Errorf("held start = %+v err=%v", started, err)
		}
	})
	assistantReceive(t, held.entered, "held model call")
	within(t, "an unrelated session's prompt", func() {
		if _, err := assistantStart(st, "s-other", "prompt-other", domain.AssistantWorkflowBatch); err != nil {
			t.Error(err)
		}
	})
	if other := st.waitSettled(t, "s-other"); other.Session.Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("unrelated session phase %s", other.Session.Phase)
	}
	within(t, "a second prompt on the busy session", func() {
		if _, err := assistantStart(st, "s-held", "prompt-again", domain.AssistantWorkflowIterative); !errors.Is(err, ErrAssistantRunInProgress) {
			t.Errorf("second prompt: %v", err)
		}
	})
	within(t, "cancel", func() {
		res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-held", started.Session.CurrentRunID, "cancel-held"))
		if err != nil || res.Session.Phase != domain.AssistantExecutionCancelled {
			t.Errorf("cancel = %s err=%v", res.Session.Phase, err)
		}
	})
	if run := assistantReceive(t, held.interrupted, "model call interrupted"); run != started.Session.CurrentRunID {
		t.Fatalf("interrupted run %s", run)
	}
	st.waitSettled(t, "s-held")
}

// Done-when: cancel during proposing interrupts the model call and reaches a
// terminal phase for both workflows; nothing from the late call is applied.
func TestAssistantExecutionCancelDuringProposingReachesTerminalPhase(t *testing.T) {
	for _, workflow := range []domain.AssistantWorkflow{domain.AssistantWorkflowBatch, domain.AssistantWorkflowIterative} {
		t.Run(string(workflow), func(t *testing.T) {
			relay := newAssistantTestRelay()
			held := newAssistantHeldProposer()
			plan := assistantOneReadPlan()
			held.plan = &plan
			if workflow == domain.AssistantWorkflowIterative {
				held.plan = nil
			}
			server := newAssistantTestToolServer(relay.touch)
			st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{iterative: held, batch: held})
			started, err := assistantStart(st, "s-cancel", "prompt", workflow)
			if err != nil {
				t.Fatal(err)
			}
			assistantReceive(t, held.entered, "model call")
			res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-cancel", started.Session.CurrentRunID, "cancel"))
			if err != nil || res.Session.Phase != domain.AssistantExecutionCancelled {
				t.Fatalf("cancel = %s err=%v", res.Session.Phase, err)
			}
			assistantReceive(t, held.interrupted, "model call interrupted")
			settled := st.waitSettled(t, "s-cancel")
			head, err := st.store.Load(context.Background(), "s-cancel", started.Session.CurrentRunID)
			if err != nil {
				t.Fatal(err)
			}
			if settled.Session.Phase != domain.AssistantExecutionCancelled || head.Execution.Phase != domain.AssistantExecutionCancelled || head.Execution.Revision != res.ExecutionRevision || len(head.Execution.Work) != 0 || server.total() != 0 {
				t.Fatalf("after cancel: projection=%s journal=%s rev=%d work=%d calls=%d", settled.Session.Phase, head.Execution.Phase, head.Execution.Revision, len(head.Execution.Work), server.total())
			}
		})
	}
}

// A proposer error lands as a checkpointed failed phase and frees the session
// for the next turn.
func TestAssistantExecutionProposalErrorIsCheckpointedFailed(t *testing.T) {
	relay := newAssistantTestRelay()
	held := newAssistantHeldProposer()
	held.err = errors.New("model endpoint returned 503")
	close(held.release)
	st := newAssistantStack(t, relay, testAssistantSigner(t), newAssistantTestToolServer(relay.touch), assistantStackOptions{iterative: held})
	started, err := assistantStart(st, "s-error", "prompt-1", domain.AssistantWorkflowIterative)
	if err != nil {
		t.Fatal(err)
	}
	if settled := st.waitSettled(t, "s-error"); settled.Session.Phase != domain.AssistantExecutionFailed {
		t.Fatalf("phase %s", settled.Session.Phase)
	}
	head, err := st.store.Load(context.Background(), "s-error", started.Session.CurrentRunID)
	if err != nil || head.Execution.Phase != domain.AssistantExecutionFailed {
		t.Fatalf("journal head %+v err=%v", head.Execution.Phase, err)
	}
	if _, err := assistantStart(st, "s-error", "prompt-2", domain.AssistantWorkflowIterative); err != nil {
		t.Fatalf("next turn after a failed proposal: %v", err)
	}
	st.waitSettled(t, "s-error")
}

// Done-when: restart while proposing. Shutdown interrupts the model call,
// waits for it and records nothing; recovery ends the interrupted initial
// proposal failed (it dispatched nothing and its prompt is not in the
// record) and the session accepts a new turn.
func TestAssistantExecutionRestartWhileProposingRecovers(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	held := newAssistantHeldProposer()
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: held})
	started, err := assistantStart(first, "s-restart", "prompt-1", domain.AssistantWorkflowIterative)
	if err != nil {
		t.Fatal(err)
	}
	assistantReceive(t, held.entered, "model call")
	within(t, "shutdown", first.crash)
	assistantReceive(t, held.interrupted, "model call interrupted by shutdown")
	head, err := first.store.Load(context.Background(), "s-restart", started.Session.CurrentRunID)
	if err != nil || head.Execution.Revision != 1 || head.Execution.Phase != domain.AssistantExecutionProposing {
		t.Fatalf("shutdown recorded an outcome: rev=%d phase=%s err=%v", head.Execution.Revision, head.Execution.Phase, err)
	}

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: &assistantScriptedProposer{}})
	second.recover(t, relay)
	if x := second.snapshot("s-restart"); x.Phase != domain.AssistantExecutionFailed || len(x.Work) != 0 {
		t.Fatalf("recovered phase=%s work=%d", x.Phase, len(x.Work))
	}
	if _, err := assistantStart(second, "s-restart", "prompt-2", domain.AssistantWorkflowIterative); err != nil {
		t.Fatalf("new turn after recovery: %v", err)
	}
	if settled := second.waitSettled(t, "s-restart"); settled.Session.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("new turn phase %s", settled.Session.Phase)
	}
}

// A cancelled model call that only returns after a newer run started must not
// clear the newer run's cancel handle: cancelling the newer run still
// interrupts its own model call. Shutdown then leaves no goroutine behind.
func TestAssistantExecutionLateProposalCannotOrphanNewerRunCancel(t *testing.T) {
	relay := newAssistantTestRelay()
	held := newAssistantHeldProposer()
	held.ignoreCancel[0] = true // the first model call returns only when released, late
	held.untilCancel[1] = true  // the second returns only when cancelled
	st := newAssistantStack(t, relay, testAssistantSigner(t), newAssistantTestToolServer(relay.touch), assistantStackOptions{iterative: held})
	s := st.engine.session("s-late")

	first, err := assistantStart(st, "s-late", "prompt-1", domain.AssistantWorkflowIterative)
	if err != nil {
		t.Fatal(err)
	}
	assistantReceive(t, held.entered, "first model call")
	if _, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-late", first.Session.CurrentRunID, "cancel-1")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	firstCall := s.active
	s.mu.Unlock()
	if firstCall == nil || firstCall.runID != first.Session.CurrentRunID {
		t.Fatalf("first model call not active after cancel: %+v", firstCall)
	}
	second, err := assistantStart(st, "s-late", "prompt-2", domain.AssistantWorkflowIterative)
	if err != nil {
		t.Fatalf("new turn after cancel: %v", err)
	}
	assistantReceive(t, held.entered, "second model call")
	close(held.release)
	select {
	case <-firstCall.done:
	case <-time.After(10 * time.Second):
		t.Fatal("late first call never ended")
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active == nil || active.runID != second.Session.CurrentRunID {
		t.Fatalf("late first call cleared run 2's cancel handle: %+v", active)
	}
	if res, err := st.engine.Cancel(context.Background(), assistantCancelRequest("s-late", second.Session.CurrentRunID, "cancel-2")); err != nil || res.Session.Phase != domain.AssistantExecutionCancelled {
		t.Fatalf("cancel run 2 = %s err=%v", res.Session.Phase, err)
	}
	if run := assistantReceive(t, held.interrupted, "run 2 model call interrupted"); run != second.Session.CurrentRunID {
		t.Fatalf("interrupted %s", run)
	}
	st.waitSettled(t, "s-late")
	if x := st.snapshot("s-late"); x.RunID != second.Session.CurrentRunID || x.Phase != domain.AssistantExecutionCancelled || len(x.Work) != 0 {
		t.Fatalf("run 2 = %+v", x)
	}
	within(t, "shutdown", st.crash)
}
