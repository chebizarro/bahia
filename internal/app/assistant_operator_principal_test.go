package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/service"
)

var (
	allowedOperator = strings.Repeat("a1", 32)
	strangerPubkey  = strings.Repeat("b2", 32)
)

// principalRecorder captures the principal each real MCP handler dependency
// was called with.
type principalRecorder struct {
	mu   sync.Mutex
	seen []*auth.Principal
}

func (r *principalRecorder) record(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, auth.GetPrincipal(ctx))
}

func (r *principalRecorder) all() []*auth.Principal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*auth.Principal(nil), r.seen...)
}

type recordingDNSLister struct{ *principalRecorder }

func (l recordingDNSLister) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	l.record(ctx)
	return []domain.DNSEndpoint{}, nil
}

type recordingServiceCommands struct{ *principalRecorder }

func (p recordingServiceCommands) receipt(ctx context.Context, key string) *controlplane.ServiceCommandReceipt {
	p.record(ctx)
	return &controlplane.ServiceCommandReceipt{RequestEventID: strings.Repeat("cd", 32), RequestKind: 25910, ResultKind: 7961, IdempotencyKey: key}
}
func (p recordingServiceCommands) PublishServiceCreateRequest(ctx context.Context, cmd controlplane.ServiceCreateCommand) (*controlplane.ServiceCommandReceipt, error) {
	return p.receipt(ctx, cmd.IdempotencyKey), nil
}
func (p recordingServiceCommands) PublishServiceUpdateRequest(ctx context.Context, cmd controlplane.ServiceUpdateCommand) (*controlplane.ServiceCommandReceipt, error) {
	return p.receipt(ctx, cmd.IdempotencyKey), nil
}
func (p recordingServiceCommands) PublishDeployRequest(ctx context.Context, cmd controlplane.ServiceDeployCommand) (*controlplane.ServiceCommandReceipt, error) {
	return p.receipt(ctx, cmd.IdempotencyKey), nil
}
func (p recordingServiceCommands) PublishRollbackRequest(ctx context.Context, cmd controlplane.ServiceRollbackCommand) (*controlplane.ServiceCommandReceipt, error) {
	return p.receipt(ctx, cmd.IdempotencyKey), nil
}
func (p recordingServiceCommands) PublishDeploymentApprovalRequest(ctx context.Context, cmd controlplane.ServiceApprovalCommand) (*controlplane.ServiceCommandReceipt, error) {
	return p.receipt(ctx, cmd.IdempotencyKey), nil
}

type principalFixture struct {
	server   *mcp.Server
	adapter  assistantMCPRuntimeAdapter
	runtime  *service.AssistantToolRuntime
	dns      *principalRecorder
	commands *principalRecorder
}

// newPrincipalFixture builds a real mcp.Server with the production operator
// allowlist and the production adapter/registry wiring.
func newPrincipalFixture(t *testing.T) principalFixture {
	t.Helper()
	f := principalFixture{dns: &principalRecorder{}, commands: &principalRecorder{}}
	f.server = mcp.NewServerWithOptions(nil, zap.NewNop(), mcp.ServerDeps{DNSEndpoints: recordingDNSLister{f.dns}, ServiceCommandPublisher: recordingServiceCommands{f.commands}, AuthorizedPubkeys: []string{allowedOperator}})
	f.adapter = assistantMCPRuntimeAdapter{server: f.server}
	registry, err := mcp.NewAssistantToolRegistryForServerWithExternal(f.server, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = service.NewAssistantToolRuntime(service.AssistantToolRuntimeConfig{MCPServer: f.adapter, Registry: assistantToolRegistryAdapter{registry: registry}, Permissions: service.NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil)})
	return f
}

func principalExecution(operator string) domain.AssistantExecution {
	return domain.AssistantExecution{Version: 2, SessionID: "s-principal", RunID: "run-principal", TurnID: "t", RequestID: "r", OperatorPubkey: operator, Workflow: domain.AssistantWorkflowIterative, Phase: domain.AssistantExecutionExecuting}
}

func principalWork(t *testing.T, tool string, args map[string]any) domain.AssistantWorkItem {
	t.Helper()
	digest, err := domain.ComputeAssistantArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	return domain.AssistantWorkItem{WorkID: "run-principal:" + tool, OriginID: tool, ToolName: tool, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkReady}
}

// dispatch runs one item through the executor's single dispatch boundary.
func (f principalFixture) dispatch(t *testing.T, ctx context.Context, x domain.AssistantExecution, w domain.AssistantWorkItem) (*domain.AssistantToolObservation, *domain.AsyncToolReceipt, error) {
	t.Helper()
	prepared, err := f.runtime.PrepareWork(ctx, x, w)
	if err != nil {
		t.Fatalf("prepare %s: %v", w.ToolName, err)
	}
	return f.runtime.DispatchPreparedWork(ctx, prepared)
}

func assertOperatorPrincipal(t *testing.T, p *auth.Principal, pubkey string) {
	t.Helper()
	if p == nil || p.Method != auth.MethodNIP98 || p.PubKey != pubkey || p.Subject != pubkey || len(p.Roles) != 0 || p.HasRole(string(domain.RoleAdmin)) {
		t.Fatalf("tool ran as %+v, want the roleless nostr operator principal %s", p, pubkey)
	}
}

func deployArgs() map[string]any {
	return map[string]any{"service_id": "00000000-0000-0000-0000-000000000001", "environment_id": "00000000-0000-0000-0000-000000000002", "artifact_id": "00000000-0000-0000-0000-000000000003"}
}

func TestAssistantMCPAdapterRejectsUnauthenticatedCalls(t *testing.T) {
	f := newPrincipalFixture(t)
	result, err := f.adapter.CallTool(context.Background(), "bahia_dns_list_endpoints", map[string]any{})
	if err != nil || result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "authentication required") {
		t.Fatalf("unauthenticated sync call = %+v err=%v", result, err)
	}
	args := deployArgs()
	args["idempotency_key"] = "k"
	if _, err := f.adapter.InvokeAssistantAsyncTool(context.Background(), "bahia_assistant_service_deploy", args); !errors.Is(err, service.ErrAssistantToolCallRefused) {
		t.Fatalf("unauthenticated async call err=%v", err)
	}
	// An execution without a persisted operator gets no principal at all.
	obs, receipt, err := f.dispatch(t, context.Background(), principalExecution(""), principalWork(t, "bahia_dns_list_endpoints", map[string]any{}))
	if err != nil || receipt != nil || obs == nil || obs.Status != domain.AssistantToolObservationFailed {
		t.Fatalf("operator-less dispatch obs=%+v err=%v", obs, err)
	}
	if len(f.dns.all()) != 0 || len(f.commands.all()) != 0 {
		t.Fatal("an unauthenticated call reached a handler dependency")
	}
}

func TestAssistantDispatchActsAsAllowlistedOperator(t *testing.T) {
	f := newPrincipalFixture(t)
	obs, _, err := f.dispatch(t, context.Background(), principalExecution(allowedOperator), principalWork(t, "bahia_dns_list_endpoints", map[string]any{}))
	if err != nil || obs == nil || obs.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("allowlisted sync dispatch obs=%+v err=%v", obs, err)
	}
	obs, receipt, err := f.dispatch(t, context.Background(), principalExecution(allowedOperator), principalWork(t, "bahia_assistant_service_deploy", deployArgs()))
	if err != nil || obs != nil || receipt == nil || receipt.RequestEventID == "" {
		t.Fatalf("allowlisted async dispatch obs=%+v receipt=%+v err=%v", obs, receipt, err)
	}
	for _, p := range append(f.dns.all(), f.commands.all()...) {
		assertOperatorPrincipal(t, p, allowedOperator)
	}
	if len(f.dns.all()) != 1 || len(f.commands.all()) != 1 {
		t.Fatalf("handler calls dns=%d commands=%d", len(f.dns.all()), len(f.commands.all()))
	}
}

// A non-allowlisted operator is refused exactly as their direct request
// would be, and the refusal is a definite failed observation: never a crash
// and never an uncertain (possibly submitted) effect. An ambient system
// principal in the caller's context cannot lift the refusal.
func TestAssistantDispatchRefusesNonAllowlistedOperatorAsToolFailure(t *testing.T) {
	f := newPrincipalFixture(t)
	system := auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("ambient"))
	obs, receipt, err := f.dispatch(t, system, principalExecution(strangerPubkey), principalWork(t, "bahia_dns_list_endpoints", map[string]any{}))
	if err != nil || receipt != nil || obs == nil || obs.Status != domain.AssistantToolObservationFailed || !strings.Contains(obs.Error, "access denied") {
		t.Fatalf("stranger sync dispatch obs=%+v err=%v", obs, err)
	}
	obs, receipt, err = f.dispatch(t, system, principalExecution(strangerPubkey), principalWork(t, "bahia_assistant_service_deploy", deployArgs()))
	if err != nil || receipt != nil || obs == nil || obs.Status != domain.AssistantToolObservationFailed || !strings.Contains(obs.Error, "access denied") {
		t.Fatalf("stranger async dispatch obs=%+v receipt=%+v err=%v", obs, receipt, err)
	}
	if len(f.dns.all()) != 0 || len(f.commands.all()) != 0 {
		t.Fatal("a refused operator's call reached a handler dependency")
	}
}

// Approval-gated work acts as the operator who approved it, not the one who
// asked for it.
func TestAssistantApprovedWorkActsAsApprovingOperator(t *testing.T) {
	f := newPrincipalFixture(t)
	x := principalExecution(strangerPubkey)
	w := principalWork(t, "bahia_dns_list_endpoints", map[string]any{})
	w.Authorization = &domain.AssistantAuthorizationBinding{OperatorPubkey: allowedOperator, DecisionRequestID: "decision", ActionID: w.WorkID, ArgumentsDigest: w.ArgumentsDigest, Scope: x.Scope}
	obs, _, err := f.dispatch(t, context.Background(), x, w)
	if err != nil || obs == nil || obs.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("approved dispatch obs=%+v err=%v", obs, err)
	}
	assertOperatorPrincipal(t, f.dns.all()[0], allowedOperator)

	x = principalExecution(allowedOperator)
	w.Authorization.OperatorPubkey = strangerPubkey
	obs, _, err = f.dispatch(t, context.Background(), x, w)
	if err != nil || obs == nil || obs.Status != domain.AssistantToolObservationFailed {
		t.Fatalf("work approved by a stranger ran as the requester: obs=%+v err=%v", obs, err)
	}
}

// A run recovered after a restart dispatches as the operator persisted in its
// checkpoint: the lost read-only dispatch is re-dispatched through the real
// MCP server as that operator (and a non-allowlisted persisted operator is
// refused as a tool failure while the run still finishes).
func TestAssistantRecoveredRunDispatchesAsPersistedOperator(t *testing.T) {
	for _, tc := range []struct {
		name     string
		operator string
		want     domain.AssistantWorkState
	}{{"allowlisted", allowedOperator, domain.AssistantWorkSucceeded}, {"not allowlisted", strangerPubkey, domain.AssistantWorkFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrincipalFixture(t)
			relay := newMemoryRelay()
			wiring, signer := buildTestAssistantExecutionWith(t, wiringConfig(t, true, ""), relay, func(deps *assistantExecutionDeps) { deps.MCPServer = f.server })
			w := principalWork(t, "bahia_dns_list_endpoints", map[string]any{})
			w.WorkID, w.OriginID, w.State = "run-recover:call-1", "call-1", domain.AssistantWorkDispatching
			x := domain.AssistantExecution{Version: 2, SessionID: "s-recover", RunID: "run-recover", TurnID: "t", RequestID: "r", OperatorPubkey: tc.operator, Workflow: domain.AssistantWorkflowIterative, Revision: 1, Phase: domain.AssistantExecutionExecuting, Work: []domain.AssistantWorkItem{w}}
			checkpoint, err := wiring.Store.Append(context.Background(), x, "")
			if err != nil {
				t.Fatal(err)
			}
			projection, _ := json.Marshal(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: "s-recover", OperatorPubkey: tc.operator, ExecutionVersion: 2, Workflow: x.Workflow, CurrentRunID: x.RunID, ExecutionRevision: 1, Phase: x.Phase, CheckpointEventID: checkpoint})
			ev := nostr.Event{Kind: domain.KindAssistantSessionState, CreatedAt: nostr.Now() - 60, Tags: nostr.Tags{{"d", domain.AssistantSessionSchemaV2 + ":s-recover"}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"session", "s-recover"}}, Content: string(projection)}
			if err := signer.SignEvent(context.Background(), &ev); err != nil {
				t.Fatal(err)
			}
			if _, err := relay.Publish(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			if err := wiring.Recovery.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			relay.waitFor(t, "recovered run finishes", func() bool {
				got, _ := wiring.Engine.Snapshot("s-recover")
				return got.Phase == domain.AssistantExecutionCompleted
			})
			got, _ := wiring.Engine.Snapshot("s-recover")
			if got.OperatorPubkey != tc.operator || got.Work[0].State != tc.want || len(got.Work[0].Redispatches) != 1 {
				t.Fatalf("recovered work = %+v operator=%s", got.Work[0], got.OperatorPubkey)
			}
			seen := f.dns.all()
			if tc.want == domain.AssistantWorkSucceeded {
				if len(seen) != 1 {
					t.Fatalf("handler calls = %d", len(seen))
				}
				assertOperatorPrincipal(t, seen[0], tc.operator)
			} else if len(seen) != 0 {
				t.Fatal("a non-allowlisted persisted operator reached the handler")
			}
		})
	}
}

// PreToolUse mcp-tool hooks and subagent children call the real MCP server as
// the same operator as the work they belong to; an ambient system principal
// never leaks into either.
func TestAssistantHooksAndSubagentChildrenActAsWorkOperator(t *testing.T) {
	f := newPrincipalFixture(t)
	registry, err := mcp.NewAssistantToolRegistryForServerWithExternal(f.server, nil)
	if err != nil {
		t.Fatal(err)
	}
	set, err := service.ParseAssistantHookDocument([]byte(`{"PreToolUse":[{"matcher":"delegate","hooks":[{"type":"mcp-tool","tool":"bahia_dns_list_endpoints"}]}]}`), "hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks := service.NewAssistantHookRunner(service.AssistantHookRunnerConfig{Set: set, MCP: service.NewAssistantReadOnlyMCPHookCaller(service.AssistantReadOnlyMCPHookCallerConfig{MCPServer: f.adapter, Registry: assistantToolRegistryAdapter{registry: registry}})})
	f.runtime = service.NewAssistantToolRuntime(service.AssistantToolRuntimeConfig{MCPServer: f.adapter, Registry: assistantToolRegistryAdapter{registry: registry}, Permissions: service.NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil), Hooks: hooks})
	var child *domain.AssistantToolObservation
	if err := f.runtime.RegisterInternalTools(service.AssistantInternalTool{Name: "delegate", InputSchema: map[string]any{"type": "object"}, Effect: domain.AssistantToolEffectRead, Risk: domain.AssistantToolRiskLow, Handler: func(ctx context.Context, call service.AssistantInternalToolCall) (*domain.AssistantToolObservation, error) {
		child = f.runtime.ExecuteSubagentTool(ctx, call, "child-session", domain.AssistantAgentToolCall{ID: "child-call", Name: "bahia_dns_list_endpoints", Arguments: map[string]any{}})
		return &domain.AssistantToolObservation{ObservationID: "obs", Status: domain.AssistantToolObservationSucceeded, Summary: "delegated"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	system := auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("ambient"))

	obs, _, err := f.dispatch(t, system, principalExecution(allowedOperator), principalWork(t, "delegate", map[string]any{}))
	if err != nil || obs == nil || obs.Status != domain.AssistantToolObservationSucceeded || child == nil || child.Status != domain.AssistantToolObservationSucceeded {
		t.Fatalf("delegate obs=%+v child=%+v err=%v", obs, child, err)
	}
	seen := f.dns.all()
	if len(seen) != 2 { // the PreToolUse hook, then the subagent child
		t.Fatalf("handler calls = %d, want hook + child", len(seen))
	}
	for _, p := range seen {
		assertOperatorPrincipal(t, p, allowedOperator)
	}

	// For a non-allowlisted operator the hook itself is refused by MCP, which
	// blocks the work before dispatch; nothing reaches the handler.
	child = nil
	var denial *service.AssistantWorkDenial
	if _, err := f.runtime.PrepareWork(system, principalExecution(strangerPubkey), principalWork(t, "delegate", map[string]any{})); !errors.As(err, &denial) {
		t.Fatalf("stranger delegate prepare err=%v", err)
	}
	// Dispatching a subagent child directly as the stranger is refused too.
	if obs := f.runtime.ExecuteSubagentTool(system, service.AssistantInternalToolCall{SessionID: "s", RunID: "r", WorkID: "w", OperatorPubkey: strangerPubkey}, "child-session", domain.AssistantAgentToolCall{ID: "c", Name: "bahia_dns_list_endpoints", Arguments: map[string]any{}}); obs == nil || obs.Status == domain.AssistantToolObservationSucceeded {
		t.Fatalf("stranger subagent child obs=%+v", obs)
	}
	if child != nil || len(f.dns.all()) != 2 {
		t.Fatal("a stranger's hook or subagent child reached the handler")
	}
}
