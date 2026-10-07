package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func syncDescriptor(name string) AssistantToolRuntimeToolDescriptor {
	return AssistantToolRuntimeToolDescriptor{Name: name, ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}
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

// assistantRuntimeMCPServer is a counting provider boundary.
type assistantRuntimeMCPServer struct {
	mu           sync.Mutex
	syncResult   *AssistantToolRuntimeToolResult
	asyncReceipt *domain.AsyncToolReceipt
	calls        int
	invokes      int
	callNames    []string
}

func (s *assistantRuntimeMCPServer) CallTool(_ context.Context, name string, _ map[string]interface{}) (*AssistantToolRuntimeToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.callNames = append(s.callNames, name)
	if s.syncResult == nil {
		return &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}, nil
	}
	return s.syncResult, nil
}

func (s *assistantRuntimeMCPServer) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invokes++
	receipt := s.asyncReceipt
	if receipt == nil {
		receipt = &domain.AsyncToolReceipt{ToolName: name, RequestEventID: "downstream-generated", RequestKind: 25910, ResultKinds: []int{7961}}
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

func TestAssistantRuntimeWorkScopedBatchApprovalCannotBeBypassed(t *testing.T) {
	descriptor := AssistantToolRuntimeToolDescriptor{Name: "mutate", ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"idempotency_key": map[string]any{"type": "string"}}, "required": []string{"idempotency_key"}}}
	server := &assistantRuntimeMCPServer{}
	r := NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: assistantRuntimeRegistryWith(descriptor), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil)})
	x := domain.AssistantExecution{Version: 2, SessionID: "session", RunID: "run", Workflow: domain.AssistantWorkflowBatch, Proposal: &domain.AssistantProposalRevision{ProposalID: "proposal", Revision: 1, Hash: "hash"}}
	work := domain.AssistantWorkItem{WorkID: "run:step", OriginID: "step", ToolName: "mutate", Arguments: map[string]any{}, State: domain.AssistantWorkReady}
	if _, err := r.PrepareWork(context.Background(), x, work); err == nil {
		t.Fatal("audited mode bypassed mandatory batch approval")
	}
	digest, err := domain.ComputeAssistantArgumentsDigest(work.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	work.ArgumentsDigest = digest
	work.Authorization = &domain.AssistantAuthorizationBinding{OperatorPubkey: "operator", DecisionRequestID: "decision", ProposalID: "proposal", ProposalRevision: 1, ArgumentsDigest: digest, Scope: x.Scope}
	prepared, err := r.PrepareWork(context.Background(), x, work)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Work.IdempotencyKey != "assistant:session:hash:step" {
		t.Fatalf("key=%q", prepared.Work.IdempotencyKey)
	}
	readonly := NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: assistantRuntimeRegistryWith(descriptor), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeReadonly}, nil)})
	if _, err := readonly.PrepareWork(context.Background(), x, work); err == nil {
		t.Fatal("approval bypassed readonly posture")
	}
	if server.invokeCount() != 0 {
		t.Fatal("preparation dispatched tool")
	}
}

func TestAssistantRuntimeChangedApprovedArgumentsBlock(t *testing.T) {
	descriptor := AssistantToolRuntimeToolDescriptor{Name: "read", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{AssistantHookEventPreToolUse: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "change"}}}}}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"change": {{UpdatedInput: map[string]any{"target": "different"}}}}})
	r := NewAssistantToolRuntime(AssistantToolRuntimeConfig{Registry: assistantRuntimeRegistryWith(descriptor), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeReview}, nil), Hooks: hooks})
	x := domain.AssistantExecution{Version: 2, SessionID: "session", RunID: "run", Workflow: domain.AssistantWorkflowBatch, Proposal: &domain.AssistantProposalRevision{ProposalID: "proposal", Revision: 1, Hash: "hash"}}
	args := map[string]any{"target": "original"}
	digest, err := domain.ComputeAssistantArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	work := domain.AssistantWorkItem{WorkID: "run:step", OriginID: "step", ToolName: "read", Arguments: args, ArgumentsDigest: digest, Authorization: &domain.AssistantAuthorizationBinding{OperatorPubkey: "operator", DecisionRequestID: "decision", ProposalID: "proposal", ProposalRevision: 1, ArgumentsDigest: digest, Scope: x.Scope}}
	prepared, err := r.PrepareWork(context.Background(), x, work)
	if !errors.Is(err, ErrAssistantApprovedInputChanged) || prepared.Work.Arguments["target"] != "different" || work.Arguments["target"] != "original" {
		t.Fatalf("approved input change=%v prepared=%+v work=%+v", err, prepared.Work, work)
	}
}

func TestAssistantRuntimeHooksTightenButNeverLoosen(t *testing.T) {
	evaluator := &assistantMutableHookEvaluator{}
	hooks := newAssistantTestHooks(t, evaluator)
	askMutate := []AssistantPermissionRule{{ID: "ask", Decision: domain.AssistantPermissionDecisionAsk, ToolNames: []string{"mutate"}}}
	r := assistantTestRuntime(&assistantRuntimeMCPServer{}, hooks, askMutate)
	x := domain.AssistantExecution{Version: 2, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative}
	work := domain.AssistantWorkItem{WorkID: "r:c", OriginID: "c", ToolName: "mutate", Arguments: map[string]any{}}

	evaluator.set(AssistantHookOutcome{Decision: AssistantHookDecisionAllow})
	if _, err := r.PrepareWork(context.Background(), x, work); !errors.Is(err, ErrAssistantApprovalRequired) {
		t.Fatalf("hook allow loosened ask: %v", err)
	}
	evaluator.set(AssistantHookOutcome{Decision: AssistantHookDecisionDeny, Reason: "change freeze"})
	work.ToolName = "read-one"
	var denial *AssistantWorkDenial
	if _, err := r.PrepareWork(context.Background(), x, work); !errors.As(err, &denial) || !strings.Contains(denial.Reason, "change freeze") {
		t.Fatalf("hook deny did not tighten an allow: %v", err)
	}
}

func TestAssistantRuntimeApprovalBindsExactIterativeWorkAndBatchNeedsApproval(t *testing.T) {
	r := assistantTestRuntime(&assistantRuntimeMCPServer{}, nil, nil)
	x := domain.AssistantExecution{Version: 2, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative}
	args := map[string]any{"zone": "z"}
	digest, err := domain.ComputeAssistantArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	// Audited mode lets this iterative mutation run autonomously...
	autonomous := domain.AssistantWorkItem{WorkID: "r:a", OriginID: "a", ToolName: "mutate", Arguments: args}
	if _, err := r.PrepareWork(context.Background(), x, autonomous); err != nil {
		t.Fatalf("iterative autonomy changed: %v", err)
	}
	// ...but the same call in a batch still requires plan approval.
	batch := x
	batch.Workflow = domain.AssistantWorkflowBatch
	batch.Proposal = &domain.AssistantProposalRevision{ProposalID: "p", Revision: 1, Hash: "h"}
	if _, err := r.PrepareWork(context.Background(), batch, autonomous); !errors.Is(err, ErrAssistantApprovalRequired) {
		t.Fatalf("batch approval bypassed: %v", err)
	}
	// An action binding for another work item never authorizes this one.
	bound := autonomous
	bound.ArgumentsDigest = digest
	bound.Authorization = &domain.AssistantAuthorizationBinding{OperatorPubkey: "operator", DecisionRequestID: "d", ActionID: "r:other", ArgumentsDigest: digest}
	var denial *AssistantWorkDenial
	if _, err := r.PrepareWork(context.Background(), x, bound); !errors.As(err, &denial) {
		t.Fatalf("foreign action binding accepted: %v", err)
	}
	bound.Authorization.ActionID = "r:a"
	prepared, err := r.PrepareWork(context.Background(), x, bound)
	if err != nil || prepared.Work.IdempotencyKey != "assistant-agent:s:r:a" {
		t.Fatalf("exact binding: key=%q err=%v", prepared.Work.IdempotencyKey, err)
	}
}

func assistantTestInternalTool(name string, calls *int) AssistantInternalTool {
	return AssistantInternalTool{Name: name, Effect: domain.AssistantToolEffectRead, Risk: domain.AssistantToolRiskLow,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}},
		Handler: func(_ context.Context, call AssistantInternalToolCall) (*domain.AssistantToolObservation, error) {
			*calls++
			return &domain.AssistantToolObservation{ObservationID: "obs-internal", Status: domain.AssistantToolObservationSucceeded, Summary: "internal ok " + call.SessionID}, nil
		}}
}

// Internal tools keep a separate, service-owned registration but pass the same
// schema, scope, permission and hook gate as MCP tools, and are unavailable to
// the batch catalog.
func TestAssistantRuntimeInternalToolsPassCommonGateAndStayOutOfBatch(t *testing.T) {
	calls := 0
	r := assistantTestRuntime(&assistantRuntimeMCPServer{}, nil, nil)
	if err := r.RegisterInternalTools(assistantTestInternalTool("internal_lookup", &calls)); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterInternalTools(assistantTestInternalTool("internal_lookup", &calls)); err == nil {
		t.Fatal("duplicate internal registration accepted")
	}
	if err := r.RegisterInternalTools(assistantTestInternalTool("read-one", &calls)); err == nil {
		t.Fatal("internal tool shadowed a registered MCP tool")
	}
	iterative := domain.AssistantExecution{Version: 2, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative}
	work := domain.AssistantWorkItem{WorkID: "r:c", OriginID: "c", ToolName: "internal_lookup", Arguments: map[string]any{"q": "x"}}
	prepared, err := r.PrepareWork(context.Background(), iterative, work)
	if err != nil {
		t.Fatalf("internal tool refused by common gate: %v", err)
	}
	obs, receipt, err := r.DispatchPreparedWork(context.Background(), prepared)
	if err != nil || receipt != nil || obs == nil || obs.Status != domain.AssistantToolObservationSucceeded || obs.ToolCallID != "c" || calls != 1 {
		t.Fatalf("internal dispatch obs=%+v receipt=%v err=%v calls=%d", obs, receipt, err, calls)
	}
	var denial *AssistantWorkDenial
	bad := work
	bad.Arguments = map[string]any{}
	if _, err := r.PrepareWork(context.Background(), iterative, bad); !errors.As(err, &denial) || !strings.Contains(denial.Reason, "schema") {
		t.Fatalf("internal tool schema not enforced: %v", err)
	}
	scoped := iterative
	scoped.Scope = domain.AssistantCommandScope{AllowedTools: []string{"read-one"}}
	if _, err := r.PrepareWork(context.Background(), scoped, work); !errors.As(err, &denial) || !strings.Contains(denial.Reason, "scope") {
		t.Fatalf("internal tool escaped command scope: %v", err)
	}
	readonly := NewAssistantToolRuntime(AssistantToolRuntimeConfig{Registry: assistantTestRegistry(), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeReadonly}, []AssistantPermissionRule{{ID: "deny-internal", Decision: domain.AssistantPermissionDecisionDeny, ToolNames: []string{"internal_lookup"}}})})
	if err := readonly.RegisterInternalTools(assistantTestInternalTool("internal_lookup", &calls)); err != nil {
		t.Fatal(err)
	}
	if _, err := readonly.PrepareWork(context.Background(), iterative, work); !errors.As(err, &denial) {
		t.Fatalf("internal tool bypassed permission policy: %v", err)
	}
	batch := iterative
	batch.Workflow = domain.AssistantWorkflowBatch
	batch.Proposal = &domain.AssistantProposalRevision{ProposalID: "p", Revision: 1, Hash: "h"}
	if _, err := r.PrepareWork(context.Background(), batch, work); !errors.As(err, &denial) || !strings.Contains(denial.Reason, "batch") {
		t.Fatalf("internal tool reachable from batch workflow: %v", err)
	}
	if calls != 1 {
		t.Fatalf("gate refusals invoked the handler: calls=%d", calls)
	}
}

// Subagent child calls pass the parent's scope and policy and may only run
// synchronous tools that policy allows outright; they never submit async work.
func TestAssistantRuntimeSubagentChildCallsAreGatedAndSyncOnly(t *testing.T) {
	server := &assistantRuntimeMCPServer{}
	askRead := []AssistantPermissionRule{{ID: "ask-read-two", Decision: domain.AssistantPermissionDecisionAsk, ToolNames: []string{"read-two"}}}
	r := NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: assistantRuntimeRegistryWith(syncDescriptor("read-one"), syncDescriptor("read-two"), AssistantToolRuntimeToolDescriptor{Name: "mutate", ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}, AssistantToolRuntimeToolDescriptor{Name: "sync-write", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}), Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, askRead)})
	calls := 0
	if err := r.RegisterInternalTools(assistantTestInternalTool("internal_lookup", &calls)); err != nil {
		t.Fatal(err)
	}
	parent := AssistantInternalToolCall{SessionID: "s", RunID: "r", WorkID: "r:delegate", Scope: domain.AssistantCommandScope{AllowedTools: []string{"read-one", "read-two", "mutate", "sync-write", "internal_lookup"}}}
	call := func(name string) *domain.AssistantToolObservation {
		return r.ExecuteSubagentTool(context.Background(), parent, "s:subagent:x", domain.AssistantAgentToolCall{ID: "child-" + name, Name: name, Arguments: map[string]any{}})
	}
	if obs := call("read-one"); obs.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("allowed sync child call: %+v", obs)
	}
	for name, want := range map[string]string{"mutate": "async", "sync-write": "mutation", "read-two": "approval", "internal_lookup": "internal", "unknown": "not registered"} {
		if obs := call(name); obs.Status != domain.AssistantToolObservationDenied || !strings.Contains(obs.Error, want) {
			t.Fatalf("child %s: %+v", name, obs)
		}
	}
	scoped := parent
	scoped.Scope = domain.AssistantCommandScope{AllowedTools: []string{"read-two"}}
	if obs := r.ExecuteSubagentTool(context.Background(), scoped, "s:subagent:x", domain.AssistantAgentToolCall{ID: "c", Name: "read-one", Arguments: map[string]any{}}); obs.Status != domain.AssistantToolObservationDenied || !strings.Contains(obs.Error, "scope") {
		t.Fatalf("child escaped parent scope: %+v", obs)
	}
	if server.invokeCount() != 0 || server.callCount() != 1 || calls != 0 {
		t.Fatalf("child dispatch invokes=%d calls=%d internal=%d", server.invokeCount(), server.callCount(), calls)
	}
}
