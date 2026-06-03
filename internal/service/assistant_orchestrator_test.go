package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/nbd-wtf/go-nostr"

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

	firstDone := make(chan error, 1)
	go func() {
		_, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-1"), assistantApprovalRequest("session-1", planHash, "approve"))
		firstDone <- err
	}()
	invoker.waitForCalls(t, 1)

	secondDone := make(chan error, 1)
	go func() {
		_, err := orchestrator.HandleApprovalRequest(ctx, assistantApprovalSource("session-1", "approval-2"), assistantApprovalRequest("session-1", planHash, "approve"))
		secondDone <- err
	}()

	subscriber.publishResult(&nostr.Event{ID: "result-1", Kind: 7961, Tags: nostr.Tags{{"e", "downstream-1"}, {"status", "completed"}}, Content: `{"status":"completed"}`})

	if err := waitErr(t, firstDone); err != nil {
		t.Fatalf("first HandleApproval: %v", err)
	}
	if err := waitErr(t, secondDone); err != nil {
		t.Fatalf("second HandleApproval: %v", err)
	}
	if got := invoker.callCount(); got != 1 {
		t.Fatalf("tool invocations = %d, want 1", got)
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
	return AssistantRequestSource{Event: &nostr.Event{ID: id, PubKey: "operator", Kind: 25910}, OperatorPubkey: "operator", RequestID: id, DedupKey: "assistant-turn:" + sessionID + ":" + turnID}
}

func assistantApprovalRequest(sessionID, planHash, decision string) domain.AssistantApprovalRequest {
	return domain.AssistantApprovalRequest{SessionID: sessionID, PlanHash: planHash, Decision: decision}
}

func assistantApprovalSource(sessionID, suffix string) AssistantRequestSource {
	id := "approval-" + suffix
	return AssistantRequestSource{Event: &nostr.Event{ID: id, PubKey: "operator", Kind: 25910}, OperatorPubkey: "operator", RequestID: id, DedupKey: "assistant-approval:" + sessionID + ":" + suffix}
}

func testAssistantSigner(t *testing.T) canonicalnostr.Signer {
	t.Helper()
	decoded, err := hex.DecodeString(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	var secret [32]byte
	copy(secret[:], decoded)
	return keyer.NewPlainKeySigner(secret)
}

type assistantTestChatClient struct{ plan *domain.AssistantPlan }

func (c *assistantTestChatClient) PlanFromPrompt(context.Context, string, string) (*domain.AssistantPlan, error) {
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
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

type assistantTestToolInvoker struct {
	mu      sync.Mutex
	calls   []assistantToolCall
	receipt *domain.AsyncToolReceipt
	called  chan struct{}
}

type assistantToolCall struct {
	name string
	args map[string]interface{}
}

func (i *assistantTestToolInvoker) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	i.mu.Lock()
	i.calls = append(i.calls, assistantToolCall{name: name, args: args})
	if i.called != nil {
		select {
		case <-i.called:
		default:
			close(i.called)
		}
	}
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

func (i *assistantTestToolInvoker) waitForCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if i.callCount() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d tool calls; got %d", n, i.callCount())
		case <-tick.C:
		}
	}
}

type assistantTestSubscriber struct {
	mu  sync.Mutex
	sub *assistantTestMergedSubscription
}

func newBlockingAssistantTestSubscriber() *assistantTestSubscriber { return &assistantTestSubscriber{} }

func (s *assistantTestSubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (AssistantMergedSubscription, error) {
	sub := &assistantTestMergedSubscription{events: make(chan *nostr.Event, 1), closed: make(chan AssistantRelayClosed, 1), eose: make(chan struct{}, 1)}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()
	return sub, nil
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

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval handler")
		return nil
	}
}
