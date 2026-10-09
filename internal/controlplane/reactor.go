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
	"github.com/google/uuid"
	"go.uber.org/zap"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// Legacy Bahia control-plane kind aliases, kept for direct handler tests and
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
	KindPolicyCreate             = nostrpool.KindControlPlanePolicyCreate             // Create a deployment policy
	KindPolicyUpdate             = nostrpool.KindControlPlanePolicyUpdate             // Update a deployment policy
	KindPolicyDelete             = nostrpool.KindControlPlanePolicyDelete             // Delete a deployment policy
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

	// Generic AI/ML command/result kinds (38390-38399). They stay separate from
	// the retired DVM allocation range; fleet-local Loom, Hive-CI, and
	// SoulFactory kinds within 5000-7000 are explicit independent protocols.
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

	// Replaceable registry kinds (d-tag indexed)
	KindServiceState             = nostrpool.KindServiceState             // Replaceable service state (d=service:env)
	KindServiceRegistry          = nostrpool.KindServiceRegistry          // Replaceable service registry entry (d=service_id)
	KindEnvironmentRegistry      = nostrpool.KindEnvironmentRegistry      // Replaceable environment registry entry (d=env_id)
	KindLLMRouteState            = nostrpool.KindLLMRouteState            // Replaceable LLM route state (d=route:env)
	KindArtifactRegistry         = nostrpool.KindArtifactRegistry         // Replaceable artifact registry entry (d=artifact_id)
	KindDeploymentIntentRegistry = nostrpool.KindDeploymentIntentRegistry // Replaceable deployment intent entry (d=intent_id)
	KindDeploymentRunRegistry    = nostrpool.KindDeploymentRunRegistry    // Replaceable deployment run entry (d=run_id)
	KindBuildRegistry            = nostrpool.KindBuildRegistry            // Replaceable build registry entry (d=build_id)

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
	signer      nostr.Signer
	logger      *slog.Logger
	zapLog      *zap.Logger
	dedup       *nostrpool.EventDeduplicator
	backoff     *nostrpool.Backoff
	caughtUp    atomic.Bool

	kindCatalog     *nostrpool.KindCatalog
	lastSeenByGroup map[string]nostr.Timestamp

	toolProvisioning              repository.ToolProvisioningRepository
	toolResponder                 *ToolResponder
	toolCoordinator               toolApprovalProcessor
	policyService                 *service.PolicyService
	workerRepo                    repository.WorkerRepository
	workerCleanupOrchestrator     *service.WorkerCleanupOrchestrator
	mlExecutor                    MLInferenceControlPlaneExecutor
	mlRecipeExecutor              MLRecipeControlPlaneExecutor
	nostrEvents                   repository.NostrEventRepository
	canonicalWorkflowEvents       CanonicalWorkflowEvents
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
	workerReadModelPublisher      *WorkerReadModelPublisher

	mu   sync.Mutex
	runs map[string]*DeploymentRun // requestEventID -> run

	mlWorkCh chan mlWork
}

type mlWorkKind int

const (
	mlWorkRecipeRun mlWorkKind = iota
	mlWorkDeploymentIntent
)

type mlWork struct {
	kind     mlWorkKind
	runID    uuid.UUID
	intentID uuid.UUID
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

// ReactorOption configures optional reactor dependencies without breaking the existing constructor shape.
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

func WithWorkerRepository(repo repository.WorkerRepository) ReactorOption {
	return func(r *Reactor) { r.workerRepo = repo }
}

func WithWorkerCleanupOrchestrator(orchestrator *service.WorkerCleanupOrchestrator) ReactorOption {
	return func(r *Reactor) { r.workerCleanupOrchestrator = orchestrator }
}

// WithWorkerReadModelPublisher enables direct worker read model publication.
func WithWorkerReadModelPublisher(publisher *WorkerReadModelPublisher) ReactorOption {
	return func(r *Reactor) { r.workerReadModelPublisher = publisher }
}

func WithNostrEventRepository(repo repository.NostrEventRepository) ReactorOption {
	return func(r *Reactor) {
		r.nostrEvents = repo
		if r.workerStatePublisher != nil {
			r.workerStatePublisher.ConfigureAudit(repo, r.zapLog)
		}
	}
}

func WithCanonicalWorkflowEvents(source CanonicalWorkflowEvents) ReactorOption {
	return func(r *Reactor) { r.canonicalWorkflowEvents = source }
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
func NewReactor(config Config, registry *service.RegistryService, pool *nostrpool.RelayPool, signer nostr.Signer, zapLog *zap.Logger, opts ...ReactorOption) *Reactor {
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
		mlWorkCh:        make(chan mlWork, 32),
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

func (r *Reactor) logPublishError(err error) {
	if err == nil {
		return
	}
	if r != nil && r.zapLog != nil {
		r.zapLog.Warn("control-plane response publish failed", zap.Error(err))
		return
	}
	slog.Default().Warn("control-plane response publish failed", "error", err)
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

	r.startMLWorkers(ctx)

	// Subscribe to control plane request events from the newest persisted or
	// in-process replay cursor, with nostr.Now as the no-history fallback.
	filters := r.buildRequestSubscriptionFiltersForCurrentCursor(ctx)

	r.caughtUp.Store(false)
	merged, err := r.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	r.logger.Info("subscribed to control plane events")

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
				// NIP-42 AUTH and resubscription are handled by the relay pool
				// internally (closedRetryBudget + authenticateLiveRelay). The
				// consumer only needs to log terminal closures for diagnostics.
				r.pool.RecordRelayClosed(closed.RelayURL, closed.Reason)
				r.logger.Warn("relay closed control-plane subscription",
					"relay", closed.RelayURL,
					"subscription_id", closed.SubscriptionID,
					"reason", closed.Reason,
				)
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			r.handleEOSE()
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				// GaveUp means every relay permanently refused the subscription.
				// This is recoverable when the topology changes (relay added,
				// reconnect, reconfigured AUTH), so wait event-driven instead
				// of killing the consumer.
				if gaveUp := merged.GaveUp(); gaveUp != nil {
					r.logger.Error("control-plane subscription gave up — waiting for topology change", "error", gaveUp)
					if err := r.pool.WaitForTopologyChange(ctx); err != nil {
						r.pool.Close()
						return err
					}
				}
				delay := r.backoff.Next()
				r.logger.Warn("subscription closed, reconnecting...", "delay", delay)
				select {
				case <-ctx.Done():
					r.logger.Info("reactor shutting down during reconnect backoff")
					r.pool.Close()
					return ctx.Err()
				case <-time.After(delay):
				}
				r.caughtUp.Store(false)
				filters = r.buildRequestSubscriptionFiltersForReconnect(filters)
				r.pool.RecordRelayReREQ()
				merged, err = keepSubscriptionOnResubscribeFailure(merged, func() (*nostrpool.MergedSubscription, error) {
					return r.pool.SubscribeAllWithEOSE(ctx, filters)
				})
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

// keepSubscriptionOnResubscribeFailure prevents a transient relay failure from
// replacing a usable subscription object with nil. The old subscription may be
// closed, but its channels remain safe to select on while the reactor retries.
func keepSubscriptionOnResubscribeFailure(current *nostrpool.MergedSubscription, subscribe func() (*nostrpool.MergedSubscription, error)) (*nostrpool.MergedSubscription, error) {
	next, err := subscribe()
	if err != nil {
		return current, err
	}
	if next == nil {
		return current, fmt.Errorf("relay resubscribe returned a nil subscription")
	}
	return next, nil
}

func (r *Reactor) handleRelayEOSE(eose nostrpool.RelayEOSE) {
	r.logger.Debug("relay sent control-plane EOSE", "relay", eose.RelayURL, "subscription_id", eose.SubscriptionID)
}

func (r *Reactor) handleEOSE() {
	if r.caughtUp.CompareAndSwap(false, true) {
		r.logger.Info("control-plane EOSE received: caught up with stored events")
	}
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
		// The audit table is an archive, not the dedupe authority: a
		// database error must not make the reactor deaf to its relays. The
		// in-memory dedupe still stops relay replays within this process.
		r.logger.Warn("failed to audit inbound control-plane event; handling it anyway", "event_id", event.ID.Hex(), "kind", int(event.Kind), "error", err)
		return true
	}
	if !inserted {
		r.logger.Debug("skipping already-audited control-plane event", "event_id", event.ID, "kind", event.Kind)
		return false
	}
	return true
}

// handleEvent audits and tracks canonical runtime replay events. Legacy
// Bahia command/status/result/read-model kind-number flows are rejected after
// the startup migration boundary; operator commands use the signed intent
// processor instead of this production reactor subscription.
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
	if !isCanonicalRuntimeReplayKind(eventKind) {
		r.logger.Debug("ignoring non-canonical replay event", "kind", eventKind)
		return
	}

	// Deduplicate events (relays may replay during reconnection).
	if r.dedup.IsDuplicate(eventID) {
		return
	}
	if !r.auditInboundEvent(ctx, event) {
		return
	}
	r.dedup.MarkSeen(eventID)
	r.trackLastSeen(event)

	if isHeartbeatObservationEvent(event) {
		go r.handleHeartbeatObservation(ctx, event)
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

func (r *Reactor) handleToolProvisionRequest(ctx context.Context, event *nostr.Event) error {
	logger := r.zapLog.With(zap.String("event_id", event.ID.Hex()), zap.String("requester", event.PubKey.Hex()), zap.Int("kind", int(event.Kind)))
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.logPublishError(r.publishError(ctx, event, "unauthorized", "requester not in authorized list"))
		return fmt.Errorf("unauthorized requester")
	}
	if r.toolProvisioning == nil {
		r.logPublishError(r.publishError(ctx, event, "tool_provisioning_unavailable", "tool provisioning repository not configured"))
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
		r.logPublishError(r.publishError(ctx, event, "parse_error", err.Error()))
		return fmt.Errorf("parse tool provisioning request: %w", err)
	}
	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.logPublishError(r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service_id: %v", err)))
		return err
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.logPublishError(r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err)))
		return err
	}
	if len(req.Tools) == 0 {
		r.logPublishError(r.publishError(ctx, event, "validation_error", "tools are required"))
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
		r.logPublishError(r.publishError(ctx, event, "intent_error", err.Error()))
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
		return fmt.Errorf("unauthorized operator")
	}
	if r.toolProvisioning == nil {
		return fmt.Errorf("tool provisioning repository not configured")
	}
	decisionRepository, ok := r.toolProvisioning.(toolApprovalDecisionRepository)
	if !ok {
		return fmt.Errorf("tool provisioning repository does not support atomic approval decisions")
	}
	var req struct {
		IntentID string `json:"intent_id"`
		Action   string `json:"action"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		return fmt.Errorf("parse tool approval response: %w", err)
	}
	intentID, err := uuid.Parse(req.IntentID)
	if err != nil {
		return fmt.Errorf("invalid intent_id: %w", err)
	}
	if req.Action != "approve" && req.Action != "reject" {
		return fmt.Errorf("invalid action")
	}
	stored, err := r.toolProvisioning.GetIntent(ctx, intentID)
	if err != nil {
		return fmt.Errorf("load tool provisioning intent: %w", err)
	}
	servicePubkey, err := r.workflowServicePubkey(ctx)
	if err != nil {
		return err
	}
	if err := verifyToolApprovalSource(r.canonicalWorkflowEvents, servicePubkey, stored, r.isAuthorized); err != nil {
		return fmt.Errorf("tool approval refused without canonical request provenance: %w", err)
	}
	decision := domain.ToolProvisionStatusRejected
	if req.Action == "approve" {
		decision = domain.ToolProvisionStatusApproved
	}
	intent, err := decisionRepository.ApplyToolApprovalDecision(ctx, intentID, decision, event.PubKey.Hex(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("apply tool approval decision: %w", err)
	}
	if err := verifyToolApprovalSource(r.canonicalWorkflowEvents, servicePubkey, intent, r.isAuthorized); err != nil {
		return fmt.Errorf("tool approval result refused after row change: %w", err)
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

// A connection with no delivered events has no newer replay cursor. Retain
// its previous since so events published during the disconnect remain in the
// next REQ's backfill instead of starting at the new current time.
func (r *Reactor) buildRequestSubscriptionFiltersForReconnect(previous []nostr.Filter) []nostr.Filter {
	since := previous[0].Since
	if lastSeen := r.latestLastSeen(requestSubscriptionKinds()); lastSeen != nil {
		since = replayCursorWithOverlap(*lastSeen)
	}
	return r.buildRequestSubscriptionFilters(since)
}

func (r *Reactor) requestSubscriptionSince(_ context.Context) nostr.Timestamp {
	if lastSeen := r.latestLastSeen(requestSubscriptionKinds()); lastSeen != nil {
		return replayCursorWithOverlap(*lastSeen)
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
	filter := nostr.Filter{
		Kinds:   nostrKindsFromInts(canonicalReactorSubscriptionKinds()),
		Authors: r.requestSubscriptionAuthors(),
		Since:   since,
	}

	return []nostr.Filter{filter}
}

func requestSubscriptionKinds() []int {
	return canonicalReactorSubscriptionKinds()
}

func defaultRequestSubscriptionKinds() []int {
	return canonicalReactorSubscriptionKinds()
}

func canonicalReactorSubscriptionKinds() []int {
	return []int{nostrpool.KindNIP38Status}
}

func canonicalRuntimeReplayKinds() []int {
	return []int{
		nostrpool.KindCASControlState,
		nostrpool.KindNIP38Status,
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
func (r *Reactor) publishStatus(ctx context.Context, requestEvent *nostr.Event, step, message string) {
	tags := nostr.Tags{
		{"status", "processing"},
		{"step", step},
		{"category", "deployment"},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	if err := r.publishCanonicalStatus(ctx, requestEvent, tags, map[string]any{
		"status":  "processing",
		"step":    step,
		"message": message,
	}); err != nil {
		r.zapLog.Warn("control-plane status publish failed",
			zap.String("category", "deployment"),
			zap.String("step", step),
			zap.Error(err))
	}
}

// publishDeploymentResult publishes a ContextVM deployment result for retained direct handler paths.
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

// publishDomainResult publishes a ContextVM result event for a control-plane domain.
// Handlers assemble the domain-specific content and tags; the builder attaches the
// domain/schema envelope, derives a JSON-RPC error for terminal statuses, and logs
// internally on publish failure.
func (r *Reactor) publishDomainResult(ctx context.Context, requestEvent *nostr.Event, domain, schema, status, code, message string, content any, tags nostr.Tags) {
	tags = append(tags, nostr.Tag{"domain", domain}, nostr.Tag{"schema", schema})
	if code != "" {
		tags = append(tags, nostr.Tag{"result", code})
	}
	var rpcErr *JSONRPCError
	if status == "failed" || status == "rejected" {
		rpcErr = &JSONRPCError{Code: -32000, Message: message}
	}
	if err := r.publishContextVMResult(ctx, requestEvent, content, tags, rpcErr); err != nil {
		r.zapLog.Warn("control-plane domain result publish failed",
			zap.String("domain", domain),
			zap.String("status", status),
			zap.Error(err))
	}
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

func (r *Reactor) startMLWorkers(ctx context.Context) {
	bgCtx := context.WithoutCancel(ctx)
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case work, ok := <-r.mlWorkCh:
					if !ok {
						return
					}
					switch work.kind {
					case mlWorkRecipeRun:
						_ = r.mlRecipeExecutor.ProcessRecipeRun(bgCtx, work.runID)
					case mlWorkDeploymentIntent:
						_ = r.mlExecutor.ProcessDeploymentIntent(bgCtx, work.intentID)
					}
				}
			}
		}()
	}
}

func (r *Reactor) submitMLWork(work mlWork) bool {
	select {
	case r.mlWorkCh <- work:
		return true
	default:
		return false
	}
}
