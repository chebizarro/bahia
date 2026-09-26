package app

import (
	"context"
	"fmt"
	"log/slog"

	"fiatjaf.com/nostr"

	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/service"
)

// assistantExecutionDeps are the dependencies of the unified assistant
// executor. Publisher and Subscriber are interfaces so the wiring can be
// exercised against a deterministic relay.
type assistantExecutionDeps struct {
	Config          *config.Config
	MCPServer       *mcp.Server
	ContextBuilder  *service.AssistantContextBuilder
	ChatClient      service.AssistantChatClient
	ModelClient     llmadapter.AgentModelClient
	Publisher       service.AssistantEventPublisher
	Subscriber      service.AssistantRelaySubscriber
	Signer          nostr.Signer
	Identity        service.AssistantIdentity
	ServicePubkey   string
	Transcript      *service.AssistantTranscriptStore
	KeyProvider     service.AssistantTranscriptKeyProvider
	InitialSessions []domain.AssistantSession
	ExternalMCP     assistantExternalMCPRuntime
}

// assistantExecutionWiring is the constructed assistant stack. Both workflows
// always share one runtime, permission engine, transcript store, checkpoint
// store and executor; assistant.agentic.enabled only feeds the default
// workflow and never decides what is constructed. The batch proposer is the
// one optional part: it is built only when assistant.llm_model is set (Batch
// is nil otherwise and batch requests are refused with workflow_unavailable).
type assistantExecutionWiring struct {
	Orchestrator       *service.AssistantOrchestrator
	Engine             *service.AssistantExecutionEngine
	Store              *service.AssistantExecutionStore
	Runtime            *service.AssistantToolRuntime
	Batch              *service.AssistantBatchPlanner
	Iterative          *service.AssistantAgentLoop
	Recovery           *service.AssistantSessionRecoveryRunner
	Lifecycle          *assistantExecutorLifecycle
	DefaultWorkflow    domain.AssistantWorkflow
	AvailableWorkflows []domain.AssistantWorkflow
}

// assistantBatchUnavailableReason is logged at startup when the batch
// proposer is not constructed.
const assistantBatchUnavailableReason = "assistant.llm_model (batch proposer model) is not set; batch prompts and batch approvals are refused with workflow_unavailable, while already-approved batch runs still finish"

func buildAssistantExecution(deps assistantExecutionDeps) (*assistantExecutionWiring, error) {
	cfg := deps.Config
	if cfg == nil || deps.MCPServer == nil || deps.Publisher == nil || deps.Subscriber == nil || deps.Signer == nil || deps.Transcript == nil || deps.KeyProvider == nil {
		return nil, fmt.Errorf("assistant executor wiring requires config, MCP server, relay publisher/subscriber, signer, transcript store and key provider")
	}
	defaultWorkflow := domain.AssistantWorkflow(cfg.Assistant.ResolvedDefaultWorkflow())
	if !defaultWorkflow.Valid() {
		return nil, fmt.Errorf("assistant.default_workflow %q is invalid", defaultWorkflow)
	}
	batchAvailable := cfg.Assistant.BatchWorkflowAvailable()
	if defaultWorkflow == domain.AssistantWorkflowBatch && !batchAvailable {
		return nil, fmt.Errorf("assistant default workflow is batch but assistant.llm_model (batch proposer model) is not set")
	}
	agentToolRegistry, err := mcp.NewAssistantToolRegistryForServerWithExternal(deps.MCPServer, deps.ExternalMCP.descriptors)
	if err != nil {
		return nil, err
	}
	permissionEngine := service.NewAssistantPermissionEngine(cfg.Assistant.Permissions, deps.ExternalMCP.permissionRules)
	registry := assistantToolRegistryAdapter{registry: agentToolRegistry}
	var subagents *service.AssistantSubagentLibrary
	var skills *service.AssistantSkillLibrary
	var commands *service.AssistantCommandLibrary
	var hooks *service.AssistantHookRunner
	if cfg.Assistant.Subagents.Enabled {
		if subagents, err = service.LoadAssistantSubagents(cfg.Assistant.Subagents.Paths); err != nil {
			return nil, err
		}
	}
	if cfg.Assistant.Skills.Enabled {
		if skills, err = service.LoadAssistantSkills(cfg.Assistant.Skills.Paths); err != nil {
			return nil, err
		}
	}
	if cfg.Assistant.Commands.Enabled {
		if commands, err = service.LoadAssistantCommands(cfg.Assistant.Commands.Paths); err != nil {
			return nil, err
		}
	}
	if cfg.Assistant.Hooks.Enabled {
		hookSet, loadErr := service.LoadAssistantHooks(cfg.Assistant.Hooks.Paths)
		if loadErr != nil {
			return nil, loadErr
		}
		hooks = service.NewAssistantHookRunner(service.AssistantHookRunnerConfig{
			Set:    hookSet,
			Prompt: service.NewAssistantHookModelPromptEvaluator(service.AssistantHookModelPromptEvaluatorConfig{ModelClient: deps.ModelClient, Model: cfg.Assistant.Agentic.Model}),
			MCP:    service.NewAssistantReadOnlyMCPHookCaller(service.AssistantReadOnlyMCPHookCallerConfig{MCPServer: assistantMCPRuntimeAdapter{server: deps.MCPServer}, Registry: registry}),
		})
	}
	runtime := service.NewAssistantToolRuntime(service.AssistantToolRuntimeConfig{
		MCPServer:   assistantMCPRuntimeAdapter{server: deps.MCPServer, externalTools: deps.ExternalMCP.clients},
		Registry:    registry,
		Permissions: permissionEngine,
		Hooks:       hooks,
	})
	status := service.NewAssistantStatusEventPublisher(deps.Publisher, deps.Signer, deps.Identity)
	proposalContext := service.NewAssistantProposalContext(service.AssistantProposalContextConfig{Commands: commands, Hooks: hooks})
	available := []domain.AssistantWorkflow{domain.AssistantWorkflowIterative}
	// The engine's Batch must be a nil interface (not a typed nil pointer) when
	// the batch proposer is absent, so it can refuse batch requests.
	var batch *service.AssistantBatchPlanner
	var batchProposer service.AssistantBatchProposer
	if batchAvailable {
		batch = service.NewAssistantBatchPlanner(service.AssistantBatchPlannerConfig{
			ChatClient:       deps.ChatClient,
			ContextBuilder:   deps.ContextBuilder,
			Context:          proposalContext,
			History:          deps.Transcript,
			Transcript:       deps.Transcript,
			Status:           status,
			AllowedToolNames: assistantToolNames(deps.MCPServer),
			StreamingEnabled: cfg.Assistant.LLMStreaming,
			Logger:           slog.Default(),
		})
		batchProposer = batch
		available = []domain.AssistantWorkflow{domain.AssistantWorkflowBatch, domain.AssistantWorkflowIterative}
	}
	iterative, err := service.NewAssistantAgentLoop(service.AssistantAgentLoopConfig{
		ModelClient:    deps.ModelClient,
		ToolRuntime:    runtime,
		ContextBuilder: deps.ContextBuilder,
		ToolSchemas:    assistantToolSchemaProvider{registry: agentToolRegistry},
		Transcript:     deps.Transcript,
		Context:        proposalContext,
		Status:         status,
		Agentic:        cfg.Assistant.Agentic,
		Subagents:      subagents,
		Skills:         skills,
		Hooks:          hooks,
		Logger:         slog.Default(),
	})
	if err != nil {
		return nil, err
	}
	store := service.NewAssistantExecutionStore(service.AssistantExecutionStoreConfig{Publisher: deps.Publisher, Subscriber: deps.Subscriber, Signer: deps.Signer, KeyProvider: deps.KeyProvider, ServicePubkey: deps.ServicePubkey})
	lifecycle := newAssistantExecutorLifecycle()
	engine := service.NewAssistantExecutionEngine(service.AssistantExecutionEngineConfig{
		Store:                      store,
		Runtime:                    runtime,
		Observer:                   &service.AssistantExecutionObserver{Subscriber: deps.Subscriber},
		Batch:                      batchProposer,
		Iterative:                  iterative,
		Transcript:                 deps.Transcript,
		ScopeResolver:              proposalContext,
		Evidence:                   &service.AssistantContextVMRequestEvidenceResolver{Subscriber: deps.Subscriber, RequestAuthor: deps.ServicePubkey, Methods: mcp.AssistantAsyncToolRequestMethods()},
		Publisher:                  deps.Publisher,
		Signer:                     deps.Signer,
		Subscriber:                 deps.Subscriber,
		Identity:                   deps.Identity,
		Lifecycle:                  lifecycle.ctx,
		MaxConsecutiveToolFailures: cfg.Assistant.Agentic.MaxConsecutiveToolFailures,
		Logger:                     slog.Default(),
	})
	lifecycle.engine = engine
	orchestrator := service.NewAssistantOrchestrator(service.AssistantOrchestratorConfig{
		Engine:          engine,
		DefaultWorkflow: defaultWorkflow,
		Publisher:       deps.Publisher,
		Subscriber:      deps.Subscriber,
		Signer:          deps.Signer,
		Identity:        deps.Identity,
		InitialSessions: deps.InitialSessions,
		Logger:          slog.Default(),
	})
	recovery := service.NewAssistantSessionRecoveryRunner(orchestrator, service.AssistantSessionRecoveryConfig{RecentLimit: 500, ServicePubkey: deps.ServicePubkey, Logger: slog.Default(), Engine: engine, Store: store, Subscriber: deps.Subscriber})
	return &assistantExecutionWiring{Orchestrator: orchestrator, Engine: engine, Store: store, Runtime: runtime, Batch: batch, Iterative: iterative, Recovery: recovery, Lifecycle: lifecycle, DefaultWorkflow: defaultWorkflow, AvailableWorkflows: available}, nil
}

// assistantExecutorLifecycle owns the executor's application-lifetime context.
// Execution and observation never run under a request deadline; stopping the
// application cancels this context, which closes subscriptions and stops
// dispatch without recording any cancellation or downstream outcome, then
// waits for every executor goroutine.
type assistantExecutorLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	engine *service.AssistantExecutionEngine
}

func newAssistantExecutorLifecycle() *assistantExecutorLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &assistantExecutorLifecycle{ctx: ctx, cancel: cancel}
}

func (l *assistantExecutorLifecycle) Name() string { return "assistant-executor-lifecycle" }

func (l *assistantExecutorLifecycle) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
	case <-l.ctx.Done():
	}
	l.Stop()
	return nil
}

// Stop cancels executor work and waits for it to exit.
func (l *assistantExecutorLifecycle) Stop() {
	l.cancel()
	if l.engine != nil {
		l.engine.Wait()
	}
}
