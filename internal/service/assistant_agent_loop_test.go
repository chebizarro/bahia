package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantAgentLoopReadToolContinuesToCompletion(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"services":[{"name":"api"}],"total":1}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-read", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("api is running"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, transcript, persister := newAssistantLoopForTest(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-read")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-read", Prompt: "list services", OperatorPubkey: "operator"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed || res.State != domain.AssistantAgentLoopStateCompleted || session.State != domain.AssistantSessionStateCompleted {
		t.Fatalf("result/session = %#v / %#v", res, session)
	}
	if server.callCount() != 1 || model.callCount() != 2 {
		t.Fatalf("calls: server=%d model=%d", server.callCount(), model.callCount())
	}
	second := model.request(1)
	if !requestHasToolObservation(second, "call-read", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("second model request missing successful tool observation: %#v", second.Messages)
	}
	if got := transcript.roles("session-read"); strings.Join(got, ",") != "user,assistant,tool,assistant" {
		t.Fatalf("transcript roles = %v", got)
	}
	foundAnswer := false
	for _, st := range persister.statuses {
		if st["phase"] == "loop_completed" && st["summary"] == "api is running" {
			foundAnswer = true
		}
	}
	if !foundAnswer {
		t.Fatalf("loop_completed status missing final answer summary: %#v", persister.statuses)
	}
}

func TestAssistantAgentLoopAsyncSuspendsThenResumesAndContinues(t *testing.T) {
	receipt := assistantRuntimeReceipt("bahia_assistant_dns_zone_create", "downstream-loop")
	server := &assistantRuntimeMCPServer{asyncReceipt: receipt}
	observer := &assistantRuntimeObserver{status: "completed", event: assistantSignedResultEvent(t, "loop-result", 7961, "downstream-loop", "completed")}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-async", Name: "bahia_assistant_dns_zone_create", Arguments: map[string]any{"zone": "example.test"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("zone creation completed"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, _, _ := newAssistantLoopForTest(t, model, server, assistantRuntimeRegistryWith(asyncDescriptor("bahia_assistant_dns_zone_create", domain.AssistantToolRiskMedium)), domain.AssistantPermissionModeAudited, observer, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-async-loop")

	started, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-async", Prompt: "create zone"})
	if err != nil {
		t.Fatalf("StartTurn async: %v", err)
	}
	if !started.Suspended || started.State != domain.AssistantAgentLoopStateWaitingAsync || session.State != domain.AssistantSessionStateExecuting {
		t.Fatalf("started result/session = %#v / %#v", started, session)
	}
	if server.invokeCount() != 1 || model.callCount() != 1 {
		t.Fatalf("before resume calls: invoke=%d model=%d", server.invokeCount(), model.callCount())
	}

	resumed, err := loop.ResumeAfterAsyncObservation(context.Background(), AssistantAgentResumeAsyncRequest{Session: session})
	if err != nil {
		t.Fatalf("ResumeAfterAsyncObservation: %v", err)
	}
	if !resumed.Completed || observer.callCount() != 1 || model.callCount() != 2 {
		t.Fatalf("resumed=%#v observer=%d model=%d", resumed, observer.callCount(), model.callCount())
	}
	second := model.request(1)
	if !requestHasToolObservation(second, "call-async", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("resume model request missing async observation: %#v", second.Messages)
	}
}

func TestAssistantAgentLoopAskApproveExecutesDeferredAction(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"changed":true}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-ask", Name: "bahia_assistant_policy_change", Arguments: map[string]any{"target": "prod"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("policy change executed"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	descriptor := AssistantToolRuntimeToolDescriptor{Name: "bahia_assistant_policy_change", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskHigh}
	loop, _, _ := newAssistantLoopForTest(t, model, server, assistantRuntimeRegistryWith(descriptor), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-approve-loop")

	started, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-approve", Prompt: "change policy"})
	if err != nil {
		t.Fatalf("StartTurn ask: %v", err)
	}
	if !started.Suspended || started.DeferredAction == nil || started.State != domain.AssistantAgentLoopStateAwaitingApproval {
		t.Fatalf("started = %#v", started)
	}
	if server.callCount() != 0 {
		t.Fatalf("tool executed before approval")
	}

	resumed, err := loop.ResumeAfterActionDecision(context.Background(), AssistantAgentActionDecisionRequest{Session: session, ActionID: started.DeferredAction.ActionID, Decision: "approve"})
	if err != nil {
		t.Fatalf("ResumeAfterActionDecision approve: %v", err)
	}
	if !resumed.Completed || server.callCount() != 1 || model.callCount() != 2 {
		t.Fatalf("resumed=%#v server=%d model=%d", resumed, server.callCount(), model.callCount())
	}
	if !requestHasToolObservation(model.request(1), "call-ask", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("approved resume did not feed success observation: %#v", model.request(1).Messages)
	}
}

func TestAssistantAgentLoopRejectFeedsDenialObservation(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"changed":true}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-reject", Name: "bahia_assistant_policy_change", Arguments: map[string]any{"target": "prod"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("I will not make that change"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	descriptor := AssistantToolRuntimeToolDescriptor{Name: "bahia_assistant_policy_change", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskHigh}
	loop, _, _ := newAssistantLoopForTest(t, model, server, assistantRuntimeRegistryWith(descriptor), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-reject-loop")

	started, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-reject", Prompt: "change policy"})
	if err != nil {
		t.Fatalf("StartTurn ask: %v", err)
	}
	resumed, err := loop.ResumeAfterActionDecision(context.Background(), AssistantAgentActionDecisionRequest{Session: session, ActionID: started.DeferredAction.ActionID, Decision: "reject", Reason: "not safe"})
	if err != nil {
		t.Fatalf("ResumeAfterActionDecision reject: %v", err)
	}
	if !resumed.Completed || server.callCount() != 0 || model.callCount() != 2 {
		t.Fatalf("resumed=%#v server=%d model=%d", resumed, server.callCount(), model.callCount())
	}
	if !requestHasToolObservation(model.request(1), "call-reject", domain.AssistantToolObservationDenied) {
		t.Fatalf("reject resume did not feed denied observation: %#v", model.request(1).Messages)
	}
}

func TestAssistantAgentLoopReplaysMemoryAcrossTurns(t *testing.T) {
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{Content: textBlocks("first answer"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("second answer"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, _, _ := newAssistantLoopForTest(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-memory-loop")

	first, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-one", Prompt: "first question"})
	if err != nil || !first.Completed {
		t.Fatalf("first turn = %#v err=%v", first, err)
	}
	session.State = domain.AssistantSessionStateIdle
	second, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-two", Prompt: "follow up"})
	if err != nil || !second.Completed {
		t.Fatalf("second turn = %#v err=%v", second, err)
	}
	secondReq := model.request(1)
	if !requestHasText(secondReq, "first question") || !requestHasText(secondReq, "first answer") || !requestHasText(secondReq, "follow up") {
		t.Fatalf("second turn did not replay memory: %#v", secondReq.Messages)
	}
}

func TestAssistantAgentLoopMaxIterationsBlocksRunawayToolLoop(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-loop", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
	}}
	loop, _, _ := newAssistantLoopForTest(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{MaxIterations: 1})
	session := assistantRuntimeSession("session-max-iterations")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-loop", Prompt: "loop"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if res.State != domain.AssistantAgentLoopStateBlocked || session.State != domain.AssistantSessionStateBlocked || !strings.Contains(res.Error, "max_iterations=1") {
		t.Fatalf("guard result/session = %#v / %#v", res, session)
	}
	if model.callCount() != 1 || server.callCount() != 1 {
		t.Fatalf("guard calls model=%d server=%d", model.callCount(), server.callCount())
	}
}

func TestAssistantAgentLoopMaxConsecutiveToolFailuresBlocksDeniedLoop(t *testing.T) {
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{
			{ID: "call-deny-1", Name: "unknown_tool"},
			{ID: "call-deny-2", Name: "unknown_tool"},
		}, StopReason: llm.AgentStopReasonToolCalls},
	}}
	loop, _, _ := newAssistantLoopForTest(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{MaxConsecutiveToolFailures: 2})
	session := assistantRuntimeSession("session-max-failures")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-failures", Prompt: "call bad tools"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if res.State != domain.AssistantAgentLoopStateBlocked || !strings.Contains(res.Error, "max_consecutive_tool_failures=2") {
		t.Fatalf("failure guard result = %#v", res)
	}
	if len(res.Observations) != 2 || res.Observations[0].Status != domain.AssistantToolObservationDenied || res.Observations[1].Status != domain.AssistantToolObservationDenied {
		t.Fatalf("failure observations = %#v", res.Observations)
	}
}

func newAssistantLoopForTest(t *testing.T, model *assistantLoopModel, server *assistantRuntimeMCPServer, registry assistantRuntimeRegistry, mode domain.AssistantPermissionMode, observer AssistantAsyncResultObserver, agentic config.AssistantAgenticConfig) (*AssistantAgentLoop, *assistantLoopTranscript, *assistantLoopPersister) {
	t.Helper()
	transcript := &assistantLoopTranscript{history: map[string][]domain.AssistantAgentMessage{}}
	persister := &assistantLoopPersister{}
	runtime := newAssistantRuntimeForTest(t, server, registry, mode, observer, nil)
	runtime.sessions = persister
	ids := 0
	loop := NewAssistantAgentLoop(AssistantAgentLoopConfig{ModelClient: model, ToolRuntime: runtime, ContextBuilder: transcript, ToolSchemas: assistantLoopSchemas{schemas: schemasFromRuntimeRegistry(registry)}, Transcript: transcript, Sessions: persister, Agentic: agentic, Now: func() time.Time { return time.Unix(2000, 0).UTC() }, NewID: func(prefix string) string { ids++; return prefix + "-loop-" + string(rune('a'+ids-1)) }})
	return loop, transcript, persister
}

type assistantLoopModel struct {
	mu        sync.Mutex
	responses []*llm.AgentModelResponse
	requests  []llm.AgentModelRequest
}

func (m *assistantLoopModel) Next(_ context.Context, req llm.AgentModelRequest, _ llm.AgentModelEventHandler) (*llm.AgentModelResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if len(m.responses) == 0 {
		return &llm.AgentModelResponse{Content: textBlocks("done"), StopReason: llm.AgentStopReasonEndTurn}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func (m *assistantLoopModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *assistantLoopModel) request(i int) llm.AgentModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[i]
}

type assistantLoopSchemas struct {
	schemas []llm.AgentToolSchema
}

func (s assistantLoopSchemas) AgentToolSchemas(context.Context) ([]llm.AgentToolSchema, error) {
	return append([]llm.AgentToolSchema(nil), s.schemas...), nil
}

func schemasFromRuntimeRegistry(registry assistantRuntimeRegistry) []llm.AgentToolSchema {
	schemas := make([]llm.AgentToolSchema, 0, len(registry))
	for name := range registry {
		schemas = append(schemas, llm.AgentToolSchema{Name: name, Description: "test schema", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}})
	}
	return schemas
}

type assistantLoopTranscript struct {
	mu      sync.Mutex
	history map[string][]domain.AssistantAgentMessage
	appends []AssistantTranscriptAppend
}

func (t *assistantLoopTranscript) BuildModelHistory(_ context.Context, sessionID string, _ map[string]string, _ []string, currentOperatorPrompt string) ([]domain.AssistantAgentMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	messages := []domain.AssistantAgentMessage{{Role: domain.AssistantAgentMessageRoleSystem, Content: textBlocks("system context")}}
	messages = append(messages, cloneAssistantLoopMessages(t.history[sessionID])...)
	if strings.TrimSpace(currentOperatorPrompt) != "" {
		messages = append(messages, domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: textBlocks(strings.TrimSpace(currentOperatorPrompt))})
	}
	return messages, nil
}

func (t *assistantLoopTranscript) AppendMessage(_ context.Context, appendReq AssistantTranscriptAppend) (*AssistantTranscriptRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	msg := appendReq.Message
	if msg.Role != domain.AssistantAgentMessageRoleSystem {
		t.history[appendReq.SessionID] = append(t.history[appendReq.SessionID], msg)
	}
	t.appends = append(t.appends, appendReq)
	return &AssistantTranscriptRecord{Payload: domain.AssistantTranscriptPayload{SessionID: appendReq.SessionID, TurnID: appendReq.TurnID, RunID: appendReq.RunID, Sequence: appendReq.Sequence, Message: appendReq.Message}, EventID: appendReq.SessionID + "-event-" + string(rune('a'+len(t.appends)-1)), CreatedAt: time.Unix(2000, 0).UTC()}, nil
}

func (t *assistantLoopTranscript) roles(sessionID string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	roles := make([]string, 0, len(t.history[sessionID]))
	for _, msg := range t.history[sessionID] {
		roles = append(roles, string(msg.Role))
	}
	return roles
}

type assistantLoopPersister struct {
	mu       sync.Mutex
	sessions []*domain.AssistantSession
	statuses []map[string]any
}

func (p *assistantLoopPersister) PersistAssistantSession(_ context.Context, session *domain.AssistantSession) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	copySession := *session
	p.sessions = append(p.sessions, &copySession)
	return nil
}

func (p *assistantLoopPersister) PublishAssistantStatus(_ context.Context, _ string, status string, content map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := map[string]any{"status": status}
	for k, v := range content {
		entry[k] = v
	}
	p.statuses = append(p.statuses, entry)
	return nil
}

func textBlocks(text string) []domain.AssistantAgentContentBlock {
	return []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: text}}
}

func requestHasToolObservation(req llm.AgentModelRequest, toolCallID string, status domain.AssistantToolObservationStatus) bool {
	for _, msg := range req.Messages {
		if msg.Role == domain.AssistantAgentMessageRoleTool && msg.ToolCallID == toolCallID && msg.Observation != nil && msg.Observation.Status == status {
			return true
		}
	}
	return false
}

func requestHasText(req llm.AgentModelRequest, text string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(assistantAgentMessageText(msg), text) {
			return true
		}
	}
	return false
}

func cloneAssistantLoopMessages(in []domain.AssistantAgentMessage) []domain.AssistantAgentMessage {
	out := make([]domain.AssistantAgentMessage, len(in))
	copy(out, in)
	return out
}

var _ llm.AgentModelClient = (*assistantLoopModel)(nil)

func TestAssistantAgentLoopCompletionSurfacesFinalAnswer(t *testing.T) {
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{Content: textBlocks("here is your answer"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, _, persister := newAssistantLoopForTest(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, nil, config.AssistantAgenticConfig{})
	session := assistantRuntimeSession("session-answer")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-answer", Prompt: "hello", OperatorPubkey: "operator"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected completed result: %#v", res)
	}
	var completed map[string]any
	for _, st := range persister.statuses {
		if st["phase"] == "loop_completed" {
			completed = st
		}
	}
	if completed == nil {
		t.Fatalf("no loop_completed status published; statuses=%#v", persister.statuses)
	}
	if completed["summary"] != "here is your answer" || completed["message"] != "here is your answer" {
		t.Fatalf("loop_completed status missing final answer: %#v", completed)
	}
}
