package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"go.uber.org/zap"

	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/service"
)

// ---------------------------------------------------------------------------
// memoryRelay: stored events, backfill then EOSE then live delivery.

type memoryRelay struct {
	mu      sync.Mutex
	events  []nostr.Event
	subs    []*memoryRelaySub
	changed chan struct{}
}

type memoryRelaySub struct {
	filters []nostr.Filter
	events  chan *nostr.Event
	eose    chan struct{}
	closed  chan service.AssistantRelayClosed
	done    chan struct{}
	once    sync.Once
}

func newMemoryRelay() *memoryRelay { return &memoryRelay{changed: make(chan struct{})} }

func (r *memoryRelay) touchLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *memoryRelay) Publish(_ context.Context, ev nostr.Event) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.touchLocked()
	if int(ev.Kind) >= 30000 && int(ev.Kind) < 40000 {
		d := ""
		if tag := ev.Tags.Find("d"); tag != nil {
			d = tag[1]
		}
		for i := range r.events {
			old := r.events[i]
			if old.Kind == ev.Kind && old.PubKey == ev.PubKey && old.Tags.Find("d") != nil && old.Tags.Find("d")[1] == d {
				if old.CreatedAt > ev.CreatedAt {
					return 1, nil
				}
				r.events = append(r.events[:i], r.events[i+1:]...)
				break
			}
		}
	}
	r.events = append(r.events, ev)
	for _, sub := range r.subs {
		for _, f := range sub.filters {
			if f.Matches(ev) {
				copyEv := ev
				go func(s *memoryRelaySub) {
					select {
					case s.events <- &copyEv:
					case <-s.done:
					}
				}(sub)
				break
			}
		}
	}
	return 1, nil
}

func (r *memoryRelay) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (service.AssistantMergedSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.touchLocked()
	sub := &memoryRelaySub{filters: filters, events: make(chan *nostr.Event), eose: make(chan struct{}), closed: make(chan service.AssistantRelayClosed), done: make(chan struct{})}
	var backfill []nostr.Event
	for _, ev := range r.events {
		for _, f := range filters {
			if f.Matches(ev) {
				backfill = append(backfill, ev)
				break
			}
		}
	}
	r.subs = append(r.subs, sub)
	go func() {
		for i := range backfill {
			select {
			case sub.events <- &backfill[i]:
			case <-sub.done:
				return
			}
		}
		close(sub.eose)
		r.mu.Lock()
		r.touchLocked()
		r.mu.Unlock()
	}()
	return sub, nil
}

func (s *memoryRelaySub) EventChan() <-chan *nostr.Event                  { return s.events }
func (s *memoryRelaySub) ClosedChan() <-chan service.AssistantRelayClosed { return s.closed }
func (s *memoryRelaySub) EOSEChan() <-chan struct{}                       { return s.eose }
func (s *memoryRelaySub) Close()                                          { s.once.Do(func() { close(s.done) }) }

func (r *memoryRelay) liveSubscriptionFor(kind nostr.Kind, e string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sub := range r.subs {
		select {
		case <-sub.done:
			continue
		default:
		}
		for _, f := range sub.filters {
			for _, k := range f.Kinds {
				if k == kind {
					for _, v := range f.Tags["e"] {
						if v == e {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (r *memoryRelay) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		ch := r.changed
		r.mu.Unlock()
		if cond() {
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ---------------------------------------------------------------------------
// Model fakes at the provider seam.

type wiringPlanner struct{}

func (wiringPlanner) PlanFromPrompt(context.Context, string, string) (*domain.AssistantPlan, error) {
	return &domain.AssistantPlan{Summary: "Deploy", RiskLevel: "low", Steps: []domain.AssistantPlanStep{{StepID: "one", Title: "Deploy", ToolName: "bahia_assistant_service_deploy", ToolArgs: map[string]any{"service_id": "00000000-0000-0000-0000-000000000001", "environment_id": "00000000-0000-0000-0000-000000000002", "artifact_id": "00000000-0000-0000-0000-000000000003"}}}}, nil
}

type wiringAgent struct{}

func (wiringAgent) Next(context.Context, llmadapter.AgentModelRequest, llmadapter.AgentModelEventHandler) (*llmadapter.AgentModelResponse, error) {
	return &llmadapter.AgentModelResponse{Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "answered"}}, StopReason: llmadapter.AgentStopReasonEndTurn}, nil
}

func wiringConfig(t *testing.T, agentic bool, defaultWorkflow string) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Assistant.Enabled = true
	cfg.Assistant.LLMModel = "planner"
	cfg.Assistant.Agentic.Enabled = agentic
	cfg.Assistant.DefaultWorkflow = defaultWorkflow
	cfg.Nostr.PrivateKey = strings.Repeat("1", 64)
	return cfg
}

func buildTestAssistantExecution(t *testing.T, cfg *config.Config, relay *memoryRelay) (*assistantExecutionWiring, nostr.Signer) {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(cfg.Nostr.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	signer := keyer.NewPlainKeySigner([32]byte(secret))
	keys, err := assistantTranscriptKeyProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	identity := service.AssistantIdentity{AgentID: "assistant-wiring", Pubkey: secret.Public().Hex()}
	transcript := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: identity, KeyProvider: keys, ServicePubkey: secret.Public().Hex()})
	wiring, err := buildAssistantExecution(assistantExecutionDeps{
		Config:         cfg,
		MCPServer:      mcp.NewServerWithOptions(nil, zap.NewNop(), mcp.ServerDeps{}),
		ContextBuilder: service.NewAssistantContextBuilder(nil, nil, nil, nil, nil, nil, service.AssistantContextBuilderConfig{TranscriptHistory: transcript}),
		ChatClient:     wiringPlanner{},
		ModelClient:    wiringAgent{},
		Publisher:      relay,
		Subscriber:     relay,
		Signer:         signer,
		Identity:       identity,
		ServicePubkey:  secret.Public().Hex(),
		Transcript:     transcript,
		KeyProvider:    keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wiring.Lifecycle.Stop)
	return wiring, signer
}

func wiringSource(label string) service.AssistantRequestSource {
	var id nostr.ID
	copy(id[:], []byte(strings.Repeat(label, 32)))
	return service.AssistantRequestSource{Event: &nostr.Event{ID: id, Kind: 25910}, OperatorPubkey: "operator", RequestID: id.Hex()}
}

// Both workflows are constructed and routed through the one executor whatever
// the deprecated agentic flag says; the flag only picks the default.
func TestAssistantExecutionWiringIsUnconditionalAndFlagOnlySelectsDefault(t *testing.T) {
	cases := []struct {
		agentic  bool
		explicit string
		want     domain.AssistantWorkflow
	}{
		{agentic: true, want: domain.AssistantWorkflowIterative},
		{agentic: false, want: domain.AssistantWorkflowBatch},
		{agentic: true, explicit: "batch", want: domain.AssistantWorkflowBatch},
	}
	for _, tc := range cases {
		relay := newMemoryRelay()
		wiring, _ := buildTestAssistantExecution(t, wiringConfig(t, tc.agentic, tc.explicit), relay)
		if wiring.DefaultWorkflow != tc.want || wiring.Batch == nil || wiring.Iterative == nil || wiring.Engine == nil || wiring.Store == nil || wiring.Recovery == nil {
			t.Fatalf("agentic=%v default=%q wiring=%+v", tc.agentic, tc.explicit, wiring)
		}
		batch, err := wiring.Orchestrator.HandlePromptRequest(context.Background(), wiringSource("b"), domain.AssistantPromptRequest{ContractVersion: 2, Workflow: domain.AssistantWorkflowBatch, SessionID: "s-batch", TurnID: "t", Prompt: "deploy"})
		if err != nil || batch["status"] != "accepted" || batch["phase"] != string(domain.AssistantExecutionAwaitingApproval) {
			t.Fatalf("agentic=%v batch prompt = %#v err=%v", tc.agentic, batch, err)
		}
		iterative, err := wiring.Orchestrator.HandlePromptRequest(context.Background(), wiringSource("i"), domain.AssistantPromptRequest{ContractVersion: 2, Workflow: domain.AssistantWorkflowIterative, SessionID: "s-iter", TurnID: "t", Prompt: "question"})
		if err != nil || iterative["status"] != "accepted" || iterative["phase"] != string(domain.AssistantExecutionCompleted) {
			t.Fatalf("agentic=%v iterative prompt = %#v err=%v", tc.agentic, iterative, err)
		}
		defaulted, err := wiring.Orchestrator.HandlePromptRequest(context.Background(), wiringSource("d"), domain.AssistantPromptRequest{ContractVersion: 2, SessionID: "s-default", TurnID: "t", Prompt: "deploy"})
		if err != nil || defaulted["workflow"] != string(tc.want) {
			t.Fatalf("agentic=%v default prompt = %#v err=%v", tc.agentic, defaulted, err)
		}
	}
}

// Startup recovery is wired to the engine and store: an in-flight run found
// on the relay is resumed (its receipt observed and the run completed), not
// parked.
func TestAssistantExecutionWiringStartupRecoveryResumes(t *testing.T) {
	relay := newMemoryRelay()
	cfg := wiringConfig(t, true, "")
	wiring, signer := buildTestAssistantExecution(t, cfg, relay)

	args := map[string]any{"service_id": "00000000-0000-0000-0000-000000000001", "environment_id": "00000000-0000-0000-0000-000000000002", "artifact_id": "00000000-0000-0000-0000-000000000003"}
	digest, err := domain.ComputeAssistantArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &domain.AsyncToolReceipt{ToolName: "bahia_assistant_service_deploy", RequestEventID: strings.Repeat("ab", 32), RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: "assistant:s-resume:hash:one"}
	x := domain.AssistantExecution{Version: 2, SessionID: "s-resume", RunID: "run-resume", TurnID: "t", RequestID: "r", Workflow: domain.AssistantWorkflowBatch, Revision: 1, Phase: domain.AssistantExecutionWaitingAsync,
		Proposal: &domain.AssistantProposalRevision{ProposalID: "p", Revision: 1, Hash: "hash", Plan: domain.AssistantPlan{Summary: "Deploy", Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: receipt.ToolName, ToolArgs: args}}}},
		Work:     []domain.AssistantWorkItem{{WorkID: "run-resume:one", OriginID: "one", ToolName: receipt.ToolName, Arguments: args, ArgumentsDigest: digest, IdempotencyKey: receipt.IdempotencyKey, State: domain.AssistantWorkWaitingAsync, Receipt: receipt}}}
	checkpoint, err := wiring.Store.Append(context.Background(), x, "")
	if err != nil {
		t.Fatal(err)
	}
	projection, _ := json.Marshal(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: "s-resume", OperatorPubkey: "operator", ExecutionVersion: 2, Workflow: domain.AssistantWorkflowBatch, CurrentRunID: "run-resume", ExecutionRevision: 1, Phase: domain.AssistantExecutionWaitingAsync, CheckpointEventID: checkpoint})
	ev := nostr.Event{Kind: domain.KindAssistantSessionState, CreatedAt: nostr.Now() - 60, Tags: nostr.Tags{{"d", domain.AssistantSessionSchemaV2 + ":s-resume"}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"session", "s-resume"}}, Content: string(projection)}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Publish(context.Background(), ev); err != nil {
		t.Fatal(err)
	}

	if err := wiring.Recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := wiring.Engine.Snapshot("s-resume"); !ok || got.RunID != "run-resume" {
		t.Fatalf("recovery parked the run instead of loading it: %+v ok=%v", got, ok)
	}
	relay.waitFor(t, "receipt observation resumed", func() bool { return relay.liveSubscriptionFor(7961, receipt.RequestEventID) })

	result := nostr.Event{Kind: 7961, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"e", receipt.RequestEventID}, {"status", "completed"}}, Content: `{"status":"completed"}`}
	if err := result.Sign(nostr.Generate()); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Publish(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "recovered run completes", func() bool {
		got, _ := wiring.Engine.Snapshot("s-resume")
		return got.Phase == domain.AssistantExecutionCompleted
	})
}
