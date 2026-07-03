package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantToolRuntimeSyncReadReturnsCompletedObservation(t *testing.T) {
	session := assistantRuntimeSession("session-sync")
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"services":[{"name":"api"}],"total":1}`}}}}
	runtime := newAssistantRuntimeForTest(t, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, nil, nil)

	obs, err := runtime.Execute(context.Background(), AssistantToolRuntimeRequest{Session: session, RunID: "run-1", Iteration: 1, ToolCall: domain.AssistantAgentToolCall{ID: "call-sync", Name: "bahia_list_services"}})
	if err != nil {
		t.Fatalf("Execute sync: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationSucceeded || obs.ToolCallID != "call-sync" || obs.ToolName != "bahia_list_services" {
		t.Fatalf("observation = %#v", obs)
	}
	if got := obs.Result["total"]; got != float64(1) {
		t.Fatalf("result total = %#v", got)
	}
	if server.callCount() != 1 || server.invokeCount() != 0 {
		t.Fatalf("server calls: call=%d invoke=%d", server.callCount(), server.invokeCount())
	}
	metadata := assistantAgentLoopMetadata(session)
	if metadata.State != domain.AssistantAgentLoopStateRunning || metadata.LastObservationID != obs.ObservationID {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestAssistantToolRuntimeAsyncAllowedSuspendsThenResumesLiveSuccess(t *testing.T) {
	session := assistantRuntimeSession("session-async-live")
	receipt := assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-live")
	server := &assistantRuntimeMCPServer{asyncReceipt: receipt}
	subscriber := newBlockingAssistantTestSubscriber()
	observer := newTestAssistantOrchestrator(t, &assistantTestPublisher{}, &assistantTestToolInvoker{}, nil, subscriber, nil)
	runtime := newAssistantRuntimeForTest(t, server, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, observer, nil)

	obs, err := runtime.Execute(context.Background(), AssistantToolRuntimeRequest{Session: session, RunID: "run-live", Iteration: 2, ToolCall: domain.AssistantAgentToolCall{ID: "call-async", Name: "bahia_assistant_dns_zone_create", Arguments: map[string]any{"zone": "example.test"}}})
	if err != nil {
		t.Fatalf("Execute async: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationWaitingAsync {
		t.Fatalf("async observation = %#v", obs)
	}
	if session.State != domain.AssistantSessionStateExecuting {
		t.Fatalf("session state = %s", session.State)
	}
	metadata := assistantAgentLoopMetadata(session)
	if metadata.State != domain.AssistantAgentLoopStateWaitingAsync || metadata.WaitingReceipt == nil || metadata.PendingToolCallID != "call-async" {
		t.Fatalf("waiting metadata = %#v", metadata)
	}
	if server.invokeCount() != 1 {
		t.Fatalf("invoke count = %d", server.invokeCount())
	}

	resumeDone := make(chan *domain.AssistantToolObservation, 1)
	go func() {
		resumed, err := runtime.ResumeAsync(context.Background(), AssistantToolResumeRequest{Session: session})
		if err != nil {
			t.Errorf("ResumeAsync: %v", err)
		}
		resumeDone <- resumed
	}()
	subscriber.waitForSubscription(t)
	subscriber.publishResult(assistantSignedResultEvent(t, "result-live", 7961, "downstream-live", "completed"))

	resumed := waitRuntimeObservation(t, resumeDone)
	if resumed.Status != domain.AssistantToolObservationSucceeded || resumed.EventID == "" || resumed.Result["status"] != "completed" {
		t.Fatalf("resumed observation = %#v", resumed)
	}
	metadata = assistantAgentLoopMetadata(session)
	if metadata.State != domain.AssistantAgentLoopStateRunning || metadata.WaitingReceipt != nil || metadata.PendingToolCallID != "" {
		t.Fatalf("resumed metadata = %#v", metadata)
	}
}

func TestAssistantToolRuntimeAsyncBackfillResultResumes(t *testing.T) {
	session := assistantRuntimeSession("session-async-backfill")
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-backfill", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-backfill", WaitingReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-backfill")})
	session.State = domain.AssistantSessionStateExecuting
	backfill := &assistantBackfillSubscriber{event: assistantSignedResultEvent(t, "result-backfill", 7961, "downstream-backfill", "completed")}
	observer := newTestAssistantOrchestrator(t, &assistantTestPublisher{}, &assistantTestToolInvoker{}, nil, backfill, nil)
	runtime := newAssistantRuntimeForTest(t, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, observer, nil)

	obs, err := runtime.ResumeAsync(context.Background(), AssistantToolResumeRequest{Session: session})
	if err != nil {
		t.Fatalf("ResumeAsync backfill: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationSucceeded || obs.EventID == "" {
		t.Fatalf("backfill observation = %#v", obs)
	}
	if backfill.subscribeCount() != 1 {
		t.Fatalf("subscribe count = %d", backfill.subscribeCount())
	}
}

func TestAssistantToolRuntimeAsyncFailureObservation(t *testing.T) {
	session := assistantRuntimeSession("session-async-failure")
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-fail", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-fail", WaitingReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-fail")})
	runtime := newAssistantRuntimeForTest(t, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, &assistantRuntimeObserver{status: "failed", event: assistantSignedResultEvent(t, "result-fail", 7961, "downstream-fail", "failed")}, nil)

	obs, err := runtime.ResumeAsync(context.Background(), AssistantToolResumeRequest{Session: session})
	if err != nil {
		t.Fatalf("ResumeAsync failure: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationFailed || obs.Result["status"] != "failed" {
		t.Fatalf("failure observation = %#v", obs)
	}
	if metadata := assistantAgentLoopMetadata(session); metadata.State != domain.AssistantAgentLoopStateRunning {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestAssistantToolRuntimeRelayClosedBlocksSession(t *testing.T) {
	session := assistantRuntimeSession("session-blocked")
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-blocked", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-blocked", WaitingReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-blocked")})
	runtime := newAssistantRuntimeForTest(t, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, &assistantRuntimeObserver{status: "blocked", err: errors.New("relay subscription closed before terminal result: relay=wss://relay.test reason=closed")}, nil)

	obs, err := runtime.ResumeAsync(context.Background(), AssistantToolResumeRequest{Session: session})
	if err != nil {
		t.Fatalf("ResumeAsync blocked: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationFailed || obs.Metadata["blocked"] != true {
		t.Fatalf("blocked observation = %#v", obs)
	}
	if session.State != domain.AssistantSessionStateBlocked {
		t.Fatalf("session state = %s", session.State)
	}
	if metadata := assistantAgentLoopMetadata(session); metadata.State != domain.AssistantAgentLoopStateBlocked {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestAssistantToolRuntimeAskDefersWithoutExecuting(t *testing.T) {
	session := assistantRuntimeSession("session-ask")
	server := &assistantRuntimeMCPServer{}
	runtime := newAssistantRuntimeForTest(t, server, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_service_rollback", domain.AssistantToolRiskHigh)), domain.AssistantPermissionModeReview, nil, nil)

	obs, err := runtime.Execute(context.Background(), AssistantToolRuntimeRequest{Session: session, RunID: "run-ask", TurnID: "turn-1", ToolCall: domain.AssistantAgentToolCall{ID: "call-ask", Name: "bahia_assistant_service_rollback", Arguments: map[string]any{"service_id": "svc", "environment_id": "prod"}}})
	if err != nil {
		t.Fatalf("Execute ask: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationDeferred || obs.Deferred == nil || obs.Deferred.ActionID == "" {
		t.Fatalf("deferred observation = %#v", obs)
	}
	if session.State != domain.AssistantSessionStateAwaitingApproval {
		t.Fatalf("session state = %s", session.State)
	}
	if server.invokeCount() != 0 || server.callCount() != 0 {
		t.Fatalf("server should not execute, call=%d invoke=%d", server.callCount(), server.invokeCount())
	}
	actions, _ := session.Metadata[assistantDeferredActionsMetadataKey].(map[string]any)
	if len(actions) != 1 {
		t.Fatalf("deferred actions = %#v", session.Metadata[assistantDeferredActionsMetadataKey])
	}
}

func TestAssistantToolRuntimeDenyReturnsDeniedObservation(t *testing.T) {
	session := assistantRuntimeSession("session-deny")
	server := &assistantRuntimeMCPServer{}
	rule := AssistantPermissionRule{ID: "deny-dns", Decision: domain.AssistantPermissionDecisionDeny, ToolNames: []string{"bahia_assistant_dns_zone_create"}, Reason: "DNS changes disabled"}
	runtime := newAssistantRuntimeForTest(t, server, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, nil, []AssistantPermissionRule{rule})

	obs, err := runtime.Execute(context.Background(), AssistantToolRuntimeRequest{Session: session, RunID: "run-deny", ToolCall: domain.AssistantAgentToolCall{ID: "call-deny", Name: "bahia_assistant_dns_zone_create"}})
	if err != nil {
		t.Fatalf("Execute deny: %v", err)
	}
	if obs.Status != domain.AssistantToolObservationDenied || !strings.Contains(obs.Error, "DNS changes disabled") {
		t.Fatalf("denied observation = %#v", obs)
	}
	if server.invokeCount() != 0 || server.callCount() != 0 {
		t.Fatalf("server should not execute, call=%d invoke=%d", server.callCount(), server.invokeCount())
	}
}

func TestAssistantToolRuntimeRestartRecoveryResumesWaitingAsync(t *testing.T) {
	session := assistantRuntimeSession("session-restart")
	session.State = domain.AssistantSessionStateExecuting
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-restart", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-restart", WaitingReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-restart")})
	observer := &assistantRuntimeObserver{status: "completed", event: assistantSignedResultEvent(t, "result-restart", 7961, "downstream-restart", "completed")}
	runtime := newAssistantRuntimeForTest(t, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, observer, nil)
	orchestrator := newTestAssistantOrchestrator(t, &assistantTestPublisher{}, &assistantTestToolInvoker{}, nil, nil, nil)
	runner := NewAssistantSessionRecoveryRunner(orchestrator, AssistantSessionRecoveryConfig{AgentRuntime: runtime, Logger: slog.New(slog.NewTextHandler(testingWriter{t: t}, nil))})

	runner.recoverSession(context.Background(), session)

	stored := orchestrator.session("session-restart")
	if stored == nil {
		t.Fatal("recovered session was not loaded into orchestrator")
	}
	metadata := assistantAgentLoopMetadata(stored)
	if metadata.State != domain.AssistantAgentLoopStateRunning || metadata.WaitingReceipt != nil || metadata.LastObservationID == "" {
		t.Fatalf("recovered metadata = %#v", metadata)
	}
	if observer.callCount() != 1 {
		t.Fatalf("observer calls = %d", observer.callCount())
	}
}

func TestAssistantSessionRecoveryWaitingAsyncWithoutRuntimeBlocks(t *testing.T) {
	session := assistantRuntimeSession("session-no-runtime")
	session.State = domain.AssistantSessionStateExecuting
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-no-runtime", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-no-runtime", WaitingReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-no-runtime")})
	publisher := &assistantTestPublisher{}
	orchestrator := newTestAssistantOrchestrator(t, publisher, &assistantTestToolInvoker{}, nil, nil, nil)
	runner := NewAssistantSessionRecoveryRunner(orchestrator, AssistantSessionRecoveryConfig{Logger: slog.New(slog.NewTextHandler(testingWriter{t: t}, nil))})

	runner.recoverSession(context.Background(), session)

	stored := orchestrator.session("session-no-runtime")
	if stored == nil || stored.State != domain.AssistantSessionStateBlocked {
		t.Fatalf("stored session = %#v", stored)
	}
	if metadata := assistantAgentLoopMetadata(stored); metadata.State != domain.AssistantAgentLoopStateBlocked {
		t.Fatalf("metadata = %#v", metadata)
	}
	statusEvents := publisher.eventsOfKind(domain.KindAssistantStatus)
	if len(statusEvents) == 0 {
		t.Fatal("expected blocked status event")
	}
	var status map[string]any
	mustUnmarshalEventContent(t, lastAssistantEvent(t, statusEvents), &status)
	if status["status"] != "blocked" || status["phase"] != "tool_observation_blocked" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAssistantToolRuntimeDuplicateWaitingReceiptDoesNotInvokeAgain(t *testing.T) {
	session := assistantRuntimeSession("session-duplicate")
	server := &assistantRuntimeMCPServer{asyncReceipt: assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-dup")}
	runtime := newAssistantRuntimeForTest(t, server, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, nil, nil)
	request := AssistantToolRuntimeRequest{Session: session, RunID: "run-dup", ToolCall: domain.AssistantAgentToolCall{ID: "call-dup", Name: "bahia_assistant_dns_zone_create", Arguments: map[string]any{"zone": "example.test"}}}

	first, err := runtime.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := runtime.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if first.Status != domain.AssistantToolObservationWaitingAsync || second.Status != domain.AssistantToolObservationWaitingAsync {
		t.Fatalf("observations = %#v / %#v", first, second)
	}
	if server.invokeCount() != 1 {
		t.Fatalf("duplicate invoke count = %d", server.invokeCount())
	}
	if second.Receipt == nil || second.Receipt.RequestEventID != "downstream-dup" {
		t.Fatalf("duplicate receipt = %#v", second.Receipt)
	}
}

func newAssistantRuntimeForTest(t *testing.T, server *assistantRuntimeMCPServer, registry assistantRuntimeRegistry, mode domain.AssistantPermissionMode, observer AssistantAsyncResultObserver, rules []AssistantPermissionRule) *AssistantToolRuntime {
	t.Helper()
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: mode}, rules)
	ids := 0
	return NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: registry, Permissions: engine, Observer: observer, Sessions: &assistantRuntimePersister{}, Now: func() time.Time { return time.Unix(1000, 0).UTC() }, NewID: func(prefix string) string { ids++; return prefix + "-test-" + string(rune('a'+ids-1)) }})
}

func assistantRuntimeSession(id string) *domain.AssistantSession {
	return &domain.AssistantSession{SessionID: id, State: domain.AssistantSessionStateExecuting, OperatorPubkey: "operator", Participants: []string{"operator"}, AssistantID: "assistant-test", Metadata: map[string]any{}}
}

func syncDescriptor(name string) AssistantToolRuntimeToolDescriptor {
	return AssistantToolRuntimeToolDescriptor{Name: name, ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow}
}

func asyncDescriptor(name string, risk domain.AssistantToolRisk) AssistantToolRuntimeToolDescriptor {
	return AssistantToolRuntimeToolDescriptor{Name: name, ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: risk, ResourceTypes: []string{"dns_zone"}}
}

func assistantRuntimeReceipt(tool, requestID string) *domain.AsyncToolReceipt {
	return &domain.AsyncToolReceipt{ToolName: tool, RequestEventID: requestID, RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: "idem-" + requestID}
}

type assistantRuntimeRegistry map[string]AssistantToolRuntimeToolDescriptor

func assistantRuntimeRegistryWith(descriptors ...AssistantToolRuntimeToolDescriptor) assistantRuntimeRegistry {
	out := assistantRuntimeRegistry{}
	for _, descriptor := range descriptors {
		out[descriptor.Name] = descriptor
	}
	return out
}

func (r assistantRuntimeRegistry) GetAgentTool(name string) (AssistantToolRuntimeToolDescriptor, bool) {
	descriptor, ok := r[name]
	if !ok {
		return AssistantToolRuntimeToolDescriptor{}, false
	}
	return descriptor, true
}

type assistantRuntimeMCPServer struct {
	mu           sync.Mutex
	syncResult   *AssistantToolRuntimeToolResult
	asyncReceipt *domain.AsyncToolReceipt
	calls        int
	invokes      int
}

func (s *assistantRuntimeMCPServer) CallTool(context.Context, string, map[string]interface{}) (*AssistantToolRuntimeToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.syncResult, nil
}

func (s *assistantRuntimeMCPServer) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invokes++
	receipt := s.asyncReceipt
	if receipt == nil {
		receipt = assistantRuntimeReceipt(name, "downstream-generated")
	}
	copyReceipt := *receipt
	copyReceipt.ToolName = name
	if key, _ := args["idempotency_key"].(string); key != "" {
		copyReceipt.IdempotencyKey = key
	}
	return &copyReceipt, nil
}

func (s *assistantRuntimeMCPServer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *assistantRuntimeMCPServer) invokeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invokes
}

type assistantRuntimePersister struct{}

func (assistantRuntimePersister) PersistAssistantSession(context.Context, *domain.AssistantSession) error {
	return nil
}
func (assistantRuntimePersister) PublishAssistantStatus(context.Context, string, string, map[string]any) error {
	return nil
}

type assistantRuntimeObserver struct {
	mu     sync.Mutex
	status string
	event  *nostr.Event
	err    error
	calls  int
}

func (o *assistantRuntimeObserver) ObserveAssistantAsyncResult(context.Context, string, string, string, *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	return AssistantAsyncObservationOutcome{Status: o.status, Event: o.event}, o.err
}

func (o *assistantRuntimeObserver) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

type assistantBackfillSubscriber struct {
	mu    sync.Mutex
	event *nostr.Event
	calls int
}

func (s *assistantBackfillSubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (AssistantMergedSubscription, error) {
	s.mu.Lock()
	s.calls++
	event := s.event
	s.mu.Unlock()
	sub := &assistantTestMergedSubscription{events: make(chan *nostr.Event, 1), closed: make(chan AssistantRelayClosed, 1), eose: make(chan struct{}, 1)}
	if event != nil {
		sub.events <- event
	}
	sub.eose <- struct{}{}
	return sub, nil
}

func (s *assistantBackfillSubscriber) subscribeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func waitRuntimeObservation(t *testing.T, ch <-chan *domain.AssistantToolObservation) *domain.AssistantToolObservation {
	t.Helper()
	select {
	case obs := <-ch:
		return obs
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime observation")
		return nil
	}
}

type testingWriter struct{ t *testing.T }

func (w testingWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

var _ AssistantAsyncResultObserver = (*assistantRuntimeObserver)(nil)
