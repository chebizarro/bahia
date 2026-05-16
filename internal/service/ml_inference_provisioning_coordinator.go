package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const defaultMLInferenceRecoveryPollInterval = 30 * time.Second

// MLDeploymentRunQueueRepository provides atomic queue primitives for durable ML deployment work.
type MLDeploymentRunQueueRepository interface {
	EnsureQueuedMLDeploymentRunForNextReadyIntent(ctx context.Context) (*domain.MLDeploymentRun, error)
	ClaimNextQueuedMLDeploymentRun(ctx context.Context) (*domain.MLDeploymentRun, error)
	RequeueStaleMLDeploymentRuns(ctx context.Context, olderThan time.Duration) (int, error)
}

// MLInferenceProvisioningResponder publishes optional command/result lifecycle hooks for Nostr-originated ML deploy requests.
type MLInferenceProvisioningResponder interface {
	PublishStatus(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, step, message string) error
	PublishResult(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, status, message string) error
	PublishError(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, step string, cause error) error
}

// MLInferenceProvisionerResolver resolves a runtime-specific endpoint provisioner.
type MLInferenceProvisionerResolver interface {
	Resolve(runtime domain.MLRuntimeKind) (MLInferenceProvisioner, error)
}

// StaticMLInferenceProvisionerResolver is a map-backed provisioner resolver useful for tests and simple wiring.
type StaticMLInferenceProvisionerResolver map[domain.MLRuntimeKind]MLInferenceProvisioner

func (r StaticMLInferenceProvisionerResolver) Resolve(runtime domain.MLRuntimeKind) (MLInferenceProvisioner, error) {
	p := r[runtime]
	if p == nil {
		return nil, fmt.Errorf("no ML inference provisioner registered for runtime %s", runtime)
	}
	return p, nil
}

// MLInferenceProvisioner deploys, observes, and tears down one ML endpoint runtime target.
type MLInferenceProvisioner interface {
	Provision(ctx context.Context, req MLInferenceProvisionRequest) (*MLInferenceProvisionResult, error)
	Observe(ctx context.Context, req MLInferenceProvisionRequest) (*MLInferenceBackendObservation, error)
	Deprovision(ctx context.Context, req MLInferenceProvisionRequest) error
}

// MLInferenceProvisionRequest is the coordinator-to-runtime deployment contract.
type MLInferenceProvisionRequest struct {
	Endpoint     *domain.MLInferenceEndpoint
	Intent       *domain.MLDeploymentIntent
	ModelVersion *domain.MLModelVersion
	Artifacts    []domain.MLArtifactRef
	Run          *domain.MLDeploymentRun
	RuntimeKind  domain.MLRuntimeKind
	Worker       *domain.Worker
	TargetName   string
}

// MLInferenceProvisionResult captures runtime deployment output.
type MLInferenceProvisionResult struct {
	RuntimeKind     domain.MLRuntimeKind
	EndpointRef     string
	WorkerPubkey    string
	WorkerName      string
	BackendEndpoint string
	VerifiedDigests map[string]string
	TargetName      string
	Metadata        map[string]any
}

// MLInferenceBackendObservation captures runtime health after provisioning.
type MLInferenceBackendObservation struct {
	RuntimeKind     domain.MLRuntimeKind
	BackendEndpoint string
	HealthStatus    domain.HealthStatus
	Source          string
	Metadata        map[string]any
}

// MLInferenceGatewayRouteManager syncs an optional external gateway/read-model route for an ML endpoint.
type MLInferenceGatewayRouteManager interface {
	UpsertEndpoint(ctx context.Context, gatewayRef string, spec MLInferenceGatewayRouteSpec) (*MLInferenceGatewayObservation, error)
}

// MLInferenceGatewayRouteSpec describes an endpoint gateway target.
type MLInferenceGatewayRouteSpec struct {
	Endpoint        *domain.MLInferenceEndpoint
	ModelVersion    *domain.MLModelVersion
	TargetURL       string
	RuntimeKind     domain.MLRuntimeKind
	GatewayMetadata map[string]any
}

// MLInferenceGatewayObservation is the observed gateway state after sync.
type MLInferenceGatewayObservation struct {
	Status            domain.GatewayRouteStatus
	TargetURL         string
	GatewayConfigHash string
	Metadata          map[string]any
}

// MLInferenceProvisioningConfig configures durable recovery and optional gateway defaults.
type MLInferenceProvisioningConfig struct {
	RecoveryPollInterval time.Duration
	StaleRunTimeout      time.Duration
	DefaultGatewayRef    string
}

// MLInferenceProvisioningCoordinator executes generic ML deployment runs.
type MLInferenceProvisioningCoordinator struct {
	registry     *MLRegistryService
	queue        MLDeploymentRunQueueRepository
	placement    *MLPlacementService
	provenance   *MLProvenanceService
	provisioners MLInferenceProvisionerResolver
	gateway      MLInferenceGatewayRouteManager
	responder    MLInferenceProvisioningResponder
	logger       *zap.Logger
	config       MLInferenceProvisioningConfig

	runGroup    singleflight.Group
	locksMu     sync.Mutex
	targetLocks map[string]*sync.Mutex
}

type MLInferenceProvisioningCoordinatorOption func(*MLInferenceProvisioningCoordinator)

func WithMLInferenceProvisioningResponder(responder MLInferenceProvisioningResponder) MLInferenceProvisioningCoordinatorOption {
	return func(c *MLInferenceProvisioningCoordinator) { c.responder = responder }
}

func WithMLInferenceProvisioningGateway(gateway MLInferenceGatewayRouteManager) MLInferenceProvisioningCoordinatorOption {
	return func(c *MLInferenceProvisioningCoordinator) { c.gateway = gateway }
}

func WithMLInferenceProvisioningConfig(cfg MLInferenceProvisioningConfig) MLInferenceProvisioningCoordinatorOption {
	return func(c *MLInferenceProvisioningCoordinator) {
		if cfg.RecoveryPollInterval > 0 {
			c.config.RecoveryPollInterval = cfg.RecoveryPollInterval
		}
		if cfg.StaleRunTimeout > 0 {
			c.config.StaleRunTimeout = cfg.StaleRunTimeout
		}
		if strings.TrimSpace(cfg.DefaultGatewayRef) != "" {
			c.config.DefaultGatewayRef = strings.TrimSpace(cfg.DefaultGatewayRef)
		}
	}
}

func NewMLInferenceProvisioningCoordinator(registry *MLRegistryService, placement *MLPlacementService, provenance *MLProvenanceService, provisioners MLInferenceProvisionerResolver, logger *zap.Logger, opts ...MLInferenceProvisioningCoordinatorOption) *MLInferenceProvisioningCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &MLInferenceProvisioningCoordinator{
		registry:     registry,
		placement:    placement,
		provenance:   provenance,
		provisioners: provisioners,
		logger:       logger.Named("ml-inference-provisioning-coordinator"),
		config: MLInferenceProvisioningConfig{
			RecoveryPollInterval: defaultMLInferenceRecoveryPollInterval,
			StaleRunTimeout:      15 * time.Minute,
		},
		targetLocks: make(map[string]*sync.Mutex),
	}
	if registry != nil && registry.repo != nil {
		if queue, ok := registry.repo.(MLDeploymentRunQueueRepository); ok {
			c.queue = queue
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *MLInferenceProvisioningCoordinator) Name() string {
	return "ml-inference-provisioning-recovery"
}

// Run performs explicit durable recovery for stored ML deployment work.
func (c *MLInferenceProvisioningCoordinator) Run(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	c.runRecoveryOnce(ctx)
	ticker := time.NewTicker(c.config.RecoveryPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.runRecoveryOnce(ctx)
		}
	}
}

func (c *MLInferenceProvisioningCoordinator) runRecoveryOnce(ctx context.Context) {
	if err := c.ProcessOnce(ctx); err != nil {
		c.logger.Warn("ML inference provisioning recovery scan failed", zap.Error(err))
	}
}

// ProcessOnce performs one stale-recovery, queue, claim, and process cycle. It is exposed for focused tests.
func (c *MLInferenceProvisioningCoordinator) ProcessOnce(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	if c.config.StaleRunTimeout > 0 {
		if n, err := c.queue.RequeueStaleMLDeploymentRuns(ctx, c.config.StaleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale ML deployment runs", zap.Int("count", n))
		}
	}
	if _, err := c.queue.EnsureQueuedMLDeploymentRunForNextReadyIntent(ctx); err != nil {
		return err
	}
	run, err := c.queue.ClaimNextQueuedMLDeploymentRun(ctx)
	if err != nil || run == nil {
		return err
	}
	return c.ProcessRun(ctx, run.ID)
}

// ProcessRun processes a stored run and serializes work by endpoint/environment target.
func (c *MLInferenceProvisioningCoordinator) ProcessRun(ctx context.Context, runID uuid.UUID) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("ML registry is not configured")
	}
	_, err, _ := c.runGroup.Do(runID.String(), func() (any, error) {
		run, err := c.registry.repo.GetDeploymentRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, fmt.Errorf("ML deployment run %s not found", runID)
		}
		intent, err := c.registry.GetDeploymentIntent(ctx, run.DeploymentIntentID)
		if err != nil {
			return nil, c.failRun(ctx, run, nil, fmt.Errorf("load ML deployment intent: %w", err), nil, MLInferenceProvisionRequest{})
		}
		if intent == nil {
			return nil, c.failRun(ctx, run, nil, fmt.Errorf("ML deployment intent %s not found", run.DeploymentIntentID), nil, MLInferenceProvisionRequest{})
		}
		return nil, c.withTargetLock(intent.EndpointID, intent.EnvironmentID, func() error {
			return c.processRunLocked(ctx, run, intent)
		})
	})
	return err
}

func (c *MLInferenceProvisioningCoordinator) processRunLocked(ctx context.Context, run *domain.MLDeploymentRun, intent *domain.MLDeploymentIntent) error {
	if run.Status == domain.RunStatusQueued {
		now := time.Now().UTC()
		run.Status = domain.RunStatusRunning
		run.StartedAt = &now
		if err := c.registry.CreateOrUpdateDeploymentRun(ctx, run); err != nil {
			return err
		}
	}
	endpoint, version, artifacts, err := c.loadDeploymentInputs(ctx, intent)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, MLInferenceProvisionRequest{})
	}
	stillDesired, err := c.isStillDesired(ctx, intent)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, MLInferenceProvisionRequest{})
	}
	if !stillDesired {
		return c.cancelRun(ctx, run, intent)
	}
	runtimeKind := effectiveMLRuntime(intent, version)
	if runtimeKind == "" || !runtimeKind.IsValid() {
		return c.failRun(ctx, run, intent, fmt.Errorf("unsupported or missing ML runtime %q", runtimeKind), nil, MLInferenceProvisionRequest{})
	}
	if c.placement == nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("ML placement service is not configured"), nil, MLInferenceProvisionRequest{})
	}
	deployArtifacts, err := deploymentArtifactsForRuntime(runtimeKind, MLInferenceProvisionRequest{Endpoint: endpoint, Intent: intent, ModelVersion: version, Artifacts: artifacts})
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, MLInferenceProvisionRequest{})
	}
	placementReq := buildMLPlacementRequest(endpoint, intent, version, deployArtifacts, runtimeKind)
	candidate, err := c.placement.SelectCandidate(ctx, placementReq)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, MLInferenceProvisionRequest{})
	}
	if c.provisioners == nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("ML inference provisioner resolver is not configured"), nil, MLInferenceProvisionRequest{})
	}
	provisioner, err := c.provisioners.Resolve(candidate.RuntimeKind)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, MLInferenceProvisionRequest{})
	}
	targetName := mlTargetName(endpoint, run)
	req := MLInferenceProvisionRequest{Endpoint: endpoint, Intent: intent, ModelVersion: version, Artifacts: deployArtifacts, Run: run, RuntimeKind: candidate.RuntimeKind, Worker: candidate.Worker, TargetName: targetName}

	mergeMLRunMetadata(run, map[string]any{"placement_reason": candidate.Reason, "placement_score": candidate.Score, "target_name": targetName})
	run.RuntimeKind = candidate.RuntimeKind
	if candidate.Worker != nil {
		run.WorkerPubkey = candidate.Worker.PubKey
		run.WorkerName = candidate.Worker.Name
		if candidate.Worker.RuntimeTarget != nil {
			run.EndpointRef = candidate.Worker.RuntimeTarget.EndpointRef
			mergeMLRunMetadata(run, map[string]any{"runtime_target": candidate.Worker.RuntimeTarget})
		}
	}
	if err := c.registry.CreateOrUpdateDeploymentRun(ctx, run); err != nil {
		return err
	}
	c.publishStatus(ctx, intent, run, "placing_runtime", "selected ML inference placement")

	c.publishStatus(ctx, intent, run, "provisioning_runtime", "provisioning ML inference endpoint")
	var result *MLInferenceProvisionResult
	err = c.withRunHeartbeat(ctx, run.ID, func() error {
		var provisionErr error
		result, provisionErr = provisioner.Provision(ctx, req)
		return provisionErr
	})
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	if result == nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("ML inference provisioner returned no result"), provisioner, req)
	}
	run.RuntimeKind = firstMLRuntime(result.RuntimeKind, candidate.RuntimeKind)
	run.EndpointRef = firstNonEmptyString(result.EndpointRef, run.EndpointRef)
	run.WorkerPubkey = firstNonEmptyString(result.WorkerPubkey, run.WorkerPubkey)
	run.WorkerName = firstNonEmptyString(result.WorkerName, run.WorkerName)
	run.BackendEndpoint = result.BackendEndpoint
	run.VerifiedDigests = copyMLStringMap(result.VerifiedDigests)
	mergeMLRunMetadata(run, result.Metadata)
	mergeMLRunMetadata(run, map[string]any{"target_name": firstNonEmptyString(result.TargetName, targetName)})
	if err := c.registry.CreateOrUpdateDeploymentRun(ctx, run); err != nil {
		_ = provisioner.Deprovision(ctx, req)
		return err
	}

	state, err := c.registry.GetInferenceState(ctx, intent.EndpointID, intent.EnvironmentID)
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		_ = provisioner.Deprovision(ctx, req)
		return c.cancelRun(ctx, run, intent)
	}

	c.publishStatus(ctx, intent, run, "evaluating_gate", "evaluating ML inference promotion gate")
	gateResult, err := c.evaluateGate(ctx, run, deployArtifacts)
	mergeMLRunMetadata(run, map[string]any{"promotion_gate": gateResult})
	_ = c.registry.CreateOrUpdateDeploymentRun(ctx, run)
	if err != nil || gateResult == nil || !gateResult.Passed {
		if err == nil {
			err = fmt.Errorf("ML inference promotion gate failed")
		}
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}

	c.publishStatus(ctx, intent, run, "observing_endpoint", "observing ML inference endpoint")
	var backendObs *MLInferenceBackendObservation
	observeErr := c.withRunHeartbeat(ctx, run.ID, func() error {
		var err error
		backendObs, err = provisioner.Observe(ctx, req)
		return err
	})
	if observeErr != nil {
		backendObs = &MLInferenceBackendObservation{RuntimeKind: run.RuntimeKind, BackendEndpoint: result.BackendEndpoint, HealthStatus: domain.HealthStatusUnhealthy, Source: "coordinator", Metadata: map[string]any{"observe_error": observeErr.Error()}}
	}
	if backendObs == nil {
		backendObs = &MLInferenceBackendObservation{RuntimeKind: run.RuntimeKind, BackendEndpoint: result.BackendEndpoint, HealthStatus: domain.HealthStatusUnknown, Source: "coordinator"}
	}
	if backendObs.HealthStatus == "" {
		backendObs.HealthStatus = domain.HealthStatusUnknown
	}
	if observeErr != nil {
		return c.failRun(ctx, run, intent, observeErr, provisioner, req)
	}
	if backendObs.HealthStatus == domain.HealthStatusUnhealthy {
		return c.failRun(ctx, run, intent, fmt.Errorf("ML inference endpoint unhealthy after provisioning"), provisioner, req)
	}

	gatewayObs := defaultMLInferenceGatewayObservation(endpoint, result.BackendEndpoint)
	if gatewayRef := c.gatewayRef(endpoint); gatewayRef != "" && c.gateway != nil {
		c.publishStatus(ctx, intent, run, "syncing_gateway", "syncing ML inference gateway endpoint")
		err = c.withRunHeartbeat(ctx, run.ID, func() error {
			var gatewayErr error
			gatewayObs, gatewayErr = c.gateway.UpsertEndpoint(ctx, gatewayRef, MLInferenceGatewayRouteSpec{Endpoint: endpoint, ModelVersion: version, TargetURL: result.BackendEndpoint, RuntimeKind: run.RuntimeKind, GatewayMetadata: endpoint.Gateway})
			return gatewayErr
		})
		if err != nil {
			return c.failRun(ctx, run, intent, err, provisioner, req)
		}
		if gatewayObs == nil {
			gatewayObs = &MLInferenceGatewayObservation{Status: domain.GatewayRouteStatusUnknown, TargetURL: result.BackendEndpoint}
		}
	}
	obs := &domain.MLInferenceObservation{EndpointID: endpoint.ID, EnvironmentID: intent.EnvironmentID, ObservedModelVersionID: &intent.ModelVersionID, ObservedRunID: &run.ID, RuntimeKind: firstMLRuntime(backendObs.RuntimeKind, run.RuntimeKind), BackendEndpoint: firstNonEmptyString(backendObs.BackendEndpoint, result.BackendEndpoint), BackendHealth: backendObs.HealthStatus, GatewayStatus: gatewayObs.Status, GatewayTarget: firstNonEmptyString(gatewayObs.TargetURL, result.BackendEndpoint), GatewayConfigHash: gatewayObs.GatewayConfigHash, Source: firstNonEmptyString(backendObs.Source, "coordinator"), Metadata: map[string]any{"backend": backendObs.Metadata, "gateway": gatewayObs.Metadata}}
	if err := c.registry.RecordObservation(ctx, obs); err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		_ = provisioner.Deprovision(ctx, req)
		return err
	}
	c.publishResult(ctx, intent, run, "succeeded", "ML inference deployment completed")
	return nil
}

type mlInferenceGateResult struct {
	Passed            bool   `json:"passed"`
	ProvenanceChecked bool   `json:"provenance_checked"`
	Message           string `json:"message,omitempty"`
}

func deploymentArtifactsForRuntime(runtimeKind domain.MLRuntimeKind, req MLInferenceProvisionRequest) ([]domain.MLArtifactRef, error) {
	if runtimeKind != domain.MLRuntimeKindRKNNServer {
		return req.Artifacts, nil
	}
	artifact, err := selectRKNNArtifact(req)
	if err != nil {
		return nil, err
	}
	return []domain.MLArtifactRef{artifact}, nil
}

func (c *MLInferenceProvisioningCoordinator) evaluateGate(ctx context.Context, run *domain.MLDeploymentRun, artifacts []domain.MLArtifactRef) (*mlInferenceGateResult, error) {
	if c.provenance == nil {
		return &mlInferenceGateResult{Passed: false, Message: "ML provenance service is not configured"}, fmt.Errorf("ML provenance service is not configured")
	}
	if err := c.provenance.VerifyWorkerReportedDigests(ctx, run, artifacts); err != nil {
		return &mlInferenceGateResult{Passed: false, ProvenanceChecked: true, Message: err.Error()}, err
	}
	return &mlInferenceGateResult{Passed: true, ProvenanceChecked: true, Message: "worker digests match canonical artifact refs"}, nil
}

func (c *MLInferenceProvisioningCoordinator) loadDeploymentInputs(ctx context.Context, intent *domain.MLDeploymentIntent) (*domain.MLInferenceEndpoint, *domain.MLModelVersion, []domain.MLArtifactRef, error) {
	endpoint, err := c.registry.GetInferenceEndpoint(ctx, intent.EndpointID)
	if err != nil {
		return nil, nil, nil, err
	}
	if endpoint == nil {
		return nil, nil, nil, fmt.Errorf("ML inference endpoint %s not found", intent.EndpointID)
	}
	version, err := c.registry.GetModelVersion(ctx, intent.ModelVersionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if version == nil {
		return nil, nil, nil, fmt.Errorf("ML model version %s not found", intent.ModelVersionID)
	}
	artifacts, err := c.registry.repo.ListArtifactRefsByModelVersion(ctx, version.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return endpoint, version, artifacts, nil
}

func (c *MLInferenceProvisioningCoordinator) isStillDesired(ctx context.Context, intent *domain.MLDeploymentIntent) (bool, error) {
	if intent == nil {
		return false, nil
	}
	state, err := c.registry.GetInferenceState(ctx, intent.EndpointID, intent.EnvironmentID)
	if err != nil {
		return false, err
	}
	return state != nil && state.DesiredIntentID != nil && *state.DesiredIntentID == intent.ID, nil
}

func (c *MLInferenceProvisioningCoordinator) withRunHeartbeat(ctx context.Context, runID uuid.UUID, fn func() error) error {
	if c == nil || c.registry == nil || c.config.StaleRunTimeout <= 0 {
		return fn()
	}
	interval := c.config.StaleRunTimeout / 3
	if interval <= 0 {
		interval = time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				c.touchRun(heartbeatCtx, runID)
			}
		}
	}()
	err := fn()
	cancel()
	<-done
	return err
}

func (c *MLInferenceProvisioningCoordinator) touchRun(ctx context.Context, runID uuid.UUID) {
	run, err := c.registry.repo.GetDeploymentRun(ctx, runID)
	if err != nil || run == nil || run.Status != domain.RunStatusRunning {
		return
	}
	if err := c.registry.CreateOrUpdateDeploymentRun(ctx, run); err != nil {
		c.logger.Warn("failed to heartbeat ML deployment run", zap.String("run_id", runID.String()), zap.Error(err))
	}
}

func (c *MLInferenceProvisioningCoordinator) failRun(ctx context.Context, run *domain.MLDeploymentRun, intent *domain.MLDeploymentIntent, cause error, provisioner MLInferenceProvisioner, req MLInferenceProvisionRequest) error {
	if cause == nil {
		cause = fmt.Errorf("ML inference provisioning failed")
	}
	if provisioner != nil {
		_ = provisioner.Deprovision(ctx, req)
	}
	mergeMLRunMetadata(run, map[string]any{"error": cause.Error()})
	_ = c.registry.CreateOrUpdateDeploymentRun(ctx, run)
	if intent != nil {
		c.publishError(ctx, intent, run, "failed", cause)
		if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, nil); err != nil {
			return err
		}
	} else {
		now := time.Now().UTC()
		run.Status = domain.RunStatusFailed
		run.FinishedAt = &now
		_ = c.registry.CreateOrUpdateDeploymentRun(ctx, run)
	}
	return cause
}

func (c *MLInferenceProvisioningCoordinator) cancelRun(ctx context.Context, run *domain.MLDeploymentRun, intent *domain.MLDeploymentIntent) error {
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusCancelled, nil); err != nil {
		return err
	}
	c.publishResult(ctx, intent, run, "cancelled", "ML inference deployment superseded before promotion")
	return nil
}

func (c *MLInferenceProvisioningCoordinator) withTargetLock(endpointID, envID uuid.UUID, fn func() error) error {
	key := mlInferenceTargetKey(endpointID, envID)
	c.locksMu.Lock()
	lock := c.targetLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		c.targetLocks[key] = lock
	}
	c.locksMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func mlInferenceTargetKey(endpointID, envID uuid.UUID) string {
	return endpointID.String() + ":" + envID.String()
}

func effectiveMLRuntime(intent *domain.MLDeploymentIntent, version *domain.MLModelVersion) domain.MLRuntimeKind {
	if intent != nil && intent.RuntimePreference != "" {
		return intent.RuntimePreference
	}
	if version != nil && len(version.RuntimeRequirements.PreferredRuntimes) > 0 {
		return version.RuntimeRequirements.PreferredRuntimes[0]
	}
	return ""
}

func buildMLPlacementRequest(endpoint *domain.MLInferenceEndpoint, intent *domain.MLDeploymentIntent, version *domain.MLModelVersion, artifacts []domain.MLArtifactRef, runtimeKind domain.MLRuntimeKind) MLPlacementRequest {
	req := MLPlacementRequest{RuntimeKind: runtimeKind}
	if endpoint != nil && len(endpoint.TaskKinds) > 0 {
		req.TaskKind = endpoint.TaskKinds[0]
	}
	seenFormats := map[domain.MLArtifactFormat]struct{}{}
	for _, artifact := range artifacts {
		if artifact.Format != "" {
			seenFormats[artifact.Format] = struct{}{}
		}
		if req.CachedArtifact == "" && artifact.SHA256 != "" {
			req.CachedArtifact = artifact.SHA256
		}
	}
	for format := range seenFormats {
		req.ArtifactFormats = append(req.ArtifactFormats, format)
	}
	sort.Slice(req.ArtifactFormats, func(i, j int) bool { return req.ArtifactFormats[i] < req.ArtifactFormats[j] })
	if version != nil {
		req.MinVRAMGB = version.RuntimeRequirements.MinVRAMGB
		req.MinSystemMemoryGB = version.RuntimeRequirements.MinSystemMemoryGB
		req.Toolchains = append(req.Toolchains, version.RuntimeRequirements.Toolchains...)
		if len(version.RuntimeRequirements.Accelerators) > 0 {
			req.Accelerator = version.RuntimeRequirements.Accelerators[0]
		}
	}
	applyMLPlacementPolicy(&req, endpointMap(endpoint, "placement_policy"))
	applyMLPlacementPolicy(&req, intentMap(intent, "placement"))
	return req
}

func endpointMap(endpoint *domain.MLInferenceEndpoint, field string) map[string]any {
	if endpoint == nil || field != "placement_policy" {
		return nil
	}
	return endpoint.PlacementPolicy
}

func intentMap(intent *domain.MLDeploymentIntent, key string) map[string]any {
	if intent == nil || intent.Metadata == nil {
		return nil
	}
	raw, ok := intent.Metadata[key]
	if !ok {
		return nil
	}
	m, _ := raw.(map[string]any)
	return m
}

func applyMLPlacementPolicy(req *MLPlacementRequest, values map[string]any) {
	if req == nil || values == nil {
		return
	}
	if v, ok := stringValue(values["accelerator"]); ok {
		req.Accelerator = v
	}
	if v, ok := intValue(values["min_vram_gb"]); ok {
		req.MinVRAMGB = v
	}
	if v, ok := intValue(values["min_system_memory_gb"]); ok {
		req.MinSystemMemoryGB = v
	}
	if v, ok := intValue(values["max_price"]); ok {
		req.MaxPrice = v
	}
	if selector, ok := values["worker_selector"].(map[string]any); ok {
		req.WorkerSelector = selector
	}
	if tools, ok := stringSliceValue(values["toolchains"]); ok {
		req.Toolchains = tools
	}
}

func defaultMLInferenceGatewayObservation(endpoint *domain.MLInferenceEndpoint, backendEndpoint string) *MLInferenceGatewayObservation {
	obs := &MLInferenceGatewayObservation{Status: domain.GatewayRouteStatusUnknown, TargetURL: backendEndpoint}
	if endpoint != nil && strings.EqualFold(strings.TrimSpace(endpoint.Protocol), "raw_http") {
		obs.Status = domain.GatewayRouteStatusSynced
		obs.Metadata = map[string]any{"raw_http": true, "gateway_bypassed": true}
	}
	return obs
}

func (c *MLInferenceProvisioningCoordinator) gatewayRef(endpoint *domain.MLInferenceEndpoint) string {
	if endpoint != nil && endpoint.Gateway != nil {
		for _, key := range []string{"gateway_ref", "ref"} {
			if ref, ok := stringValue(endpoint.Gateway[key]); ok && ref != "" {
				return ref
			}
		}
	}
	return strings.TrimSpace(c.config.DefaultGatewayRef)
}

func (c *MLInferenceProvisioningCoordinator) publishStatus(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, step, message string) {
	if c.responder == nil || intent == nil {
		return
	}
	if err := c.responder.PublishStatus(ctx, intent, run, step, message); err != nil {
		c.logger.Warn("publish ML inference provisioning status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *MLInferenceProvisioningCoordinator) publishResult(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, status, message string) {
	if c.responder == nil || intent == nil {
		return
	}
	if err := c.responder.PublishResult(ctx, intent, run, status, message); err != nil {
		c.logger.Warn("publish ML inference provisioning result failed", zap.String("status", status), zap.Error(err))
	}
}

func (c *MLInferenceProvisioningCoordinator) publishError(ctx context.Context, intent *domain.MLDeploymentIntent, run *domain.MLDeploymentRun, step string, cause error) {
	if c.responder == nil || intent == nil || cause == nil {
		return
	}
	if err := c.responder.PublishError(ctx, intent, run, step, cause); err != nil {
		c.logger.Warn("publish ML inference provisioning error failed", zap.String("step", step), zap.Error(err))
	}
}

func mergeMLRunMetadata(run *domain.MLDeploymentRun, values map[string]any) {
	if run == nil || values == nil {
		return
	}
	if run.Metadata == nil {
		run.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			run.Metadata[k] = v
		}
	}
}

func mlTargetName(endpoint *domain.MLInferenceEndpoint, run *domain.MLDeploymentRun) string {
	name := "endpoint"
	if endpoint != nil && strings.TrimSpace(endpoint.Name) != "" {
		name = endpoint.Name
	}
	id := "run"
	if run != nil {
		id = run.ID.String()
	}
	if len(id) > 8 {
		id = id[:8]
	}
	name = strings.Trim(strings.ToLower(name), "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")
	return "ml-" + name + "-" + id
}

func firstMLRuntime(values ...domain.MLRuntimeKind) domain.MLRuntimeKind {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func stringValue(raw any) (string, bool) {
	v, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func intValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func stringSliceValue(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...), true
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := stringValue(item); ok && text != "" {
				out = append(out, text)
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func copyMLStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
