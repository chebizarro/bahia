package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Shared assistant test fixtures: signing identities, deterministic
// publishers/subscribers and a scripted agent model.

func testAssistantSigner(t *testing.T) nostr.Signer {
	t.Helper()
	secret := nostr.Generate()
	return keyer.NewPlainKeySigner([32]byte(secret))
}

func assistantTestID(label string) nostr.ID {
	sum := sha256.Sum256([]byte(label))
	return nostr.ID(sum)
}

func assistantTestPubKey(label string) nostr.PubKey {
	sum := sha256.Sum256([]byte(label))
	return nostr.PubKey(sum)
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

func mustUnmarshalEventContent(t *testing.T, ev *nostr.Event, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(ev.Content), dst); err != nil {
		t.Fatalf("unmarshal event content: %v", err)
	}
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

// assistantTestSubscriber hands the test the one live subscription so it can
// drive EOSE, events and CLOSED explicitly.
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

// assistantLoopModel returns scripted responses, then a final "done" answer.
type assistantLoopModel struct {
	mu        sync.Mutex
	responses []*llm.AgentModelResponse
	requests  []llm.AgentModelRequest
}

var _ llm.AgentModelClient = (*assistantLoopModel)(nil)

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

func findToolObservation(req llm.AgentModelRequest, toolCallID string) *domain.AssistantToolObservation {
	for _, msg := range req.Messages {
		if msg.Role == domain.AssistantAgentMessageRoleTool && msg.ToolCallID == toolCallID {
			return msg.Observation
		}
	}
	return nil
}

func requestToolNames(req llm.AgentModelRequest) map[string]bool {
	names := map[string]bool{}
	for _, tool := range req.Tools {
		names[tool.Name] = true
	}
	return names
}

// assistantStatusRecorder captures informational status events.
type assistantStatusRecorder struct {
	mu       sync.Mutex
	statuses []map[string]any
}

func (r *assistantStatusRecorder) PublishAssistantStatus(_ context.Context, _ string, status string, content map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := map[string]any{"status": status}
	for k, v := range content {
		entry[k] = v
	}
	r.statuses = append(r.statuses, entry)
	return nil
}

func (r *assistantStatusRecorder) phase(phase string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.statuses) - 1; i >= 0; i-- {
		if r.statuses[i]["phase"] == phase {
			return r.statuses[i]
		}
	}
	return nil
}

// assistantLoopStack is a full executor stack whose iterative proposer is the
// real AssistantAgentLoop over the real encrypted transcript store and the
// real context builder, so continuation reads the same durable history.
type assistantLoopStack struct {
	*assistantStack
	loop   *AssistantAgentLoop
	status *assistantStatusRecorder
}

type assistantLoopStackOptions struct {
	registry assistantRuntimeRegistry
	agentic  AssistantAgentLoopConfig
	rules    []AssistantPermissionRule
	hooks    *AssistantHookRunner
	commands *AssistantCommandLibrary
	batch    AssistantBatchProposer
	permMode domain.AssistantPermissionMode
}

func newAssistantLoopStack(t *testing.T, relay *assistantTestRelay, signer nostr.Signer, server AssistantToolRuntimeMCPServer, model llm.AgentModelClient, opts assistantLoopStackOptions) *assistantLoopStack {
	t.Helper()
	registry := opts.registry
	if registry == nil {
		registry = assistantTestRegistry()
	}
	mode := opts.permMode
	if mode == "" {
		mode = domain.AssistantPermissionModeAudited
	}
	runtime := NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: registry, Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: mode}, opts.rules), Hooks: opts.hooks})
	proposalContext := NewAssistantProposalContext(AssistantProposalContextConfig{Commands: opts.commands, Hooks: opts.hooks})
	status := &assistantStatusRecorder{}
	var loop *AssistantAgentLoop
	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{runtime: runtime, scope: proposalContext, batch: opts.batch, iterativeFactory: func(transcript *AssistantTranscriptStore) AssistantIterativeProposer {
		cfg := opts.agentic
		cfg.ModelClient = model
		cfg.ToolRuntime = runtime
		cfg.ContextBuilder = NewAssistantContextBuilder(nil, nil, nil, nil, nil, nil, AssistantContextBuilderConfig{TranscriptHistory: transcript})
		cfg.ToolSchemas = assistantLoopSchemas{schemas: schemasFromRuntimeRegistry(registry)}
		cfg.Transcript = transcript
		cfg.Context = proposalContext
		cfg.Status = status
		if cfg.Hooks == nil {
			cfg.Hooks = opts.hooks
		}
		var err error
		loop, err = NewAssistantAgentLoop(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return loop
	}})
	return &assistantLoopStack{assistantStack: st, loop: loop, status: status}
}

func assistantSettledPhase(p domain.AssistantExecutionPhase) bool {
	switch p {
	case domain.AssistantExecutionCompleted, domain.AssistantExecutionFailed, domain.AssistantExecutionCancelled, domain.AssistantExecutionBlocked, domain.AssistantExecutionAwaitingApproval:
		return true
	}
	return false
}

// settle waits (on relay barriers, never sleeps) until the session's run is
// finished, blocked or awaiting an operator decision.
func (st *assistantStack) settle(t *testing.T, relay *assistantTestRelay, sessionID string) domain.AssistantExecution {
	t.Helper()
	var x domain.AssistantExecution
	relay.waitFor(t, "run of "+sessionID+" to settle", func() bool {
		x = st.snapshot(sessionID)
		return x.RunID != "" && assistantSettledPhase(x.Phase)
	})
	return x
}

func (st *assistantLoopStack) runIterative(t *testing.T, relay *assistantTestRelay, sessionID, prompt string) domain.AssistantExecution {
	t.Helper()
	if _, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: sessionID, TurnID: "turn-" + sessionID, Prompt: prompt}, OperatorPubkey: "operator", RequestEventID: "prompt-" + sessionID, DefaultWorkflow: domain.AssistantWorkflowIterative}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	return st.settle(t, relay, sessionID)
}

func (st *assistantLoopStack) runIterativeUntil(t *testing.T, relay *assistantTestRelay, sessionID, prompt string, cond func(domain.AssistantExecution) bool) domain.AssistantExecution {
	t.Helper()
	if _, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: sessionID, TurnID: "turn-" + sessionID, Prompt: prompt}, OperatorPubkey: "operator", RequestEventID: "prompt-" + sessionID, DefaultWorkflow: domain.AssistantWorkflowIterative}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	var x domain.AssistantExecution
	relay.waitFor(t, "run condition", func() bool {
		x = st.snapshot(sessionID)
		return cond(x)
	})
	return x
}
