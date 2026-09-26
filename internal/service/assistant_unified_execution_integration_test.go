package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Joined unified-execution tests: real service wiring end to end. The only
// fakes sit at process boundaries: an HTTP model provider (spoken to by the
// production ChatClient and OpenAIAgentClient), a deterministic relay, and a
// recording tool provider. Every restart rebuilds every service.

// ---------------------------------------------------------------------------
// HTTP model provider

type assistantJoinedProvider struct {
	mu            sync.Mutex
	server        *httptest.Server
	plans         []string
	agentReplies  []map[string]any
	batchRequests []map[string]any
	agentRequests []map[string]any
}

func newAssistantJoinedProvider(t *testing.T) *assistantJoinedProvider {
	t.Helper()
	p := &assistantJoinedProvider{}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)
	return p
}

func (p *assistantJoinedProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	var message map[string]any
	finish := "stop"
	if _, planner := body["response_format"]; planner {
		p.batchRequests = append(p.batchRequests, body)
		if len(p.plans) == 0 {
			p.mu.Unlock()
			http.Error(w, "no scripted plan", http.StatusInternalServerError)
			return
		}
		message = map[string]any{"role": "assistant", "content": p.plans[0]}
		p.plans = p.plans[1:]
	} else {
		p.agentRequests = append(p.agentRequests, body)
		if len(p.agentReplies) == 0 {
			message = map[string]any{"role": "assistant", "content": "done"}
		} else {
			message = p.agentReplies[0]
			p.agentReplies = p.agentReplies[1:]
		}
		if _, calls := message["tool_calls"]; calls {
			finish = "tool_calls"
		}
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "cmpl", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
}

func (p *assistantJoinedProvider) requestCounts() (batch, agent int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.batchRequests), len(p.agentRequests)
}

func (p *assistantJoinedProvider) agentRequest(i int) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.agentRequests[i]
}

func assistantJoinedToolCall(id, name string, args map[string]any) map[string]any {
	encoded, _ := json.Marshal(args)
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}
}

// ---------------------------------------------------------------------------
// Recording tool provider (the MCP boundary)

type assistantJoinedDispatch struct {
	tool string
	args map[string]interface{}
}

type assistantJoinedTools struct {
	mu       sync.Mutex
	notify   func()
	dispatch []assistantJoinedDispatch
}

func (s *assistantJoinedTools) record(name string, args map[string]interface{}) {
	copied := map[string]interface{}{}
	for k, v := range args {
		copied[k] = v
	}
	s.mu.Lock()
	s.dispatch = append(s.dispatch, assistantJoinedDispatch{tool: name, args: copied})
	s.mu.Unlock()
	if s.notify != nil {
		s.notify()
	}
}

func (s *assistantJoinedTools) CallTool(_ context.Context, name string, args map[string]interface{}) (*AssistantToolRuntimeToolResult, error) {
	s.record(name, args)
	return &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true,"tool":"` + name + `"}`}}}, nil
}

func (s *assistantJoinedTools) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	s.record(name, args)
	key, _ := args["idempotency_key"].(string)
	return &domain.AsyncToolReceipt{ToolName: name, RequestEventID: assistantTestRequestID(key), RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: key}, nil
}

func (s *assistantJoinedTools) calls() []assistantJoinedDispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]assistantJoinedDispatch(nil), s.dispatch...)
}

func (s *assistantJoinedTools) count(tool string) int {
	n := 0
	for _, call := range s.calls() {
		if call.tool == tool {
			n++
		}
	}
	return n
}

func assistantJoinedRegistry() assistantRuntimeRegistry {
	return assistantRuntimeRegistryWith(
		AssistantToolRuntimeToolDescriptor{Name: "read-one", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "number"}}}},
		AssistantToolRuntimeToolDescriptor{Name: "read-two", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}},
		AssistantToolRuntimeToolDescriptor{Name: "mutate", ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"zone": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"}}, "required": []any{"zone", "idempotency_key"}}},
	)
}

// ---------------------------------------------------------------------------
// The joined stack: every service the application wires for the assistant.

type assistantJoinedOptions struct {
	permissions config.AssistantPermissionsConfig
	rules       []AssistantPermissionRule
	commands    *AssistantCommandLibrary
}

type assistantJoinedStack struct {
	engine     *AssistantExecutionEngine
	router     *AssistantOrchestrator
	recovery   *AssistantSessionRecoveryRunner
	transcript *AssistantTranscriptStore
	cancel     context.CancelFunc
	pubkey     string
}

func newAssistantJoinedStack(t *testing.T, relay *assistantTestRelay, signer nostr.Signer, tools *assistantJoinedTools, provider *assistantJoinedProvider, defaultWorkflow domain.AssistantWorkflow, opts assistantJoinedOptions) *assistantJoinedStack {
	t.Helper()
	pk, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if opts.permissions.Mode == "" {
		opts.permissions.Mode = domain.AssistantPermissionModeAudited
	}
	identity := AssistantIdentity{AgentID: "assistant-joined", Pubkey: pk.Hex()}
	keys := StaticAssistantTranscriptKeyProvider{Key: testAssistantTranscriptKey()}
	transcript := NewAssistantTranscriptStore(AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: identity, KeyProvider: keys})
	store := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, KeyProvider: keys})
	registry := assistantJoinedRegistry()
	runtime := NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: tools, Registry: registry, Permissions: NewAssistantPermissionEngine(opts.permissions, opts.rules)})
	status := NewAssistantStatusEventPublisher(relay, signer, identity)
	proposal := NewAssistantProposalContext(AssistantProposalContextConfig{Commands: opts.commands})
	contextBuilder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, nil, AssistantContextBuilderConfig{TranscriptHistory: transcript})
	batch := NewAssistantBatchPlanner(AssistantBatchPlannerConfig{
		ChatClient:       llm.NewChatClient(llm.ChatClientConfig{BaseURL: provider.server.URL, Model: "planner-model"}, slog.Default()),
		ContextBuilder:   contextBuilder,
		Context:          proposal,
		History:          transcript,
		Transcript:       transcript,
		Status:           status,
		AllowedToolNames: []string{"read-one", "read-two", "mutate"},
	})
	iterative, err := NewAssistantAgentLoop(AssistantAgentLoopConfig{
		ModelClient:    llm.NewOpenAIAgentClient(llm.OpenAIAgentClientConfig{BaseURL: provider.server.URL, Model: "agent-model"}, slog.Default()),
		ToolRuntime:    runtime,
		ContextBuilder: contextBuilder,
		ToolSchemas:    assistantLoopSchemas{schemas: schemasFromRuntimeRegistry(registry)},
		Transcript:     transcript,
		Context:        proposal,
		Status:         status,
		Agentic:        config.AssistantAgenticConfig{Model: "agent-model", MaxIterations: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: runtime, Observer: &AssistantExecutionObserver{Subscriber: relay}, Batch: batch, Iterative: iterative, Transcript: transcript, ScopeResolver: proposal, Publisher: relay, Signer: signer, Subscriber: relay, Identity: identity, Lifecycle: lifecycle})
	router := NewAssistantOrchestrator(AssistantOrchestratorConfig{Engine: engine, DefaultWorkflow: defaultWorkflow, Publisher: relay, Subscriber: relay, Signer: signer, Identity: identity})
	st := &assistantJoinedStack{engine: engine, router: router, transcript: transcript, cancel: cancel, pubkey: pk.Hex()}
	st.recovery = NewAssistantSessionRecoveryRunner(router, AssistantSessionRecoveryConfig{Engine: engine, Store: store, Subscriber: relay, ServicePubkey: pk.Hex()})
	t.Cleanup(st.crash)
	return st
}

func (st *assistantJoinedStack) crash() {
	st.cancel()
	st.engine.Wait()
}

type assistantJoinedClient struct {
	router *AssistantOrchestrator
	seq    int
}

func (c *assistantJoinedClient) source() AssistantRequestSource {
	c.seq++
	id := assistantTestID("joined-request-" + string(rune('a'+c.seq)))
	return AssistantRequestSource{Event: &nostr.Event{ID: id, PubKey: assistantTestPubKey("operator"), Kind: 25910}, OperatorPubkey: "operator", RequestID: id.Hex()}
}

func assistantJoinedAccepted(t *testing.T, res AssistantOperationResult, err error) domain.AssistantSessionV2 {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return requireAccepted(t, res)
}

// acceptSettled checks a prompt was accepted at its checkpointed turn start
// and waits for the asynchronous proposal to settle.
func (st *assistantJoinedStack) acceptSettled(t *testing.T, relay *assistantTestRelay, res AssistantOperationResult, err error) domain.AssistantSessionV2 {
	t.Helper()
	started := assistantJoinedAccepted(t, res, err)
	if started.Phase != domain.AssistantExecutionProposing {
		t.Fatalf("prompt accepted at phase %s, want proposing", started.Phase)
	}
	relay.waitFor(t, "proposal settled", func() bool {
		x, _ := st.engine.Snapshot(started.SessionID)
		return x.Phase != domain.AssistantExecutionProposing
	})
	p, _ := st.engine.Projection(started.SessionID)
	return p
}

func (st *assistantJoinedStack) settle(t *testing.T, relay *assistantTestRelay, sessionID string, want domain.AssistantExecutionPhase) domain.AssistantExecution {
	t.Helper()
	var x domain.AssistantExecution
	relay.waitFor(t, "phase "+string(want), func() bool {
		x, _ = st.engine.Snapshot(sessionID)
		return x.Phase == want
	})
	return x
}

func assistantJoinedTranscriptLogicalIDs(t *testing.T, st *assistantJoinedStack, sessionID string) map[string]int {
	t.Helper()
	records, err := st.transcript.Replay(context.Background(), AssistantTranscriptReplayQuery{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, record := range records {
		counts[assistantTranscriptLogicalID(record)]++
	}
	return counts
}

func assistantJoinedLatestProjection(t *testing.T, relay *assistantTestRelay, sessionID string) domain.AssistantSessionV2 {
	t.Helper()
	for _, ev := range relay.eventsOfKind(nostr.Kind(domain.KindAssistantSessionState)) {
		if tagValue(ev.Tags, "d") == domain.AssistantSessionSchemaV2+":"+sessionID {
			var p domain.AssistantSessionV2
			mustUnmarshalEventContent(t, &ev, &p)
			return p
		}
	}
	t.Fatalf("no v2 projection for %s", sessionID)
	return domain.AssistantSessionV2{}
}

const assistantJoinedPlan = `{"summary":"Inspect, change zone a, verify.","needs_clarification":false,"risk_level":"medium","steps":[` +
	`{"step_id":"step-1","title":"Inspect","description":"read one","tool_name":"read-one","tool_args":{"q":1}},` +
	`{"step_id":"step-2","title":"Change zone","description":"mutate zone","tool_name":"mutate","tool_args":{"zone":"a"}},` +
	`{"step_id":"step-3","title":"Verify","description":"read two","tool_name":"read-two","tool_args":{}}]}`

// Batch: model proposal -> operator edits (reorder, delete, change an
// argument) -> revision-bound v2 approval -> common executor -> async receipt
// -> full restart -> exactly-once completion. Continuation never asks a model.
func TestAssistantUnifiedExecutionJoinedBatchEditApproveRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	tools := &assistantJoinedTools{notify: relay.touch}
	provider := newAssistantJoinedProvider(t)
	provider.plans = []string{assistantJoinedPlan}

	first := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowIterative, assistantJoinedOptions{})
	client := &assistantJoinedClient{router: first.router}
	res, err := first.router.HandlePromptRequest(context.Background(), client.source(), domain.AssistantPromptRequest{ContractVersion: 2, Workflow: domain.AssistantWorkflowBatch, SessionID: "s-joined-batch", TurnID: "turn-1", Prompt: "change zone a safely"})
	draft := first.acceptSettled(t, relay, res, err)
	if draft.Workflow != domain.AssistantWorkflowBatch || draft.Phase != domain.AssistantExecutionAwaitingApproval || draft.Proposal == nil || draft.Proposal.Revision != 1 || len(draft.Proposal.Plan.Steps) != 3 {
		t.Fatalf("draft = %+v", draft)
	}
	if batchCalls, agentCalls := provider.requestCounts(); batchCalls != 1 || agentCalls != 0 {
		t.Fatalf("proposal model requests batch=%d agent=%d", batchCalls, agentCalls)
	}

	// The operator edits the public proposal: reorder, delete, change an arg.
	edited := draft.Proposal.Plan
	steps := edited.Steps
	changed := steps[1]
	changed.ToolArgs = map[string]any{"zone": "b"}
	edited.Steps = []domain.AssistantPlanStep{steps[2], changed}
	editedHash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: 2, SessionID: "s-joined-batch", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, Revision: 2, Scope: draft.Scope, Plan: edited})
	if err != nil {
		t.Fatal(err)
	}
	approval := domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approval-joined-batch", SessionID: "s-joined-batch", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, BaseRevision: 1, BasePlanHash: draft.Proposal.Hash, ApprovedRevision: 2, ApprovedPlanHash: editedHash, Decision: "approve", ModifiedPlan: &edited}

	// An edit approved under the base revision's hash is not bound to what
	// the operator saw: refused, nothing dispatched.
	unbound := approval
	unbound.RequestID = "approval-unbound"
	unbound.ApprovedPlanHash = draft.Proposal.Hash
	res, err = first.router.HandleApprovalRequest(context.Background(), client.source(), unbound)
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, res, AssistantRefusalStaleApproval)
	if len(tools.calls()) != 0 {
		t.Fatal("unbound edit dispatched work")
	}

	res, err = first.router.HandleApprovalRequest(context.Background(), client.source(), approval)
	approved := assistantJoinedAccepted(t, res, err)
	if approved.Proposal == nil || approved.Proposal.Revision != 2 || approved.Proposal.Hash != editedHash || approved.Proposal.PreviousHash != draft.Proposal.Hash {
		t.Fatalf("approved proposal not revision-bound: %+v", approved.Proposal)
	}
	x := first.settle(t, relay, "s-joined-batch", domain.AssistantExecutionWaitingAsync)
	if len(x.Work) != 2 || x.Work[0].OriginID != "step-3" || x.Work[1].OriginID != "step-2" || x.Work[0].State != domain.AssistantWorkSucceeded || x.Work[1].Receipt == nil {
		t.Fatalf("executor did not run exactly the approved sequence: %+v", x.Work)
	}
	wantKey := "assistant:s-joined-batch:" + editedHash + ":step-2"
	if x.Work[1].IdempotencyKey != wantKey || x.Work[1].Receipt.IdempotencyKey != wantKey {
		t.Fatalf("mutation key = %q receipt=%q, want %q", x.Work[1].IdempotencyKey, x.Work[1].Receipt.IdempotencyKey, wantKey)
	}
	requestID := x.Work[1].Receipt.RequestEventID

	// Restart: every service is rebuilt; the downstream result lands while
	// the process is down.
	first.crash()
	publishAssistantResult(t, relay, requestID, "completed")
	second := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowIterative, assistantJoinedOptions{})
	if err := second.recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := second.settle(t, relay, "s-joined-batch", domain.AssistantExecutionCompleted)

	calls := tools.calls()
	if len(calls) != 2 || calls[0].tool != "read-two" || calls[1].tool != "mutate" || calls[1].args["zone"] != "b" || calls[1].args["idempotency_key"] != wantKey {
		t.Fatalf("provider dispatches = %+v, want read-two then mutate(zone=b) exactly once", calls)
	}
	if tools.count("read-one") != 0 {
		t.Fatal("deleted step executed")
	}
	for _, w := range final.Work {
		if w.State != domain.AssistantWorkSucceeded {
			t.Fatalf("final work = %+v", final.Work)
		}
	}
	if batchCalls, agentCalls := provider.requestCounts(); batchCalls != 1 || agentCalls != 0 {
		t.Fatalf("batch continuation made model requests: batch=%d agent=%d", batchCalls, agentCalls)
	}
	logical := assistantJoinedTranscriptLogicalIDs(t, second, "s-joined-batch")
	for _, w := range final.Work {
		if n := logical[final.RunID+":"+w.WorkID+":observation"]; n != 1 {
			t.Fatalf("observation for %s recorded %d times", w.WorkID, n)
		}
	}
	if revisions := assistantCheckpointRevisions(relay, final.RunID); len(revisions) == 0 || revisions[len(revisions)-1] != final.Revision {
		t.Fatalf("checkpoint chain %v does not end at %d", revisions, final.Revision)
	}
	if p := assistantJoinedLatestProjection(t, relay, "s-joined-batch"); p.Phase != domain.AssistantExecutionCompleted || p.CurrentRunID != final.RunID || p.SubmittedEffects != 0 {
		t.Fatalf("latest projection = %+v", p)
	}
	// A replayed downstream result after completion changes nothing.
	publishAssistantResult(t, relay, requestID, "completed")
	if again, _ := second.engine.Snapshot("s-joined-batch"); again.Revision != final.Revision || tools.count("mutate") != 1 {
		t.Fatalf("replayed result advanced the run: rev %d -> %d", final.Revision, again.Revision)
	}
}

// Iterative: one model response with two calls -> read executes, the mutation
// needs an action approval -> v2 action decision -> common executor -> async
// receipt -> full restart -> observation consumed exactly once -> the model is
// asked to continue exactly once and sees both observations.
func TestAssistantUnifiedExecutionJoinedIterativeApproveRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	tools := &assistantJoinedTools{notify: relay.touch}
	provider := newAssistantJoinedProvider(t)
	provider.agentReplies = []map[string]any{
		{"role": "assistant", "content": "", "tool_calls": []any{assistantJoinedToolCall("call-read", "read-one", map[string]any{"q": 7}), assistantJoinedToolCall("call-mutate", "mutate", map[string]any{"zone": "c", "idempotency_key": "model-invented"})}},
		{"role": "assistant", "content": "zone c changed and verified"},
	}
	askMutate := []AssistantPermissionRule{{ID: "ask-mutate", Decision: domain.AssistantPermissionDecisionAsk, ToolNames: []string{"mutate"}}}
	opts := assistantJoinedOptions{rules: askMutate}

	first := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowIterative, opts)
	client := &assistantJoinedClient{router: first.router}
	res, err := first.router.HandlePromptRequest(context.Background(), client.source(), domain.AssistantPromptRequest{ContractVersion: 2, SessionID: "s-joined-iter", TurnID: "turn-1", Prompt: "change zone c"})
	started := assistantJoinedAccepted(t, res, err)
	if started.Workflow != domain.AssistantWorkflowIterative {
		t.Fatalf("default workflow not applied: %+v", started)
	}
	x := first.settle(t, relay, "s-joined-iter", domain.AssistantExecutionAwaitingApproval)
	if len(x.Work) != 2 || x.Work[0].State != domain.AssistantWorkSucceeded || x.Work[1].State != domain.AssistantWorkAwaitingApproval || tools.count("read-one") != 1 || tools.count("mutate") != 0 {
		t.Fatalf("pre-approval work = %+v", x.Work)
	}
	pending := assistantJoinedLatestProjection(t, relay, "s-joined-iter")
	if len(pending.PendingApprovals) != 1 || pending.PendingApprovals[0] != x.Work[1].WorkID {
		t.Fatalf("projection pending approvals = %v", pending.PendingApprovals)
	}
	res, err = first.router.HandleApprovalRequest(context.Background(), client.source(), domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approve-mutate", SessionID: "s-joined-iter", RunID: x.RunID, Workflow: domain.AssistantWorkflowIterative, ActionID: x.Work[1].WorkID, Decision: "approve"})
	assistantJoinedAccepted(t, res, err)
	x = first.settle(t, relay, "s-joined-iter", domain.AssistantExecutionWaitingAsync)
	wantKey := "assistant-agent:s-joined-iter:" + x.RunID + ":call-mutate"
	if x.Work[1].Receipt == nil || x.Work[1].IdempotencyKey != wantKey {
		t.Fatalf("mutation key = %q (model-supplied keys must be ignored)", x.Work[1].IdempotencyKey)
	}
	if _, agentCalls := provider.requestCounts(); agentCalls != 1 {
		t.Fatalf("model asked to continue before the submitted effect was observed: %d", agentCalls)
	}

	first.crash()
	publishAssistantResult(t, relay, x.Work[1].Receipt.RequestEventID, "completed")
	second := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowIterative, opts)
	if err := second.recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := second.settle(t, relay, "s-joined-iter", domain.AssistantExecutionCompleted)

	if tools.count("read-one") != 1 || tools.count("mutate") != 1 {
		t.Fatalf("dispatch counts read-one=%d mutate=%d, want exactly once each", tools.count("read-one"), tools.count("mutate"))
	}
	if calls := tools.calls(); calls[1].args["zone"] != "c" || calls[1].args["idempotency_key"] != wantKey {
		t.Fatalf("mutation dispatched with %+v", calls[1].args)
	}
	if _, agentCalls := provider.requestCounts(); agentCalls != 2 {
		t.Fatalf("agent model requests = %d, want exactly 2 across the restart", agentCalls)
	}
	toolMessages := map[string]bool{}
	for _, raw := range provider.agentRequest(1)["messages"].([]any) {
		message := raw.(map[string]any)
		if message["role"] == "tool" {
			toolMessages[message["tool_call_id"].(string)] = true
		}
	}
	if !toolMessages["call-read"] || !toolMessages["call-mutate"] {
		t.Fatalf("continuation lacked observations: %v", toolMessages)
	}
	logical := assistantJoinedTranscriptLogicalIDs(t, second, "s-joined-iter")
	for _, w := range final.Work {
		if n := logical[final.RunID+":"+w.WorkID+":observation"]; n != 1 {
			t.Fatalf("observation for %s recorded %d times", w.WorkID, n)
		}
		if w.State != domain.AssistantWorkSucceeded {
			t.Fatalf("final work = %+v", final.Work)
		}
	}
	if logical[final.RunID+":model:1"] != 1 || logical[final.RunID+":model:2"] != 1 || logical[final.RunID+":user_prompt"] != 1 {
		t.Fatalf("model/user transcript entries duplicated or missing: %v", logical)
	}
}

// Approval never bypasses the current permission policy: a policy tightened
// between proposal and approval (here, across a restart) refuses the approval
// and nothing runs.
func TestAssistantUnifiedExecutionJoinedApprovalCannotBypassChangedPolicy(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	tools := &assistantJoinedTools{notify: relay.touch}
	provider := newAssistantJoinedProvider(t)
	provider.plans = []string{assistantJoinedPlan}

	first := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowBatch, assistantJoinedOptions{})
	client := &assistantJoinedClient{router: first.router}
	res, err := first.router.HandlePromptRequest(context.Background(), client.source(), domain.AssistantPromptRequest{ContractVersion: 2, SessionID: "s-policy", TurnID: "t", Prompt: "change zone a"})
	draft := first.acceptSettled(t, relay, res, err)
	first.crash()

	readonly := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowBatch, assistantJoinedOptions{permissions: config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeReadonly}})
	if err := readonly.recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := draft.Proposal
	res, err = readonly.router.HandleApprovalRequest(context.Background(), client.source(), domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approve-policy", SessionID: "s-policy", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, res, AssistantRefusalApprovalDenied)
	if !strings.Contains(res["error"].(string), "readonly mode denies") {
		t.Fatalf("refusal reason = %v", res["error"])
	}
	if x, _ := readonly.engine.Snapshot("s-policy"); x.Phase != domain.AssistantExecutionAwaitingApproval || len(x.Work) != 0 || len(tools.calls()) != 0 {
		t.Fatalf("denied approval changed state or dispatched: phase=%s work=%d calls=%d", x.Phase, len(x.Work), len(tools.calls()))
	}
}

// Approval never widens the persisted command scope: neither an approved
// out-of-scope proposal step nor an edit introducing one can run.
func TestAssistantUnifiedExecutionJoinedApprovalCannotBypassCommandScope(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	tools := &assistantJoinedTools{notify: relay.touch}
	provider := newAssistantJoinedProvider(t)
	provider.plans = []string{`{"summary":"Inspect only.","needs_clarification":false,"risk_level":"low","steps":[{"step_id":"only","title":"Inspect","description":"read","tool_name":"read-one","tool_args":{"q":2}}]}`}
	commands := mustCommandLibrary(AssistantCommandSpec{Name: "inspect", Template: "Inspect $ARGUMENTS without changing anything.", AllowedTools: []string{"read-one"}})
	st := newAssistantJoinedStack(t, relay, signer, tools, provider, domain.AssistantWorkflowBatch, assistantJoinedOptions{commands: commands})
	client := &assistantJoinedClient{router: st.router}

	res, err := st.router.HandlePromptRequest(context.Background(), client.source(), domain.AssistantPromptRequest{ContractVersion: 2, SessionID: "s-scope", TurnID: "t", Prompt: "/inspect zone a"})
	draft := st.acceptSettled(t, relay, res, err)
	if draft.Scope.CommandName != "inspect" || len(draft.Scope.AllowedTools) != 1 || draft.Scope.ArgumentsDigest == "" {
		t.Fatalf("persisted scope commitment = %+v", draft.Scope)
	}
	if !strings.Contains(provider.batchRequests[0]["messages"].([]any)[0].(map[string]any)["content"].(string), "read-one.") {
		t.Fatal("planner prompt advertised tools outside the command scope")
	}
	edited := draft.Proposal.Plan
	edited.Steps = append(edited.Steps, domain.AssistantPlanStep{StepID: "sneak", Title: "Change", ToolName: "mutate", ToolArgs: map[string]any{"zone": "a"}})
	hash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: 2, SessionID: "s-scope", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, Revision: 2, Scope: draft.Scope, Plan: edited})
	if err != nil {
		t.Fatal(err)
	}
	res, err = st.router.HandleApprovalRequest(context.Background(), client.source(), domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approve-sneak", SessionID: "s-scope", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, BaseRevision: 1, BasePlanHash: draft.Proposal.Hash, ApprovedRevision: 2, ApprovedPlanHash: hash, Decision: "approve", ModifiedPlan: &edited})
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, res, AssistantRefusalApprovalDenied)
	if !strings.Contains(res["error"].(string), "allowed-tools scope") || len(tools.calls()) != 0 {
		t.Fatalf("out-of-scope edit: %v calls=%d", res["error"], len(tools.calls()))
	}
	// The unedited in-scope proposal still executes through the executor.
	p := draft.Proposal
	res, err = st.router.HandleApprovalRequest(context.Background(), client.source(), domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approve-scope", SessionID: "s-scope", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: 1, BasePlanHash: p.Hash, ApprovedRevision: 1, ApprovedPlanHash: p.Hash, Decision: "approve"})
	assistantJoinedAccepted(t, res, err)
	st.settle(t, relay, "s-scope", domain.AssistantExecutionCompleted)
	if calls := tools.calls(); len(calls) != 1 || calls[0].tool != "read-one" {
		t.Fatalf("in-scope approval dispatches = %+v", calls)
	}
}
