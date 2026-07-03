package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"

	"github.com/openagentsinc/bahia/internal/domain"
)

const assistantTestTool = "deploy_service"

func TestAssistantOrchestratorPromptPublishesPlanWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, &assistantTestChatClient{plan: plan}, nil, nil)

	result, err := orchestrator.HandlePromptRequest(ctx, assistantPromptSource("session-1", "turn-1"), assistantPromptRequest("session-1", "turn-1", "deploy api"))
	if err != nil {
		t.Fatalf("HandlePromptRequest: %v", err)
	}

	if got := result["status"]; got != "planned" {
		t.Fatalf("result status = %q, want planned", got)
	}
	if result["plan_hash"] == "" {
		t.Fatalf("planned result missing plan_hash: %#v", result)
	}
	if invoker.callCount() != 0 {
		t.Fatalf("tool invoker called during planning: %d", invoker.callCount())
	}
}

func TestAssistantOrchestratorPlanFromPromptHonorsStreamingToggle(t *testing.T) {
	tests := []struct {
		name             string
		streamingEnabled bool
		wantNonStreaming int
		wantStreaming    int
	}{
		{name: "disabled uses non-streaming even when client supports streaming", wantNonStreaming: 1},
		{name: "enabled uses streaming", streamingEnabled: true, wantStreaming: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chat := &assistantStreamingRoutingChatClient{plan: executableTestPlan()}
			orchestrator := NewAssistantOrchestrator(AssistantOrchestratorConfig{
				ChatClient:       chat,
				StreamingEnabled: tc.streamingEnabled,
				AllowedToolNames: []string{assistantTestTool},
				ContextBuilder:   assistantTestContextBuilder{},
				ToolInvoker:      &assistantTestToolInvoker{},
				Publisher:        &assistantTestPublisher{},
				Signer:           testAssistantSigner(t),
				Identity:         AssistantIdentity{AgentID: "assistant-test", Pubkey: "assistant-pubkey"},
				InitialSessions:  nil,
				AgenticEnabled:   false,
				AgentLoop:        nil,
				Subscriber:       nil,
			})

			plan, err := orchestrator.planFromPrompt(context.Background(), nil, "session-1", "system", "user")
			if err != nil {
				t.Fatalf("planFromPrompt: %v", err)
			}
			if plan == nil || plan.Summary != "Deploy the API service." {
				t.Fatalf("plan = %#v", plan)
			}
			if chat.nonStreamingCalls != tc.wantNonStreaming {
				t.Fatalf("non-streaming calls = %d, want %d", chat.nonStreamingCalls, tc.wantNonStreaming)
			}
			if chat.streamingCalls != tc.wantStreaming {
				t.Fatalf("streaming calls = %d, want %d", chat.streamingCalls, tc.wantStreaming)
			}
		})
	}
}

func TestAssistantOrchestratorRejectsStaleApprovalAsFailed(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	latestHash := domain.ComputePlanHash(*plan, "session-1")
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, nil, nil, []domain.AssistantSession{{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateAwaitingApproval,
		OperatorPubkey: "operator",
		LastPlanHash:   latestHash,
		CurrentPlan:    plan,
		PendingSteps:   plan.Steps,
	}})

	result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-1"), assistantApprovalRequest("session-1", "stale-hash", "approve"))
	if err != nil {
		t.Fatalf("HandleApprovalRequest: %v", err)
	}

	if got := result["status"]; got != "failed" {
		t.Fatalf("result status = %q, want failed", got)
	}
	if got := result["step"]; got != "stale_approval" {
		t.Fatalf("result step = %q, want stale_approval", got)
	}
	if invoker.callCount() != 0 {
		t.Fatalf("stale approval dispatched tool calls: %d", invoker.callCount())
	}
}

func TestAssistantOrchestratorSuppressesDuplicateApprovalWhileExecuting(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	planHash := domain.ComputePlanHash(*plan, "session-1")
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{receipt: &domain.AsyncToolReceipt{ToolName: assistantTestTool, RequestEventID: "downstream-1", RequestKind: 5961, ResultKinds: []int{7961}, IdempotencyKey: "placeholder"}}
	subscriber := newBlockingAssistantTestSubscriber()
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, nil, subscriber, []domain.AssistantSession{{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateAwaitingApproval,
		OperatorPubkey: "operator",
		LastPlanHash:   planHash,
		CurrentPlan:    plan,
		PendingSteps:   plan.Steps,
	}})

	firstDone := make(chan assistantApprovalCallResult, 1)
	go func() {
		result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-1"), assistantApprovalRequest("session-1", planHash, "approve"))
		firstDone <- assistantApprovalCallResult{result: result, err: err}
	}()
	subscriber.waitForSubscription(t)

	secondDone := make(chan assistantApprovalCallResult, 1)
	go func() {
		result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-2"), assistantApprovalRequest("session-1", planHash, "approve"))
		secondDone <- assistantApprovalCallResult{result: result, err: err}
	}()
	second := waitApprovalResult(t, secondDone)
	if second.err != nil {
		t.Fatalf("second HandleApproval: %v", second.err)
	}
	if got := second.result["status"]; got != "executing" {
		t.Fatalf("second result status = %q, want executing", got)
	}
	if got := second.result["step"]; got != "already_submitted" {
		t.Fatalf("second result step = %q, want already_submitted", got)
	}
	if got := invoker.callCount(); got != 1 {
		t.Fatalf("tool invocations before downstream result = %d, want 1", got)
	}

	subscriber.publishResult(assistantSignedResultEvent(t, "result-1", 7961, "downstream-1", "completed"))

	first := waitApprovalResult(t, firstDone)
	if first.err != nil {
		t.Fatalf("first HandleApproval: %v", first.err)
	}
	if got := invoker.callCount(); got != 1 {
		t.Fatalf("tool invocations after downstream result = %d, want 1", got)
	}
}

func TestDownstreamResultMatchesReceiptRejectsWrongCorrelation(t *testing.T) {
	receipt := &domain.AsyncToolReceipt{RequestEventID: "downstream-1", ResultKinds: []int{7961}}
	event := assistantSignedResultEvent(t, "result-wrong-correlation", 7961, "other-request", "completed")

	if downstreamResultMatchesReceipt(event, receipt) {
		t.Fatal("downstreamResultMatchesReceipt accepted terminal event with wrong #e correlation")
	}
}

func TestAssistantOrchestratorAcknowledgesDuplicateApprovalForExecutingSubmittedPlan(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	planHash := domain.ComputePlanHash(*plan, "session-1")
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, nil, nil, []domain.AssistantSession{{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateExecuting,
		OperatorPubkey: "operator",
		LastPlanHash:   planHash,
		CurrentPlan:    plan,
		PendingSteps:   plan.Steps,
	}})
	orchestrator.markPlanSubmitted("session-1:" + planHash)

	result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-duplicate"), assistantApprovalRequest("session-1", planHash, "approve"))
	if err != nil {
		t.Fatalf("HandleApprovalRequest: %v", err)
	}

	if got := result["status"]; got != "executing" {
		t.Fatalf("result status = %q, want executing", got)
	}
	if got := result["step"]; got != "already_submitted" {
		t.Fatalf("result step = %q, want already_submitted", got)
	}
	if got := invoker.callCount(); got != 0 {
		t.Fatalf("duplicate executing approval dispatched tool calls: %d", got)
	}
}

func TestAssistantOrchestratorRejectsExecutingApprovalWithoutSubmittedPlan(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	planHash := domain.ComputePlanHash(*plan, "session-1")
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, nil, nil, []domain.AssistantSession{{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateExecuting,
		OperatorPubkey: "operator",
		LastPlanHash:   planHash,
		CurrentPlan:    plan,
		PendingSteps:   plan.Steps,
	}})

	result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-duplicate"), assistantApprovalRequest("session-1", planHash, "approve"))
	if err != nil {
		t.Fatalf("HandleApprovalRequest: %v", err)
	}

	if got := result["status"]; got != "failed" {
		t.Fatalf("result status = %q, want failed", got)
	}
	if got := result["step"]; got != "invalid_state" {
		t.Fatalf("result step = %q, want invalid_state", got)
	}
	if got := invoker.callCount(); got != 0 {
		t.Fatalf("executing approval without submitted marker dispatched tool calls: %d", got)
	}
}

func TestAssistantOrchestratorCancelMovesExecutingSessionToFailed(t *testing.T) {
	ctx := context.Background()
	plan := executableTestPlan()
	planHash := domain.ComputePlanHash(*plan, "session-1")
	publisher := &assistantTestPublisher{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, &assistantTestToolInvoker{}, nil, nil, []domain.AssistantSession{{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateExecuting,
		OperatorPubkey: "operator",
		LastPlanHash:   planHash,
		CurrentPlan:    plan,
		PendingSteps:   plan.Steps,
	}})

	result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-1"), assistantApprovalRequest("session-1", planHash, "cancel"))
	if err != nil {
		t.Fatalf("HandleApprovalRequest: %v", err)
	}

	if got := result["status"]; got != "failed" {
		t.Fatalf("result status = %q, want failed", got)
	}
	session := lastAssistantEvent(t, publisher.eventsOfKind(domain.KindAssistantSessionState))
	if got := tagValue(session.Tags, "status"); got != string(domain.AssistantSessionStateFailed) {
		t.Fatalf("session status = %q, want failed", got)
	}
}

func TestAssistantOrchestratorReadOnlyPlanCompletesWithoutAwaitingApproval(t *testing.T) {
	ctx := context.Background()
	plan := &domain.AssistantPlan{Summary: "No action is needed.", RiskLevel: "low", Steps: []domain.AssistantPlanStep{}}
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, &assistantTestChatClient{plan: plan}, nil, nil)

	result, err := orchestrator.HandlePromptRequest(ctx, assistantPromptSource("session-1", "turn-1"), assistantPromptRequest("session-1", "turn-1", "what is happening?"))
	if err != nil {
		t.Fatalf("HandlePromptRequest: %v", err)
	}

	if got := result["status"]; got != "completed" {
		t.Fatalf("result status = %q, want completed", got)
	}
	for _, ev := range publisher.eventsOfKind(domain.KindAssistantSessionState) {
		if got := tagValue(ev.Tags, "status"); got == string(domain.AssistantSessionStateAwaitingApproval) {
			t.Fatalf("read-only turn published awaiting_approval session: %#v", ev.Tags)
		}
	}
	if invoker.callCount() != 0 {
		t.Fatalf("read-only turn dispatched tool calls: %d", invoker.callCount())
	}
}

func TestAssistantOrchestratorAgenticPromptUsesLoopWhenFlagEnabled(t *testing.T) {
	ctx := context.Background()
	loop := &assistantFakeAgentLoop{startResult: &AssistantAgentLoopResult{RunID: "run-1", TurnID: "turn-1", Iteration: 1, State: domain.AssistantAgentLoopStateCompleted, SessionState: domain.AssistantSessionStateCompleted, Completed: true}}
	chat := &assistantTestChatClient{plan: executableTestPlan()}
	orchestrator := NewAssistantOrchestrator(AssistantOrchestratorConfig{
		ChatClient:       chat,
		ContextBuilder:   assistantTestContextBuilder{},
		ToolInvoker:      &assistantTestToolInvoker{},
		Publisher:        &assistantTestPublisher{},
		Signer:           testAssistantSigner(t),
		Identity:         AssistantIdentity{AgentID: "assistant-test", Pubkey: "assistant-pubkey"},
		AllowedToolNames: []string{assistantTestTool},
		AgenticEnabled:   true,
		AgentLoop:        loop,
	})

	result, err := orchestrator.HandlePromptRequest(ctx, assistantPromptSource("session-1", "turn-1"), assistantPromptRequest("session-1", "turn-1", "inspect dns"))
	if err != nil {
		t.Fatalf("HandlePromptRequest: %v", err)
	}
	if loop.startCalls != 1 {
		t.Fatalf("loop StartTurn calls = %d, want 1", loop.startCalls)
	}
	if got := loop.lastStart.Prompt; got != "inspect dns" {
		t.Fatalf("loop prompt = %q", got)
	}
	if got := result["status"]; got != string(domain.AssistantSessionStateCompleted) {
		t.Fatalf("result status = %q, want completed", got)
	}
	if got := result["agentic"]; got != true {
		t.Fatalf("result agentic = %#v, want true", got)
	}
	if got := result["run_id"]; got != "run-1" {
		t.Fatalf("run_id = %q, want run-1", got)
	}
}

func TestAssistantOrchestratorActionIDApprovalResumesLoop(t *testing.T) {
	ctx := context.Background()
	loop := &assistantFakeAgentLoop{decisionResult: &AssistantAgentLoopResult{RunID: "run-1", TurnID: "turn-1", Iteration: 2, State: domain.AssistantAgentLoopStateWaitingAsync, SessionState: domain.AssistantSessionStateExecuting, Suspended: true}}
	orchestrator := NewAssistantOrchestrator(AssistantOrchestratorConfig{
		ChatClient:     &assistantTestChatClient{plan: executableTestPlan()},
		Publisher:      &assistantTestPublisher{},
		Signer:         testAssistantSigner(t),
		Identity:       AssistantIdentity{AgentID: "assistant-test", Pubkey: "assistant-pubkey"},
		AgenticEnabled: true,
		AgentLoop:      loop,
		InitialSessions: []domain.AssistantSession{{
			SessionID:      "session-1",
			State:          domain.AssistantSessionStateAwaitingApproval,
			OperatorPubkey: "operator",
			Participants:   []string{"operator"},
		}},
	})

	req := domain.AssistantApprovalRequest{SessionID: "session-1", ActionID: "action-1", Decision: "approve", Reason: "ok"}
	result, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "action-1"), req)
	if err != nil {
		t.Fatalf("HandleApprovalRequest: %v", err)
	}
	if loop.decisionCalls != 1 {
		t.Fatalf("loop action-decision calls = %d, want 1", loop.decisionCalls)
	}
	if got := loop.lastDecision.ActionID; got != "action-1" {
		t.Fatalf("action_id = %q, want action-1", got)
	}
	if got := result["status"]; got != string(domain.AssistantSessionStateExecuting) {
		t.Fatalf("result status = %q, want executing", got)
	}
	if got := result["action_id"]; got != "action-1" {
		t.Fatalf("result action_id = %q, want action-1", got)
	}
}

func TestAssistantOrchestratorRejectsUnknownToolAtPlanningTime(t *testing.T) {
	ctx := context.Background()
	plan := &domain.AssistantPlan{Summary: "Use unsafe tool.", RiskLevel: "medium", Steps: []domain.AssistantPlanStep{{StepID: "s1", Title: "Unsafe", ToolName: "shell_exec", ToolArgs: map[string]any{"cmd": "date"}}}}
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, &assistantTestChatClient{plan: plan}, nil, nil)

	result, err := orchestrator.HandlePromptRequest(ctx, assistantPromptSource("session-1", "turn-1"), assistantPromptRequest("session-1", "turn-1", "run a command"))
	if err != nil {
		t.Fatalf("HandlePromptRequest: %v", err)
	}

	if got := result["status"]; got != "failed" {
		t.Fatalf("result status = %q, want failed", got)
	}
	if got := result["step"]; got != "plan_validation_error" {
		t.Fatalf("result step = %q, want plan_validation_error", got)
	}
	if invoker.callCount() != 0 {
		t.Fatalf("invalid plan dispatched tool calls: %d", invoker.callCount())
	}
}

func TestAssistantOrchestratorPromptReturnsLLMErrorDetail(t *testing.T) {
	ctx := context.Background()
	publisher := &assistantTestPublisher{}
	invoker := &assistantTestToolInvoker{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, invoker, &assistantTestChatClient{err: errors.New("upstream 503: model overloaded")}, nil, nil)

	result, err := orchestrator.HandlePromptRequest(ctx, assistantPromptSource("session-1", "turn-1"), assistantPromptRequest("session-1", "turn-1", "deploy api"))
	if err != nil {
		t.Fatalf("HandlePromptRequest: %v", err)
	}

	if got := result["status"]; got != "failed" {
		t.Fatalf("result status = %q, want failed", got)
	}
	if got := result["summary"]; got != "assistant planning failed" {
		t.Fatalf("summary = %q, want assistant planning failed", got)
	}
	if got := result["error"]; got != "upstream 503: model overloaded" {
		t.Fatalf("error detail = %q", got)
	}
	if invoker.callCount() != 0 {
		t.Fatalf("llm failure dispatched tool calls: %d", invoker.callCount())
	}
}

func TestAssistantOrchestratorSystemPromptIncludesDNSGuidance(t *testing.T) {
	orchestrator := NewAssistantOrchestrator(AssistantOrchestratorConfig{AllowedToolNames: []string{
		"bahia_assistant_dns_policy_apply",
		"bahia_assistant_dns_zone_create",
		"bahia_assistant_dns_record_override",
		"bahia_assistant_dns_drift_remediate",
		"bahia_assistant_dns_list_endpoints",
		"bahia_assistant_dns_list_drift",
	}})

	got := orchestrator.systemPrompt()
	for _, want := range []string{
		"Address the operator directly with second-person pronouns (you/your)",
		"never describe the operator in third person",
		"DNS intent mapping:",
		"\"expose X internally only\" → bahia_assistant_dns_policy_apply with split-horizon visibility=internal",
		"\"add DNS for X\" / \"create zone\" → bahia_assistant_dns_zone_create",
		"\"override X to point to Y\" → bahia_assistant_dns_record_override",
		"\"fix drift\" / \"remediate\" → bahia_assistant_dns_drift_remediate",
		"\"show endpoints\" / \"list DNS\" → bahia_assistant_dns_list_endpoints",
		"\"show drift\" → bahia_assistant_dns_list_drift",
		"Generate a UUID v4 idempotency_key for each mutation tool call.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, got)
		}
	}
}

func newTestAssistantOrchestrator(t *testing.T, publisher *assistantTestPublisher, invoker *assistantTestToolInvoker, chat *assistantTestChatClient, subscriber AssistantRelaySubscriber, sessions []domain.AssistantSession) *AssistantOrchestrator {
	t.Helper()
	if chat == nil {
		chat = &assistantTestChatClient{plan: executableTestPlan()}
	}
	if publisher == nil {
		publisher = &assistantTestPublisher{}
	}
	if invoker == nil {
		invoker = &assistantTestToolInvoker{}
	}
	return NewAssistantOrchestrator(AssistantOrchestratorConfig{
		ChatClient:       chat,
		ContextBuilder:   assistantTestContextBuilder{},
		ToolInvoker:      invoker,
		Publisher:        publisher,
		Subscriber:       subscriber,
		Signer:           testAssistantSigner(t),
		Identity:         AssistantIdentity{AgentID: "assistant-test", Pubkey: "assistant-pubkey"},
		AllowedToolNames: []string{assistantTestTool},
		InitialSessions:  sessions,
	})
}

func executableTestPlan() *domain.AssistantPlan {
	return &domain.AssistantPlan{
		Summary:   "Deploy the API service.",
		RiskLevel: "medium",
		Steps: []domain.AssistantPlanStep{{
			StepID:      "step-1",
			Title:       "Deploy API",
			Description: "Deploy api to staging",
			ToolName:    assistantTestTool,
			ToolArgs:    map[string]any{"service": "api", "environment": "staging"},
		}},
	}
}

func assistantPromptRequest(sessionID, turnID, prompt string) domain.AssistantPromptRequest {
	return domain.AssistantPromptRequest{SessionID: sessionID, TurnID: turnID, Prompt: prompt}
}

func assistantPromptSource(sessionID, turnID string) AssistantRequestSource {
	id := "prompt-" + turnID
	return AssistantRequestSource{Event: &nostr.Event{ID: assistantTestID(id), PubKey: assistantTestPubKey("operator"), Kind: 25910}, OperatorPubkey: "operator", RequestID: id, DedupKey: "assistant-turn:" + sessionID + ":" + turnID}
}

func assistantApprovalRequest(sessionID, planHash, decision string) domain.AssistantApprovalRequest {
	return domain.AssistantApprovalRequest{SessionID: sessionID, PlanHash: planHash, Decision: decision}
}

func assistantApprovalSource(sessionID, suffix string) AssistantRequestSource {
	id := "approval-" + suffix
	return AssistantRequestSource{Event: &nostr.Event{ID: assistantTestID(id), PubKey: assistantTestPubKey("operator"), Kind: 25910}, OperatorPubkey: "operator", RequestID: id, DedupKey: "assistant-approval:" + sessionID + ":" + suffix}
}

func testAssistantSigner(t *testing.T) canonicalnostr.Signer {
	t.Helper()
	secret := nostr.Generate()
	return keyer.NewPlainKeySigner([32]byte(secret))
}

func assistantTestID(label string) nostr.ID {
	sum := sha256.Sum256([]byte(label))
	return nostr.ID(sum)
}

func assistantSignedResultEvent(t *testing.T, label string, kind int, requestEventID string, status string) *nostr.Event {
	t.Helper()
	secret := nostr.Generate()
	event := &nostr.Event{
		Kind:      nostr.Kind(kind),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"e", requestEventID}, {"status", status}},
		Content:   `{"status":"` + status + `"}`,
	}
	if err := event.Sign(secret); err != nil {
		t.Fatalf("sign %s result event: %v", label, err)
	}
	return event
}

func assistantTestPubKey(label string) nostr.PubKey {
	sum := sha256.Sum256([]byte(label))
	return nostr.PubKey(sum)
}

type assistantFakeAgentLoop struct {
	startCalls     int
	lastStart      AssistantAgentTurnRequest
	startResult    *AssistantAgentLoopResult
	startErr       error
	asyncCalls     int
	lastAsync      AssistantAgentResumeAsyncRequest
	asyncResult    *AssistantAgentLoopResult
	asyncErr       error
	decisionCalls  int
	lastDecision   AssistantAgentActionDecisionRequest
	decisionResult *AssistantAgentLoopResult
	decisionErr    error
}

func (l *assistantFakeAgentLoop) StartTurn(_ context.Context, req AssistantAgentTurnRequest) (*AssistantAgentLoopResult, error) {
	l.startCalls++
	l.lastStart = req
	return l.startResult, l.startErr
}

func (l *assistantFakeAgentLoop) ResumeAfterAsyncObservation(_ context.Context, req AssistantAgentResumeAsyncRequest) (*AssistantAgentLoopResult, error) {
	l.asyncCalls++
	l.lastAsync = req
	return l.asyncResult, l.asyncErr
}

func (l *assistantFakeAgentLoop) ResumeAfterActionDecision(_ context.Context, req AssistantAgentActionDecisionRequest) (*AssistantAgentLoopResult, error) {
	l.decisionCalls++
	l.lastDecision = req
	return l.decisionResult, l.decisionErr
}

type assistantTestChatClient struct {
	plan *domain.AssistantPlan
	err  error
}

func (c *assistantTestChatClient) PlanFromPrompt(context.Context, string, string) (*domain.AssistantPlan, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.plan, nil
}

type assistantStreamingRoutingChatClient struct {
	plan              *domain.AssistantPlan
	nonStreamingCalls int
	streamingCalls    int
}

func (c *assistantStreamingRoutingChatClient) PlanFromPrompt(context.Context, string, string) (*domain.AssistantPlan, error) {
	c.nonStreamingCalls++
	return c.plan, nil
}

func (c *assistantStreamingRoutingChatClient) PlanFromPromptStreaming(context.Context, string, string, func(string)) (*domain.AssistantPlan, error) {
	c.streamingCalls++
	return c.plan, nil
}

type assistantTestContextBuilder struct{}

func (assistantTestContextBuilder) BuildContext(context.Context, map[string]string, []string, string) (string, error) {
	return "test context", nil
}

type assistantTestPublisher struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (p *assistantTestPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return 1, nil
}

func (p *assistantTestPublisher) eventsOfKind(kind int) []nostr.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]nostr.Event, 0)
	for _, ev := range p.events {
		if ev.Kind == nostr.Kind(kind) {
			out = append(out, ev)
		}
	}
	return out
}

type assistantTestToolInvoker struct {
	mu      sync.Mutex
	calls   []assistantToolCall
	receipt *domain.AsyncToolReceipt
}

type assistantToolCall struct {
	name string
	args map[string]interface{}
}

func (i *assistantTestToolInvoker) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	i.mu.Lock()
	i.calls = append(i.calls, assistantToolCall{name: name, args: args})
	receipt := i.receipt
	i.mu.Unlock()
	if receipt == nil {
		receipt = &domain.AsyncToolReceipt{ToolName: name, RequestEventID: "downstream-1", RequestKind: 5961, ResultKinds: []int{7961}}
	}
	copyReceipt := *receipt
	copyReceipt.ToolName = name
	if key, _ := args["idempotency_key"].(string); key != "" {
		copyReceipt.IdempotencyKey = key
	}
	return &copyReceipt, nil
}

func (i *assistantTestToolInvoker) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.calls)
}

type assistantTestSubscriber struct {
	mu         sync.Mutex
	sub        *assistantTestMergedSubscription
	subscribed chan struct{}
}

func newBlockingAssistantTestSubscriber() *assistantTestSubscriber {
	return &assistantTestSubscriber{subscribed: make(chan struct{})}
}

func (s *assistantTestSubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (AssistantMergedSubscription, error) {
	sub := &assistantTestMergedSubscription{events: make(chan *nostr.Event, 1), closed: make(chan AssistantRelayClosed, 1), eose: make(chan struct{}, 1)}
	s.mu.Lock()
	s.sub = sub
	select {
	case <-s.subscribed:
	default:
		close(s.subscribed)
	}
	s.mu.Unlock()
	return sub, nil
}

func (s *assistantTestSubscriber) waitForSubscription(t *testing.T) {
	t.Helper()
	select {
	case <-s.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for downstream result subscription")
	}
}

func (s *assistantTestSubscriber) publishResult(ev *nostr.Event) {
	s.mu.Lock()
	sub := s.sub
	s.mu.Unlock()
	if sub != nil {
		sub.events <- ev
	}
}

type assistantTestMergedSubscription struct {
	events chan *nostr.Event
	closed chan AssistantRelayClosed
	eose   chan struct{}
}

func (s *assistantTestMergedSubscription) EventChan() <-chan *nostr.Event          { return s.events }
func (s *assistantTestMergedSubscription) ClosedChan() <-chan AssistantRelayClosed { return s.closed }
func (s *assistantTestMergedSubscription) EOSEChan() <-chan struct{}               { return s.eose }
func (s *assistantTestMergedSubscription) Close()                                  { close(s.events); close(s.closed); close(s.eose) }

func lastAssistantEvent(t *testing.T, events []nostr.Event) *nostr.Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatalf("no events found")
	}
	return &events[len(events)-1]
}

func mustUnmarshalEventContent(t *testing.T, ev *nostr.Event, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(ev.Content), dst); err != nil {
		t.Fatalf("unmarshal event content: %v", err)
	}
}

type assistantApprovalCallResult struct {
	result AssistantOperationResult
	err    error
}

func waitApprovalResult(t *testing.T, ch <-chan assistantApprovalCallResult) assistantApprovalCallResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval handler")
		return assistantApprovalCallResult{}
	}
}
