// Package controlplane implements the Nostr-based control plane for Bahia.
// It provides a reactive event-driven interface for deployment operations,
// allowing agents and external systems to manage deployments via Nostr events.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"go.uber.org/zap"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// Legacy Bahia control-plane kind aliases retained for direct handler tests and
// migration rejection. Production subscriptions use ContextVM/canonical kinds.
const (
	// Legacy request kind aliases
	KindDeployRequest            = nostrpool.KindControlPlaneDeployRequest            // Request to deploy a service
	KindRollbackRequest          = nostrpool.KindControlPlaneRollbackRequest          // Request to rollback a service
	KindServiceAction            = nostrpool.KindControlPlaneServiceAction            // Lifecycle action (scale, restart, stop)
	KindServiceCreate            = nostrpool.KindControlPlaneServiceCreate            // Create a new service
	KindEnvironmentCreate        = nostrpool.KindControlPlaneEnvironmentCreate        // Create a new environment
	KindDeploymentApproval       = nostrpool.KindControlPlaneDeploymentApproval       // Approve or reject a deployment
	KindObservationSubmit        = nostrpool.KindControlPlaneObservationSubmit        // Submit runtime observation
	KindDriftRemediate           = nostrpool.KindControlPlaneDriftRemediate           // Request drift remediation
	KindLLMRouteCreate           = nostrpool.KindControlPlaneLLMRouteCreate           // Create an LLM route
	KindLLMReleaseRegister       = nostrpool.KindControlPlaneLLMReleaseRegister       // Register an LLM release
	KindLLMDeployRequest         = nostrpool.KindControlPlaneLLMDeployRequest         // Request LLM route deployment
	KindLLMDeploymentApproval    = nostrpool.KindControlPlaneLLMDeploymentApproval    // Approve or reject an LLM deployment
	KindLLMRollbackRequest       = nostrpool.KindControlPlaneLLMRollbackRequest       // Request LLM route rollback
	KindToolProvisionRequest     = nostrpool.KindControlPlaneToolProvisionRequest     // Agent → Bahia
	KindToolApprovalRequest      = nostrpool.KindControlPlaneToolApprovalRequest      // Bahia → Operator
	KindAdoptionScanRequest      = nostrpool.KindControlPlaneAdoptionScanRequest      // Request adoption scan previews
	KindAdoptionImportRequest    = nostrpool.KindControlPlaneAdoptionImportRequest    // Request adoption import
	KindServiceUpdate            = nostrpool.KindControlPlaneServiceUpdate            // Update a service registry entry
	KindServiceDelete            = nostrpool.KindControlPlaneServiceDelete            // Delete a service registry entry
	KindEnvironmentUpdate        = nostrpool.KindControlPlaneEnvironmentUpdate        // Update an environment registry entry
	KindEnvironmentDelete        = nostrpool.KindControlPlaneEnvironmentDelete        // Delete an environment registry entry
	KindArtifactRegister         = nostrpool.KindControlPlaneArtifactRegister         // Register an artifact
	KindPolicyCreate             = nostrpool.KindControlPlanePolicyCreate             // Create a deployment policy
	KindPolicyUpdate             = nostrpool.KindControlPlanePolicyUpdate             // Update a deployment policy
	KindPolicyDelete             = nostrpool.KindControlPlanePolicyDelete             // Delete a deployment policy
	KindPolicyEvaluate           = nostrpool.KindControlPlanePolicyEvaluate           // Evaluate deployment policies
	KindPackageRepositoryApply   = nostrpool.KindControlPlanePackageRepositoryApply   // Create/update a package repository
	KindPackageRepositoryDelete  = nostrpool.KindControlPlanePackageRepositoryDelete  // Delete a package repository
	KindPackagePublishIntent     = nostrpool.KindControlPlanePackagePublishIntent     // Request package artifact publication/upload from source_url
	KindPackagePromotionRequest  = nostrpool.KindControlPlanePackagePromotionRequest  // Request package promotion to a target repository/channel
	KindPackageYankRequest       = nostrpool.KindControlPlanePackageYankRequest       // Yank/deprecate a package artifact
	KindPackageDriftDetect       = nostrpool.KindControlPlanePackageDriftDetect       // Observe package backend drift
	KindWorkerCordonRequest      = nostrpool.KindControlPlaneWorkerCordonRequest      // Request worker cordon
	KindWorkerUncordonRequest    = nostrpool.KindControlPlaneWorkerUncordonRequest    // Request worker uncordon
	KindWorkerDrainRequest       = nostrpool.KindControlPlaneWorkerDrainRequest       // Request worker drain
	KindWorkerUndrainRequest     = nostrpool.KindControlPlaneWorkerUndrainRequest     // Request worker undrain
	KindWorkerMaintenanceEnter   = nostrpool.KindControlPlaneWorkerMaintenanceEnter   // Request worker maintenance entry
	KindWorkerMaintenanceExit    = nostrpool.KindControlPlaneWorkerMaintenanceExit    // Request worker maintenance exit
	KindWorkerLabelsUpdate       = nostrpool.KindControlPlaneWorkerLabelsUpdate       // Request worker label update
	KindWorkerPolicyApplyRequest = nostrpool.KindControlPlaneWorkerPolicyApplyRequest // Apply environment worker placement policy
	KindWorkloadPinRequest       = nostrpool.KindControlPlaneWorkloadPinRequest       // Pin workload placement to a worker
	KindWorkerCleanupRequest     = nostrpool.KindControlPlaneWorkerCleanupRequest     // Request worker cleanup

	// Generic AI/ML command/result kinds (38390-38399). These intentionally
	// avoid NIP-90's 5000-7000 DVM range.
	KindMLRecipeRunRequest            = nostrpool.KindMLRecipeRunRequest            // Request a generic ML recipe run
	KindMLInferenceDeployRequest      = nostrpool.KindMLInferenceDeployRequest      // Request inference endpoint deployment
	KindMLInferenceDeploymentApproval = nostrpool.KindMLInferenceDeploymentApproval // Approve or reject an inference deployment
	KindMLInferenceRollbackRequest    = nostrpool.KindMLInferenceRollbackRequest    // Request inference endpoint rollback
	KindMLModelImportRequest          = nostrpool.KindMLModelImportRequest          // Request model/model-version import
	KindMLRecipeRunResult             = nostrpool.KindMLRecipeRunResult             // Recipe run terminal result
	KindMLInferenceDeployResult       = nostrpool.KindMLInferenceDeployResult       // Inference deployment terminal result
	KindMLInferenceApprovalResult     = nostrpool.KindMLInferenceApprovalResult     // Approval/rejection terminal result
	KindMLInferenceRollbackResult     = nostrpool.KindMLInferenceRollbackResult     // Rollback terminal result
	KindMLModelImportResult           = nostrpool.KindMLModelImportResult           // Model/model-version import terminal result

	// Legacy status kind aliases
	KindDeploymentStatus    = nostrpool.KindControlPlaneDeploymentStatus    // Deployment progress updates
	KindServiceStatus       = nostrpool.KindControlPlaneServiceStatus       // Service health/state updates
	KindActionStatus        = nostrpool.KindControlPlaneActionStatus        // Service action progress updates
	KindLLMDeploymentStatus = nostrpool.KindControlPlaneLLMDeploymentStatus // LLM deployment/rollback progress updates
	KindToolProvisionStatus = nostrpool.KindControlPlaneToolProvisionStatus // Bahia → Agent (progress)
	KindAdoptionStatus      = nostrpool.KindControlPlaneAdoptionStatus      // Adoption scan/import progress updates
	KindPackageStatus       = nostrpool.KindControlPlanePackageStatus       // Package lifecycle progress/policy events
	KindWorkerStatus        = nostrpool.KindControlPlaneWorkerStatus        // Worker lifecycle progress events

	// Legacy result kind aliases
	KindDeploymentResult         = nostrpool.KindControlPlaneDeploymentResult         // Final deployment result
	KindActionResult             = nostrpool.KindControlPlaneActionResult             // Result of a service action
	KindServiceCreateResult      = nostrpool.KindControlPlaneServiceCreateResult      // Service creation result
	KindEnvCreateResult          = nostrpool.KindControlPlaneEnvironmentCreateResult  // Environment creation result
	KindObservationResult        = nostrpool.KindControlPlaneObservationResult        // Observation submission result
	KindRemediationResult        = nostrpool.KindControlPlaneRemediationResult        // Drift remediation result
	KindLLMRouteCreateResult     = nostrpool.KindControlPlaneLLMRouteCreateResult     // LLM route creation result
	KindLLMReleaseRegisterResult = nostrpool.KindControlPlaneLLMReleaseRegisterResult // LLM release registration result
	KindLLMDeploymentResult      = nostrpool.KindControlPlaneLLMDeploymentResult      // LLM deployment/approval/rollback result
	KindToolProvisionResult      = nostrpool.KindControlPlaneToolProvisionResult      // Bahia → Agent (final)
	KindToolApprovalResponse     = nostrpool.KindControlPlaneToolApprovalResponse     // Operator → Bahia
	KindAdoptionScanResult       = nostrpool.KindControlPlaneAdoptionScanResult       // Adoption scan result
	KindAdoptionImportResult     = nostrpool.KindControlPlaneAdoptionImportResult     // Adoption import result
	KindPackageResult            = nostrpool.KindControlPlanePackageResult            // Package lifecycle terminal result
	KindPackageDriftEvent        = nostrpool.KindControlPlanePackageDriftEvent        // Package drift observation result
	KindWorkerResult             = nostrpool.KindControlPlaneWorkerResult             // Worker lifecycle terminal result

	// Replaceable registry kinds (d-tag indexed)
	KindServiceState              = nostrpool.KindServiceState              // Replaceable service state (d=service:env)
	KindServiceRegistry           = nostrpool.KindServiceRegistry           // Replaceable service registry entry (d=service_id)
	KindEnvironmentRegistry       = nostrpool.KindEnvironmentRegistry       // Replaceable environment registry entry (d=env_id)
	KindLLMRouteRegistry          = nostrpool.KindLLMRouteRegistry          // Replaceable LLM route registry entry (d=route_id)
	KindLLMRouteState             = nostrpool.KindLLMRouteState             // Replaceable LLM route state (d=route:env)
	KindArtifactRegistry          = nostrpool.KindArtifactRegistry          // Replaceable artifact registry entry (d=artifact_id)
	KindDeploymentIntentRegistry  = nostrpool.KindDeploymentIntentRegistry  // Replaceable deployment intent entry (d=intent_id)
	KindDeploymentRunRegistry     = nostrpool.KindDeploymentRunRegistry     // Replaceable deployment run entry (d=run_id)
	KindBuildRegistry             = nostrpool.KindBuildRegistry             // Replaceable build registry entry (d=build_id)
	KindPolicyRegistry            = nostrpool.KindPolicyRegistry            // Replaceable policy registry entry (d=policy_id)
	KindPackageRepositoryRegistry = nostrpool.KindPackageRepositoryRegistry // Replaceable package repository state (d=repository_id)
	KindPackageArtifactRegistry   = nostrpool.KindPackageArtifactRegistry   // Replaceable package artifact state (d=artifact_id)
	KindPackagePromotionRegistry  = nostrpool.KindPackagePromotionRegistry  // Replaceable package promotion/publication state (d=publication_id)
	KindWorkerState               = nostrpool.KindWorkerState               // Replaceable worker state (d=worker pubkey)
	KindWorkerAssignmentState     = nostrpool.KindWorkerAssignmentState     // Replaceable worker assignment state (d=worker pubkey)
	KindWorkerDrainStatus         = nostrpool.KindWorkerDrainStatus         // Replaceable worker drain status (d=worker pubkey)
	KindWorkerEligibilityPreview  = nostrpool.KindWorkerEligibilityPreview  // Replaceable worker eligibility preview (d=preview id)

	// Canonical runtime observable kinds.
	KindCASControlState = nostrpool.KindCASControlState
	KindCASAudit        = nostrpool.KindCASAudit
	KindNIP38Status     = nostrpool.KindNIP38Status
)

// Config holds reactor configuration.
type Config struct {
	// Relays is the list of public relay URLs for subscriptions and results.
	Relays []string
	// AdditionalRelays is the supplemental relay URL list for draft/provisioning events.
	AdditionalRelays []string
	// PrivateKey is the hex-encoded key used only when this reactor must create
	// its own relay pool with NIP-42 AUTH support. Event signing uses Signer.
	PrivateKey string
	// AuthorizedPubkeys is the list of pubkeys allowed to submit requests.
	AuthorizedPubkeys []string
	// AdoptionAuthorizedPubkeys is the adoption-specific operator allowlist.
	// AuthorizedPubkeys remains a global fallback for adoption requests.
	AdoptionAuthorizedPubkeys []string
	// DirectRuntimeAuthorizedPubkeys is the direct-runtime-specific operator allowlist.
	// AuthorizedPubkeys remains a global fallback for direct-runtime action requests.
	DirectRuntimeAuthorizedPubkeys []string
}

// Reactor subscribes to Nostr control plane events and dispatches handlers.
type Reactor struct {
	config      Config
	pool        *nostrpool.RelayPool
	publisher   NostrEventPublisher
	registry    *service.RegistryService
	llmRegistry *service.LLMRegistryService
	mlRegistry  *service.MLRegistryService
	signer      canonicalnostr.Signer
	logger      *slog.Logger
	zapLog      *zap.Logger
	dedup       *nostrpool.EventDeduplicator
	backoff     *nostrpool.Backoff
	caughtUp    atomic.Bool

	replayCursorPlanner *nostrpool.ReplayCursorPlanner
	kindCatalog         *nostrpool.KindCatalog
	lastSeenByGroup     map[string]nostr.Timestamp

	toolProvisioning              repository.ToolProvisioningRepository
	toolResponder                 *ToolResponder
	toolCoordinator               *service.ToolProvisioningCoordinator
	policyService                 *service.PolicyService
	adoption                      AdoptionOperatorService
	runtimeLifecycle              RuntimeLifecycleOperatorService
	packageService                *service.PackageRegistryService
	packageProjection             repository.PackageControlPlaneRepository
	workerRepo                    repository.WorkerRepository
	workerCleanupOrchestrator     *service.WorkerCleanupOrchestrator
	mlExecutor                    MLInferenceControlPlaneExecutor
	mlRecipeExecutor              MLRecipeControlPlaneExecutor
	nostrEvents                   repository.NostrEventRepository
	assistantOrchestrator         *service.AssistantOrchestrator
	dnsOperator                   DNSControlPlaneOperator
	backupRegistry                backupRunRegistry
	backupExecutor                BackupRunControlPlaneExecutor
	backupResponder               service.BackupRunResponder
	backupRestoreExecutor         BackupRestoreControlPlaneExecutor
	backupRestoreResponder        service.BackupRestoreResponder
	backupRetentionExecutor       BackupRetentionControlPlaneExecutor
	backupRetentionResponder      service.BackupRetentionResponder
	backupDefinitionRegistry      BackupDefinitionApplyRegistry
	backupVerificationExecutor    BackupVerificationControlPlaneExecutor
	backupRepositoryProbeExecutor BackupRepositoryProbeControlPlaneExecutor
	eventBus                      events.Publisher
	workerStatePublisher          *WorkerStatePublisher

	mu   sync.Mutex
	runs map[string]*DeploymentRun // requestEventID -> run
}

// DeploymentRun tracks an in-progress deployment initiated via Nostr.
type DeploymentRun struct {
	ID              uuid.UUID
	RequestEventID  string
	ServiceID       uuid.UUID
	EnvironmentID   uuid.UUID
	ArtifactID      uuid.UUID
	IntentID        *uuid.UUID
	RequesterPubkey string
	Status          string
	CurrentStep     string
	Error           string
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// ReactorOption configures optional reactor dependencies without breaking the legacy constructor shape.
type ReactorOption func(*Reactor)

// WithLLMRegistry enables LLM Nostr lifecycle request handling.
func WithLLMRegistry(registry *service.LLMRegistryService) ReactorOption {
	return func(r *Reactor) { r.llmRegistry = registry }
}

func WithMLRegistry(registry *service.MLRegistryService) ReactorOption {
	return func(r *Reactor) { r.mlRegistry = registry }
}

type MLInferenceControlPlaneExecutor interface {
	ProcessDeploymentIntent(ctx context.Context, intentID uuid.UUID) error
}

func WithMLInferenceExecutor(executor MLInferenceControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.mlExecutor = executor }
}

type MLRecipeControlPlaneExecutor interface {
	ProcessRecipeRun(ctx context.Context, runID uuid.UUID) error
}

func WithMLRecipeExecutor(executor MLRecipeControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.mlRecipeExecutor = executor }
}

type BackupRunControlPlaneExecutor interface {
	ProcessBackupRun(ctx context.Context, runID uuid.UUID) error
}

func WithBackupRegistry(registry *service.BackupRegistryService) ReactorOption {
	return func(r *Reactor) { r.backupRegistry = registry }
}

func WithBackupRunExecutor(executor BackupRunControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.backupExecutor = executor }
}

func WithBackupRunResponder(responder service.BackupRunResponder) ReactorOption {
	return func(r *Reactor) { r.backupResponder = responder }
}

func WithBackupRestoreExecutor(executor BackupRestoreControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.backupRestoreExecutor = executor }
}

func WithBackupRestoreResponder(responder service.BackupRestoreResponder) ReactorOption {
	return func(r *Reactor) { r.backupRestoreResponder = responder }
}

func WithBackupRetentionExecutor(executor BackupRetentionControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.backupRetentionExecutor = executor }
}

func WithBackupRetentionResponder(responder service.BackupRetentionResponder) ReactorOption {
	return func(r *Reactor) { r.backupRetentionResponder = responder }
}

func WithBackupDefinitionRegistry(registry BackupDefinitionApplyRegistry) ReactorOption {
	return func(r *Reactor) { r.backupDefinitionRegistry = registry }
}

func WithBackupVerificationExecutor(executor BackupVerificationControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.backupVerificationExecutor = executor }
}

func WithBackupRepositoryProbeExecutor(executor BackupRepositoryProbeControlPlaneExecutor) ReactorOption {
	return func(r *Reactor) { r.backupRepositoryProbeExecutor = executor }
}

func WithToolProvisioningRepository(repo repository.ToolProvisioningRepository) ReactorOption {
	return func(r *Reactor) { r.toolProvisioning = repo }
}

func WithToolResponder(responder *ToolResponder) ReactorOption {
	return func(r *Reactor) { r.toolResponder = responder }
}

func WithToolProvisioningCoordinator(coordinator *service.ToolProvisioningCoordinator) ReactorOption {
	return func(r *Reactor) { r.toolCoordinator = coordinator }
}

func WithPolicyService(policies *service.PolicyService) ReactorOption {
	return func(r *Reactor) { r.policyService = policies }
}

// WithAdoptionService enables signer-first adoption scan/import request handling.
func WithAdoptionService(adoption AdoptionOperatorService) ReactorOption {
	return func(r *Reactor) { r.adoption = adoption }
}

// WithRuntimeLifecycleService enables signer-first direct-runtime action handling.
func WithRuntimeLifecycleService(runtimeLifecycle RuntimeLifecycleOperatorService) ReactorOption {
	return func(r *Reactor) { r.runtimeLifecycle = runtimeLifecycle }
}

func WithPackageRegistryService(packageService *service.PackageRegistryService) ReactorOption {
	return func(r *Reactor) { r.packageService = packageService }
}

func WithPackageProjectionRepository(repo repository.PackageControlPlaneRepository) ReactorOption {
	return func(r *Reactor) { r.packageProjection = repo }
}

func WithWorkerRepository(repo repository.WorkerRepository) ReactorOption {
	return func(r *Reactor) { r.workerRepo = repo }
}

func WithWorkerCleanupOrchestrator(orchestrator *service.WorkerCleanupOrchestrator) ReactorOption {
	return func(r *Reactor) { r.workerCleanupOrchestrator = orchestrator }
}

func WithNostrEventRepository(repo repository.NostrEventRepository) ReactorOption {
	return func(r *Reactor) {
		r.nostrEvents = repo
		if r.workerStatePublisher != nil {
			r.workerStatePublisher.ConfigureAudit(repo, r.zapLog)
		}
	}
}

// WithReplayCursorPlanner enables persisted cursor replay for control-plane subscriptions.
func WithReplayCursorPlanner(planner *nostrpool.ReplayCursorPlanner) ReactorOption {
	return func(r *Reactor) { r.replayCursorPlanner = planner }
}

// WithKindCatalog configures the replay group catalog used for cursor tracking.
func WithKindCatalog(catalog *nostrpool.KindCatalog) ReactorOption {
	return func(r *Reactor) { r.kindCatalog = catalog }
}

// WithEventPublisher enables reactor handlers to emit in-process domain events.
func WithEventPublisher(publisher events.Publisher) ReactorOption {
	return func(r *Reactor) {
		if publisher != nil {
			r.eventBus = publisher
		}
	}
}

// WithControlPlanePublisher overrides the result/status publisher, primarily for tests.
func WithControlPlanePublisher(publisher NostrEventPublisher) ReactorOption {
	return func(r *Reactor) {
		if publisher != nil {
			r.publisher = publisher
			r.workerStatePublisher = NewWorkerStatePublisher(publisher, r.signer)
			r.workerStatePublisher.ConfigureAudit(r.nostrEvents, r.zapLog)
		}
	}
}

// WithAssistantOrchestrator enables operator-assistant prompt and approval handling.
func WithAssistantOrchestrator(orchestrator *service.AssistantOrchestrator) ReactorOption {
	return func(r *Reactor) { r.assistantOrchestrator = orchestrator }
}

// WithDNSOperator enables DNS control-plane request handling.
func WithDNSOperator(op DNSControlPlaneOperator) ReactorOption {
	return func(r *Reactor) { r.dnsOperator = op }
}

// NewReactor creates a new Bahia control plane reactor.
// If pool is nil, a new pool will be created from the config relays.
// signer is required for event signing. If pool is nil, config.PrivateKey is
// only used to configure relay AUTH on the created pool.
func NewReactor(config Config, registry *service.RegistryService, pool *nostrpool.RelayPool, signer canonicalnostr.Signer, zapLog *zap.Logger, opts ...ReactorOption) *Reactor {
	if zapLog == nil {
		zapLog = zap.NewNop()
	}

	// Use provided pool or create a new one
	if pool == nil {
		poolOpts := []nostrpool.RelayPoolOption{}
		if config.PrivateKey != "" {
			poolOpts = append(poolOpts, nostrpool.WithPrivateKey(config.PrivateKey))
		}

		// Copy slices to avoid mutating config's backing array
		allRelays := make([]string, 0, len(config.Relays)+len(config.AdditionalRelays))
		allRelays = append(allRelays, config.Relays...)
		allRelays = append(allRelays, config.AdditionalRelays...)
		pool = nostrpool.NewRelayPool(allRelays, zapLog, poolOpts...)
	}

	r := &Reactor{
		config:          config,
		pool:            pool,
		publisher:       pool,
		registry:        registry,
		signer:          signer,
		logger:          slog.Default().With("component", "controlplane"),
		zapLog:          zapLog,
		dedup:           nostrpool.NewEventDeduplicator(10000),
		backoff:         nostrpool.DefaultBackoff(),
		eventBus:        &events.NoopPublisher{},
		lastSeenByGroup: make(map[string]nostr.Timestamp),
		runs:            make(map[string]*DeploymentRun),
	}
	r.workerStatePublisher = NewWorkerStatePublisher(r.publisher, r.signer)
	for _, opt := range opts {
		opt(r)
	}
	if r.workerStatePublisher != nil {
		r.workerStatePublisher.ConfigureAudit(r.nostrEvents, r.zapLog)
	}
	return r
}

// Run starts the reactor and blocks until context is cancelled.
func (r *Reactor) Run(ctx context.Context) error {
	r.logger.Info("starting bahia control plane reactor",
		"relays", r.config.Relays,
		"additional_relays", r.config.AdditionalRelays,
	)

	// Connect to relays
	r.pool.Connect(ctx)

	// Start periodic cleanup of completed runs
	go r.cleanupRuns(ctx)

	// Subscribe to control plane request events from the newest persisted or
	// in-process replay cursor, with nostr.Now as the no-history fallback.
	filters := r.buildRequestSubscriptionFiltersForCurrentCursor(ctx)

	r.caughtUp.Store(false)
	merged, err := r.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	r.logger.Info("subscribed to control plane events")
	go r.recoverPackageIntents(ctx)
	authAttempted := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reactor shutting down")
			r.pool.Close()
			return ctx.Err()

		case eose, ok := <-merged.RelayEOSE:
			if ok {
				r.handleRelayEOSE(eose)
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if r.handleRelayClosed(ctx, closed, authAttempted) {
					merged.Close()
					r.caughtUp.Store(false)
					authAttempted = make(map[string]struct{})
					filters = r.buildRequestSubscriptionFiltersForCurrentCursor(ctx)
					merged, err = r.pool.SubscribeAllWithEOSE(ctx, filters)
					if err != nil {
						r.logger.Error("resubscribe after relay auth failed", "error", err)
						continue
					}
					r.backoff.Reset()
				}
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			r.handleEOSE()
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				delay := r.backoff.Next()
				r.logger.Warn("subscription closed, reconnecting...", "delay", delay)
				time.Sleep(delay)
				r.caughtUp.Store(false)
				authAttempted = make(map[string]struct{})
				filters = r.buildRequestSubscriptionFiltersForCurrentCursor(ctx)
				merged, err = r.pool.SubscribeAllWithEOSE(ctx, filters)
				if err != nil {
					r.logger.Error("reconnect failed", "error", err)
					continue
				}
				r.backoff.Reset()
				continue
			}

			r.handleEvent(ctx, ev)
		}
	}
}

func (r *Reactor) handleRelayEOSE(eose nostrpool.RelayEOSE) {
	r.logger.Debug("relay sent control-plane EOSE", "relay", eose.RelayURL, "subscription_id", eose.SubscriptionID)
}

func (r *Reactor) handleEOSE() {
	if r.caughtUp.CompareAndSwap(false, true) {
		r.logger.Info("control-plane EOSE received: caught up with stored events")
	}
}

func (r *Reactor) handleRelayClosed(ctx context.Context, closed nostrpool.RelayClosed, authAttempted map[string]struct{}) bool {
	r.logger.Warn("relay closed control-plane subscription",
		"relay", closed.RelayURL,
		"subscription_id", closed.SubscriptionID,
		"reason", closed.Reason,
	)
	if !nostrpool.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || r.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := r.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		r.logger.Warn("relay control-plane subscription auth failed", "relay", closed.RelayURL, "reason", closed.Reason, "error", err)
		return false
	}
	return true
}

func (r *Reactor) auditInboundEvent(ctx context.Context, event *nostr.Event) bool {
	if r.nostrEvents == nil {
		return true
	}
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		r.logger.Warn("failed to marshal inbound control-plane event tags for audit", "event_id", event.ID.Hex(), "kind", int(event.Kind), "error", err)
		tagsJSON = []byte("[]")
	}
	inserted, err := r.nostrEvents.Record(ctx, &repository.NostrEventRecord{
		ID:         event.ID.Hex(),
		Kind:       int(event.Kind),
		PubKey:     event.PubKey.Hex(),
		Content:    event.Content,
		Tags:       tagsJSON,
		Sig:        nostr.HexEncodeToString(event.Sig[:]),
		CreatedAt:  event.CreatedAt.Time(),
		ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		r.logger.Warn("failed to audit inbound control-plane event", "event_id", event.ID.Hex(), "kind", int(event.Kind), "error", err)
		return false
	}
	if !inserted {
		r.logger.Debug("skipping already-audited control-plane event", "event_id", event.ID, "kind", event.Kind)
		return false
	}
	return true
}

// handleEvent audits and tracks canonical runtime replay events. Legacy
// Bahia command/status/result/read-model kind-number flows are rejected after
// the startup migration boundary; command dispatch is handled by the ContextVM
// transport instead of this production reactor subscription.
func (r *Reactor) handleEvent(ctx context.Context, event *nostr.Event) {
	if err := nostrpool.ValidateInboundEvent(event, time.Now().UTC(), nostrpool.InboundEventMaxFutureSkew); err != nil {
		eventID := ""
		if event != nil {
			eventID = event.ID.Hex()
		}
		r.logger.Warn("dropping invalid control-plane event", "event_id", eventID, "error", err)
		return
	}
	eventID := event.ID.Hex()
	eventKind := int(event.Kind)
	if isLegacyProductionRuntimeKind(eventKind) {
		r.logger.Warn("dropping legacy control-plane event after migration boundary", "event_id", eventID, "kind", eventKind)
		return
	}

	// Deduplicate events (relays may replay during reconnection).
	if r.dedup.IsDuplicate(eventID) || r.isDuplicateIdempotencyCommand(ctx, event) {
		return
	}
	if !r.auditInboundEvent(ctx, event) {
		return
	}
	r.dedup.MarkSeen(eventID)
	r.trackLastSeen(event)

	switch {
	case isHeartbeatObservationEvent(event):
		go r.handleHeartbeatObservation(ctx, event)
	case isCanonicalRuntimeReplayKind(eventKind):
		return
	default:
		r.logger.Warn("unexpected event kind", "kind", eventKind)
	}
}

// handleDeployRequest processes a legacy deployment request in direct tests.
type idempotencyEventRepository interface {
	FindLatestByKindPubkeyDTag(ctx context.Context, kind int, pubkey, dTag, excludeID string) (*repository.NostrEventRecord, error)
}

func (r *Reactor) isDuplicateIdempotencyCommand(ctx context.Context, event *nostr.Event) bool {
	if r == nil || r.nostrEvents == nil || event == nil || !isIdempotencyCommandKind(int(event.Kind)) {
		return false
	}
	dTag := strings.TrimSpace(tagValueNostr(event.Tags, "d"))
	if dTag == "" {
		return false
	}
	repo, ok := r.nostrEvents.(idempotencyEventRepository)
	if !ok {
		return false
	}
	previous, err := repo.FindLatestByKindPubkeyDTag(ctx, int(event.Kind), event.PubKey.Hex(), dTag, event.ID.Hex())
	if err != nil {
		r.logger.Warn("failed to check idempotency key", "event_id", event.ID.Hex(), "idempotency_key", dTag, "error", err)
		return false
	}
	if previous == nil {
		return false
	}
	r.logger.Info("dropping duplicate idempotency-keyed control-plane command", "event_id", event.ID.Hex(), "previous_event_id", previous.ID, "idempotency_key", dTag, "kind", int(event.Kind))
	return true
}

func isIdempotencyCommandKind(kind int) bool {
	switch kind {
	case kinds.ContextVMMessage, kinds.ContextVMGiftWrap, kinds.ContextVMEphemeralGiftWrap, nostrpool.KindFailoverRequest, nostrpool.KindRecoveryRequest:
		return true
	default:
		return false
	}
}

func isHeartbeatObservationEvent(event *nostr.Event) bool {
	if event == nil || event.Kind != nostrpool.KindNIP38Status {
		return false
	}
	schema := tagValueNostr(event.Tags, "schema")
	dTag := tagValueNostr(event.Tags, "d")
	return schema == "bahia.status.continuity-heartbeat.v1" || strings.HasPrefix(dTag, "continuity:heartbeat:") || strings.HasPrefix(dTag, "heartbeat:")
}

func (r *Reactor) handleDeployRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID.Hex(), "requester", event.PubKey.Hex())
	logger.Info("received deployment request")

	// Validate authorization
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized deployment request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request
	req, err := r.parseDeployRequest(event)
	if err != nil {
		logger.Error("failed to parse request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	logger = logger.With("service_id", req.ServiceID, "environment_id", req.EnvironmentID)
	logger.Info("creating deployment intent")

	// Validate that service, environment, and artifact exist
	if _, err := r.registry.GetService(ctx, req.ServiceID); err != nil {
		logger.Error("service not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("service not found: %v", err))
		return
	}
	if _, err := r.registry.GetEnvironment(ctx, req.EnvironmentID); err != nil {
		logger.Error("environment not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("environment not found: %v", err))
		return
	}
	if _, err := r.registry.GetArtifact(ctx, req.ArtifactID); err != nil {
		logger.Error("artifact not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("artifact not found: %v", err))
		return
	}

	if r.policyService == nil {
		logger.Error("policy service is not configured")
		r.publishError(ctx, event, "policy_unavailable", "policy service is not configured")
		return
	}
	evaluation, err := r.policyService.Evaluate(ctx, req.ArtifactID, req.EnvironmentID)
	if err != nil {
		logger.Error("policy evaluation failed", "error", err)
		r.publishError(ctx, event, "policy_evaluation_error", err.Error())
		return
	}
	if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
		reason := summarizePolicyBlockReason(evaluation)
		logger.Warn("deployment blocked by policy evaluation", "blockers", evaluation.Blockers, "warnings", evaluation.Warnings, "reason", reason)
		r.publishError(ctx, event, "policy_blocked", reason)
		return
	}

	if r.runtimeLifecycle == nil {
		logger.Error("runtime lifecycle service is not configured")
		r.publishError(ctx, event, "runtime_lifecycle_unavailable", "runtime lifecycle service is not configured")
		return
	}

	// Create run tracker
	run := &DeploymentRun{
		ID:              uuid.New(),
		RequestEventID:  event.ID.Hex(),
		ServiceID:       req.ServiceID,
		EnvironmentID:   req.EnvironmentID,
		ArtifactID:      req.ArtifactID,
		RequesterPubkey: event.PubKey.Hex(),
		Status:          "running",
		CurrentStep:     "creating_intent",
		StartedAt:       time.Now(),
	}

	r.mu.Lock()
	r.runs[event.ID.Hex()] = run
	r.mu.Unlock()

	// Publish status update
	r.publishStatus(ctx, event, "creating_intent", "Creating deployment intent")

	// Create deployment intent
	desiredState, err := r.runtimeLifecycle.BuildDesiredStateSnapshot(ctx, req.ServiceID, req.EnvironmentID, req.ArtifactID)
	if err != nil {
		logger.Error("failed to build desired state", "error", err)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "desired_state_error", err.Error())
		return
	}
	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     req.ServiceID,
		EnvironmentID: req.EnvironmentID,
		ArtifactID:    req.ArtifactID,
		RequestedBy:   event.PubKey.Hex(),
		SourceKind:    domain.SourceKindEventTriggered,
		Metadata:      map[string]any{"nostr_event_id": event.ID.Hex()},
		DesiredState:  desiredState,
		DesiredHash:   desiredState.DesiredHash,
	}

	if err := r.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create deployment intent", "error", err)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "intent_error", err.Error())
		return
	}

	run.IntentID = &intent.ID
	run.CurrentStep = "intent_created"

	logger.Info("deployment intent created", "intent_id", intent.ID, "desired_hash", intent.DesiredHash)

	if intent.Status != domain.IntentStatusApproved {
		run.Status = "completed"
		now := time.Now()
		run.CompletedAt = &now
		r.publishDeploymentResult(ctx, event, intent)
		return
	}

	startedAt := time.Now().UTC()
	domainRun := &domain.DeploymentRun{
		ID:                 run.ID,
		DeploymentIntentID: intent.ID,
		Status:             domain.RunStatusRunning,
		StartedAt:          &startedAt,
		Metadata:           map[string]any{"nostr_event_id": event.ID},
		ApplyMetadata: map[string]any{
			"desired_hash":                 desiredState.DesiredHash,
			"desired_state_schema_version": desiredState.SchemaVersion,
		},
	}
	if err := r.registry.CreateDeploymentRun(ctx, domainRun); err != nil {
		logger.Error("failed to create deployment run", "error", err)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "run_error", err.Error())
		return
	}
	intent.Status = domain.IntentStatusDeploying

	r.publishStatus(ctx, event, "applying_desired_state", "Applying desired runtime state")
	artifactID := req.ArtifactID
	obs, err := r.runtimeLifecycle.DeployWithStatus(ctx, req.ServiceID, req.EnvironmentID, &artifactID, r.deploymentStatusCallbackFor(ctx, event))
	if err != nil {
		logger.Error("deployment execution failed", "error", err)
		failureExitCode := 1
		_ = r.registry.CompleteDeploymentRun(ctx, domainRun.ID, domain.RunStatusFailed, &failureExitCode)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "deployment_failed", err.Error())
		return
	}

	successExitCode := 0
	if err := r.registry.CompleteDeploymentRun(ctx, domainRun.ID, domain.RunStatusSucceeded, &successExitCode); err != nil {
		logger.Error("failed to complete deployment run", "error", err)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "run_completion_error", err.Error())
		return
	}
	intent.Status = domain.IntentStatusDeployed

	run.Status = "completed"
	now := time.Now()
	run.CompletedAt = &now
	if obs != nil {
		run.CurrentStep = "observation_recorded"
	}

	r.publishDeploymentResult(ctx, event, intent)
}

func summarizePolicyBlockReason(evaluation *domain.PolicyEvaluation) string {
	if evaluation == nil {
		return "deployment blocked by policy evaluation"
	}
	for _, result := range evaluation.Results {
		if result.Passed || result.Enforcement != domain.PolicyEnforcementBlock {
			continue
		}
		for _, violation := range result.Violations {
			if violation.Message != "" {
				return fmt.Sprintf("deployment blocked by policy evaluation: %s", violation.Message)
			}
			if violation.Rule != "" {
				return fmt.Sprintf("deployment blocked by policy evaluation: %s", violation.Rule)
			}
		}
		if result.PolicyName != "" {
			return fmt.Sprintf("deployment blocked by policy evaluation: %s", result.PolicyName)
		}
		if result.PolicyID != uuid.Nil {
			return fmt.Sprintf("deployment blocked by policy evaluation: %s", result.PolicyID.String())
		}
	}
	if evaluation.Blockers > 0 {
		return fmt.Sprintf("deployment blocked by policy evaluation: %d blocking policy result(s)", evaluation.Blockers)
	}
	return "deployment blocked by policy evaluation"
}

// handleRollbackRequest processes a legacy rollback request in direct tests.
func (r *Reactor) handleRollbackRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received rollback request")

	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized rollback request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request
	var req struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.publishError(ctx, event, "invalid_service_id", err.Error())
		return
	}

	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishError(ctx, event, "invalid_environment_id", err.Error())
		return
	}

	if r.runtimeLifecycle == nil {
		logger.Error("runtime lifecycle service is not configured")
		r.publishError(ctx, event, "runtime_lifecycle_unavailable", "runtime lifecycle service is not configured")
		return
	}

	r.publishStatus(ctx, event, "creating_rollback_intent", "Creating rollback deployment intent")

	// Select the rollback artifact by creating an approved rollback intent, then
	// execute that artifact through the same desired-state deploy helper used by
	// normal deploy requests and direct runtime action=deploy.
	intent, err := r.registry.Rollback(ctx, serviceID, envID, event.PubKey.Hex())
	if err != nil {
		logger.Error("rollback failed", "error", err)
		r.publishError(ctx, event, "rollback_error", err.Error())
		return
	}

	desiredState := intent.DesiredState
	if desiredState == nil || intent.DesiredHash == "" {
		desiredState, err = r.runtimeLifecycle.BuildDesiredStateSnapshot(ctx, serviceID, envID, intent.ArtifactID)
		if err != nil {
			logger.Error("failed to build rollback desired state", "error", err)
			r.recordRollbackPreparationFailure(ctx, logger, event, intent, err)
			r.publishError(ctx, event, "desired_state_error", err.Error())
			return
		}
		intent.DesiredState = desiredState
		intent.DesiredHash = desiredState.DesiredHash
		if err := r.registry.UpdateDeploymentIntentDesiredState(ctx, intent.ID, desiredState, desiredState.DesiredHash); err != nil {
			logger.Error("failed to persist rollback desired state", "error", err)
			r.recordRollbackPreparationFailure(ctx, logger, event, intent, err)
			r.publishError(ctx, event, "desired_state_persist_error", err.Error())
			return
		}
	}

	startedAt := time.Now().UTC()
	run := &domain.DeploymentRun{
		ID:                 uuid.New(),
		DeploymentIntentID: intent.ID,
		Status:             domain.RunStatusRunning,
		StartedAt:          &startedAt,
		Metadata: map[string]any{
			"nostr_event_id":        event.ID,
			"nostr_request_command": "service_rollback",
		},
		ApplyMetadata: map[string]any{
			"desired_hash":                 intent.DesiredHash,
			"desired_state_schema_version": desiredState.SchemaVersion,
			"source_kind":                  string(domain.SourceKindRollback),
		},
	}
	if err := r.registry.CreateDeploymentRun(ctx, run); err != nil {
		logger.Error("failed to create rollback deployment run", "error", err)
		r.publishError(ctx, event, "run_error", err.Error())
		return
	}

	r.publishStatus(ctx, event, "applying_desired_state", "Applying rollback desired runtime state")
	artifactID := intent.ArtifactID
	obs, err := r.runtimeLifecycle.DeployWithStatus(ctx, serviceID, envID, &artifactID, r.deploymentStatusCallbackFor(ctx, event))
	if err != nil {
		logger.Error("rollback deployment execution failed", "error", err)
		failureExitCode := 1
		_ = r.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, &failureExitCode)
		r.publishError(ctx, event, "rollback_failed", err.Error())
		return
	}

	successExitCode := 0
	if err := r.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, &successExitCode); err != nil {
		logger.Error("failed to complete rollback deployment run", "error", err)
		r.publishError(ctx, event, "run_completion_error", err.Error())
		return
	}
	intent.Status = domain.IntentStatusDeployed
	if obs != nil {
		logger.Info("rollback desired-state apply observed runtime", "observation_id", obs.ID.String())
	}

	logger.Info("rollback applied", "intent_id", intent.ID, "desired_hash", intent.DesiredHash)
	r.publishDeploymentResult(ctx, event, intent)
}

func (r *Reactor) recordRollbackPreparationFailure(ctx context.Context, logger *slog.Logger, event *nostr.Event, intent *domain.DeploymentIntent, cause error) {
	startedAt := time.Now().UTC()
	run := &domain.DeploymentRun{
		ID:                 uuid.New(),
		DeploymentIntentID: intent.ID,
		Status:             domain.RunStatusRunning,
		StartedAt:          &startedAt,
		Metadata: map[string]any{
			"nostr_event_id":        event.ID,
			"nostr_request_command": "service_rollback",
			"failure_step":          "building_desired_state",
			"error":                 cause.Error(),
		},
		ApplyMetadata: map[string]any{
			"source_kind": string(domain.SourceKindRollback),
		},
	}
	if err := r.registry.CreateDeploymentRun(ctx, run); err != nil {
		logger.Error("failed to create failed rollback deployment run", "error", err)
		return
	}
	failureExitCode := 1
	if err := r.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, &failureExitCode); err != nil {
		logger.Error("failed to complete failed rollback deployment run", "error", err)
	}
}

// handleServiceAction processes a legacy service action in direct tests.
func (r *Reactor) handleServiceAction(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received service action")

	if req, ok, err := parseDirectRuntimeActionRequest(event); ok || err != nil {
		if err != nil {
			action := directRuntimeActionFromContent(event.Content)
			if action == "" {
				action = "direct_runtime"
			}
			r.publishActionResult(ctx, event, action, "failed", err)
			return
		}
		r.handleDirectRuntimeActionRequest(ctx, event, req)
		return
	}

	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized service action")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse action from tags
	var serviceID, action, reason string
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "service":
			serviceID = tag[1]
		case "action":
			action = tag[1]
		case "reason":
			reason = tag[1]
		}
	}

	logger = logger.With("service_id", serviceID, "action", action)
	logger.Info("executing service action", "reason", reason)

	// For now, log and acknowledge - actual actions will be implemented
	// when service lifecycle methods are added to RegistryService
	r.publishActionResult(ctx, event, action, "acknowledged", nil)
}

// handleServiceCreate processes a legacy service creation request in direct tests.
func (r *Reactor) handleServiceCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received service create request")

	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized service create")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	var req struct {
		Name         string `json:"name"`
		ArtifactRepo string `json:"artifact_repo"`
		RepoURL      string `json:"repo_url,omitempty"`
		RuntimeType  string `json:"runtime_type,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	if req.Name == "" || req.ArtifactRepo == "" {
		r.publishError(ctx, event, "validation_error", "name and artifact_repo are required")
		return
	}

	runtimeType := domain.RuntimeTypeDocker
	if req.RuntimeType != "" {
		if err := domain.ValidateRuntimeType(domain.RuntimeType(req.RuntimeType)); err != nil {
			r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid runtime_type: %v", err))
			return
		}
		runtimeType = domain.RuntimeType(req.RuntimeType)
	}

	svc := &domain.Service{
		ID:           uuid.New(),
		Name:         req.Name,
		ArtifactRepo: req.ArtifactRepo,
		RepoURL:      req.RepoURL,
		RuntimeType:  runtimeType,
	}

	if err := r.registry.CreateService(ctx, svc); err != nil {
		logger.Error("failed to create service", "error", err)
		r.publishError(ctx, event, "create_error", err.Error())
		return
	}

	logger.Info("service created", "service_id", svc.ID, "name", svc.Name)
	r.publishServiceCreated(ctx, event, svc)
}

// handleEnvironmentCreate processes a legacy environment creation request in direct tests.
func (r *Reactor) handleEnvironmentCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received environment create request")

	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized environment create")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	var req struct {
		Name           string `json:"name"`
		Protected      bool   `json:"protected,omitempty"`
		DeployStrategy string `json:"deploy_strategy,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	if req.Name == "" {
		r.publishError(ctx, event, "validation_error", "name is required")
		return
	}

	deployStrategy := domain.DeployStrategyReplace
	if req.DeployStrategy != "" {
		deployStrategy = domain.DeployStrategy(req.DeployStrategy)
	}

	env := &domain.Environment{
		ID:             uuid.New(),
		Name:           req.Name,
		Protected:      req.Protected,
		DeployStrategy: deployStrategy,
	}

	if err := r.registry.CreateEnvironment(ctx, env); err != nil {
		logger.Error("failed to create environment", "error", err)
		r.publishError(ctx, event, "create_error", err.Error())
		return
	}

	logger.Info("environment created", "environment_id", env.ID, "name", env.Name)
	r.publishEnvironmentCreated(ctx, event, env)
}

func (r *Reactor) handleServiceUpdate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	var req struct {
		ID            string                       `json:"id"`
		Name          string                       `json:"name"`
		ArtifactRepo  string                       `json:"artifact_repo"`
		RepoURL       string                       `json:"repo_url,omitempty"`
		DefaultBranch string                       `json:"default_branch,omitempty"`
		RuntimeType   string                       `json:"runtime_type,omitempty"`
		RuntimeConfig *domain.ServiceRuntimeConfig `json:"runtime_config,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "service")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service id: %v", err))
		return
	}
	svc, err := r.registry.GetService(ctx, id)
	if err != nil || svc == nil {
		r.publishError(ctx, event, "not_found", "service not found")
		return
	}
	if req.Name != "" {
		svc.Name = req.Name
	}
	if req.ArtifactRepo != "" {
		svc.ArtifactRepo = req.ArtifactRepo
	}
	svc.RepoURL = req.RepoURL
	if req.DefaultBranch != "" {
		svc.DefaultBranch = req.DefaultBranch
	}
	if req.RuntimeType != "" {
		svc.RuntimeType = domain.RuntimeType(req.RuntimeType)
	}
	if req.RuntimeConfig != nil {
		svc.RuntimeConfig = req.RuntimeConfig
	}
	if err := r.registry.UpdateService(ctx, svc); err != nil {
		logger.Error("failed to update service", "error", err)
		r.publishError(ctx, event, "update_error", err.Error())
		return
	}
	r.publishActionResult(ctx, event, "service_update", "success", nil)
}

func (r *Reactor) handleServiceDelete(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "service")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service id: %v", err))
		return
	}
	if err := r.registry.DeleteService(ctx, id, req.Force); err != nil {
		r.publishError(ctx, event, "delete_error", err.Error())
		return
	}
	r.publishActionResult(ctx, event, "service_delete", "success", nil)
}

func (r *Reactor) handleEnvironmentUpdate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	var req struct {
		ID                 string         `json:"id"`
		Name               string         `json:"name"`
		LoomWorkerSelector map[string]any `json:"loom_worker_selector,omitempty"`
		RuntimeConfig      map[string]any `json:"runtime_config,omitempty"`
		DeployStrategy     string         `json:"deploy_strategy,omitempty"`
		Protected          bool           `json:"protected,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "environment")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment id: %v", err))
		return
	}
	env, err := r.registry.GetEnvironment(ctx, id)
	if err != nil || env == nil {
		r.publishError(ctx, event, "not_found", "environment not found")
		return
	}
	if req.Name != "" {
		env.Name = req.Name
	}
	if req.LoomWorkerSelector != nil {
		env.LoomWorkerSelector = req.LoomWorkerSelector
	}
	if req.RuntimeConfig != nil {
		env.RuntimeConfig = req.RuntimeConfig
	}
	if req.DeployStrategy != "" {
		env.DeployStrategy = domain.DeployStrategy(req.DeployStrategy)
	}
	env.Protected = req.Protected
	if err := r.registry.UpdateEnvironment(ctx, env); err != nil {
		logger.Error("failed to update environment", "error", err)
		r.publishError(ctx, event, "update_error", err.Error())
		return
	}
	r.publishActionResult(ctx, event, "environment_update", "success", nil)
}

func (r *Reactor) handleEnvironmentDelete(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "environment")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment id: %v", err))
		return
	}
	if err := r.registry.DeleteEnvironment(ctx, id, req.Force); err != nil {
		r.publishError(ctx, event, "delete_error", err.Error())
		return
	}
	r.publishActionResult(ctx, event, "environment_delete", "success", nil)
}

func (r *Reactor) handleArtifactRegister(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	var req struct {
		BuildID           string         `json:"build_id"`
		ServiceID         string         `json:"service_id"`
		ImageRepo         string         `json:"image_repo"`
		ImageTag          string         `json:"image_tag"`
		ImageDigest       string         `json:"image_digest"`
		ManifestMediaType string         `json:"manifest_media_type,omitempty"`
		SizeBytes         *int64         `json:"size_bytes,omitempty"`
		SBOMURL           string         `json:"sbom_url,omitempty"`
		SignatureRef      string         `json:"signature_ref,omitempty"`
		ScanStatus        string         `json:"scan_status,omitempty"`
		Metadata          map[string]any `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	buildID, err := uuid.Parse(req.BuildID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid build_id: %v", err))
		return
	}
	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service_id: %v", err))
		return
	}
	if req.ScanStatus == "" {
		req.ScanStatus = string(domain.ScanStatusUnknown)
	}
	artifact := &domain.Artifact{BuildID: buildID, ServiceID: serviceID, ImageRepo: req.ImageRepo, ImageTag: req.ImageTag, ImageDigest: req.ImageDigest, ManifestMediaType: req.ManifestMediaType, SizeBytes: req.SizeBytes, SBOMURL: req.SBOMURL, SignatureRef: req.SignatureRef, ScanStatus: domain.ScanStatus(req.ScanStatus), Metadata: req.Metadata}
	if err := r.registry.RegisterArtifact(ctx, artifact); err != nil {
		r.publishError(ctx, event, "register_error", err.Error())
		return
	}
	r.publishActionResult(ctx, event, "artifact_register", "success", nil)
}

// handleDeploymentApproval processes a legacy deployment approval/rejection request in direct tests.
func (r *Reactor) handleDeploymentApproval(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "approver", event.PubKey)
	logger.Info("received deployment approval request")

	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized approval request")
		r.publishError(ctx, event, "unauthorized", "approver not in authorized list")
		return
	}

	// Parse approval from tags
	var intentID, decision string
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "intent":
			intentID = tag[1]
		case "decision":
			decision = tag[1] // "approve" or "reject"
		}
	}

	if intentID == "" {
		r.publishError(ctx, event, "validation_error", "intent tag is required")
		return
	}
	if decision != "approve" && decision != "reject" {
		r.publishError(ctx, event, "validation_error", "decision must be 'approve' or 'reject'")
		return
	}

	parsedIntentID, err := uuid.Parse(intentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid intent id: %v", err))
		return
	}

	logger = logger.With("intent_id", intentID, "decision", decision)

	if decision == "approve" {
		if err := r.registry.ApproveDeploymentIntent(ctx, parsedIntentID); err != nil {
			logger.Error("failed to approve deployment", "error", err)
			r.publishError(ctx, event, "approval_error", err.Error())
			return
		}
		logger.Info("deployment approved")
	} else {
		if err := r.registry.RejectDeploymentIntent(ctx, parsedIntentID); err != nil {
			logger.Error("failed to reject deployment", "error", err)
			r.publishError(ctx, event, "rejection_error", err.Error())
			return
		}
		logger.Info("deployment rejected")
	}

	r.publishApprovalResult(ctx, event, intentID, decision)
}

// handleLLMRouteCreate processes a legacy LLM route creation request in direct tests.
func (r *Reactor) handleLLMRouteCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "route_create") {
		return
	}
	var req struct {
		Name                   string                         `json:"name"`
		Description            string                         `json:"description,omitempty"`
		GatewayConfig          *domain.LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
		DefaultPlacementPolicy *domain.LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
		DefaultPromotionGate   *domain.LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
		Metadata               map[string]any                 `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	route := &domain.LLMRoute{Name: req.Name, Description: req.Description, GatewayConfig: req.GatewayConfig, DefaultPlacementPolicy: req.DefaultPlacementPolicy, DefaultPromotionGate: req.DefaultPromotionGate, Metadata: req.Metadata}
	if err := r.llmRegistry.CreateRoute(ctx, route); err != nil {
		logger.Error("failed to create LLM route", "error", err)
		r.publishLLMError(ctx, event, "create_error", err.Error())
		return
	}
	logger.Info("LLM route created", "route_id", route.ID.String(), "name", route.Name)
	r.publishLLMRouteCreateResult(ctx, event, route)
}

// handleLLMReleaseRegister processes a legacy LLM release registration request in direct tests.
func (r *Reactor) handleLLMReleaseRegister(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "release_register") {
		return
	}
	var req struct {
		RouteID            string                                 `json:"route_id"`
		Version            string                                 `json:"version"`
		ModelRef           string                                 `json:"model_ref"`
		ModelSource        string                                 `json:"model_source"`
		ModelRevision      string                                 `json:"model_revision,omitempty"`
		EstimatedVRAMGB    int                                    `json:"estimated_vram_gb,omitempty"`
		BackendPreferences []domain.LLMBackendKind                `json:"backend_preferences,omitempty"`
		RuntimeBackend     *domain.LLMRuntimeManagedBackendConfig `json:"runtime_backend,omitempty"`
		ExternalBackend    *domain.LLMExternalBackendConfig       `json:"external_backend,omitempty"`
		PlacementPolicy    *domain.LLMPlacementPolicy             `json:"placement_policy,omitempty"`
		PromotionGate      *domain.LLMPromotionGateConfig         `json:"promotion_gate,omitempty"`
		Metadata           map[string]any                         `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	release := &domain.LLMRelease{RouteID: routeID, Version: req.Version, ModelRef: req.ModelRef, ModelSource: req.ModelSource, ModelRevision: req.ModelRevision, EstimatedVRAMGB: req.EstimatedVRAMGB, BackendPreferences: req.BackendPreferences, RuntimeBackend: req.RuntimeBackend, ExternalBackend: req.ExternalBackend, PlacementPolicy: req.PlacementPolicy, PromotionGate: req.PromotionGate, Metadata: req.Metadata}
	if err := r.llmRegistry.CreateRelease(ctx, release); err != nil {
		logger.Error("failed to register LLM release", "error", err)
		r.publishLLMError(ctx, event, "register_error", err.Error())
		return
	}
	logger.Info("LLM release registered", "route_id", routeID.String(), "release_id", release.ID.String())
	r.publishLLMReleaseRegisterResult(ctx, event, release)
}

// handleLLMDeployRequest processes a legacy LLM deployment request in direct tests.
func (r *Reactor) handleLLMDeployRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "deploy") {
		return
	}
	var req struct {
		RouteID       string         `json:"route_id"`
		EnvironmentID string         `json:"environment_id"`
		ReleaseID     string         `json:"release_id"`
		RequestedBy   string         `json:"requested_by,omitempty"`
		Metadata      map[string]any `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = tagValueNostr(event.Tags, "environment")
	}
	if req.ReleaseID == "" {
		req.ReleaseID = tagValueNostr(event.Tags, "release")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return
	}
	releaseID, err := uuid.Parse(req.ReleaseID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid release_id: %v", err))
		return
	}
	if req.RequestedBy == "" {
		req.RequestedBy = event.PubKey.Hex()
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["nostr_event_id"] = event.ID.Hex()
	metadata["nostr_request_pubkey"] = event.PubKey.Hex()
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: req.RequestedBy, SourceKind: domain.SourceKindEventTriggered, Metadata: metadata}
	if err := r.llmRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create LLM deployment intent", "error", err)
		r.publishLLMError(ctx, event, "intent_error", err.Error())
		return
	}
	logger.Info("LLM deployment intent created", "intent_id", intent.ID.String())
	r.publishLLMDeploymentStatus(ctx, event, intent, "accepted", "LLM deployment intent accepted")
}

// handleLLMDeploymentApproval processes a legacy LLM approval/rejection request in direct tests.
func (r *Reactor) handleLLMDeploymentApproval(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "approver", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "approval") {
		return
	}
	var content struct {
		IntentID string `json:"intent_id,omitempty"`
		Decision string `json:"decision,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &content)
	if content.IntentID == "" {
		content.IntentID = tagValueNostr(event.Tags, "intent")
	}
	if content.Decision == "" {
		content.Decision = tagValueNostr(event.Tags, "decision")
	}
	if content.Decision != "approve" && content.Decision != "reject" {
		r.publishLLMError(ctx, event, "validation_error", "decision must be 'approve' or 'reject'")
		return
	}
	intentID, err := uuid.Parse(content.IntentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid intent_id: %v", err))
		return
	}
	if content.Decision == "approve" {
		err = r.llmRegistry.ApproveDeploymentIntent(ctx, intentID)
	} else {
		err = r.llmRegistry.RejectDeploymentIntent(ctx, intentID)
	}
	if err != nil {
		logger.Error("failed to apply LLM deployment approval decision", "error", err)
		r.publishLLMError(ctx, event, "approval_error", err.Error())
		return
	}
	intent, _ := r.llmRegistry.GetDeploymentIntent(ctx, intentID)
	if intent == nil {
		intent = &domain.LLMDeploymentIntent{ID: intentID}
	}
	r.publishLLMDeploymentResult(ctx, event, intent, content.Decision, "LLM deployment approval decision recorded")
}

// handleLLMRollbackRequest processes a legacy LLM rollback request in direct tests.
func (r *Reactor) handleLLMRollbackRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "rollback") {
		return
	}
	var req struct {
		RouteID       string `json:"route_id,omitempty"`
		EnvironmentID string `json:"environment_id,omitempty"`
		RequestedBy   string `json:"requested_by,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = tagValueNostr(event.Tags, "environment")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return
	}
	if req.RequestedBy == "" {
		req.RequestedBy = event.PubKey.Hex()
	}

	metadata := map[string]any{
		"nostr_event_id":         event.ID.Hex(),
		"nostr_request_pubkey":   event.PubKey.Hex(),
		"nostr_request_kind":     int(event.Kind),
		"nostr_request_command":  "llm_rollback",
		"nostr_requested_by_raw": req.RequestedBy,
	}
	intent, err := r.llmRegistry.RollbackWithMetadata(ctx, routeID, envID, req.RequestedBy, metadata)
	if err != nil {
		logger.Error("failed to initiate LLM rollback", "error", err)
		r.publishLLMError(ctx, event, "rollback_error", err.Error())
		return
	}
	logger.Info("LLM rollback intent created", "intent_id", intent.ID.String())
	r.publishLLMDeploymentStatus(ctx, event, intent, "accepted", "LLM rollback intent accepted")
}

func (r *Reactor) handleToolProvisionRequest(ctx context.Context, event *nostr.Event) error {
	logger := r.zapLog.With(zap.String("event_id", event.ID.Hex()), zap.String("requester", event.PubKey.Hex()), zap.Int("kind", int(event.Kind)))
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return fmt.Errorf("unauthorized requester")
	}
	if r.toolProvisioning == nil {
		r.publishError(ctx, event, "tool_provisioning_unavailable", "tool provisioning repository not configured")
		return fmt.Errorf("tool provisioning repository not configured")
	}
	var req struct {
		ServiceID     string               `json:"service_id"`
		EnvironmentID string               `json:"environment_id"`
		Operation     string               `json:"operation"`
		Tools         []domain.ToolRequest `json:"tools"`
		Reason        string               `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return fmt.Errorf("parse tool provisioning request: %w", err)
	}
	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service_id: %v", err))
		return err
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return err
	}
	if len(req.Tools) == 0 {
		r.publishError(ctx, event, "validation_error", "tools are required")
		return fmt.Errorf("empty tools")
	}
	intent := &domain.ToolProvisionIntent{
		ID:              uuid.New(),
		ServiceID:       serviceID,
		EnvironmentID:   envID,
		RequestedTools:  req.Tools,
		Status:          domain.ToolProvisionStatusPending,
		NostrEventID:    event.ID.Hex(),
		RequesterPubkey: event.PubKey.Hex(),
		CreatedAt:       time.Now().UTC(),
	}
	if err := r.toolProvisioning.CreateIntent(ctx, intent); err != nil {
		r.publishError(ctx, event, "intent_error", err.Error())
		return fmt.Errorf("create tool provisioning intent: %w", err)
	}
	if r.toolResponder != nil {
		_ = r.toolResponder.PublishStatus(ctx, event, intent, "queued", "Tool provisioning intent accepted and queued")
	}
	logger.Info("tool provisioning request accepted", zap.String("intent_id", intent.ID.String()), zap.String("operation", req.Operation), zap.String("reason", req.Reason), zap.Int("tool_count", len(req.Tools)))
	if r.toolCoordinator != nil {
		if err := r.toolCoordinator.ProcessIntent(ctx, intent.ID); err != nil {
			logger.Error("processing tool provisioning intent failed", zap.String("intent_id", intent.ID.String()), zap.Error(err))
		}
	}
	return nil
}

func (r *Reactor) handleToolApprovalResponse(ctx context.Context, event *nostr.Event) error {
	logger := r.zapLog.With(zap.String("event_id", event.ID.Hex()), zap.String("operator", event.PubKey.Hex()), zap.Int("kind", int(event.Kind)))
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "operator not in authorized list")
		return fmt.Errorf("unauthorized operator")
	}
	if r.toolProvisioning == nil {
		r.publishError(ctx, event, "tool_provisioning_unavailable", "tool provisioning repository not configured")
		return fmt.Errorf("tool provisioning repository not configured")
	}
	var req struct {
		IntentID string `json:"intent_id"`
		Action   string `json:"action"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return fmt.Errorf("parse tool approval response: %w", err)
	}
	intentID, err := uuid.Parse(req.IntentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid intent_id: %v", err))
		return err
	}
	if req.Action != "approve" && req.Action != "reject" {
		r.publishError(ctx, event, "validation_error", "action must be 'approve' or 'reject'")
		return fmt.Errorf("invalid action")
	}
	intent, err := r.toolProvisioning.GetIntent(ctx, intentID)
	if err != nil {
		r.publishError(ctx, event, "lookup_error", err.Error())
		return fmt.Errorf("get tool provisioning intent: %w", err)
	}
	if intent == nil {
		r.publishError(ctx, event, "not_found", "tool provisioning intent not found")
		return fmt.Errorf("intent not found")
	}
	now := time.Now().UTC()
	if req.Action == "approve" {
		intent.Status = domain.ToolProvisionStatusApproved
		intent.ApprovedBy = event.PubKey.Hex()
		intent.ApprovedAt = &now
	} else {
		intent.Status = domain.ToolProvisionStatusRejected
	}
	if err := r.toolProvisioning.UpdateIntent(ctx, intent); err != nil {
		r.publishError(ctx, event, "update_error", err.Error())
		return fmt.Errorf("update tool provisioning intent: %w", err)
	}
	if err := r.toolProvisioning.LogApproval(ctx, intent.ID, req.Action, event.PubKey.Hex(), req.Reason); err != nil {
		logger.Warn("failed to log tool approval action", zap.Error(err))
	}
	if req.Action == "approve" {
		logger.Info("tool provisioning approved and queued", zap.String("intent_id", intent.ID.String()))
		if r.toolCoordinator != nil {
			if err := r.toolCoordinator.ProcessApprovedIntent(ctx, intent.ID); err != nil {
				logger.Error("processing approved tool intent failed", zap.Error(err))
			}
		}
	} else {
		logger.Info("tool provisioning rejected", zap.String("intent_id", intent.ID.String()))
	}
	if r.toolResponder != nil {
		requestEventID, idErr := nostr.IDFromHex(strings.TrimSpace(intent.NostrEventID))
		requestPubkey, pubkeyErr := nostr.PubKeyFromHex(strings.TrimSpace(intent.RequesterPubkey))
		if idErr != nil || pubkeyErr != nil {
			logger.Warn("tool approval result publish skipped: invalid original request metadata", zap.String("id_error", fmt.Sprint(idErr)), zap.String("pubkey_error", fmt.Sprint(pubkeyErr)))
		} else {
			requestEvent := &nostr.Event{ID: requestEventID, PubKey: requestPubkey}
			_ = r.toolResponder.PublishResult(ctx, requestEvent, intent, req.Action == "approve", req.Reason)
		}
	}
	return nil
}

func (r *Reactor) handlePolicyCreate(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.policyService == nil {
		r.publishError(ctx, event, "policy_unavailable", "policy service is not configured")
		return
	}
	var req struct {
		Name          string              `json:"name"`
		EnvironmentID *string             `json:"environment_id,omitempty"`
		Rules         []domain.PolicyRule `json:"rules"`
		Enforcement   string              `json:"enforcement"`
		Enabled       bool                `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	policy := &domain.DeploymentPolicy{Name: req.Name, Rules: req.Rules, Enforcement: domain.PolicyEnforcement(req.Enforcement), Enabled: req.Enabled}
	if req.EnvironmentID != nil && *req.EnvironmentID != "" {
		id, err := uuid.Parse(*req.EnvironmentID)
		if err != nil {
			r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
			return
		}
		policy.EnvironmentID = &id
	}
	if policy.Enforcement == "" {
		policy.Enforcement = domain.PolicyEnforcementWarn
	}
	if err := r.policyService.CreatePolicy(ctx, policy); err != nil {
		r.publishError(ctx, event, "create_error", err.Error())
		return
	}
	r.publishPolicyRegistry(ctx, policy, false)
	r.publishActionResult(ctx, event, "policy_create", "success", nil)
}

func (r *Reactor) handlePolicyUpdate(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.policyService == nil {
		r.publishError(ctx, event, "policy_unavailable", "policy service is not configured")
		return
	}
	var req struct {
		ID            string              `json:"id"`
		Name          *string             `json:"name,omitempty"`
		EnvironmentID *string             `json:"environment_id,omitempty"`
		Rules         []domain.PolicyRule `json:"rules,omitempty"`
		Enforcement   *string             `json:"enforcement,omitempty"`
		Enabled       *bool               `json:"enabled,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "policy")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid policy id: %v", err))
		return
	}
	policy, err := r.policyService.GetPolicy(ctx, id)
	if err != nil {
		r.publishError(ctx, event, "lookup_error", err.Error())
		return
	}
	if policy == nil {
		r.publishError(ctx, event, "not_found", "policy not found")
		return
	}
	if req.Name != nil {
		policy.Name = strings.TrimSpace(*req.Name)
	}
	if req.Rules != nil {
		policy.Rules = req.Rules
	}
	if req.Enforcement != nil {
		policy.Enforcement = domain.PolicyEnforcement(strings.TrimSpace(*req.Enforcement))
	}
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}
	if req.EnvironmentID != nil {
		if strings.TrimSpace(*req.EnvironmentID) == "" {
			policy.EnvironmentID = nil
		} else {
			envID, err := uuid.Parse(*req.EnvironmentID)
			if err != nil {
				r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
				return
			}
			policy.EnvironmentID = &envID
		}
	}
	if policy.Enforcement == "" {
		policy.Enforcement = domain.PolicyEnforcementWarn
	}
	if err := r.policyService.UpdatePolicy(ctx, policy); err != nil {
		r.publishError(ctx, event, "update_error", err.Error())
		return
	}
	r.publishPolicyRegistry(ctx, policy, false)
	r.publishActionResult(ctx, event, "policy_update", "success", nil)
}

func (r *Reactor) handlePolicyDelete(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.policyService == nil {
		r.publishError(ctx, event, "policy_unavailable", "policy service is not configured")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.ID == "" {
		req.ID = tagValueNostr(event.Tags, "policy")
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid policy id: %v", err))
		return
	}
	if err := r.policyService.DeletePolicy(ctx, id); err != nil {
		r.publishError(ctx, event, "delete_error", err.Error())
		return
	}
	r.publishPolicyRegistry(ctx, &domain.DeploymentPolicy{ID: id, UpdatedAt: time.Now().UTC()}, true)
	r.publishActionResult(ctx, event, "policy_delete", "success", nil)
}

func (r *Reactor) handlePolicyEvaluate(ctx context.Context, event *nostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.policyService == nil {
		r.publishError(ctx, event, "policy_unavailable", "policy service is not configured")
		return
	}
	var req struct {
		ArtifactID    string `json:"artifact_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}
	artifactID, err := uuid.Parse(req.ArtifactID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid artifact_id: %v", err))
		return
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return
	}
	evaluation, err := r.policyService.Evaluate(ctx, artifactID, envID)
	if err != nil {
		r.publishError(ctx, event, "evaluate_error", err.Error())
		return
	}
	tags := nostr.Tags{{"status", "success"}, {"action", "policy_evaluate"}, {"artifact", artifactID.String()}, {"environment", envID.String()}}
	_ = r.publishContextVMResult(ctx, event, evaluation, tags, nil)
}

func (r *Reactor) authorizeLLMRequest(ctx context.Context, event *nostr.Event, step string) bool {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.publishLLMError(ctx, event, "unauthorized", "requester not in authorized list")
		return false
	}
	if r.llmRegistry == nil {
		r.publishLLMError(ctx, event, step+"_unavailable", "LLM registry is not configured")
		return false
	}
	return true
}

func tagValueNostr(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

type operatorScope string

const (
	operatorScopeDefault       operatorScope = "default"
	operatorScopeAdoption      operatorScope = "adoption"
	operatorScopeDirectRuntime operatorScope = "direct_runtime"
)

func acceptedWorkerReadModelKinds() []int {
	return []int{KindCASControlState}
}

func isAcceptedWorkerReadModelKind(kind int) bool {
	for _, accepted := range acceptedWorkerReadModelKinds() {
		if kind == accepted {
			return true
		}
	}
	return false
}

func (r *Reactor) buildRequestSubscriptionFiltersForCurrentCursor(ctx context.Context) []nostr.Filter {
	return r.buildRequestSubscriptionFilters(r.requestSubscriptionSince(ctx))
}

func (r *Reactor) requestSubscriptionSince(ctx context.Context) nostr.Timestamp {
	kinds := requestSubscriptionKinds()
	var since *nostr.Timestamp
	if r.replayCursorPlanner != nil {
		since = r.replayCursorPlanner.ComputeSince(ctx, kinds)
	}
	if lastSeen := r.latestLastSeen(kinds); lastSeen != nil {
		adjusted := replayCursorWithOverlap(*lastSeen)
		if since == nil || adjusted > *since {
			since = &adjusted
		}
	}
	if since != nil {
		return *since
	}
	return nostr.Now()
}

func (r *Reactor) latestLastSeen(kinds []int) *nostr.Timestamp {
	groups := r.replayGroupsForKinds(kinds)
	if len(groups) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var latest nostr.Timestamp
	for _, group := range groups {
		seen, ok := r.lastSeenByGroup[group]
		if !ok {
			continue
		}
		if latest == 0 || seen > latest {
			latest = seen
		}
	}
	if latest == 0 {
		return nil
	}
	return &latest
}

func (r *Reactor) trackLastSeen(event *nostr.Event) {
	if event == nil || event.CreatedAt == 0 {
		return
	}
	groups := r.replayGroupsForKind(int(event.Kind))
	if len(groups) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, group := range groups {
		if event.CreatedAt > r.lastSeenByGroup[group] {
			r.lastSeenByGroup[group] = event.CreatedAt
		}
	}
}

func (r *Reactor) replayGroupsForKind(kind int) []string {
	catalog := r.kindCatalog
	if catalog == nil {
		return []string{"control_plane_live"}
	}

	groups := make([]string, 0, 1)
	for _, group := range catalog.Groups {
		if slices.Contains(group.Kinds, kind) {
			groups = append(groups, group.Name)
		}
	}
	return groups
}

func (r *Reactor) replayGroupsForKinds(kinds []int) []string {
	seenKinds := make(map[int]struct{}, len(kinds))
	for _, kind := range kinds {
		seenKinds[kind] = struct{}{}
	}

	seenGroups := make(map[string]struct{})
	groups := make([]string, 0)
	catalog := r.kindCatalog
	if catalog == nil {
		return []string{"control_plane_live"}
	}
	for _, group := range catalog.Groups {
		for _, kind := range group.Kinds {
			if _, ok := seenKinds[kind]; !ok {
				continue
			}
			if _, exists := seenGroups[group.Name]; !exists {
				seenGroups[group.Name] = struct{}{}
				groups = append(groups, group.Name)
			}
			break
		}
	}
	return groups
}

func replayCursorWithOverlap(timestamp nostr.Timestamp) nostr.Timestamp {
	if timestamp <= 1 {
		return 0
	}
	return timestamp - 1
}

func (r *Reactor) buildRequestSubscriptionFilters(since nostr.Timestamp) []nostr.Filter {
	return []nostr.Filter{{
		Kinds:   nostrKindsFromInts(canonicalReactorSubscriptionKinds()),
		Authors: r.requestSubscriptionAuthors(),
		Since:   since,
	}}
}

func requestSubscriptionKinds() []int {
	return canonicalReactorSubscriptionKinds()
}

func defaultRequestSubscriptionKinds() []int {
	return canonicalReactorSubscriptionKinds()
}

func canonicalReactorSubscriptionKinds() []int {
	return []int{
		kinds.ContextVMMessage,
		kinds.ContextVMGiftWrap,
		kinds.ContextVMEphemeralGiftWrap,
	}
}

func canonicalRuntimeReplayKinds() []int {
	return []int{
		nostrpool.KindCASControlState,
		nostrpool.KindNIP38Status,
		kinds.ContextVMMessage,
		kinds.ContextVMGiftWrap,
		kinds.ContextVMEphemeralGiftWrap,
		kinds.ContextVMToolsList,
		kinds.ContextVMResourcesList,
		kinds.ContextVMResourceTemplatesList,
		kinds.ContextVMPromptsList,
		nostrpool.KindRelaySetDiscovery,
		nostrpool.KindNIP65RelayList,
	}
}

func isCanonicalRuntimeReplayKind(kind int) bool {
	return slices.Contains(canonicalRuntimeReplayKinds(), kind)
}

func isLegacyProductionRuntimeKind(kind int) bool {
	return (kind >= 5941 && kind <= 5999) ||
		(kind >= 6961 && kind <= 6999) ||
		(kind >= 7961 && kind <= 7999) ||
		(kind >= 31100 && kind <= 31399) ||
		(kind >= 31900 && kind <= 32099) ||
		(kind >= 38390 && kind <= 38499)
}

func (r *Reactor) requestSubscriptionAuthors() []nostr.PubKey {
	return nostrPubKeysFromHex(r.subscriptionAuthors(operatorScopeDefault, operatorScopeAdoption, operatorScopeDirectRuntime))
}

func nostrKindsFromInts(kinds []int) []nostr.Kind {
	out := make([]nostr.Kind, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, nostr.Kind(kind))
	}
	return out
}

func nostrPubKeysFromHex(pubkeys []string) []nostr.PubKey {
	out := make([]nostr.PubKey, 0, len(pubkeys))
	invalidSeen := false
	for _, raw := range pubkeys {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		pubkey, err := nostr.PubKeyFromHex(trimmed)
		if err != nil {
			invalidSeen = true
			continue
		}
		out = append(out, pubkey)
	}
	if invalidSeen {
		out = append(out, invalidSubscriptionAuthorPubKey())
	}
	return out
}

func invalidSubscriptionAuthorPubKey() nostr.PubKey {
	pubkey, err := nostr.PubKeyFromHex("8541adf5c61099c9f8160c7555e7bb7e98330bdedb27d6ac6eaf38d6c39dce3a")
	if err != nil {
		panic("invalid controlplane subscription author sentinel: " + err.Error())
	}
	return pubkey
}

func (r *Reactor) subscriptionAuthors(scopes ...operatorScope) []string {
	seen := make(map[string]struct{})
	authors := make([]string, 0, len(r.config.AuthorizedPubkeys)+len(r.config.AdoptionAuthorizedPubkeys)+len(r.config.DirectRuntimeAuthorizedPubkeys))
	add := func(pubkeys []string) {
		for _, pubkey := range pubkeys {
			if pubkey == "" {
				continue
			}
			if _, ok := seen[pubkey]; ok {
				continue
			}
			seen[pubkey] = struct{}{}
			authors = append(authors, pubkey)
		}
	}
	for _, scope := range scopes {
		switch scope {
		case operatorScopeDefault:
			add(r.config.AuthorizedPubkeys)
		case operatorScopeAdoption:
			add(r.config.AdoptionAuthorizedPubkeys)
		case operatorScopeDirectRuntime:
			add(r.config.DirectRuntimeAuthorizedPubkeys)
		}
	}
	if len(authors) == 0 {
		return nil
	}
	return authors
}

// isAuthorized checks if a pubkey is authorized to use the control plane.
func (r *Reactor) isAuthorized(pubkey string) bool {
	return r.isAuthorizedFor(pubkey, operatorScopeDefault)
}

// isAuthorizedFor checks if a pubkey is authorized for a scoped operator path.
func (r *Reactor) isAuthorizedFor(pubkey string, scope operatorScope) bool {
	if pubkey == "" {
		return false
	}
	if slices.Contains(r.config.AuthorizedPubkeys, pubkey) {
		return true
	}
	switch scope {
	case operatorScopeAdoption:
		return slices.Contains(r.config.AdoptionAuthorizedPubkeys, pubkey)
	case operatorScopeDirectRuntime:
		return slices.Contains(r.config.DirectRuntimeAuthorizedPubkeys, pubkey)
	default:
		return false
	}
}

// parseDeployRequest extracts deployment request data from an event.
func (r *Reactor) parseDeployRequest(event *nostr.Event) (*deployRequest, error) {
	var req deployRequest

	// Parse from content JSON
	var content struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ArtifactID    string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return nil, fmt.Errorf("invalid JSON content: %w", err)
	}

	var err error
	req.ServiceID, err = uuid.Parse(content.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service_id: %w", err)
	}

	req.EnvironmentID, err = uuid.Parse(content.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("invalid environment_id: %w", err)
	}

	req.ArtifactID, err = uuid.Parse(content.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("invalid artifact_id: %w", err)
	}

	return &req, nil
}

type deployRequest struct {
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
	ArtifactID    uuid.UUID
}

// --- Event Publishing ---

func (r *Reactor) appendRequestResourceTags(ctx context.Context, tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if len(tag) >= 2 {
			seen[tag[0]+"="+tag[1]] = struct{}{}
		}
	}
	add := func(key, value string) {
		if value == "" {
			return
		}
		dedupeKey := key + "=" + value
		if _, ok := seen[dedupeKey]; ok {
			return
		}
		seen[dedupeKey] = struct{}{}
		tags = append(tags, nostr.Tag{key, value})
	}

	r.mu.Lock()
	run := r.runs[requestEvent.ID.Hex()]
	r.mu.Unlock()
	if run != nil {
		add("service", run.ServiceID.String())
		add("environment", run.EnvironmentID.String())
		if run.ArtifactID != uuid.Nil {
			add("artifact", run.ArtifactID.String())
		}
		if run.IntentID != nil {
			add("intent", run.IntentID.String())
		}
		add("run", run.ID.String())
	}

	var content struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ArtifactID    string `json:"artifact_id"`
		IntentID      string `json:"intent_id"`
		RunID         string `json:"run_id"`
	}
	if requestEvent.Content != "" {
		_ = json.Unmarshal([]byte(requestEvent.Content), &content)
	}
	add("service", content.ServiceID)
	add("environment", content.EnvironmentID)
	add("artifact", content.ArtifactID)
	add("intent", content.IntentID)
	add("run", content.RunID)

	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "service", "environment", "artifact", "intent", "run":
			add(tag[0], tag[1])
		}
	}

	// Approval requests normally provide only an intent tag; enrich with the
	// referenced resources when possible so consumers can filter result events.
	if content.IntentID == "" {
		for _, tag := range tags {
			if len(tag) >= 2 && tag[0] == "intent" {
				content.IntentID = tag[1]
				break
			}
		}
	}
	if r.registry != nil {
		if intentID, err := uuid.Parse(content.IntentID); err == nil {
			if intent, err := r.registry.GetDeploymentIntent(ctx, intentID); err == nil && intent != nil {
				add("service", intent.ServiceID.String())
				add("environment", intent.EnvironmentID.String())
				add("artifact", intent.ArtifactID.String())
			}
		}
	}
	return tags
}

// publishStatus publishes canonical deployment progress for retained direct handler paths.
func (r *Reactor) publishStatus(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	tags := nostr.Tags{
		{"status", "processing"},
		{"step", step},
		{"category", "deployment"},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	return r.publishCanonicalStatus(ctx, requestEvent, tags, map[string]any{
		"status":  "processing",
		"step":    step,
		"message": message,
	})
}

// publishDeploymentResult publishes a ContextVM deployment result for retained direct handler paths.
func (r *Reactor) publishDeploymentResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.DeploymentIntent) error {
	payload := map[string]interface{}{
		"intent_id":      intent.ID.String(),
		"service_id":     intent.ServiceID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"artifact_id":    intent.ArtifactID.String(),
		"status":         intent.Status,
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"artifact", intent.ArtifactID.String()},
		{"intent", intent.ID.String()},
	}
	if intent.DesiredHash != "" {
		payload["desired_hash"] = intent.DesiredHash
		tags = append(tags, nostr.Tag{"desired_hash", intent.DesiredHash})
	}
	if intent.DesiredState != nil {
		appendDesiredStateMeta(intent.DesiredState, payload, &tags)
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

// publishActionResult publishes a ContextVM action result for retained direct handler paths.
func (r *Reactor) publishActionResult(ctx context.Context, requestEvent *nostr.Event, action, status string, err error) error {
	tags := nostr.Tags{
		{"action", action},
		{"status", status},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	content := map[string]interface{}{
		"action": action,
		"status": status,
	}
	if err != nil {
		tags = append(tags, nostr.Tag{"error", err.Error()})
		content["error"] = err.Error()
		return r.publishContextVMResult(ctx, requestEvent, nil, tags, &JSONRPCError{Code: -32000, Message: err.Error()})
	}
	return r.publishContextVMResult(ctx, requestEvent, content, tags, nil)
}

// publishApprovalResult publishes a result for deployment approval/rejection.
func (r *Reactor) publishApprovalResult(ctx context.Context, requestEvent *nostr.Event, intentID, decision string) error {
	content := map[string]interface{}{
		"intent_id": intentID,
		"decision":  decision,
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"intent", intentID},
		{"decision", decision},
	}
	if parsedIntentID, err := uuid.Parse(intentID); err == nil {
		if intent, err := r.registry.GetDeploymentIntent(ctx, parsedIntentID); err == nil && intent != nil {
			tags = append(tags,
				nostr.Tag{"service", intent.ServiceID.String()},
				nostr.Tag{"environment", intent.EnvironmentID.String()},
				nostr.Tag{"artifact", intent.ArtifactID.String()},
			)
		}
	}
	return r.publishContextVMResult(ctx, requestEvent, content, tags, nil)
}

func (r *Reactor) publishLLMRouteCreateResult(ctx context.Context, requestEvent *nostr.Event, route *domain.LLMRoute) error {
	payload := map[string]any{
		"route_id": route.ID.String(),
		"name":     route.Name,
		"status":   "success",
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"route", route.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishLLMReleaseRegisterResult(ctx context.Context, requestEvent *nostr.Event, release *domain.LLMRelease) error {
	payload := map[string]any{
		"route_id":   release.RouteID.String(),
		"release_id": release.ID.String(),
		"version":    release.Version,
		"status":     "success",
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"route", release.RouteID.String()},
		{"release", release.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishLLMDeploymentStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.LLMDeploymentIntent, step, message string) error {
	tags := nostr.Tags{
		{"status", "processing"},
		{"step", step},
		{"category", "llm_deployment"},
		{"route", intent.RouteID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"release", intent.ReleaseID.String()},
		{"intent", intent.ID.String()},
	}
	return r.publishCanonicalStatus(ctx, requestEvent, tags, map[string]any{
		"intent_id":      intent.ID.String(),
		"route_id":       intent.RouteID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"release_id":     intent.ReleaseID.String(),
		"status":         "processing",
		"step":           step,
		"message":        message,
	})
}

func (r *Reactor) publishLLMDeploymentResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.LLMDeploymentIntent, status, message string) error {
	payload := map[string]any{
		"intent_id":      intent.ID.String(),
		"route_id":       intent.RouteID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"release_id":     intent.ReleaseID.String(),
		"status":         status,
		"message":        message,
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"result", status},
		{"route", intent.RouteID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"release", intent.ReleaseID.String()},
		{"intent", intent.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

func (r *Reactor) publishLLMError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	tags := nostr.Tags{
		{"status", "error"},
		{"step", step},
		{"error", message},
	}
	tags = appendLLMRequestTags(tags, requestEvent)
	return r.publishContextVMResult(ctx, requestEvent, nil, tags, &JSONRPCError{Code: -32000, Message: message})
}

func appendLLMRequestTags(tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	seen := map[string]struct{}{}
	for _, tag := range tags {
		if len(tag) >= 2 {
			seen[tag[0]+"="+tag[1]] = struct{}{}
		}
	}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k := key + "=" + value
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		tags = append(tags, nostr.Tag{key, value})
	}
	if requestEvent.Content != "" {
		var raw map[string]any
		if json.Unmarshal([]byte(requestEvent.Content), &raw) == nil {
			add("route", stringFromAny(raw["route_id"]))
			add("environment", stringFromAny(raw["environment_id"]))
			add("release", stringFromAny(raw["release_id"]))
			add("intent", stringFromAny(raw["intent_id"]))
			add("run", stringFromAny(raw["run_id"]))
		}
	}
	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "route", "environment", "release", "intent", "run":
			add(tag[0], tag[1])
		}
	}
	return tags
}

// publishServiceCreated publishes a result for service creation.
func (r *Reactor) publishServiceCreated(ctx context.Context, requestEvent *nostr.Event, svc *domain.Service) error {
	payload := map[string]interface{}{
		"service_id":    svc.ID.String(),
		"name":          svc.Name,
		"artifact_repo": svc.ArtifactRepo,
		"runtime_type":  svc.RuntimeType,
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"type", "service_created"},
		{"service", svc.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

// publishEnvironmentCreated publishes a result for environment creation.
func (r *Reactor) publishEnvironmentCreated(ctx context.Context, requestEvent *nostr.Event, env *domain.Environment) error {
	payload := map[string]interface{}{
		"environment_id":  env.ID.String(),
		"name":            env.Name,
		"protected":       env.Protected,
		"deploy_strategy": env.DeployStrategy,
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"type", "environment_created"},
		{"environment", env.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

// publishError publishes a ContextVM error response for retained direct handler paths.
func (r *Reactor) publishError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	tags := nostr.Tags{
		{"status", "error"},
		{"step", step},
		{"error", message},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	return r.publishContextVMResult(ctx, requestEvent, nil, tags, &JSONRPCError{Code: -32000, Message: message})
}

func (r *Reactor) publishCanonicalStatus(ctx context.Context, requestEvent *nostr.Event, tags nostr.Tags, content map[string]any) error {
	if requestEvent == nil {
		return fmt.Errorf("request event is nil")
	}
	if content == nil {
		content = map[string]any{}
	}
	content["request_event_id"] = requestEvent.ID.Hex()
	content["request_pubkey"] = requestEvent.PubKey.Hex()
	body, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal canonical status: %w", err)
	}
	eventTags := nostr.Tags{{"d", canonicalReplyDTag("status", requestEvent, tags)}, {"e", requestEvent.ID.Hex(), "", "reply"}, {"p", requestEvent.PubKey.Hex()}}
	eventTags = append(eventTags, compactTags(tags)...)
	event := &nostr.Event{Kind: KindNIP38Status, CreatedAt: nostr.Now(), Tags: dedupeTags(eventTags), Content: string(body)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign canonical status: %w", err)
	}
	_, err = r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishContextVMResult(ctx context.Context, requestEvent *nostr.Event, result any, tags nostr.Tags, rpcErr *JSONRPCError) error {
	if requestEvent == nil {
		return fmt.Errorf("request event is nil")
	}
	response := ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: contextVMReplyID(requestEvent), Result: result}
	if rpcErr != nil {
		response.Result = nil
		response.Error = rpcErr
	}
	content, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal ContextVM response: %w", err)
	}
	eventTags := nostr.Tags{{"e", requestEvent.ID.Hex(), "", "reply"}, {"p", requestEvent.PubKey.Hex()}, {ContextVMRoutingTag, ContextVMWireVersion}}
	eventTags = append(eventTags, compactTags(tags)...)
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: dedupeTags(eventTags), Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign ContextVM response: %w", err)
	}
	_, err = r.publishEvent(ctx, event)
	return err
}

func contextVMReplyID(requestEvent *nostr.Event) json.RawMessage {
	if requestEvent != nil && strings.TrimSpace(requestEvent.Content) != "" {
		var rpc ContextVMJSONRPCRequest
		if err := json.Unmarshal([]byte(requestEvent.Content), &rpc); err == nil && len(rpc.ID) > 0 {
			return contextVMResponseID(rpc.ID)
		}
	}
	if requestEvent != nil {
		if dTag := strings.TrimSpace(tagValueNostr(requestEvent.Tags, "d")); dTag != "" {
			body, _ := json.Marshal(dTag)
			return body
		}
		if requestEvent.ID != (nostr.ID{}) {
			body, _ := json.Marshal(requestEvent.ID.Hex())
			return body
		}
	}
	return json.RawMessage("null")
}

func canonicalReplyDTag(prefix string, requestEvent *nostr.Event, tags nostr.Tags) string {
	parts := []string{prefix}
	if requestEvent != nil && requestEvent.ID != (nostr.ID{}) {
		parts = append(parts, requestEvent.ID.Hex())
	}
	for _, key := range []string{"step", "action", "operation", "intent", "service", "environment", "route"} {
		if value := strings.TrimSpace(tagValueNostr(tags, key)); value != "" {
			parts = append(parts, key+":"+value)
		}
	}
	return strings.Join(parts, ":")
}

// signEvent signs an event through the canonical signer compatibility boundary.
func (r *Reactor) signEvent(ctx context.Context, event *nostr.Event) error {
	return SignGoNostrEvent(ctx, r.signer, event)
}

func (r *Reactor) publishEvent(ctx context.Context, event *nostr.Event) (int, error) {
	if r.publisher == nil {
		return 0, fmt.Errorf("control-plane publisher is not configured")
	}
	if event == nil {
		return 0, fmt.Errorf("nostr event is nil")
	}
	published, err := r.publisher.Publish(ctx, *event)
	if err == nil && published > 0 && r.nostrEvents != nil {
		tagsJSON, marshalErr := json.Marshal(event.Tags)
		if marshalErr != nil {
			r.logger.Warn("failed to marshal outbound event tags for audit", "event_id", event.ID.Hex(), "error", marshalErr)
		} else if _, recordErr := r.nostrEvents.Record(ctx, &repository.NostrEventRecord{
			ID:         event.ID.Hex(),
			Kind:       int(event.Kind),
			PubKey:     event.PubKey.Hex(),
			Content:    event.Content,
			Tags:       tagsJSON,
			Sig:        nostr.HexEncodeToString(event.Sig[:]),
			CreatedAt:  event.CreatedAt.Time(),
			ReceivedAt: time.Now().UTC(),
		}); recordErr != nil {
			r.logger.Warn("failed to audit outbound control-plane event", "event_id", event.ID.Hex(), "kind", int(event.Kind), "error", recordErr)
		}
	}
	return published, err
}

// GetRun returns the current deployment run for a request event.
func (r *Reactor) GetRun(requestEventID string) *DeploymentRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[requestEventID]
}

// ObservationRequest represents a legacy observation submission payload.
type ObservationRequest struct {
	ServiceID           uuid.UUID `json:"service_id"`
	EnvironmentID       uuid.UUID `json:"environment_id"`
	ObservedImageDigest string    `json:"observed_image_digest"`
	ObservedImageRepo   string    `json:"observed_image_repo,omitempty"`
	ObservedContainerID string    `json:"observed_container_id,omitempty"`
	ObservedHost        string    `json:"observed_host,omitempty"`
	ObservedVersion     string    `json:"observed_version,omitempty"`
	HealthStatus        string    `json:"health_status"`
	Source              string    `json:"source"`
}

// handleObservationSubmit processes a legacy observation submission in direct tests.
func (r *Reactor) handleObservationSubmit(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received observation submission")

	// Validate authorization
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized observation submission")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse observation request
	var req ObservationRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		logger.Error("failed to parse observation request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	// Validate required fields
	if req.ServiceID == uuid.Nil {
		r.publishError(ctx, event, "validation_error", "service_id is required")
		return
	}
	if req.EnvironmentID == uuid.Nil {
		r.publishError(ctx, event, "validation_error", "environment_id is required")
		return
	}
	if req.ObservedImageDigest == "" {
		r.publishError(ctx, event, "validation_error", "observed_image_digest is required")
		return
	}

	// Parse health status
	healthStatus := domain.HealthStatus(req.HealthStatus)
	if healthStatus == "" {
		healthStatus = domain.HealthStatusUnknown
	}

	// Create and record observation
	obs := &domain.RuntimeObservation{
		ID:                  uuid.New(),
		ServiceID:           req.ServiceID,
		EnvironmentID:       req.EnvironmentID,
		ObservedImageDigest: req.ObservedImageDigest,
		ObservedImageRepo:   req.ObservedImageRepo,
		ObservedContainerID: req.ObservedContainerID,
		ObservedHost:        req.ObservedHost,
		ObservedVersion:     req.ObservedVersion,
		HealthStatus:        healthStatus,
		Source:              req.Source,
		ObservedAt:          time.Now(),
	}

	if err := r.registry.RecordObservation(ctx, obs); err != nil {
		logger.Error("failed to record observation", "error", err)
		r.publishError(ctx, event, "record_error", err.Error())
		return
	}

	logger.Info("observation recorded", "observation_id", obs.ID)

	// Publish observation result
	if err := r.publishObservationResult(ctx, event, obs); err != nil {
		logger.Error("failed to publish observation result", "error", err)
	}

	// Publish updated state event
	if err := r.publishStateEvent(ctx, req.ServiceID, req.EnvironmentID); err != nil {
		logger.Error("failed to publish state event", "error", err)
	}
}

// handleDriftRemediate processes a legacy drift remediation request in direct tests.
func (r *Reactor) handleDriftRemediate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received drift remediation request")

	// Validate authorization
	if !r.isAuthorized(event.PubKey.Hex()) {
		logger.Warn("unauthorized drift remediation request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request - expects service_id and environment_id
	var req struct {
		ServiceID     uuid.UUID `json:"service_id"`
		EnvironmentID uuid.UUID `json:"environment_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		logger.Error("failed to parse remediation request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	// Get current state
	state, err := r.registry.GetEnvironmentServiceState(ctx, req.ServiceID, req.EnvironmentID)
	if err != nil {
		logger.Error("failed to get service state", "error", err)
		r.publishError(ctx, event, "state_error", err.Error())
		return
	}

	// Check if remediation is currently needed. approval_required reconciliation
	// records remediation_needed and waits for an authorized remediation request.
	if state.DriftStatus != domain.DriftStatusDrifted && state.DriftStatus != domain.DriftStatusRemediationNeeded {
		r.publishRemediationResult(ctx, event, state, "not_drifted", "Service is not in drifted or remediation-needed state")
		return
	}

	// If we have a desired artifact, create a new deployment intent to remediate
	if state.DesiredArtifactID == nil {
		r.publishRemediationResult(ctx, event, state, "no_desired_artifact", "No desired artifact configured")
		return
	}

	// Create deployment intent to restore desired state
	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     req.ServiceID,
		EnvironmentID: req.EnvironmentID,
		ArtifactID:    *state.DesiredArtifactID,
		RequestedBy:   event.PubKey.Hex(),
		SourceKind:    domain.SourceKindEventTriggered,
		Status:        domain.IntentStatusPending,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := r.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create remediation intent", "error", err)
		r.publishError(ctx, event, "intent_error", err.Error())
		return
	}

	logger.Info("remediation intent created", "intent_id", intent.ID)
	r.publishRemediationResult(ctx, event, state, "remediation_started", fmt.Sprintf("Deployment intent %s created", intent.ID))
}

// publishObservationResult publishes a ContextVM observation result for retained direct handler paths.
func (r *Reactor) publishObservationResult(ctx context.Context, requestEvent *nostr.Event, obs *domain.RuntimeObservation) error {
	payload := map[string]interface{}{
		"observation_id":        obs.ID.String(),
		"service_id":            obs.ServiceID.String(),
		"environment_id":        obs.EnvironmentID.String(),
		"observed_image_digest": obs.ObservedImageDigest,
		"health_status":         string(obs.HealthStatus),
		"observed_at":           obs.ObservedAt.Format(time.RFC3339),
	}
	tags := nostr.Tags{
		{"status", "success"},
		{"service", obs.ServiceID.String()},
		{"environment", obs.EnvironmentID.String()},
		{"observation", obs.ID.String()},
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

// publishRemediationResult publishes a ContextVM remediation result for retained direct handler paths.
func (r *Reactor) publishRemediationResult(ctx context.Context, requestEvent *nostr.Event, state *domain.EnvironmentServiceState, status, message string) error {
	payload := map[string]interface{}{
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"status":         status,
		"message":        message,
	}
	tags := nostr.Tags{
		{"status", status},
		{"service", state.ServiceID.String()},
		{"environment", state.EnvironmentID.String()},
	}
	if state.DesiredArtifactID != nil {
		tags = append(tags, nostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, nostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	return r.publishContextVMResult(ctx, requestEvent, payload, tags, nil)
}

// publishStateEvent publishes canonical CAS service state.
// The d-tag is formatted as "service:env" to allow per-service-environment state tracking.
func (r *Reactor) publishStateEvent(ctx context.Context, serviceID, envID uuid.UUID) error {
	state, err := r.registry.GetEnvironmentServiceState(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("get state: %w", err)
	}

	// Build state content
	content := map[string]interface{}{
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"deleted":        false,
		"updated_at":     state.UpdatedAt.Format(time.RFC3339),
	}
	if state.DesiredArtifactID != nil {
		content["desired_artifact_id"] = state.DesiredArtifactID.String()
	}
	if state.DesiredIntentID != nil {
		content["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.LastSuccessfulRunID != nil {
		content["last_successful_run_id"] = state.LastSuccessfulRunID.String()
	}
	if state.CurrentObservationID != nil {
		content["current_observation_id"] = state.CurrentObservationID.String()
	}
	if state.LastReconciledAt != nil {
		content["last_reconciled_at"] = state.LastReconciledAt.Format(time.RFC3339)
	}

	contentJSON, _ := json.Marshal(content)
	dTag := fmt.Sprintf("%s:%s", serviceID.String(), envID.String())

	event := &nostr.Event{
		Kind:      nostrpool.KindCASControlState,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"domain", "service"},
			{"entity", "state"},
			{"schema", "bahia.cp-state.v1"},
			{"service", serviceID.String()},
			{"environment", envID.String()},
			{"deleted", "false"},
			{"drift_status", string(state.DriftStatus)},
		},
		Content: string(contentJSON),
	}
	if state.DesiredArtifactID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.LastSuccessfulRunID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"run", state.LastSuccessfulRunID.String()})
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign state event: %w", err)
	}

	_, err = r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishPolicyRegistry(ctx context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
	content := map[string]any{"deleted": deleted, "id": policy.ID.String()}
	if !deleted {
		content["name"] = policy.Name
		content["environment_id"] = nil
		if policy.EnvironmentID != nil {
			content["environment_id"] = policy.EnvironmentID.String()
		}
		content["rules"] = policy.Rules
		content["rule_count"] = len(policy.Rules)
		content["enforcement"] = string(policy.Enforcement)
		content["enabled"] = policy.Enabled
		content["created_at"] = policy.CreatedAt.Format(time.RFC3339)
	}
	content["updated_at"] = policy.UpdatedAt.Format(time.RFC3339)
	contentJSON, _ := json.Marshal(content)
	tags := nostr.Tags{{"d", policy.ID.String()}, {"policy", policy.ID.String()}, {"deleted", fmt.Sprintf("%t", deleted)}}
	if !deleted {
		tags = append(tags, nostr.Tag{"name", policy.Name}, nostr.Tag{"enabled", fmt.Sprintf("%t", policy.Enabled)}, nostr.Tag{"enforcement", string(policy.Enforcement)})
		if policy.EnvironmentID != nil {
			tags = append(tags, nostr.Tag{"environment", policy.EnvironmentID.String()})
		}
	}
	event := &nostr.Event{Kind: KindPolicyRegistry, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign policy registry event: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

// PublishServiceRegistry publishes canonical CAS service registry state.
func (r *Reactor) PublishServiceRegistry(ctx context.Context, svc *domain.Service) error {
	content, _ := json.Marshal(map[string]interface{}{
		"deleted":        false,
		"id":             svc.ID.String(),
		"name":           svc.Name,
		"repo_url":       svc.RepoURL,
		"artifact_repo":  svc.ArtifactRepo,
		"default_branch": svc.DefaultBranch,
		"runtime_type":   string(svc.RuntimeType),
		"created_at":     svc.CreatedAt.Format(time.RFC3339),
		"updated_at":     svc.UpdatedAt.Format(time.RFC3339),
	})

	event := &nostr.Event{
		Kind:      nostrpool.KindCASControlState,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", svc.ID.String()},
			{"domain", "service"},
			{"entity", "registry"},
			{"schema", "bahia.cp-state.v1"},
			{"deleted", "false"},
			{"name", svc.Name},
			{"runtime", string(svc.RuntimeType)},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign service registry event: %w", err)
	}

	_, err := r.publishEvent(ctx, event)
	return err
}

// PublishEnvironmentRegistry publishes canonical CAS environment registry state.
func (r *Reactor) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment) error {
	content, _ := json.Marshal(map[string]interface{}{
		"deleted":              false,
		"id":                   env.ID.String(),
		"name":                 env.Name,
		"protected":            env.Protected,
		"deploy_strategy":      string(env.DeployStrategy),
		"loom_worker_selector": env.LoomWorkerSelector,
		"runtime_config":       env.RuntimeConfig,
		"created_at":           env.CreatedAt.Format(time.RFC3339),
		"updated_at":           env.UpdatedAt.Format(time.RFC3339),
	})

	event := &nostr.Event{
		Kind:      nostrpool.KindCASControlState,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", env.ID.String()},
			{"domain", "environment"},
			{"entity", "registry"},
			{"schema", "bahia.cp-state.v1"},
			{"deleted", "false"},
			{"name", env.Name},
			{"protected", fmt.Sprintf("%t", env.Protected)},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign environment registry event: %w", err)
	}

	_, err := r.publishEvent(ctx, event)
	return err
}

// cleanupRuns periodically removes completed runs older than 1 hour.
func (r *Reactor) cleanupRuns(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			cutoff := time.Now().Add(-1 * time.Hour)
			deleted := 0
			for id, run := range r.runs {
				if run.CompletedAt != nil && run.CompletedAt.Before(cutoff) {
					delete(r.runs, id)
					deleted++
				}
			}
			r.mu.Unlock()
			if deleted > 0 {
				r.logger.Info("cleaned up completed runs", "deleted", deleted)
			}
		}
	}
}

// appendDesiredStateMeta adds renderer and target metadata from a DesiredServiceSpec
// to a result/status payload and tags. This is additive — old decoders ignore
// unknown content fields and tags.
func appendDesiredStateMeta(spec *domain.DesiredServiceSpec, payload map[string]interface{}, tags *nostr.Tags) {
	if spec == nil {
		return
	}
	renderer := ""
	switch {
	case spec.ComposeExtension != nil:
		renderer = "compose"
	case spec.DockerExtension != nil:
		renderer = "docker"
	case spec.KubernetesExtension != nil:
		renderer = "kubernetes"
	case spec.PodmanExtension != nil:
		renderer = "podman"
	}
	if renderer != "" {
		payload["renderer"] = renderer
		*tags = append(*tags, nostr.Tag{"renderer", renderer})
	}
	if spec.StableServiceKey != "" {
		payload["target"] = spec.StableServiceKey
		*tags = append(*tags, nostr.Tag{"target", spec.StableServiceKey})
	}
}
