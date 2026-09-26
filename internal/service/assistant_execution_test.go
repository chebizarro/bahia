package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

func assistantSyncAsyncSyncPlan() domain.AssistantPlan {
	return domain.AssistantPlan{Summary: "sync async sync", Steps: []domain.AssistantPlanStep{
		{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}},
		{StepID: "two", ToolName: "mutate", ToolArgs: map[string]any{"zone": "example.test"}},
		{StepID: "three", ToolName: "read-two", ToolArgs: map[string]any{}},
	}}
}

func assistantWait(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// Done-when: a batch with sync -> async -> sync work resumes after every
// service is reconstructed and executes each eligible item exactly once.
func TestAssistantExecutionBatchSyncAsyncSyncResumesAfterFullRestartExactlyOnce(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}})
	start := first.startBatch(t, "s-batch")
	if start.Session.Phase != domain.AssistantExecutionAwaitingApproval || server.total() != 0 {
		t.Fatalf("draft phase=%s calls=%d", start.Session.Phase, server.total())
	}
	first.approve(t, "s-batch", start)
	relay.waitFor(t, "async step submitted and observed", func() bool {
		return assistantWorkState(first.snapshot("s-batch"), 1) == domain.AssistantWorkWaitingAsync && relay.liveSubs(7961) == 1
	})
	if server.count("read-one") != 1 || server.count("mutate") != 1 || server.count("read-two") != 0 {
		t.Fatalf("before restart read-one=%d mutate=%d read-two=%d", server.count("read-one"), server.count("mutate"), server.count("read-two"))
	}
	first.crash()

	// The downstream operation completes while no assistant process runs.
	keys := server.keysFor("mutate")
	runID := start.Session.CurrentRunID
	x := first.snapshot("s-batch")
	if len(keys) != 1 || keys[0] != "assistant:s-batch:"+x.Proposal.Hash+":two" {
		t.Fatalf("executor-issued key=%v", keys)
	}
	publishAssistantResult(t, relay, assistantTestRequestID(keys[0]), "completed")

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	second.recover(t, relay)
	relay.waitFor(t, "batch completed after restart", func() bool { return second.snapshot("s-batch").Phase == domain.AssistantExecutionCompleted })
	x = second.snapshot("s-batch")
	if server.count("read-one") != 1 || server.count("mutate") != 1 || server.count("read-two") != 1 {
		t.Fatalf("after restart read-one=%d mutate=%d read-two=%d", server.count("read-one"), server.count("mutate"), server.count("read-two"))
	}
	if x.Cursor != 3 || x.RunID != runID {
		t.Fatalf("cursor=%d run=%s", x.Cursor, x.RunID)
	}
	for i, w := range x.Work {
		if w.State != domain.AssistantWorkSucceeded {
			t.Fatalf("work %d state=%s", i, w.State)
		}
	}
	observations := second.transcriptObservations(t, "s-batch")
	if len(observations) != 3 {
		t.Fatalf("transcript observations=%d", len(observations))
	}
	for i, want := range []string{"read-one", "mutate", "read-two"} {
		if observations[i].Payload.Message.Name != want {
			t.Fatalf("transcript order[%d]=%s", i, observations[i].Payload.Message.Name)
		}
	}
	revisions := assistantCheckpointRevisions(relay, runID)
	second.crash()

	// A further full restart replays the journal without any effect.
	third := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	third.recover(t, relay)
	if err := third.engine.Recover(context.Background(), AssistantExecutionReference{SessionID: "s-batch", RunID: runID}); err != nil {
		t.Fatal(err)
	}
	third.crash()
	if server.total() != 3 || len(third.transcriptObservations(t, "s-batch")) != 3 || len(assistantCheckpointRevisions(relay, runID)) != len(revisions) {
		t.Fatalf("replay caused effects: calls=%d", server.total())
	}
}

// Done-when: a multi-call iterative response survives suspension (and a full
// restart) without dropping its tail.
func TestAssistantExecutionIterativeMultiCallTailSurvivesSuspensionAndRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	proposer := &assistantScriptedProposer{responses: []AssistantProposal{
		{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "first", Name: "mutate", Arguments: map[string]any{"idempotency_key": "model-supplied"}}, {ID: "tail", Name: "read-two", Arguments: map[string]any{}}}},
		{Kind: AssistantProposalFinal, Text: "done"},
	}}
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer})
	first.startIterative(t, "s-iter")
	relay.waitFor(t, "first call suspended on async result", func() bool {
		return assistantWorkState(first.snapshot("s-iter"), 0) == domain.AssistantWorkWaitingAsync && relay.liveSubs(7961) == 1
	})
	x := first.snapshot("s-iter")
	if len(x.Work) != 2 || x.Work[1].State != domain.AssistantWorkReady || server.count("read-two") != 0 || proposer.calls() != 1 {
		t.Fatalf("tail not checkpointed before suspension: %+v read-two=%d", x.Work, server.count("read-two"))
	}
	if keys := server.keysFor("mutate"); len(keys) != 1 || keys[0] != "assistant-agent:s-iter:"+x.RunID+":first" {
		t.Fatalf("model-supplied key was not replaced: %v", keys)
	}
	first.crash()

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer})
	second.recover(t, relay)
	relay.waitFor(t, "observer resubscribed after restart", func() bool { return relay.liveSubs(7961) == 1 })
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
	relay.waitFor(t, "iterative run completed", func() bool { return second.snapshot("s-iter").Phase == domain.AssistantExecutionCompleted })
	x = second.snapshot("s-iter")
	if server.count("mutate") != 1 || server.count("read-two") != 1 || proposer.calls() != 2 {
		t.Fatalf("mutate=%d read-two=%d proposals=%d", server.count("mutate"), server.count("read-two"), proposer.calls())
	}
	if len(x.Work) != 2 || x.Work[0].OriginID != "first" || x.Work[1].OriginID != "tail" || x.Work[0].State != domain.AssistantWorkSucceeded || x.Work[1].State != domain.AssistantWorkSucceeded {
		t.Fatalf("work=%+v", x.Work)
	}
	if req := proposer.requests[1]; req.RunID != x.RunID || req.Prompt != "" {
		t.Fatalf("continuation request=%+v", req)
	}
}

// Done-when: replayed terminal events do not duplicate observations or
// transcript entries, and do not advance the cursor twice.
func TestAssistantExecutionReplayedTerminalEventsDoNotDuplicate(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "two", ToolName: "mutate", ToolArgs: map[string]any{}}, {StepID: "three", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	start := st.startBatch(t, "s-replay")
	st.approve(t, "s-replay", start)
	relay.waitFor(t, "waiting on async", func() bool { return relay.liveSubs(7961) == 1 })
	ref := AssistantExecutionReference{SessionID: "s-replay", RunID: start.Session.CurrentRunID}
	for i := 0; i < 2; i++ {
		if err := st.engine.Recover(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
	if n := relay.liveSubs(7961); n != 1 {
		t.Fatalf("repeated recovery opened %d observers", n)
	}
	requestID := assistantTestRequestID(server.keysFor("mutate")[0])
	terminal := publishAssistantResult(t, relay, requestID, "completed")
	if _, err := relay.Publish(context.Background(), *terminal); err != nil {
		t.Fatal(err)
	}
	publishAssistantResult(t, relay, requestID, "failed")
	relay.waitFor(t, "completed", func() bool { return st.snapshot("s-replay").Phase == domain.AssistantExecutionCompleted })
	x := st.snapshot("s-replay")
	if x.Work[0].Observation.EventID != terminal.ID.Hex() || x.Work[0].State != domain.AssistantWorkSucceeded || server.count("read-two") != 1 || x.Cursor != 2 {
		t.Fatalf("work=%+v read-two=%d", x.Work, server.count("read-two"))
	}
	revisions := assistantCheckpointRevisions(relay, x.RunID)
	for i, rev := range revisions {
		if rev != uint64(i+1) {
			t.Fatalf("checkpoint revisions not a single chain: %v", revisions)
		}
	}
	st.crash()

	fresh := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	if err := fresh.engine.Recover(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Publish(context.Background(), *terminal); err != nil {
		t.Fatal(err)
	}
	fresh.crash()
	if got := assistantCheckpointRevisions(relay, x.RunID); len(got) != len(revisions) {
		t.Fatalf("replay advanced the journal: %v -> %v", revisions, got)
	}
	if n := len(fresh.transcriptObservations(t, "s-replay")); n != 2 || server.total() != 2 {
		t.Fatalf("transcript=%d calls=%d", n, server.total())
	}
}

// A crash between the transcript append and the consumed checkpoint replays
// consumption; the logical transcript identity prevents a duplicate entry and
// the cursor advances once.
func TestAssistantExecutionConsumeReplayAfterTranscriptAppendIsIdempotent(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	transcriptSeen, rejected := false, false
	relay.setReject(func(ev nostr.Event) error {
		if ev.Kind == nostr.Kind(domain.KindAssistantTranscript) {
			transcriptSeen = true
			return nil
		}
		if ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) && transcriptSeen && !rejected {
			rejected = true
			return errors.New("rate-limited: slow down")
		}
		return nil
	})
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	start := first.startBatch(t, "s-consume")
	first.approve(t, "s-consume", start)
	relay.waitFor(t, "consumed checkpoint rejected", func() bool { return first.faulted("s-consume") })
	if server.count("read-two") != 0 {
		t.Fatal("successor ran after a failed checkpoint")
	}
	first.crash()
	relay.setReject(nil)

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	second.recover(t, relay)
	relay.waitFor(t, "completed", func() bool { return second.snapshot("s-consume").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 || server.count("read-two") != 1 {
		t.Fatalf("read-one=%d read-two=%d", server.count("read-one"), server.count("read-two"))
	}
	if n := len(relay.published(nostr.Kind(domain.KindAssistantTranscript))); n != 2 {
		t.Fatalf("transcript publications=%d, want one per work item", n)
	}
	if n := len(second.transcriptObservations(t, "s-consume")); n != 2 {
		t.Fatalf("transcript observations=%d", n)
	}
}

// The v2 projection is replaceable: its created_at must strictly increase per
// session, even for transitions within one second and across restarts.
func TestAssistantExecutionProjectionCreatedAtStrictlyIncreasesAcrossRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	projections := func() []nostr.Event {
		out := []nostr.Event{}
		for _, ev := range relay.published(nostr.Kind(domain.KindAssistantSessionState)) {
			if tagValue(ev.Tags, "d") == assistantProjectionCoordinate("s-clock") {
				out = append(out, ev)
			}
		}
		return out
	}
	assertIncreasing := func(evs []nostr.Event) {
		t.Helper()
		for i := 1; i < len(evs); i++ {
			if evs[i].CreatedAt <= evs[i-1].CreatedAt {
				t.Fatalf("projection %d created_at %d not after %d", i, evs[i].CreatedAt, evs[i-1].CreatedAt)
			}
		}
	}
	// Every transition happens at the same simulated second.
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	start := first.startBatch(t, "s-clock")
	first.approve(t, "s-clock", start)
	relay.waitFor(t, "completed", func() bool { return first.snapshot("s-clock").Phase == domain.AssistantExecutionCompleted })
	evs := projections()
	if len(evs) < 6 {
		t.Fatalf("projections=%d", len(evs))
	}
	assertIncreasing(evs)
	if evs[len(evs)-1].CreatedAt <= nostr.Timestamp(assistantTestClock().Unix()) {
		t.Fatal("burst did not advance the clock beyond wall-clock")
	}
	first.crash()

	// Restart through recovery: hydration seeds the clock.
	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	second.recover(t, relay)
	before := len(projections())
	res, err := second.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-clock", TurnID: "turn-2", Prompt: "again"}, OperatorPubkey: "operator", RequestEventID: "prompt-2", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Session.Proposal
	if _, err = second.engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-clock", RunID: res.Session.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, Decision: "reject"}, OperatorPubkey: "operator", RequestEventID: "reject-2"}); err != nil {
		t.Fatal(err)
	}
	evs = projections()
	if len(evs) <= before {
		t.Fatal("no projection after restart")
	}
	assertIncreasing(evs)
	second.crash()

	// Restart without recovery hydration: the scoped lookup seeds the clock.
	third := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	before = len(projections())
	if _, err = third.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-clock", TurnID: "turn-3", Prompt: "third"}, OperatorPubkey: "operator", RequestEventID: "prompt-3", DefaultWorkflow: domain.AssistantWorkflowBatch}); err != nil {
		t.Fatal(err)
	}
	evs = projections()
	if len(evs) <= before {
		t.Fatal("no projection after unhydrated restart")
	}
	assertIncreasing(evs)
	// The relay converges on the newest phase, not an earlier same-second one.
	stored := relay.eventsOfKind(nostr.Kind(domain.KindAssistantSessionState))
	var latest domain.AssistantSessionV2
	for _, ev := range stored {
		if tagValue(ev.Tags, "d") == assistantProjectionCoordinate("s-clock") {
			mustUnmarshalEventContent(t, &ev, &latest)
		}
	}
	if latest.Phase != domain.AssistantExecutionAwaitingApproval || latest.CurrentTurnID != "turn-3" {
		t.Fatalf("relay converged on phase=%s turn=%s", latest.Phase, latest.CurrentTurnID)
	}
}

func TestAssistantExecutionPhaseDerivationAndTransitions(t *testing.T) {
	x := domain.AssistantExecution{Workflow: domain.AssistantWorkflowBatch, Work: []domain.AssistantWorkItem{{State: domain.AssistantWorkSucceeded}, {State: domain.AssistantWorkUncertain}, {State: domain.AssistantWorkReady}}}
	if got := assistantDerivePhase(x); got != domain.AssistantExecutionBlocked {
		t.Fatalf("uncertain must block successors, got %s", got)
	}
	x.Cancellation = &domain.AssistantExecutionCancellation{Scope: "run"}
	if got := assistantDerivePhase(x); got != domain.AssistantExecutionCancelling {
		t.Fatalf("uncertain keeps cancellation accounting open, got %s", got)
	}
	if !strings.Contains(validateAssistantExecutionTransition(
		domain.AssistantExecution{RunID: "r", Work: []domain.AssistantWorkItem{{WorkID: "w", State: domain.AssistantWorkUncertain}}},
		domain.AssistantExecution{RunID: "r", Phase: domain.AssistantExecutionExecuting, Work: []domain.AssistantWorkItem{{WorkID: "w", State: domain.AssistantWorkReady}}},
	).Error(), "not allowed") {
		t.Fatal("uncertain work must never return to ready for redispatch")
	}
}

func TestAssistantExecutionResultsDoNotAliasEngineState(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "a", ToolName: "mutate", ToolArgs: map[string]any{"zone": "original"}}}}
	st := newAssistantStack(t, relay, testAssistantSigner(t), server, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	res := st.startBatch(t, "s-alias")
	res.Session.Proposal.Plan.Steps[0].ToolArgs["zone"] = "tampered"
	if got := st.snapshot("s-alias").Proposal.Plan.Steps[0].ToolArgs["zone"]; got != "original" {
		t.Fatalf("caller mutated engine proposal: %v", got)
	}
}
