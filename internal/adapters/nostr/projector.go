package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/version"
	"go.uber.org/zap"
)

// ProjectionSource is the authoritative state reader used by the projector.
// service.RegistryService satisfies this interface.
type ProjectionSource interface {
	ListServices(ctx context.Context) ([]domain.Service, error)
	GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error)
	ListEnvironments(ctx context.Context) ([]domain.Environment, error)
	GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
	ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error)
	GetEnvironmentServiceState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error)
	GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
	GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error)
	ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error)
	GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error)
	GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error)
	ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error)
	GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error)
	ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error)
}

type PolicyProjectionSource interface {
	ListPolicies(ctx context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error)
	GetPolicy(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error)
}

// LLMProjectionSource is the authoritative LLM state reader used by the projector.
// service.LLMRegistryService satisfies this interface.
type LLMProjectionSource interface {
	ListLLMRoutes(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error)
	GetLLMRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error)
	ListAllLLMRouteStates(ctx context.Context) ([]domain.LLMRouteState, error)
	GetLLMRouteState(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error)
	GetLLMDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error)
	GetLLMDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error)
}

type MLProjectionSource interface {
	ListModels(ctx context.Context, task domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error)
	GetModel(ctx context.Context, id uuid.UUID) (*domain.MLModel, error)
	GetModelBySlug(ctx context.Context, slug string) (*domain.MLModel, error)
	ListModelVersions(ctx context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error)
	GetModelVersion(ctx context.Context, id uuid.UUID) (*domain.MLModelVersion, error)
	GetArtifactRef(ctx context.Context, id uuid.UUID) (*domain.MLArtifactRef, error)
	ListArtifactRefsByModelVersion(ctx context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error)
	ListProvenanceEdgesByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error)
	GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error)
	ListInferenceEndpoints(ctx context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error)
	GetMLDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error)
	GetMLDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error)
	GetInferenceState(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error)
	ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error)
}

type WorkerProjectionSource interface {
	List(ctx context.Context, status string, limit int) ([]domain.Worker, error)
}

type WorkerReadModelProjectionSource interface {
	ListAssignmentStates(ctx context.Context) ([]domain.WorkerAssignmentState, error)
	GetAssignmentState(ctx context.Context, workerPubKey string) (*domain.WorkerAssignmentState, error)
	ListDrainStatuses(ctx context.Context) ([]domain.WorkerDrainStatus, error)
	GetDrainStatus(ctx context.Context, workerPubKey string) (*domain.WorkerDrainStatus, error)
}

type latestObservationSource interface {
	GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
}

type BackupProjectionSource interface {
	ListRecipes(ctx context.Context, limit, offset int) ([]domain.BackupRecipe, error)
	GetRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error)
	ListPolicies(ctx context.Context, limit, offset int) ([]domain.BackupPolicy, error)
	GetPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error)
	ListRepositories(ctx context.Context, limit, offset int) ([]domain.BackupRepository, error)
	GetRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error)
	ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error)
	GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error)
	ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error)
	GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error)
	ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error)
	GetBackupRetentionRun(ctx context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error)
	GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error)
}

// DNSProjectionSource is the authoritative DNS endpoint read model source used by the projector.
type DNSProjectionSource interface {
	ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
}

// DNSZoneProjectionSource is the authoritative DNS zone read model source used by the projector.
type DNSZoneProjectionSource interface {
	ListDNSZones() []domain.DNSZone
}

// DNSBackendProjectionSource is the authoritative DNS backend read model source used by the projector.
type DNSBackendProjectionSource interface {
	ListDNSBackendStates(ctx context.Context) []domain.DNSBackendState
}

// DNSPolicyProjectionSource is the authoritative DNS policy read model source used by the projector.
type DNSPolicyProjectionSource interface {
	ListEnabledDNSPolicies(ctx context.Context) ([]domain.DNSPolicy, error)
}

type dnsPublishedEndpoint struct {
	FQDN string
}

type dnsPublishedZone struct {
	Name       string
	BackendRef string
	Visibility string
}

type dnsPublishedBackend struct {
	Ref    string
	Type   string
	Health string
}

type dnsPublishedPolicy struct {
	ID      string
	Name    string
	ZoneID  string
	Enabled bool
}

const (
	eventDNSZoneSynced           events.EventType = "dns.zone_synced"
	eventDNSRecordChanged        events.EventType = "dns.record_changed"
	eventDNSDriftDetected        events.EventType = "dns.drift_detected"
	eventDNSEndpointRegistered   events.EventType = "dns.endpoint_registered"
	eventDNSEndpointDeregistered events.EventType = "dns.endpoint_deregistered"
)

// ProjectionPublisher publishes signed Nostr events to relay-visible storage.
type ProjectionPublisher interface {
	Publish(ctx context.Context, ev gonostr.Event) (int, error)
}

// Projector republishes Bahia's authoritative DB state into canonical Nostr
// read models and append-only audit events. It is rebuildable: a startup and
// periodic snapshot can repair a cold or wiped sidecar store.
type Projector struct {
	source                ProjectionSource
	projectorSource       ProjectorSource
	llmSource             LLMProjectionSource
	mlSource              MLProjectionSource
	workerSource          WorkerProjectionSource
	workerReadModelSource WorkerReadModelProjectionSource
	policySource          PolicyProjectionSource
	backupSource          BackupProjectionSource
	dnsSource             DNSProjectionSource
	dnsZoneSource         DNSZoneProjectionSource
	dnsBackendSource      DNSBackendProjectionSource
	dnsPolicySource       DNSPolicyProjectionSource
	publisher             ProjectionPublisher
	eventRepo             repository.NostrEventRepository
	privateKey            string
	enabled               bool
	repairInterval        time.Duration
	backupStaleTimeout    time.Duration
	logger                *zap.Logger
	systemConfig          *config.Config
	mcpTransport          bool
	dnsPublishMu          sync.Mutex
	dnsPublished          map[string]dnsPublishedEndpoint
	dnsPublishedZones     map[string]dnsPublishedZone
	dnsPublishedBackends  map[string]dnsPublishedBackend
	dnsPublishedPolicies  map[string]dnsPublishedPolicy
	dnsCacheHydrated      bool
}

// ProjectorOption configures a projector.
type ProjectorOption func(*Projector)

// WithProjectorRepairInterval overrides the periodic snapshot repair interval.
// Use <=0 in tests to disable periodic repair after the startup snapshot.
func WithProjectorRepairInterval(interval time.Duration) ProjectorOption {
	return func(p *Projector) { p.repairInterval = interval }
}

// WithBackupProjectionStaleTimeout overrides the running-run staleness threshold used by backup fleet posture projection.
func WithBackupProjectionStaleTimeout(timeout time.Duration) ProjectorOption {
	return func(p *Projector) {
		if timeout > 0 {
			p.backupStaleTimeout = timeout
		}
	}
}

func WithProjectorSource(source ProjectorSource) ProjectorOption {
	return func(p *Projector) { p.projectorSource = source }
}

func WithLLMProjectionSource(source LLMProjectionSource) ProjectorOption {
	return func(p *Projector) { p.llmSource = source }
}

func WithMLProjectionSource(source MLProjectionSource) ProjectorOption {
	return func(p *Projector) { p.mlSource = source }
}

func WithWorkerProjectionSource(source WorkerProjectionSource) ProjectorOption {
	return func(p *Projector) { p.workerSource = source }
}

func WithWorkerReadModelProjectionSource(source WorkerReadModelProjectionSource) ProjectorOption {
	return func(p *Projector) { p.workerReadModelSource = source }
}

func WithPolicyProjectionSource(source PolicyProjectionSource) ProjectorOption {
	return func(p *Projector) { p.policySource = source }
}

func WithBackupProjectionSource(source BackupProjectionSource) ProjectorOption {
	return func(p *Projector) { p.backupSource = source }
}

func WithDNSProjectionSource(source DNSProjectionSource) ProjectorOption {
	return func(p *Projector) { p.dnsSource = source }
}

func WithDNSZoneProjectionSource(source DNSZoneProjectionSource) ProjectorOption {
	return func(p *Projector) { p.dnsZoneSource = source }
}

func WithDNSBackendProjectionSource(source DNSBackendProjectionSource) ProjectorOption {
	return func(p *Projector) { p.dnsBackendSource = source }
}

func WithDNSPolicyProjectionSource(source DNSPolicyProjectionSource) ProjectorOption {
	return func(p *Projector) { p.dnsPolicySource = source }
}

func WithSystemDiscoveryConfig(cfg *config.Config, mcpTransportEnabled bool) ProjectorOption {
	return func(p *Projector) {
		p.systemConfig = cfg
		p.mcpTransport = mcpTransportEnabled
	}
}

// NewProjector creates a canonical Nostr read-model projector.
func NewProjector(cfg config.NostrConfig, source ProjectionSource, publisher ProjectionPublisher, eventRepo repository.NostrEventRepository, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Projector{
		source:             source,
		publisher:          publisher,
		eventRepo:          eventRepo,
		privateKey:         cfg.PrivateKey,
		enabled:            cfg.PublishEnabled && cfg.PrivateKey != "" && source != nil && publisher != nil,
		repairInterval:     10 * time.Minute,
		backupStaleTimeout: 15 * time.Minute,
		logger:             logger.Named("nostr-projector"),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.enabled = cfg.PublishEnabled && cfg.PrivateKey != "" && publisher != nil && (source != nil || p.projectorSource != nil)
	return p
}

// Enabled reports whether the projector has enough config to publish.
func (p *Projector) Enabled() bool { return p != nil && p.enabled }

// Name implements app.BackgroundRunner.
func (p *Projector) Name() string { return "nostr-projector" }

// SetupSubscriptions registers projection handlers on the in-process event bus.
func (p *Projector) SetupSubscriptions(pub events.Publisher) {
	if !p.Enabled() {
		p.logger.Info("nostr projector disabled")
		return
	}
	for _, eventType := range []events.EventType{
		events.EventServiceCreated,
		events.EventServiceUpdated,
		events.EventServiceDeleted,
		events.EventEnvironmentCreated,
		events.EventEnvironmentUpdated,
		events.EventEnvironmentDeleted,
		events.EventDeploymentIntentCreated,
		events.EventDeploymentIntentApproved,
		events.EventDeploymentIntentRejected,
		events.EventDeploymentRunCreated,
		events.EventDeploymentRunStatusChanged,
		events.EventDeploymentRunCompleted,
		events.EventRuntimeObservation,
		events.EventEnvironmentServiceStateChanged,
		events.EventDriftDetected,
		events.EventReconcileCompleted,
		events.EventAdoptionImported,
		events.EventRuntimeDeploy,
		events.EventRuntimeRestart,
		events.EventRuntimeStop,
		events.EventLLMRouteCreated,
		events.EventLLMRouteUpdated,
		events.EventLLMReleaseRegistered,
		events.EventLLMDeploymentIntentCreated,
		events.EventLLMDeploymentIntentApproved,
		events.EventLLMDeploymentIntentRejected,
		events.EventLLMDeploymentRunCreated,
		events.EventLLMDeploymentRunStatusChanged,
		events.EventLLMDeploymentRunCompleted,
		events.EventLLMRouteObservation,
		events.EventLLMRouteStateChanged,
		events.EventLLMRouteDriftDetected,
		events.EventLLMGatewayRouteSynced,
		service.EventMLModelChanged,
		service.EventMLVersionChanged,
		service.EventMLEndpointChanged,
		service.EventMLIntentChanged,
		service.EventMLRunChanged,
		service.EventMLObservation,
		service.EventMLStateChanged,
		service.EventMLArtifactChanged,
		service.EventMLProvenanceChanged,
		service.EventMLProvenanceDefected,
		service.EventBackupRecipeChanged,
		service.EventBackupPolicyChanged,
		service.EventBackupRepositoryChanged,
		service.EventBackupRunChanged,
		service.EventBackupRestoreChanged,
		service.EventBackupVerificationChanged,
		service.EventBackupRetentionChanged,
		eventDNSZoneSynced,
		eventDNSRecordChanged,
		eventDNSDriftDetected,
		eventDNSEndpointRegistered,
		eventDNSEndpointDeregistered,
	} {
		et := eventType
		pub.Subscribe(et, func(ctx context.Context, e events.Event) {
			p.handleEvent(ctx, e)
		})
	}
}

// Run performs startup snapshot repair and then periodically republishes
// snapshots until the context is cancelled.
func (p *Projector) Run(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	if err := p.RepublishSnapshot(ctx); err != nil {
		p.logger.Warn("startup Nostr projection snapshot failed", zap.Error(err))
	}
	if p.repairInterval <= 0 {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(p.repairInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.RepublishSnapshot(ctx); err != nil {
				p.logger.Warn("periodic Nostr projection repair failed", zap.Error(err))
			}
		}
	}
}

// RepublishSnapshot republishes all replaceable read models from canonical
// state. It is safe to run repeatedly; latest replaceable events win by d-tag.
func (p *Projector) RepublishSnapshot(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	if err := p.publishConfiguredDMRelayListsFromSystemConfig(ctx); err != nil {
		p.logger.Warn("publish DM relay-list projection failed", zap.Error(err))
		return fmt.Errorf("publish DM relay-list projection: %w", err)
	}
	if err := p.publishSystemDiscovery(ctx); err != nil {
		p.logger.Warn("publish system discovery projection failed", zap.Error(err))
		return fmt.Errorf("publish system discovery projection: %w", err)
	}

	snapshotSource := p.snapshotSource()
	services, err := snapshotSource.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for i := range services {
		if err := p.publishServiceRegistry(ctx, &services[i], false); err != nil {
			p.logger.Warn("publish service registry projection failed", zap.String("service_id", services[i].ID.String()), zap.Error(err))
		}
	}

	envs, err := snapshotSource.ListEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	for i := range envs {
		if err := p.publishEnvironmentRegistry(ctx, &envs[i], false); err != nil {
			p.logger.Warn("publish environment registry projection failed", zap.String("environment_id", envs[i].ID.String()), zap.Error(err))
		}
	}

	states, err := snapshotSource.ListStates(ctx)
	if err != nil {
		return fmt.Errorf("list states: %w", err)
	}
	for i := range states {
		if err := p.publishState(ctx, &states[i]); err != nil {
			p.logger.Warn("publish service state projection failed",
				zap.String("service_id", states[i].ServiceID.String()),
				zap.String("environment_id", states[i].EnvironmentID.String()),
				zap.Error(err),
			)
		}
	}
	buildsPublished, artifactsPublished, intentsPublished, runsPublished := p.publishPublicRouteSnapshotsFromSource(ctx, snapshotSource, services, envs)
	policiesPublished := p.publishPolicySnapshots(ctx)
	llmRoutes := 0
	llmStates := 0
	if p.llmSource != nil {
		const pageSize = 500
		for offset := 0; ; offset += pageSize {
			routes, err := p.llmSource.ListLLMRoutes(ctx, pageSize, offset)
			if err != nil {
				return fmt.Errorf("list LLM routes: %w", err)
			}
			llmRoutes += len(routes)
			for i := range routes {
				if err := p.publishLLMRouteRegistry(ctx, &routes[i], false); err != nil {
					p.logger.Warn("publish LLM route registry projection failed", zap.String("route_id", routes[i].ID.String()), zap.Error(err))
				}
			}
			if len(routes) < pageSize {
				break
			}
		}
		states, err := p.llmSource.ListAllLLMRouteStates(ctx)
		if err != nil {
			return fmt.Errorf("list LLM route states: %w", err)
		}
		llmStates = len(states)
		for i := range states {
			if err := p.publishLLMRouteState(ctx, &states[i]); err != nil {
				p.logger.Warn("publish LLM route state projection failed", zap.String("route_id", states[i].RouteID.String()), zap.String("environment_id", states[i].EnvironmentID.String()), zap.Error(err))
			}
		}
	}
	mlModels, mlVersions, mlEndpoints, mlStates, mlProvenance, mlCapabilities := p.publishMLSnapshots(ctx)
	workerAssignments, workerDrains := p.publishWorkerReadModelSnapshots(ctx)
	backupRecipes, backupPolicies, backupRepositories, backupRuns, backupRestores, backupVerifications, backupRetentions, backupPostures := p.publishBackupSnapshots(ctx)
	dnsEndpoints, dnsTombstones := 0, 0
	if p.dnsSource != nil {
		published, tombstones, err := p.publishDNSEndpointSnapshot(ctx)
		if err != nil {
			p.logger.Warn("publish DNS endpoint projection failed", zap.Error(err))
		} else {
			dnsEndpoints = published
			dnsTombstones = tombstones
		}
	}
	dnsZones, dnsZoneTombstones := 0, 0
	if p.dnsZoneSource != nil {
		published, tombstones, err := p.publishDNSZoneSnapshot(ctx)
		if err != nil {
			p.logger.Warn("publish DNS zone projection failed", zap.Error(err))
		} else {
			dnsZones = published
			dnsZoneTombstones = tombstones
		}
	}
	dnsBackends, dnsBackendTombstones := 0, 0
	if p.dnsBackendSource != nil {
		published, tombstones, err := p.publishDNSBackendSnapshot(ctx)
		if err != nil {
			p.logger.Warn("publish DNS backend projection failed", zap.Error(err))
		} else {
			dnsBackends = published
			dnsBackendTombstones = tombstones
		}
	}
	dnsPolicies, dnsPolicyTombstones := 0, 0
	if p.dnsPolicySource != nil {
		published, tombstones, err := p.publishDNSPolicySnapshot(ctx)
		if err != nil {
			p.logger.Warn("publish DNS policy projection failed", zap.Error(err))
		} else {
			dnsPolicies = published
			dnsPolicyTombstones = tombstones
		}
	}
	p.logger.Info("Nostr projection snapshot republished", zap.Int("services", len(services)), zap.Int("environments", len(envs)), zap.Int("states", len(states)), zap.Int("builds", buildsPublished), zap.Int("artifacts", artifactsPublished), zap.Int("deployment_intents", intentsPublished), zap.Int("deployment_runs", runsPublished), zap.Int("policies", policiesPublished), zap.Int("llm_routes", llmRoutes), zap.Int("llm_route_states", llmStates), zap.Int("ml_models", mlModels), zap.Int("ml_model_versions", mlVersions), zap.Int("ml_endpoints", mlEndpoints), zap.Int("ml_endpoint_states", mlStates), zap.Int("ml_provenance_graphs", mlProvenance), zap.Int("ml_capabilities", mlCapabilities), zap.Int("worker_assignments", workerAssignments), zap.Int("worker_drains", workerDrains), zap.Int("backup_recipes", backupRecipes), zap.Int("backup_policies", backupPolicies), zap.Int("backup_repositories", backupRepositories), zap.Int("backup_runs", backupRuns), zap.Int("backup_restores", backupRestores), zap.Int("backup_verifications", backupVerifications), zap.Int("backup_retentions", backupRetentions), zap.Int("backup_postures", backupPostures), zap.Int("dns_zones", dnsZones), zap.Int("dns_zone_tombstones", dnsZoneTombstones), zap.Int("dns_endpoints", dnsEndpoints), zap.Int("dns_endpoint_tombstones", dnsTombstones), zap.Int("dns_backends", dnsBackends), zap.Int("dns_backend_tombstones", dnsBackendTombstones), zap.Int("dns_policies", dnsPolicies), zap.Int("dns_policy_tombstones", dnsPolicyTombstones))
	return nil
}

func (p *Projector) handleEvent(ctx context.Context, e events.Event) {
	if !p.Enabled() {
		return
	}
	if err := p.publishAudit(ctx, e); err != nil {
		p.logger.Warn("publish Nostr audit event failed", zap.String("event_type", string(e.Type)), zap.Error(err))
	}

	res := resourceFromEvent(e)
	switch e.Type {
	case events.EventBuildRegistered, events.EventBuildStatusChanged:
		if id, ok := parseUUID(e.EntityID); ok {
			if build, err := p.source.GetBuild(ctx, id); err == nil && build != nil {
				_ = p.publishBuildRegistry(ctx, build, false)
			}
		}
	case events.EventArtifactRegistered:
		if id, ok := parseUUID(e.EntityID); ok {
			if artifact, err := p.source.GetArtifact(ctx, id); err == nil && artifact != nil {
				_ = p.publishArtifactRegistry(ctx, artifact, false)
			}
		}
	case events.EventDeploymentIntentCreated, events.EventDeploymentIntentApproved, events.EventDeploymentIntentRejected:
		if id, ok := parseUUID(firstString(res.IntentID, e.EntityID)); ok {
			if intent, err := p.source.GetDeploymentIntent(ctx, id); err == nil && intent != nil {
				_ = p.publishDeploymentIntentRegistry(ctx, intent, false)
			}
		}
		if id, ok := parseUUID(firstString(res.IntentID, e.EntityID)); ok {
			p.publishStateForIntent(ctx, id)
		}
	case events.EventDeploymentRunCreated, events.EventDeploymentRunStatusChanged, events.EventDeploymentRunCompleted:
		if id, ok := parseUUID(firstString(res.RunID, e.EntityID)); ok {
			if run, err := p.source.GetDeploymentRun(ctx, id); err == nil && run != nil {
				_ = p.publishDeploymentRunRegistry(ctx, run, false)
				p.publishWorkerReadModelsForWorker(ctx, run.WorkerPubkey)
			}
			p.publishStateForRun(ctx, id)
		} else if id, ok := parseUUID(res.IntentID); ok {
			p.publishStateForIntent(ctx, id)
		}
	case events.EventServiceCreated, events.EventServiceUpdated:
		p.publishServiceByID(ctx, firstUUID(res.ServiceID, e.EntityID))
	case events.EventServiceDeleted:
		if id, ok := parseUUID(firstString(res.ServiceID, e.EntityID)); ok {
			_ = p.publishServiceRegistry(ctx, &domain.Service{ID: id, UpdatedAt: time.Now().UTC()}, true)
		}
	case events.EventEnvironmentCreated, events.EventEnvironmentUpdated:
		p.publishEnvironmentByID(ctx, firstUUID(res.EnvironmentID, e.EntityID))
	case events.EventEnvironmentDeleted:
		if id, ok := parseUUID(firstString(res.EnvironmentID, e.EntityID)); ok {
			_ = p.publishEnvironmentRegistry(ctx, &domain.Environment{ID: id, UpdatedAt: time.Now().UTC()}, true)
		}
	case events.EventRuntimeObservation, events.EventEnvironmentServiceStateChanged, events.EventDriftDetected, events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop, events.EventAdoptionImported:
		if res.Deleted {
			if err := p.publishStateTombstone(ctx, res); err != nil {
				p.logger.Warn("publish service state tombstone failed",
					zap.String("service_id", res.ServiceID),
					zap.String("environment_id", res.EnvironmentID),
					zap.Error(err),
				)
			}
		} else {
			p.publishStateForResource(ctx, res)
		}
		if e.Type == events.EventAdoptionImported {
			p.publishServiceByID(ctx, res.ServiceID)
			p.publishEnvironmentByID(ctx, res.EnvironmentID)
		}
	case events.EventLLMRouteCreated, events.EventLLMRouteUpdated:
		p.publishLLMRouteByID(ctx, firstString(res.RouteID, e.EntityID))
	case events.EventLLMReleaseRegistered:
		p.publishLLMRouteByID(ctx, res.RouteID)
	case events.EventLLMDeploymentIntentCreated, events.EventLLMDeploymentIntentApproved, events.EventLLMDeploymentIntentRejected:
		if id, ok := parseUUID(firstString(res.IntentID, e.EntityID)); ok {
			p.publishLLMStateForIntent(ctx, id)
		}
	case events.EventLLMDeploymentRunCreated, events.EventLLMDeploymentRunStatusChanged, events.EventLLMDeploymentRunCompleted:
		if id, ok := parseUUID(firstString(res.RunID, e.EntityID)); ok {
			p.publishLLMStateForRun(ctx, id)
		} else if id, ok := parseUUID(res.IntentID); ok {
			p.publishLLMStateForIntent(ctx, id)
		}
	case events.EventLLMRouteObservation, events.EventLLMRouteStateChanged, events.EventLLMRouteDriftDetected, events.EventLLMGatewayRouteSynced:
		if res.Deleted {
			if err := p.publishLLMRouteStateTombstone(ctx, res); err != nil {
				p.logger.Warn("publish LLM route state tombstone failed", zap.String("route_id", res.RouteID), zap.String("environment_id", res.EnvironmentID), zap.Error(err))
			}
		} else {
			p.publishLLMStateForResource(ctx, res)
		}
	case service.EventMLModelChanged:
		p.publishMLModelByID(ctx, firstString(stringifyMapValue(e.Data, "model_id"), e.EntityID))
	case service.EventMLVersionChanged:
		p.publishMLModelVersionByID(ctx, firstString(stringifyMapValue(e.Data, "model_version_id"), e.EntityID))
	case service.EventMLEndpointChanged:
		p.publishMLEndpointByID(ctx, firstString(stringifyMapValue(e.Data, "endpoint_id"), e.EntityID))
	case service.EventMLIntentChanged:
		if id, ok := parseUUID(firstString(stringifyMapValue(e.Data, "intent_id"), e.EntityID)); ok {
			p.publishMLStateForIntent(ctx, id)
		}
	case service.EventMLRunChanged:
		if id, ok := parseUUID(firstString(stringifyMapValue(e.Data, "run_id"), e.EntityID)); ok {
			if p.mlSource != nil {
				if run, err := p.mlSource.GetMLDeploymentRun(ctx, id); err == nil && run != nil {
					p.publishWorkerReadModelsForWorker(ctx, run.WorkerPubkey)
				}
			}
			p.publishMLStateForRun(ctx, id)
		} else if id, ok := parseUUID(stringifyMapValue(e.Data, "intent_id")); ok {
			p.publishMLStateForIntent(ctx, id)
		}
	case service.EventMLObservation, service.EventMLStateChanged:
		if endpointID, ok := parseUUID(stringifyMapValue(e.Data, "endpoint_id")); ok {
			if envID, ok := parseUUID(stringifyMapValue(e.Data, "environment_id")); ok {
				p.publishMLStateForIDs(ctx, endpointID, envID)
			}
		}
	case service.EventMLArtifactChanged:
		p.publishMLProvenanceByArtifactID(ctx, firstString(stringifyMapValue(e.Data, "artifact_id"), e.EntityID))
	case service.EventMLProvenanceChanged, service.EventMLProvenanceDefected:
		p.publishMLProvenanceFromEvent(ctx, e)
	case service.EventBackupRecipeChanged:
		p.publishBackupRecipeByID(ctx, firstString(stringifyMapValue(e.Data, "recipe_id"), e.EntityID))
	case service.EventBackupPolicyChanged:
		p.publishBackupPolicyByID(ctx, firstString(stringifyMapValue(e.Data, "policy_id"), e.EntityID))
	case service.EventBackupRepositoryChanged:
		p.publishBackupRepositoryByID(ctx, firstString(stringifyMapValue(e.Data, "repository_id"), e.EntityID))
	case service.EventBackupRunChanged:
		p.publishBackupRunByID(ctx, firstString(stringifyMapValue(e.Data, "run_id"), e.EntityID))
		p.publishBackupRuntimeObservation(ctx)
	case service.EventBackupRestoreChanged:
		p.publishBackupRestoreByID(ctx, firstString(stringifyMapValue(e.Data, "restore_id"), e.EntityID))
		p.publishBackupRuntimeObservation(ctx)
	case service.EventBackupVerificationChanged:
		p.publishBackupVerificationByRunID(ctx, firstString(stringifyMapValue(e.Data, "run_id"), e.EntityID))
		p.publishBackupRuntimeObservation(ctx)
	case service.EventBackupRetentionChanged:
		p.publishBackupRetentionByID(ctx, firstString(stringifyMapValue(e.Data, "retention_run_id"), e.EntityID))
		p.publishBackupRuntimeObservation(ctx)
	}
	if shouldRefreshDNSProjection(e.Type) {
		if _, _, err := p.publishDNSEndpointSnapshot(ctx); err != nil {
			p.logger.Warn("publish DNS endpoint projection after event failed", zap.String("event_type", string(e.Type)), zap.Error(err))
		}
	}
}

func (p *Projector) publishServiceByID(ctx context.Context, raw string) {
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	svc, err := p.source.GetService(ctx, id)
	if err != nil || svc == nil {
		if err != nil {
			p.logger.Warn("read service for projection failed", zap.String("service_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishServiceRegistry(ctx, svc, false); err != nil {
		p.logger.Warn("publish service registry projection failed", zap.String("service_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishEnvironmentByID(ctx context.Context, raw string) {
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	env, err := p.source.GetEnvironment(ctx, id)
	if err != nil || env == nil {
		if err != nil {
			p.logger.Warn("read environment for projection failed", zap.String("environment_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishEnvironmentRegistry(ctx, env, false); err != nil {
		p.logger.Warn("publish environment registry projection failed", zap.String("environment_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishStateForIntent(ctx context.Context, intentID uuid.UUID) {
	intent, err := p.source.GetDeploymentIntent(ctx, intentID)
	if err != nil || intent == nil {
		if err != nil {
			p.logger.Warn("read deployment intent for projection failed", zap.String("intent_id", intentID.String()), zap.Error(err))
		}
		return
	}
	p.publishStateForIDs(ctx, intent.ServiceID, intent.EnvironmentID)
}

func (p *Projector) publishStateForRun(ctx context.Context, runID uuid.UUID) {
	run, err := p.source.GetDeploymentRun(ctx, runID)
	if err != nil || run == nil {
		if err != nil {
			p.logger.Warn("read deployment run for projection failed", zap.String("run_id", runID.String()), zap.Error(err))
		}
		return
	}
	p.publishStateForIntent(ctx, run.DeploymentIntentID)
}

func (p *Projector) publishStateForResource(ctx context.Context, res events.ResourceData) {
	serviceID, serviceOK := parseUUID(res.ServiceID)
	envID, envOK := parseUUID(res.EnvironmentID)
	if !serviceOK || !envOK {
		return
	}
	p.publishStateForIDs(ctx, serviceID, envID)
}

func (p *Projector) publishStateForIDs(ctx context.Context, serviceID, envID uuid.UUID) {
	state, err := p.source.GetEnvironmentServiceState(ctx, serviceID, envID)
	if err != nil || state == nil {
		if err != nil {
			p.logger.Warn("read service state for projection failed",
				zap.String("service_id", serviceID.String()),
				zap.String("environment_id", envID.String()),
				zap.Error(err),
			)
		}
		return
	}
	if err := p.publishState(ctx, state); err != nil {
		p.logger.Warn("publish service state projection failed",
			zap.String("service_id", serviceID.String()),
			zap.String("environment_id", envID.String()),
			zap.Error(err),
		)
	}
}

func (p *Projector) publishLLMRouteByID(ctx context.Context, raw string) {
	if p.llmSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	route, err := p.llmSource.GetLLMRoute(ctx, id)
	if err != nil || route == nil {
		if err != nil {
			p.logger.Warn("read LLM route for projection failed", zap.String("route_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishLLMRouteRegistry(ctx, route, false); err != nil {
		p.logger.Warn("publish LLM route registry projection failed", zap.String("route_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishLLMStateForIntent(ctx context.Context, intentID uuid.UUID) {
	if p.llmSource == nil {
		return
	}
	intent, err := p.llmSource.GetLLMDeploymentIntent(ctx, intentID)
	if err != nil || intent == nil {
		if err != nil {
			p.logger.Warn("read LLM deployment intent for projection failed", zap.String("intent_id", intentID.String()), zap.Error(err))
		}
		return
	}
	p.publishLLMStateForIDs(ctx, intent.RouteID, intent.EnvironmentID)
}

func (p *Projector) publishLLMStateForRun(ctx context.Context, runID uuid.UUID) {
	if p.llmSource == nil {
		return
	}
	run, err := p.llmSource.GetLLMDeploymentRun(ctx, runID)
	if err != nil || run == nil {
		if err != nil {
			p.logger.Warn("read LLM deployment run for projection failed", zap.String("run_id", runID.String()), zap.Error(err))
		}
		return
	}
	p.publishLLMStateForIntent(ctx, run.DeploymentIntentID)
}

func (p *Projector) publishLLMStateForResource(ctx context.Context, res events.ResourceData) {
	routeID, routeOK := parseUUID(res.RouteID)
	envID, envOK := parseUUID(res.EnvironmentID)
	if !routeOK || !envOK {
		return
	}
	p.publishLLMStateForIDs(ctx, routeID, envID)
}

func (p *Projector) publishLLMStateForIDs(ctx context.Context, routeID, envID uuid.UUID) {
	if p.llmSource == nil {
		return
	}
	state, err := p.llmSource.GetLLMRouteState(ctx, routeID, envID)
	if err != nil || state == nil {
		if err != nil {
			p.logger.Warn("read LLM route state for projection failed", zap.String("route_id", routeID.String()), zap.String("environment_id", envID.String()), zap.Error(err))
		}
		return
	}
	if err := p.publishLLMRouteState(ctx, state); err != nil {
		p.logger.Warn("publish LLM route state projection failed", zap.String("route_id", routeID.String()), zap.String("environment_id", envID.String()), zap.Error(err))
	}
}

func (p *Projector) publishPublicRouteSnapshots(ctx context.Context, services []domain.Service, envs []domain.Environment) (int, int, int, int) {
	return p.publishPublicRouteSnapshotsFromSource(ctx, legacyProjectorSource{source: p.source}, services, envs)
}

func (p *Projector) publishPublicRouteSnapshotsFromSource(ctx context.Context, snapshotSource ProjectorSource, services []domain.Service, envs []domain.Environment) (int, int, int, int) {
	const pageSize = 1000
	buildsPublished, artifactsPublished, intentsPublished, runsPublished := 0, 0, 0, 0
	for i := range services {
		for offset := 0; ; offset += pageSize {
			builds, err := snapshotSource.ListBuilds(ctx, services[i].ID, pageSize, offset)
			if err != nil {
				p.logger.Warn("list builds for projection failed", zap.String("service_id", services[i].ID.String()), zap.Error(err))
				break
			}
			for j := range builds {
				if err := p.publishBuildRegistry(ctx, &builds[j], false); err != nil {
					p.logger.Warn("publish build projection failed", zap.String("build_id", builds[j].ID.String()), zap.Error(err))
				} else {
					buildsPublished++
				}
			}
			if len(builds) < pageSize {
				break
			}
		}
		for offset := 0; ; offset += pageSize {
			artifacts, err := snapshotSource.ListArtifacts(ctx, services[i].ID, pageSize, offset)
			if err != nil {
				p.logger.Warn("list artifacts for projection failed", zap.String("service_id", services[i].ID.String()), zap.Error(err))
				break
			}
			for j := range artifacts {
				if err := p.publishArtifactRegistry(ctx, &artifacts[j], false); err != nil {
					p.logger.Warn("publish artifact projection failed", zap.String("artifact_id", artifacts[j].ID.String()), zap.Error(err))
				} else {
					artifactsPublished++
				}
			}
			if len(artifacts) < pageSize {
				break
			}
		}
		for j := range envs {
			for offset := 0; ; offset += pageSize {
				intents, err := snapshotSource.ListDeploymentIntents(ctx, services[i].ID, envs[j].ID, pageSize, offset)
				if err != nil {
					p.logger.Warn("list deployment intents for projection failed", zap.String("service_id", services[i].ID.String()), zap.String("environment_id", envs[j].ID.String()), zap.Error(err))
					break
				}
				for k := range intents {
					if err := p.publishDeploymentIntentRegistry(ctx, &intents[k], false); err != nil {
						p.logger.Warn("publish deployment intent projection failed", zap.String("intent_id", intents[k].ID.String()), zap.Error(err))
					} else {
						intentsPublished++
					}
					runs, err := snapshotSource.ListDeploymentRuns(ctx, intents[k].ID)
					if err != nil {
						p.logger.Warn("list deployment runs for projection failed", zap.String("intent_id", intents[k].ID.String()), zap.Error(err))
						continue
					}
					for l := range runs {
						if err := p.publishDeploymentRunRegistry(ctx, &runs[l], false); err != nil {
							p.logger.Warn("publish deployment run projection failed", zap.String("run_id", runs[l].ID.String()), zap.Error(err))
						} else {
							runsPublished++
						}
					}
				}
				if len(intents) < pageSize {
					break
				}
			}
		}
	}
	return buildsPublished, artifactsPublished, intentsPublished, runsPublished
}

func (p *Projector) publishPolicySnapshots(ctx context.Context) int {
	if p.policySource == nil {
		return 0
	}
	policies, err := p.policySource.ListPolicies(ctx, false)
	if err != nil {
		p.logger.Warn("list policies for projection failed", zap.Error(err))
		return 0
	}
	published := 0
	for i := range policies {
		if err := p.publishPolicyRegistry(ctx, &policies[i], false); err != nil {
			p.logger.Warn("publish policy projection failed", zap.String("policy_id", policies[i].ID.String()), zap.Error(err))
		} else {
			published++
		}
	}
	return published
}

func (p *Projector) publishMLSnapshots(ctx context.Context) (modelsPublished, versionsPublished, endpointsPublished, statesPublished, provenancePublished, capabilitiesPublished int) {
	if p.mlSource != nil {
		const pageSize = 500
		for offset := 0; ; offset += pageSize {
			models, err := p.mlSource.ListModels(ctx, "", pageSize, offset)
			if err != nil {
				p.logger.Warn("list ML models for projection failed", zap.Error(err))
				break
			}
			for i := range models {
				if err := p.publishMLModelRegistry(ctx, &models[i]); err != nil {
					p.logger.Warn("publish ML model projection failed", zap.String("model_id", models[i].ID.String()), zap.Error(err))
				} else {
					modelsPublished++
				}
				versions, err := p.mlSource.ListModelVersions(ctx, models[i].ID, pageSize, 0)
				if err != nil {
					p.logger.Warn("list ML model versions for projection failed", zap.String("model_id", models[i].ID.String()), zap.Error(err))
					continue
				}
				for j := range versions {
					if err := p.publishMLModelVersionRegistry(ctx, &versions[j]); err != nil {
						p.logger.Warn("publish ML model version projection failed", zap.String("model_version_id", versions[j].ID.String()), zap.Error(err))
					} else {
						versionsPublished++
					}
					artifacts, err := p.mlSource.ListArtifactRefsByModelVersion(ctx, versions[j].ID)
					if err != nil {
						p.logger.Warn("list ML artifacts for projection failed", zap.String("model_version_id", versions[j].ID.String()), zap.Error(err))
						continue
					}
					for k := range artifacts {
						if err := p.publishMLArtifactProvenanceGraph(ctx, &artifacts[k]); err != nil {
							p.logger.Warn("publish ML provenance graph failed", zap.String("artifact_id", artifacts[k].ID.String()), zap.Error(err))
						} else {
							provenancePublished++
						}
					}
				}
			}
			if len(models) < pageSize {
				break
			}
		}
		for offset := 0; ; offset += pageSize {
			endpoints, err := p.mlSource.ListInferenceEndpoints(ctx, uuid.Nil, pageSize, offset)
			if err != nil {
				p.logger.Warn("list ML endpoints for projection failed", zap.Error(err))
				break
			}
			for i := range endpoints {
				if err := p.publishMLInferenceEndpointRegistry(ctx, &endpoints[i]); err != nil {
					p.logger.Warn("publish ML endpoint projection failed", zap.String("endpoint_id", endpoints[i].ID.String()), zap.Error(err))
				} else {
					endpointsPublished++
				}
			}
			if len(endpoints) < pageSize {
				break
			}
		}
		states, err := p.mlSource.ListInferenceStates(ctx)
		if err != nil {
			p.logger.Warn("list ML endpoint states for projection failed", zap.Error(err))
		} else {
			for i := range states {
				if err := p.publishMLInferenceEndpointState(ctx, &states[i]); err != nil {
					p.logger.Warn("publish ML endpoint state projection failed", zap.String("endpoint_id", states[i].EndpointID.String()), zap.String("environment_id", states[i].EnvironmentID.String()), zap.Error(err))
				} else {
					statesPublished++
				}
			}
		}
	}
	if workerSnapshotSource := p.workerSnapshotSource(); workerSnapshotSource != nil {
		workers, err := workerSnapshotSource.ListWorkers(ctx, "", 1000)
		if err != nil {
			p.logger.Warn("list workers for ML capability projection failed", zap.Error(err))
		} else {
			for i := range workers {
				if err := p.publishMLRuntimeCapabilityProfile(ctx, &workers[i]); err != nil {
					p.logger.Warn("publish ML runtime capability profile failed", zap.String("worker", workers[i].PubKey), zap.Error(err))
				} else {
					capabilitiesPublished++
				}
			}
		}
	}
	return
}

func (p *Projector) publishBackupSnapshots(ctx context.Context) (recipesPublished, policiesPublished, repositoriesPublished, runsPublished, restoresPublished, verificationsPublished, retentionsPublished, posturesPublished int) {
	if p.backupSource == nil {
		return
	}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		recipes, err := p.backupSource.ListRecipes(ctx, pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup recipes for projection failed", zap.Error(err))
			break
		}
		for i := range recipes {
			if err := p.publishBackupRecipeRegistry(ctx, &recipes[i]); err != nil {
				p.logger.Warn("publish backup recipe projection failed", zap.String("recipe_id", recipes[i].ID.String()), zap.Error(err))
			} else {
				recipesPublished++
			}
		}
		if len(recipes) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		policies, err := p.backupSource.ListPolicies(ctx, pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup policies for projection failed", zap.Error(err))
			break
		}
		for i := range policies {
			if err := p.publishBackupPolicyRegistry(ctx, &policies[i]); err != nil {
				p.logger.Warn("publish backup policy projection failed", zap.String("policy_id", policies[i].ID.String()), zap.Error(err))
			} else {
				policiesPublished++
			}
		}
		if len(policies) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		repositories, err := p.backupSource.ListRepositories(ctx, pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup repositories for projection failed", zap.Error(err))
			break
		}
		for i := range repositories {
			if err := p.publishBackupRepositoryRegistry(ctx, &repositories[i]); err != nil {
				p.logger.Warn("publish backup repository projection failed", zap.String("repository_id", repositories[i].ID.String()), zap.Error(err))
			} else {
				repositoriesPublished++
			}
		}
		if len(repositories) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		runs, err := p.backupSource.ListBackupRuns(ctx, "", pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup runs for projection failed", zap.Error(err))
			break
		}
		for i := range runs {
			if err := p.publishBackupRunState(ctx, &runs[i]); err != nil {
				p.logger.Warn("publish backup run state projection failed", zap.String("run_id", runs[i].ID.String()), zap.Error(err))
			} else {
				runsPublished++
			}
			verification, err := p.backupSource.GetBackupVerificationByRunID(ctx, runs[i].ID)
			if err != nil {
				p.logger.Warn("read backup verification for projection failed", zap.String("run_id", runs[i].ID.String()), zap.Error(err))
			} else if verification != nil {
				if err := p.publishBackupVerificationState(ctx, verification); err != nil {
					p.logger.Warn("publish backup verification state projection failed", zap.String("run_id", runs[i].ID.String()), zap.Error(err))
				} else {
					verificationsPublished++
				}
			}
		}
		if len(runs) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		restores, err := p.backupSource.ListBackupRestores(ctx, "", pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup restores for projection failed", zap.Error(err))
			break
		}
		for i := range restores {
			if err := p.publishBackupRestoreState(ctx, &restores[i]); err != nil {
				p.logger.Warn("publish backup restore state projection failed", zap.String("restore_id", restores[i].ID.String()), zap.Error(err))
			} else {
				restoresPublished++
			}
		}
		if len(restores) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		retentions, err := p.backupSource.ListBackupRetentionRuns(ctx, "", pageSize, offset)
		if err != nil {
			p.logger.Warn("list backup retention runs for projection failed", zap.Error(err))
			break
		}
		for i := range retentions {
			if err := p.publishBackupRetentionState(ctx, &retentions[i]); err != nil {
				p.logger.Warn("publish backup retention state projection failed", zap.String("retention_run_id", retentions[i].ID.String()), zap.Error(err))
			} else {
				retentionsPublished++
			}
		}
		if len(retentions) < pageSize {
			break
		}
	}
	if err := p.publishBackupRuntimeObservation(ctx); err != nil {
		p.logger.Warn("publish backup runtime observation failed", zap.Error(err))
	} else {
		posturesPublished++
	}
	return
}

func (p *Projector) publishBackupRecipeByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	recipe, err := p.backupSource.GetRecipe(ctx, id)
	if err != nil || recipe == nil {
		if err != nil {
			p.logger.Warn("read backup recipe for projection failed", zap.String("recipe_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupRecipeRegistry(ctx, recipe); err != nil {
		p.logger.Warn("publish backup recipe projection failed", zap.String("recipe_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupPolicyByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	policy, err := p.backupSource.GetPolicy(ctx, id)
	if err != nil || policy == nil {
		if err != nil {
			p.logger.Warn("read backup policy for projection failed", zap.String("policy_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupPolicyRegistry(ctx, policy); err != nil {
		p.logger.Warn("publish backup policy projection failed", zap.String("policy_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupRepositoryByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	repo, err := p.backupSource.GetRepository(ctx, id)
	if err != nil || repo == nil {
		if err != nil {
			p.logger.Warn("read backup repository for projection failed", zap.String("repository_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupRepositoryRegistry(ctx, repo); err != nil {
		p.logger.Warn("publish backup repository projection failed", zap.String("repository_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupRunByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	run, err := p.backupSource.GetBackupRun(ctx, id)
	if err != nil || run == nil {
		if err != nil {
			p.logger.Warn("read backup run for projection failed", zap.String("run_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupRunState(ctx, run); err != nil {
		p.logger.Warn("publish backup run state projection failed", zap.String("run_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupRestoreByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	restore, err := p.backupSource.GetBackupRestore(ctx, id)
	if err != nil || restore == nil {
		if err != nil {
			p.logger.Warn("read backup restore for projection failed", zap.String("restore_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupRestoreState(ctx, restore); err != nil {
		p.logger.Warn("publish backup restore state projection failed", zap.String("restore_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupRetentionByID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	run, err := p.backupSource.GetBackupRetentionRun(ctx, id)
	if err != nil || run == nil {
		if err != nil {
			p.logger.Warn("read backup retention run for projection failed", zap.String("retention_run_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupRetentionState(ctx, run); err != nil {
		p.logger.Warn("publish backup retention state projection failed", zap.String("retention_run_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishBackupVerificationByRunID(ctx context.Context, raw string) {
	if p.backupSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	verification, err := p.backupSource.GetBackupVerificationByRunID(ctx, id)
	if err != nil || verification == nil {
		if err != nil {
			p.logger.Warn("read backup verification for projection failed", zap.String("run_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishBackupVerificationState(ctx, verification); err != nil {
		p.logger.Warn("publish backup verification state projection failed", zap.String("run_id", raw), zap.Error(err))
	}
	p.publishBackupRunByID(ctx, raw)
}

func (p *Projector) publishMLModelByID(ctx context.Context, raw string) {
	if p.mlSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	model, err := p.mlSource.GetModel(ctx, id)
	if err != nil || model == nil {
		if err != nil {
			p.logger.Warn("read ML model for projection failed", zap.String("model_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishMLModelRegistry(ctx, model); err != nil {
		p.logger.Warn("publish ML model projection failed", zap.String("model_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishMLModelVersionByID(ctx context.Context, raw string) {
	if p.mlSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	version, err := p.mlSource.GetModelVersion(ctx, id)
	if err != nil || version == nil {
		if err != nil {
			p.logger.Warn("read ML model version for projection failed", zap.String("model_version_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishMLModelVersionRegistry(ctx, version); err != nil {
		p.logger.Warn("publish ML model version projection failed", zap.String("model_version_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishMLEndpointByID(ctx context.Context, raw string) {
	if p.mlSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	endpoint, err := p.mlSource.GetInferenceEndpoint(ctx, id)
	if err != nil || endpoint == nil {
		if err != nil {
			p.logger.Warn("read ML endpoint for projection failed", zap.String("endpoint_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishMLInferenceEndpointRegistry(ctx, endpoint); err != nil {
		p.logger.Warn("publish ML endpoint projection failed", zap.String("endpoint_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishMLStateForIntent(ctx context.Context, intentID uuid.UUID) {
	if p.mlSource == nil {
		return
	}
	intent, err := p.mlSource.GetMLDeploymentIntent(ctx, intentID)
	if err != nil || intent == nil {
		if err != nil {
			p.logger.Warn("read ML deployment intent for projection failed", zap.String("intent_id", intentID.String()), zap.Error(err))
		}
		return
	}
	p.publishMLStateForIDs(ctx, intent.EndpointID, intent.EnvironmentID)
}

func (p *Projector) publishMLStateForRun(ctx context.Context, runID uuid.UUID) {
	if p.mlSource == nil {
		return
	}
	run, err := p.mlSource.GetMLDeploymentRun(ctx, runID)
	if err != nil || run == nil {
		if err != nil {
			p.logger.Warn("read ML deployment run for projection failed", zap.String("run_id", runID.String()), zap.Error(err))
		}
		return
	}
	p.publishMLStateForIntent(ctx, run.DeploymentIntentID)
}

func (p *Projector) publishMLStateForIDs(ctx context.Context, endpointID, envID uuid.UUID) {
	if p.mlSource == nil {
		return
	}
	state, err := p.mlSource.GetInferenceState(ctx, endpointID, envID)
	if err != nil || state == nil {
		if err != nil {
			p.logger.Warn("read ML endpoint state for projection failed", zap.String("endpoint_id", endpointID.String()), zap.String("environment_id", envID.String()), zap.Error(err))
		}
		return
	}
	if err := p.publishMLInferenceEndpointState(ctx, state); err != nil {
		p.logger.Warn("publish ML endpoint state projection failed", zap.String("endpoint_id", endpointID.String()), zap.String("environment_id", envID.String()), zap.Error(err))
	}
}

func (p *Projector) publishMLProvenanceByArtifactID(ctx context.Context, raw string) {
	if p.mlSource == nil {
		return
	}
	id, ok := parseUUID(raw)
	if !ok {
		return
	}
	artifact, err := p.mlSource.GetArtifactRef(ctx, id)
	if err != nil || artifact == nil {
		if err != nil {
			p.logger.Warn("read ML artifact for projection failed", zap.String("artifact_id", raw), zap.Error(err))
		}
		return
	}
	if err := p.publishMLArtifactProvenanceGraph(ctx, artifact); err != nil {
		p.logger.Warn("publish ML provenance graph failed", zap.String("artifact_id", raw), zap.Error(err))
	}
}

func (p *Projector) publishMLProvenanceFromEvent(ctx context.Context, e events.Event) {
	switch edge := e.Data.(type) {
	case *domain.MLProvenanceEdge:
		p.publishMLProvenanceForEdge(ctx, edge)
	case domain.MLProvenanceEdge:
		p.publishMLProvenanceForEdge(ctx, &edge)
	default:
		if artifactID := stringifyMapValue(e.Data, "artifact_id"); artifactID != "" {
			p.publishMLProvenanceByArtifactID(ctx, artifactID)
		}
	}
}

func (p *Projector) publishMLProvenanceForEdge(ctx context.Context, edge *domain.MLProvenanceEdge) {
	if edge == nil {
		return
	}
	if edge.FromArtifactID != nil {
		p.publishMLProvenanceByArtifactID(ctx, edge.FromArtifactID.String())
	}
	if edge.ToArtifactID != nil {
		p.publishMLProvenanceByArtifactID(ctx, edge.ToArtifactID.String())
	}
}

func (p *Projector) publishReplaceableJSON(ctx context.Context, kind int, dTag string, tags gonostr.Tags, value any, entityType string, entityID *uuid.UUID) error {
	content, _ := json.Marshal(value)
	publishKind := kind
	baseTags := gonostr.Tags{{"d", dTag}, {"deleted", "false"}}
	if domain, entity := canonicalStateDomain(kind); domain != "" {
		publishKind = KindCASControlState
		baseTags = gonostr.Tags{{"d", canonicalStateDTag(domain, entity, dTag)}, {"domain", domain}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(kind)}, {"deleted", "false"}}
	}
	baseTags = append(baseTags, tags...)
	return p.publishSigned(ctx, publishKind, baseTags, string(content), entityType, entityID)
}

func canonicalStateDomain(kind int) (domainName string, entity string) {
	switch kind {
	case KindServiceState:
		return "service", "state"
	case KindServiceRegistry:
		return "service", "registry"
	case KindEnvironmentRegistry:
		return "environment", "registry"
	case KindLLMRouteRegistry:
		return "llm", "route"
	case KindLLMRouteState:
		return "llm", "state"
	case KindArtifactRegistry:
		return "artifact", "registry"
	case KindDeploymentIntentRegistry:
		return "deployment", "intent"
	case KindDeploymentRunRegistry:
		return "deployment", "run"
	case KindBuildRegistry:
		return "build", "registry"
	case KindPolicyRegistry:
		return "policy", "registry"
	case KindPackageRepositoryRegistry:
		return "package", "repository"
	case KindPackageArtifactRegistry:
		return "package", "artifact"
	case KindPackagePromotionRegistry:
		return "package", "promotion"
	case KindWorkerState:
		return "worker", "state"
	case KindWorkerAssignmentState:
		return "worker", "assignment"
	case KindWorkerDrainStatus:
		return "worker", "drain"
	case KindWorkerEligibilityPreview:
		return "worker", "eligibility"
	case KindDNSZoneState:
		return "dns", "zone"
	case KindDNSEndpointState:
		return "dns", "endpoint"
	case KindDNSPolicyState:
		return "dns", "policy"
	case KindDNSBackendState:
		return "dns", "backend"
	case KindMLModelRegistry:
		return "ml", "model"
	case KindMLModelVersionRegistry:
		return "ml", "model-version"
	case KindMLDatasetRegistry:
		return "ml", "dataset"
	case KindMLRecipeRegistry:
		return "ml", "recipe"
	case KindMLRecipeRunState:
		return "ml", "recipe-run"
	case KindMLInferenceEndpointRegistry:
		return "ml", "endpoint"
	case KindMLInferenceEndpointState:
		return "ml", "endpoint-state"
	case KindMLEvaluationExperimentState:
		return "ml", "evaluation"
	case KindMLArtifactProvenanceGraph:
		return "ml", "provenance"
	case KindMLRuntimeCapabilityProfile:
		return "ml", "runtime-capability"
	case KindBackupDefinitionRegistry:
		return "backup", "definition"
	case KindBackupPolicyRegistry:
		return "backup", "policy"
	case KindBackupRepositoryRegistry:
		return "backup", "repository"
	case KindBackupRetentionRegistry:
		return "backup", "retention"
	case KindBackupRecipeRegistry:
		return "backup", "recipe"
	case KindBackupRunState:
		return "backup", "run"
	case KindBackupVerificationState:
		return "backup", "verification"
	case KindBackupRestoreState:
		return "backup", "restore"
	case KindBackupRuntimeObservationState:
		return "backup", "runtime"
	default:
		return "", ""
	}
}

func canonicalStateDTag(_, _, id string) string {
	return id
}

func (p *Projector) publishDNSEndpointSnapshot(ctx context.Context) (int, int, error) {
	if !p.Enabled() || p.dnsSource == nil {
		return 0, 0, nil
	}
	p.dnsPublishMu.Lock()
	defer p.dnsPublishMu.Unlock()
	if err := p.hydrateDNSPublishedCache(ctx); err != nil {
		return 0, 0, err
	}
	endpoints, err := p.dnsSource.ListDNSEndpoints(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list DNS endpoints: %w", err)
	}
	current := make(map[string]dnsPublishedEndpoint, len(endpoints))
	desired := make(map[string]struct{}, len(endpoints))
	failures := []string{}
	published := 0
	for i := range endpoints {
		endpoint := endpoints[i]
		if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
			failures = append(failures, fmt.Sprintf("validate endpoint[%d]: %v", i, err))
			p.logger.Warn("skip invalid DNS endpoint projection", zap.Int("index", i), zap.Error(err))
			continue
		}
		if _, exists := desired[endpoint.Coordinate]; exists {
			failure := fmt.Sprintf("duplicate coordinate %q", endpoint.Coordinate)
			failures = append(failures, failure)
			p.logger.Warn("skip duplicate DNS endpoint projection", zap.String("coordinate", endpoint.Coordinate))
			continue
		}
		desired[endpoint.Coordinate] = struct{}{}
		if err := p.publishDNSEndpoint(ctx, endpoint); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", endpoint.Coordinate, err))
			p.logger.Warn("publish DNS endpoint projection failed", zap.String("coordinate", endpoint.Coordinate), zap.Error(err))
			continue
		}
		current[endpoint.Coordinate] = dnsPublishedEndpoint{FQDN: endpoint.FQDN}
		published++
	}
	nextPublished := make(map[string]dnsPublishedEndpoint, len(current))
	for coordinate, endpoint := range current {
		nextPublished[coordinate] = endpoint
	}
	tombstones := 0
	for coordinate, previous := range p.dnsPublished {
		if _, stillCurrent := current[coordinate]; stillCurrent {
			continue
		}
		if _, stillDesired := desired[coordinate]; stillDesired {
			nextPublished[coordinate] = previous
			continue
		}
		if err := p.publishDNSEndpointTombstone(ctx, coordinate, previous.FQDN); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", coordinate, err))
			p.logger.Warn("publish DNS endpoint tombstone failed", zap.String("coordinate", coordinate), zap.Error(err))
			nextPublished[coordinate] = previous
			continue
		}
		tombstones++
	}
	p.dnsPublished = nextPublished
	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("DNS endpoint projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func (p *Projector) publishDNSEndpoint(ctx context.Context, endpoint domain.DNSEndpoint) error {
	tags := dnsEndpointTags(endpoint)
	return p.publishReplaceableJSON(ctx, KindDNSEndpointState, endpoint.Coordinate, tags, endpoint, "dns_endpoint.projection", &endpoint.ID)
}

func (p *Projector) publishDNSEndpointTombstone(ctx context.Context, coordinate, fqdn string) error {
	now := time.Now().UTC()
	content, _ := json.Marshal(map[string]any{"deleted": true, "coordinate": coordinate, "fqdn": fqdn, "updated_at": formatTime(now)})
	tags := gonostr.Tags{{"d", coordinate}, {"deleted", "true"}, {"t", "dns-endpoint"}, {"t", "bahia"}}
	if strings.TrimSpace(fqdn) != "" {
		tags = append(tags, gonostr.Tag{"dns", strings.TrimSpace(fqdn)})
	}
	return p.publishSigned(ctx, KindDNSEndpointState, tags, string(content), "dns_endpoint.projection", nil)
}

func (p *Projector) publishDNSZoneSnapshot(ctx context.Context) (int, int, error) {
	if !p.Enabled() || p.dnsZoneSource == nil {
		return 0, 0, nil
	}
	p.dnsPublishMu.Lock()
	defer p.dnsPublishMu.Unlock()
	if p.dnsPublishedZones == nil {
		p.dnsPublishedZones = map[string]dnsPublishedZone{}
	}
	zones := p.dnsZoneSource.ListDNSZones()
	current := make(map[string]dnsPublishedZone, len(zones))
	failures := []string{}
	published := 0
	for i := range zones {
		zone := zones[i]
		if err := domain.ValidateDNSZone(&zone); err != nil {
			failures = append(failures, fmt.Sprintf("validate zone[%d]: %v", i, err))
			p.logger.Warn("skip invalid DNS zone projection", zap.Int("index", i), zap.Error(err))
			continue
		}
		dTag := dnsZoneDTag(zone.Name)
		if _, exists := current[dTag]; exists {
			failure := fmt.Sprintf("duplicate zone %q", zone.Name)
			failures = append(failures, failure)
			p.logger.Warn("skip duplicate DNS zone projection", zap.String("zone", zone.Name))
			continue
		}
		if err := p.publishDNSZone(ctx, zone, false); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", zone.Name, err))
			p.logger.Warn("publish DNS zone projection failed", zap.String("zone", zone.Name), zap.Error(err))
			continue
		}
		current[dTag] = dnsPublishedZone{Name: zone.Name, BackendRef: zone.BackendRef, Visibility: string(zone.Visibility)}
		published++
	}
	tombstones := 0
	for dTag, previous := range p.dnsPublishedZones {
		if _, stillCurrent := current[dTag]; stillCurrent {
			continue
		}
		if err := p.publishDNSZoneTombstone(ctx, previous); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", dTag, err))
			p.logger.Warn("publish DNS zone tombstone failed", zap.String("d_tag", dTag), zap.Error(err))
			current[dTag] = previous
			continue
		}
		tombstones++
	}
	p.dnsPublishedZones = current
	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("DNS zone projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func (p *Projector) publishDNSZone(ctx context.Context, zone domain.DNSZone, deleted bool) error {
	now := time.Now().UTC()
	content := map[string]any{"name": zone.Name, "visibility": string(zone.Visibility), "backend_ref": zone.BackendRef, "ttl": zone.TTL, "deleted": deleted, "updated_at": formatTime(now)}
	tags := gonostr.Tags{{"zone", zone.Name}, {"backend", zone.BackendRef}, {"visibility", string(zone.Visibility)}, {"t", "dns-zone"}, {"t", "bahia"}}
	return p.publishReplaceableJSON(ctx, KindDNSZoneState, dnsZoneDTag(zone.Name), tags, content, "dns_zone.projection", nil)
}

func (p *Projector) publishDNSZoneTombstone(ctx context.Context, previous dnsPublishedZone) error {
	now := time.Now().UTC()
	content, _ := json.Marshal(map[string]any{"name": previous.Name, "visibility": previous.Visibility, "backend_ref": previous.BackendRef, "deleted": true, "updated_at": formatTime(now)})
	tags := gonostr.Tags{{"d", dnsZoneDTag(previous.Name)}, {"deleted", "true"}, {"zone", previous.Name}, {"backend", previous.BackendRef}, {"visibility", previous.Visibility}, {"t", "dns-zone"}, {"t", "bahia"}}
	return p.publishSigned(ctx, KindDNSZoneState, tags, string(content), "dns_zone.projection", nil)
}

func (p *Projector) publishDNSBackendSnapshot(ctx context.Context) (int, int, error) {
	if !p.Enabled() || p.dnsBackendSource == nil {
		return 0, 0, nil
	}
	p.dnsPublishMu.Lock()
	defer p.dnsPublishMu.Unlock()
	if p.dnsPublishedBackends == nil {
		p.dnsPublishedBackends = map[string]dnsPublishedBackend{}
	}
	backends := p.dnsBackendSource.ListDNSBackendStates(ctx)
	current := make(map[string]dnsPublishedBackend, len(backends))
	failures := []string{}
	published := 0
	for i := range backends {
		backend := backends[i]
		backend.Ref = strings.TrimSpace(backend.Ref)
		if backend.Ref == "" {
			failure := fmt.Sprintf("backend[%d] ref is required", i)
			failures = append(failures, failure)
			p.logger.Warn("skip invalid DNS backend projection", zap.Int("index", i), zap.String("reason", "missing ref"))
			continue
		}
		if !backend.Type.IsValid() {
			failure := fmt.Sprintf("backend[%d] type %q is not valid", i, backend.Type)
			failures = append(failures, failure)
			p.logger.Warn("skip invalid DNS backend projection", zap.Int("index", i), zap.String("backend", backend.Ref), zap.String("type", string(backend.Type)))
			continue
		}
		dTag := dnsBackendDTag(backend.Ref)
		if _, exists := current[dTag]; exists {
			failure := fmt.Sprintf("duplicate backend %q", backend.Ref)
			failures = append(failures, failure)
			p.logger.Warn("skip duplicate DNS backend projection", zap.String("backend", backend.Ref))
			continue
		}
		if err := p.publishDNSBackend(ctx, backend, false); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", backend.Ref, err))
			p.logger.Warn("publish DNS backend projection failed", zap.String("backend", backend.Ref), zap.Error(err))
			continue
		}
		current[dTag] = dnsPublishedBackend{Ref: backend.Ref, Type: string(backend.Type), Health: string(backend.Health)}
		published++
	}
	tombstones := 0
	for dTag, previous := range p.dnsPublishedBackends {
		if _, stillCurrent := current[dTag]; stillCurrent {
			continue
		}
		if err := p.publishDNSBackendTombstone(ctx, previous); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", dTag, err))
			p.logger.Warn("publish DNS backend tombstone failed", zap.String("d_tag", dTag), zap.Error(err))
			current[dTag] = previous
			continue
		}
		tombstones++
	}
	p.dnsPublishedBackends = current
	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("DNS backend projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func (p *Projector) publishDNSBackend(ctx context.Context, backend domain.DNSBackendState, deleted bool) error {
	updatedAt := backend.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	content := map[string]any{"ref": backend.Ref, "type": string(backend.Type), "health": string(backend.Health), "zones": append([]string(nil), backend.ZoneRefs...), "deleted": deleted, "updated_at": formatTime(updatedAt)}
	if backend.LastSyncAt != nil {
		content["last_sync_at"] = formatTime(*backend.LastSyncAt)
	}
	tags := gonostr.Tags{{"backend", backend.Ref}, {"type", string(backend.Type)}, {"health", string(backend.Health)}, {"t", "dns-backend"}, {"t", "bahia"}}
	for _, zone := range backend.ZoneRefs {
		if strings.TrimSpace(zone) != "" {
			tags = append(tags, gonostr.Tag{"zone", strings.TrimSpace(zone)})
		}
	}
	return p.publishReplaceableJSON(ctx, KindDNSBackendState, dnsBackendDTag(backend.Ref), tags, content, "dns_backend.projection", nil)
}

func (p *Projector) publishDNSBackendTombstone(ctx context.Context, previous dnsPublishedBackend) error {
	now := time.Now().UTC()
	content, _ := json.Marshal(map[string]any{"ref": previous.Ref, "type": previous.Type, "health": previous.Health, "deleted": true, "updated_at": formatTime(now)})
	tags := gonostr.Tags{{"d", dnsBackendDTag(previous.Ref)}, {"deleted", "true"}, {"backend", previous.Ref}, {"type", previous.Type}, {"health", previous.Health}, {"t", "dns-backend"}, {"t", "bahia"}}
	return p.publishSigned(ctx, KindDNSBackendState, tags, string(content), "dns_backend.projection", nil)
}

func (p *Projector) publishDNSPolicySnapshot(ctx context.Context) (int, int, error) {
	if !p.Enabled() || p.dnsPolicySource == nil {
		return 0, 0, nil
	}
	p.dnsPublishMu.Lock()
	defer p.dnsPublishMu.Unlock()
	if p.dnsPublishedPolicies == nil {
		p.dnsPublishedPolicies = map[string]dnsPublishedPolicy{}
	}
	policies, err := p.dnsPolicySource.ListEnabledDNSPolicies(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list DNS policies: %w", err)
	}
	current := make(map[string]dnsPublishedPolicy, len(policies))
	failures := []string{}
	published := 0
	for i := range policies {
		policy := policies[i]
		if policy.ID == uuid.Nil {
			failure := fmt.Sprintf("policy[%d] id is required", i)
			failures = append(failures, failure)
			p.logger.Warn("skip invalid DNS policy projection", zap.Int("index", i), zap.String("reason", "missing id"))
			continue
		}
		if err := domain.ValidateDNSPolicy(&policy); err != nil {
			failures = append(failures, fmt.Sprintf("validate policy[%d]: %v", i, err))
			p.logger.Warn("skip invalid DNS policy projection", zap.Int("index", i), zap.Error(err))
			continue
		}
		dTag := dnsPolicyDTag(policy.ID)
		if _, exists := current[dTag]; exists {
			failure := fmt.Sprintf("duplicate policy %q", policy.ID.String())
			failures = append(failures, failure)
			p.logger.Warn("skip duplicate DNS policy projection", zap.String("policy_id", policy.ID.String()))
			continue
		}
		if err := p.publishDNSPolicy(ctx, policy, false); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", policy.ID.String(), err))
			p.logger.Warn("publish DNS policy projection failed", zap.String("policy_id", policy.ID.String()), zap.Error(err))
			continue
		}
		current[dTag] = dnsPublishedPolicy{ID: policy.ID.String(), Name: policy.Name, ZoneID: uuidStringPtr(policy.ZoneID), Enabled: policy.Enabled}
		published++
	}
	tombstones := 0
	for dTag, previous := range p.dnsPublishedPolicies {
		if _, stillCurrent := current[dTag]; stillCurrent {
			continue
		}
		if err := p.publishDNSPolicyTombstone(ctx, previous); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", dTag, err))
			p.logger.Warn("publish DNS policy tombstone failed", zap.String("d_tag", dTag), zap.Error(err))
			current[dTag] = previous
			continue
		}
		tombstones++
	}
	p.dnsPublishedPolicies = current
	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("DNS policy projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func (p *Projector) publishDNSPolicy(ctx context.Context, policy domain.DNSPolicy, deleted bool) error {
	updatedAt := policy.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	content := map[string]any{"id": policy.ID.String(), "name": policy.Name, "zone_id": uuidStringPtr(policy.ZoneID), "environment_id": uuidStringPtr(policy.EnvironmentID), "rules": policy.Rules, "enabled": policy.Enabled, "deleted": deleted, "created_at": formatTime(policy.CreatedAt), "updated_at": formatTime(updatedAt)}
	tags := gonostr.Tags{{"policy", policy.ID.String()}, {"enabled", fmt.Sprintf("%t", policy.Enabled)}, {"t", "dns-policy"}, {"t", "bahia"}}
	if policy.ZoneID != nil {
		tags = append(tags, gonostr.Tag{"zone", policy.ZoneID.String()})
	}
	return p.publishReplaceableJSON(ctx, KindDNSPolicyState, dnsPolicyDTag(policy.ID), tags, content, "dns_policy.projection", &policy.ID)
}

func (p *Projector) publishDNSPolicyTombstone(ctx context.Context, previous dnsPublishedPolicy) error {
	now := time.Now().UTC()
	content, _ := json.Marshal(map[string]any{"id": previous.ID, "name": previous.Name, "zone_id": previous.ZoneID, "enabled": previous.Enabled, "deleted": true, "updated_at": formatTime(now)})
	tags := gonostr.Tags{{"d", "dnspolicy:" + previous.ID}, {"deleted", "true"}, {"policy", previous.ID}, {"enabled", fmt.Sprintf("%t", previous.Enabled)}, {"t", "dns-policy"}, {"t", "bahia"}}
	if previous.ZoneID != "" {
		tags = append(tags, gonostr.Tag{"zone", previous.ZoneID})
	}
	return p.publishSigned(ctx, KindDNSPolicyState, tags, string(content), "dns_policy.projection", nil)
}

func dnsZoneDTag(name string) string {
	return "zone:" + strings.TrimSpace(name)
}

func dnsBackendDTag(ref string) string {
	return "dnsbackend:" + strings.TrimSpace(ref)
}

func dnsPolicyDTag(id uuid.UUID) string {
	return "dnspolicy:" + id.String()
}

func (p *Projector) hydrateDNSPublishedCache(ctx context.Context) error {
	if p.dnsCacheHydrated {
		return nil
	}
	p.dnsPublished = map[string]dnsPublishedEndpoint{}
	if p.eventRepo == nil {
		p.dnsCacheHydrated = true
		return nil
	}
	records, err := p.eventRepo.ListByKind(ctx, KindDNSEndpointState, 10000)
	if err != nil {
		return fmt.Errorf("hydrate DNS endpoint projection cache: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	p.dnsCacheHydrated = true
	servicePubkey := ""
	if p.privateKey != "" {
		servicePubkey, _ = gonostr.GetPublicKey(p.privateKey)
	}
	seen := map[string]struct{}{}
	for _, record := range records {
		if servicePubkey != "" && record.PubKey != servicePubkey {
			continue
		}
		coordinate, fqdn, deleted := dnsProjectionRecordState(record)
		if coordinate == "" {
			continue
		}
		if _, ok := seen[coordinate]; ok {
			continue
		}
		seen[coordinate] = struct{}{}
		if deleted {
			continue
		}
		p.dnsPublished[coordinate] = dnsPublishedEndpoint{FQDN: fqdn}
	}
	return nil
}

func dnsEndpointTags(endpoint domain.DNSEndpoint) gonostr.Tags {
	tags := gonostr.Tags{{"family", string(endpoint.Family)}, {"health", string(endpoint.Health)}, {"dns", endpoint.FQDN}, {"addr", endpoint.Address}, {"t", "dns-endpoint"}, {"t", "bahia"}}
	if endpoint.Environment != "" {
		tags = append(tags, gonostr.Tag{"environment", endpoint.Environment})
	}
	if endpoint.Runtime != "" {
		tags = append(tags, gonostr.Tag{"runtime", endpoint.Runtime})
	}
	if endpoint.Protocol != "" {
		tags = append(tags, gonostr.Tag{"proto", endpoint.Protocol})
	}
	if endpoint.Port != nil {
		tags = append(tags, gonostr.Tag{"port", fmt.Sprintf("%d", *endpoint.Port)})
	}
	if endpoint.WorkerPubkey != "" {
		tags = append(tags, gonostr.Tag{"npub", endpoint.WorkerPubkey}, gonostr.Tag{"mesh", "fips"})
	}
	switch endpoint.Family {
	case domain.DNSEndpointFamilyService:
		tags = append(tags, gonostr.Tag{"service", endpoint.Name})
		if endpoint.ServiceID != nil {
			tags = append(tags, gonostr.Tag{"service_id", endpoint.ServiceID.String()})
		}
	case domain.DNSEndpointFamilyLLM:
		tags = append(tags, gonostr.Tag{"route", endpoint.Name})
		if endpoint.LLMRouteID != nil {
			tags = append(tags, gonostr.Tag{"route_id", endpoint.LLMRouteID.String()})
		}
	case domain.DNSEndpointFamilyML:
		tags = append(tags, gonostr.Tag{"endpoint", endpoint.Name})
		if endpoint.MLEndpointID != nil {
			tags = append(tags, gonostr.Tag{"endpoint_id", endpoint.MLEndpointID.String()})
		}
	case domain.DNSEndpointFamilyWorker:
		if endpoint.WorkerPubkey != "" {
			tags = append(tags, gonostr.Tag{"worker", endpoint.WorkerPubkey})
		}
	}
	for _, capability := range endpoint.Capabilities {
		if capability != "" {
			tags = append(tags, gonostr.Tag{"capability", capability})
		}
	}
	return tags
}

func dnsProjectionRecordState(record repository.NostrEventRecord) (coordinate, fqdn string, deleted bool) {
	var tags gonostr.Tags
	_ = json.Unmarshal(record.Tags, &tags)
	for _, tag := range tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "d":
			coordinate = tag[1]
		case "dns":
			fqdn = tag[1]
		case "deleted":
			deleted = tag[1] == "true"
		}
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(record.Content), &content); err == nil {
		if coordinate == "" {
			coordinate, _ = content["coordinate"].(string)
		}
		if fqdn == "" {
			fqdn, _ = content["fqdn"].(string)
		}
		if value, ok := content["deleted"].(bool); ok {
			deleted = value
		}
	}
	return coordinate, fqdn, deleted
}

func shouldRefreshDNSProjection(eventType events.EventType) bool {
	switch eventType {
	case events.EventServiceCreated, events.EventServiceUpdated, events.EventServiceDeleted,
		events.EventEnvironmentCreated, events.EventEnvironmentUpdated, events.EventEnvironmentDeleted,
		events.EventRuntimeObservation, events.EventEnvironmentServiceStateChanged, events.EventDriftDetected,
		events.EventReconcileCompleted, events.EventAdoptionImported, events.EventRuntimeDeploy,
		events.EventRuntimeRestart, events.EventRuntimeStop,
		events.EventLLMRouteCreated, events.EventLLMRouteUpdated, events.EventLLMReleaseRegistered,
		events.EventLLMDeploymentIntentCreated, events.EventLLMDeploymentIntentApproved, events.EventLLMDeploymentIntentRejected,
		events.EventLLMDeploymentRunCreated, events.EventLLMDeploymentRunStatusChanged, events.EventLLMDeploymentRunCompleted,
		events.EventLLMRouteObservation, events.EventLLMRouteStateChanged, events.EventLLMRouteDriftDetected, events.EventLLMGatewayRouteSynced,
		service.EventMLModelChanged, service.EventMLVersionChanged, service.EventMLEndpointChanged,
		service.EventMLIntentChanged, service.EventMLRunChanged, service.EventMLObservation,
		service.EventMLStateChanged, service.EventMLArtifactChanged, service.EventMLProvenanceChanged,
		service.EventMLProvenanceDefected:
		return true
	default:
		return false
	}
}

func (p *Projector) publishSystemDiscovery(ctx context.Context) error {
	cfg := p.systemConfig
	if cfg == nil {
		return nil
	}
	if !cfg.Nostr.Sidecar.Enabled {
		return nil
	}
	browserRelays := cfg.Nostr.BrowserRelayPolicyRelays()
	if len(browserRelays) == 0 {
		return fmt.Errorf("system discovery requires nostr.browser_relays when relay sidecar is enabled")
	}
	contextVMRelays := cfg.Nostr.ContextVMRelayPolicyRelays()
	serviceRelays := cfg.Nostr.ServiceRelayPolicyRelays()
	encryptedRequestsEnabled := len(browserRelays) > 0 && cfg.Nostr.PrivateKey != ""
	payload := map[string]any{
		"schema":        "bahia.system-discovery.v1",
		"registries":    discoveryRegistries(cfg),
		"versions":      discoveryVersions(),
		"control_plane": discoveryControlPlane(cfg.LLM.Enabled, p.mcpTransport, p.dnsSource != nil || p.dnsZoneSource != nil || p.dnsBackendSource != nil || p.dnsPolicySource != nil),
		"blossom": map[string]any{
			"enabled":       cfg.Blossom.Enabled,
			"url":           cfg.Blossom.URL,
			"servers":       cfg.Blossom.Servers,
			"storage_class": cfg.Blossom.StorageClass,
		},
		"runtime": map[string]any{
			"type":         cfg.Runtime.Type,
			"environments": runtimeEnvironmentNames(cfg),
		},
		"oci": map[string]any{
			"enabled":     cfg.OCI.Enabled,
			"public_host": cfg.OCI.PublicHost,
		},
		"features": map[string]bool{
			"oci":                      cfg.OCI.Enabled,
			"harbor":                   cfg.Harbor.Enabled,
			"blossom":                  cfg.Blossom.Enabled,
			"hiveci":                   cfg.HiveCI.Enabled,
			"cashu":                    cfg.Cashu.Enabled,
			"telemetry":                cfg.Telemetry.Enabled,
			"notifications":            cfg.Notifications.Enabled,
			"auth":                     cfg.Auth.Enabled,
			"relay_sidecar":            cfg.Nostr.Sidecar.Enabled,
			"relay_read_models":        cfg.Nostr.Sidecar.Enabled && cfg.Nostr.PublishEnabled,
			"encrypted_nostr_requests": encryptedRequestsEnabled,
			"llm_control_plane":        cfg.LLM.Enabled,
			"direct_nostr_http_auth":   cfg.Auth.Enabled,
			"mcp_transport":            p.mcpTransport,
			"publish_enabled":          cfg.Nostr.PublishEnabled,
		},
	}
	content, _ := json.Marshal(payload)
	if err := p.publishSigned(ctx, kinds.ContextVMServerAnnouncement, gonostr.Tags{{"schema", "bahia.system-discovery.v1"}, {"name", "Bahia"}}, string(content), "system.discovery", nil); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, "bahia-browser-v1", browserRelays); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, "bahia-contextvm-v1", contextVMRelays); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, "bahia-service-v1", serviceRelays); err != nil {
		return err
	}
	return p.publishServiceNIP65RelayPreferences(ctx, serviceRelays, contextVMRelays)
}

func (p *Projector) publishConfiguredDMRelayListsFromSystemConfig(ctx context.Context) error {
	if p.systemConfig == nil {
		return nil
	}
	return p.publishConfiguredDMRelayLists(ctx, p.systemConfig.Nostr.EnabledDMRelayLists())
}

func (p *Projector) publishConfiguredDMRelayLists(ctx context.Context, lists []config.DMRelayListConfig) error {
	relays := []string{}
	features := map[string]struct{}{}
	for _, list := range lists {
		if !list.Enabled || list.Identity != config.DMRelayListIdentityService {
			continue
		}
		relays = append(relays, list.Relays...)
		features[list.Feature] = struct{}{}
	}
	normalizedRelays := normalizeProjectionRelays(relays)
	if len(normalizedRelays) == 0 {
		return nil
	}
	tags := gonostr.Tags{{"title", "bahia-dm-relays"}}
	for feature := range features {
		tags = append(tags, gonostr.Tag{"feature", feature})
	}
	for _, relay := range normalizedRelays {
		tags = append(tags, gonostr.Tag{"relay", relay})
	}
	return p.publishSigned(ctx, kinds.NIP51DMRelayList, tags, "", "system.discovery.dm_relay_list", nil)
}

func (p *Projector) publishServiceNIP65RelayPreferences(ctx context.Context, writeRelays, readRelays []string) error {
	tags := gonostr.Tags{}
	for _, relay := range normalizeProjectionRelays(readRelays) {
		tags = append(tags, gonostr.Tag{"r", relay, "read"})
	}
	for _, relay := range normalizeProjectionRelays(writeRelays) {
		tags = append(tags, gonostr.Tag{"r", relay, "write"})
	}
	return p.publishSigned(ctx, kinds.NIP65RelayList, tags, "", "system.discovery.nip65_relay_preferences", nil)
}

func (p *Projector) publishRelaySet(ctx context.Context, dTag string, relays []string) error {
	tags := gonostr.Tags{{"d", dTag}, {"title", dTag}}
	for _, relay := range normalizeProjectionRelays(relays) {
		tags = append(tags, gonostr.Tag{"relay", relay})
	}
	return p.publishSigned(ctx, kinds.RelaySetDiscovery, tags, "", "system.discovery.relay_set", nil)
}

func discoveryVersions() map[string]any {
	return map[string]any{
		"backend":    version.Semantic(),
		"components": version.Components(),
	}
}

func discoveryRegistries(cfg *config.Config) []map[string]any {
	registries := []map[string]any{}
	if cfg.OCI.Enabled && cfg.OCI.PublicHost != "" {
		registries = append(registries, map[string]any{"id": "bahia-oci", "name": "Bahia Registry", "base_url": cfg.OCI.PublicHost, "type": "native", "default": true, "enabled": true})
	}
	if cfg.Harbor.Enabled && cfg.Harbor.URL != "" {
		registries = append(registries, map[string]any{"id": "harbor", "name": "Harbor", "base_url": cfg.Harbor.URL, "type": "harbor", "enabled": true})
	}
	if cfg.Registry.URL != "" {
		registries = append(registries, map[string]any{"id": "configured", "name": "Configured Registry", "base_url": cfg.Registry.URL, "type": cfg.Registry.Type, "enabled": true})
	}
	registries = append(registries,
		map[string]any{"id": "ghcr", "name": "GitHub Container Registry", "base_url": "ghcr.io", "type": "ghcr", "enabled": true},
		map[string]any{"id": "dockerhub", "name": "Docker Hub", "base_url": "docker.io", "type": "dockerhub", "enabled": true},
		map[string]any{"id": "quay", "name": "Quay.io", "base_url": "quay.io", "type": "quay", "enabled": true},
	)
	return registries
}

func discoveryControlPlane(llmEnabled, mcpTransportEnabled, dnsEnabled bool) map[string]any {
	capabilities := []string{"service_deployments", "service_registry_read_models", "worker_management", "worker_read_models", "relay_read_models"}
	methods := []string{
		"service/deploy",
		"service/rollback",
		"worker/cordon",
		"worker/uncordon",
		"worker/drain",
		"worker/undrain",
		"worker/maintenance-enter",
		"worker/maintenance-exit",
		"worker/labels-update",
		"worker/policy-apply",
		"workload/pin",
		"worker/cleanup",
		"package/repository-apply",
		"package/repository-delete",
		"package/publish",
		"package/promote",
		"package/yank",
		"package/drift-detect",
		"approval/approve",
		"tools/call",
	}
	correlationTags := []string{"service", "environment", "artifact", "intent", "run", "worker", "command", "e", "p", "status", "step"}
	mcpFields := []string{"request_event_id", "request_kind", "service_id", "environment_id", "intent_id", "run_id", "worker_pubkey", "d_tag", "observable_kinds"}
	if llmEnabled {
		capabilities = append(capabilities, "llm_routes", "llm_deployments", "llm_rollback")
		methods = append(methods, "llm/route-create", "llm/release-register", "llm/deploy", "llm/deployment-approval", "llm/rollback")
		correlationTags = append(correlationTags, "route", "release")
		mcpFields = append(mcpFields, "route_id", "release_id")
	}
	if mcpTransportEnabled {
		capabilities = append(capabilities, "mcp_async_correlation")
	}
	if dnsEnabled {
		capabilities = append(capabilities, "dns_endpoint_catalog")
		methods = append(methods, "dns/zone-create", "dns/policy-apply", "dns/record-override", "dns/drift-remediate", "dns/backend-register")
	}
	aiMLMethods := []string{"ml/model-import", "ml/recipe-run", "ml/inference-deploy", "ml/inference-approval", "ml/inference-rollback"}
	methods = append(methods, aiMLMethods...)
	transportKinds := map[string]int{
		"contextvm_message":        kinds.ContextVMMessage,
		"contextvm_gift_wrap":      kinds.ContextVMGiftWrap,
		"contextvm_ephemeral_wrap": kinds.ContextVMEphemeralGiftWrap,
	}
	observableKinds := map[string]int{
		"control_state": KindCASControlState,
		"status":        KindNIP38Status,
		"audit":         KindCASAudit,
	}
	announcementKinds := map[string]int{
		"server":             kinds.ContextVMServerAnnouncement,
		"tools":              kinds.ContextVMToolsList,
		"resources":          kinds.ContextVMResourcesList,
		"resource_templates": kinds.ContextVMResourceTemplatesList,
		"prompts":            kinds.ContextVMPromptsList,
	}
	relayKinds := map[string]int{
		"relay_set": kinds.RelaySetDiscovery,
		"nip65":     kinds.NIP65RelayList,
	}
	aiML := map[string]any{
		"enabled":               true,
		"transport_kinds":       transportKinds,
		"methods":               aiMLMethods,
		"observable_kinds":      observableKinds,
		"capabilities":          []string{"ml_model_registry_read_models", "ml_model_version_read_models", "ml_inference_endpoint_read_models", "ml_provenance_read_models", "ml_runtime_capability_read_models", "ml_inference_deploy_requests", "ml_inference_approval_requests", "ml_inference_rollback_requests"},
		"correlation_tags":      []string{"model", "model_version", "recipe", "run", "endpoint", "environment", "deployment", "artifact", "worker", "runtime", "e", "p", "status"},
		"contextvm_commands":    true,
		"canonical_observables": true,
		"unsupported_in_d1":     []string{"recipe_execution", "model_import_orchestration", "dataset_import", "evaluation", "benchmark", "fine_tune"},
	}
	return map[string]any{
		"version":               "bahia-controlplane-v1",
		"capabilities":          capabilities,
		"transport_kinds":       transportKinds,
		"methods":               methods,
		"observable_kinds":      observableKinds,
		"announcement_kinds":    announcementKinds,
		"relay_kinds":           relayKinds,
		"ai_ml":                 aiML,
		"correlation_tags":      correlationTags,
		"contextvm_commands":    true,
		"canonical_observables": true,
		"mcp":                   map[string]any{"async_correlation": mcpTransportEnabled, "fields": mcpFields},
	}
}

func runtimeEnvironmentNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Runtime.Environments))
	for name := range cfg.Runtime.Environments {
		names = append(names, name)
	}
	return names
}

func normalizeProjectionRelays(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, relay := range strings.Split(value, ",") {
			relay = strings.TrimSpace(relay)
			if relay == "" {
				continue
			}
			key := strings.TrimRight(relay, "/")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, relay)
		}
	}
	return out
}

func (p *Projector) publishBuildRegistry(ctx context.Context, build *domain.Build, deleted bool) error {
	if deleted {
		return nil
	}
	return p.publishReplaceableJSON(ctx, KindBuildRegistry, build.ID.String(), gonostr.Tags{{"service", build.ServiceID.String()}, {"build", build.ID.String()}, {"status", string(build.Status)}}, map[string]any{"deleted": false, "id": build.ID.String(), "service_id": build.ServiceID.String(), "git_sha": build.GitSHA, "git_ref": build.GitRef, "ci_system": build.CISystem, "ci_run_id": build.CIRunID, "loom_job_id": build.LoomJobID, "status": string(build.Status), "source_event_id": build.SourceEventID, "started_at": build.StartedAt, "finished_at": build.FinishedAt, "metadata": build.Metadata, "created_at": formatTime(build.CreatedAt)}, "build.projection", &build.ID)
}

func (p *Projector) publishArtifactRegistry(ctx context.Context, artifact *domain.Artifact, deleted bool) error {
	if deleted {
		return nil
	}
	return p.publishReplaceableJSON(ctx, KindArtifactRegistry, artifact.ID.String(), gonostr.Tags{{"service", artifact.ServiceID.String()}, {"artifact", artifact.ID.String()}, {"build", artifact.BuildID.String()}}, map[string]any{"deleted": false, "id": artifact.ID.String(), "build_id": artifact.BuildID.String(), "service_id": artifact.ServiceID.String(), "image_repo": artifact.ImageRepo, "image_tag": artifact.ImageTag, "image_digest": artifact.ImageDigest, "manifest_media_type": artifact.ManifestMediaType, "size_bytes": artifact.SizeBytes, "sbom_url": artifact.SBOMURL, "signature_ref": artifact.SignatureRef, "scan_status": string(artifact.ScanStatus), "metadata": artifact.Metadata, "created_at": formatTime(artifact.CreatedAt)}, "artifact.projection", &artifact.ID)
}

func (p *Projector) publishDeploymentIntentRegistry(ctx context.Context, intent *domain.DeploymentIntent, deleted bool) error {
	if deleted {
		return nil
	}
	tags := gonostr.Tags{{"service", intent.ServiceID.String()}, {"environment", intent.EnvironmentID.String()}, {"artifact", intent.ArtifactID.String()}, {"intent", intent.ID.String()}, {"status", string(intent.Status)}, {"approval", string(intent.ApprovalStatus)}, {"unit", unitTagValue(intent.DeploymentUnitID)}}
	content := map[string]any{"deleted": false, "id": intent.ID.String(), "service_id": intent.ServiceID.String(), "environment_id": intent.EnvironmentID.String(), "deployment_unit_id": uuidStringPtr(intent.DeploymentUnitID), "artifact_id": intent.ArtifactID.String(), "requested_by": intent.RequestedBy, "source_kind": string(intent.SourceKind), "approval_status": string(intent.ApprovalStatus), "status": string(intent.Status), "deployment_status": string(intent.Status), "approval_metadata": intent.ApprovalMetadata, "metadata": intent.Metadata, "created_at": formatTime(intent.CreatedAt), "approved_at": intent.ApprovedAt, "updated_at": formatTime(intent.UpdatedAt)}
	// Desired-state metadata (additive — old decoders ignore unknown fields).
	if intent.DesiredHash != "" {
		content["desired_hash"] = intent.DesiredHash
		tags = append(tags, gonostr.Tag{"desired_hash", intent.DesiredHash})
	}
	if intent.DesiredState != nil {
		if renderer := desiredStateRenderer(intent.DesiredState); renderer != "" {
			content["renderer"] = renderer
		}
		if target := desiredStateTarget(intent.DesiredState); target != "" {
			content["target"] = target
		}
	}
	return p.publishReplaceableJSON(ctx, KindDeploymentIntentRegistry, intent.ID.String(), tags, content, "deployment_intent.projection", &intent.ID)
}

func (p *Projector) publishDeploymentRunRegistry(ctx context.Context, run *domain.DeploymentRun, deleted bool) error {
	if deleted {
		return nil
	}
	tags := gonostr.Tags{{"intent", run.DeploymentIntentID.String()}, {"run", run.ID.String()}, {"status", string(run.Status)}, {"unit", unitTagValue(run.DeploymentUnitID)}}
	content := map[string]any{"deleted": false, "id": run.ID.String(), "deployment_intent_id": run.DeploymentIntentID.String(), "deployment_unit_id": uuidStringPtr(run.DeploymentUnitID), "loom_job_id": run.LoomJobID, "worker_pubkey": run.WorkerPubkey, "worker_name": run.WorkerName, "status": string(run.Status), "exit_code": run.ExitCode, "stdout_ref": run.StdoutRef, "stderr_ref": run.StderrRef, "started_at": run.StartedAt, "finished_at": run.FinishedAt, "metadata": run.Metadata, "created_at": formatTime(run.CreatedAt), "updated_at": formatTime(run.UpdatedAt)}
	// Apply metadata enrichment (additive — old decoders ignore unknown fields).
	if run.ApplyMetadata != nil {
		if renderer, ok := run.ApplyMetadata["renderer"].(string); ok && renderer != "" {
			content["renderer"] = renderer
			tags = append(tags, gonostr.Tag{"renderer", renderer})
		}
		if desiredHash, ok := run.ApplyMetadata["desired_hash"].(string); ok && desiredHash != "" {
			content["desired_hash"] = desiredHash
		}
		if revisionHash, ok := run.ApplyMetadata["revision_hash"].(string); ok && revisionHash != "" {
			content["revision_hash"] = revisionHash
		}
		if target, ok := run.ApplyMetadata["target"].(string); ok && target != "" {
			content["target"] = target
		}
		if applySummary, ok := run.ApplyMetadata["apply_summary"].(string); ok && applySummary != "" {
			content["apply_summary"] = applySummary
		}
		if obsID, ok := run.ApplyMetadata["observation_id"].(string); ok && obsID != "" {
			content["observation_id"] = obsID
		}
	}
	return p.publishReplaceableJSON(ctx, KindDeploymentRunRegistry, run.ID.String(), tags, content, "deployment_run.projection", &run.ID)
}

func (p *Projector) publishPolicyRegistry(ctx context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
	content := map[string]any{"deleted": deleted, "id": policy.ID.String(), "updated_at": formatTime(policy.UpdatedAt)}
	tags := gonostr.Tags{{"policy", policy.ID.String()}, {"deleted", fmt.Sprintf("%t", deleted)}}
	if !deleted {
		content["name"] = policy.Name
		if policy.EnvironmentID != nil {
			content["environment_id"] = policy.EnvironmentID.String()
			tags = append(tags, gonostr.Tag{"environment", policy.EnvironmentID.String()})
		}
		content["rules"] = policy.Rules
		content["rule_count"] = len(policy.Rules)
		content["enforcement"] = string(policy.Enforcement)
		content["enabled"] = policy.Enabled
		content["created_at"] = formatTime(policy.CreatedAt)
		tags = append(tags, gonostr.Tag{"name", policy.Name}, gonostr.Tag{"enabled", fmt.Sprintf("%t", policy.Enabled)}, gonostr.Tag{"enforcement", string(policy.Enforcement)})
	}
	contentJSON, _ := json.Marshal(content)
	baseTags := gonostr.Tags{{"d", canonicalStateDTag("policy", "registry", policy.ID.String())}, {"domain", "policy"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindPolicyRegistry)}}
	return p.publishSigned(ctx, KindCASControlState, append(baseTags, tags...), string(contentJSON), "policy.projection", &policy.ID)
}

func (p *Projector) publishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      svc.ID.String(),
	}
	if !deleted {
		content["name"] = svc.Name
		content["repo_url"] = svc.RepoURL
		content["artifact_repo"] = svc.ArtifactRepo
		content["default_branch"] = svc.DefaultBranch
		content["runtime_type"] = string(svc.RuntimeType)
		content["created_at"] = formatTime(svc.CreatedAt)
		content["updated_at"] = formatTime(svc.UpdatedAt)
	} else {
		content["updated_at"] = formatTime(svc.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"d", canonicalStateDTag("service", "registry", svc.ID.String())},
		{"domain", "service"},
		{"schema", "bahia.cp-state.v1"},
		{"legacy_kind", strconv.Itoa(KindServiceRegistry)},
		{"deleted", fmt.Sprintf("%t", deleted)},
	}
	if !deleted {
		tags = append(tags,
			gonostr.Tag{"name", svc.Name},
			gonostr.Tag{"runtime", string(svc.RuntimeType)},
		)
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(contentJSON), "service.projection", &svc.ID)
}

func (p *Projector) publishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	if env != nil {
		domain.NormalizeEnvironmentTargeting(env)
	}
	content := map[string]any{
		"deleted": deleted,
		"id":      env.ID.String(),
	}
	if !deleted {
		content["name"] = env.Name
		content["protected"] = env.Protected
		content["deploy_strategy"] = string(env.DeployStrategy)
		content["targeting"] = env.Targeting
		content["deployment_units"] = []map[string]any{{"key": env.Targeting.DefaultUnitKey, "implicit": true}}
		content["reconcile_mode"] = string(env.Targeting.DefaultReconcileMode)
		content["created_at"] = formatTime(env.CreatedAt)
		content["updated_at"] = formatTime(env.UpdatedAt)
	} else {
		content["updated_at"] = formatTime(env.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"d", canonicalStateDTag("environment", "registry", env.ID.String())},
		{"domain", "environment"},
		{"schema", "bahia.cp-state.v1"},
		{"legacy_kind", strconv.Itoa(KindEnvironmentRegistry)},
		{"deleted", fmt.Sprintf("%t", deleted)},
	}
	if !deleted {
		tags = append(tags,
			gonostr.Tag{"name", env.Name},
			gonostr.Tag{"protected", fmt.Sprintf("%t", env.Protected)},
			gonostr.Tag{"unit", env.Targeting.DefaultUnitKey},
			gonostr.Tag{"reconcile_mode", string(env.Targeting.DefaultReconcileMode)},
		)
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(contentJSON), "environment.projection", &env.ID)
}

func (p *Projector) publishBackupRecipeRegistry(ctx context.Context, recipe *domain.BackupRecipe) error {
	if recipe == nil || recipe.Name == "" || recipe.Version == "" {
		return nil
	}
	dTag := "backup-recipe:" + recipe.ID.String()
	tags := gonostr.Tags{{"recipe", dTag}, {"recipe_id", recipe.ID.String()}, {"repository_id", recipe.RepositoryID.String()}, {"backend", string(recipe.Backend)}, {"target", recipe.TargetRef}, {"version", recipe.Version}}
	if recipe.PolicyID != nil {
		tags = append(tags, gonostr.Tag{"policy", recipe.PolicyID.String()}, gonostr.Tag{"policy_id", recipe.PolicyID.String()})
	}
	return p.publishReplaceableJSON(ctx, KindBackupRecipeRegistry, dTag, tags, map[string]any{"deleted": false, "id": recipe.ID.String(), "name": recipe.Name, "version": recipe.Version, "backend": string(recipe.Backend), "repository_id": recipe.RepositoryID.String(), "policy_id": uuidStringPtr(recipe.PolicyID), "target_ref": recipe.TargetRef, "include": recipe.Include, "exclude": recipe.Exclude, "verification_mode": string(recipe.VerificationMode), "metadata": recipe.Metadata, "created_at": formatTime(recipe.CreatedAt), "updated_at": formatTime(recipe.UpdatedAt)}, "backup.recipe.projection", &recipe.ID)
}

func (p *Projector) publishBackupPolicyRegistry(ctx context.Context, policy *domain.BackupPolicy) error {
	if policy == nil || policy.Name == "" {
		return nil
	}
	dTag := "backup-policy:" + policy.ID.String()
	tags := gonostr.Tags{{"policy", dTag}, {"policy_id", policy.ID.String()}, {"name", policy.Name}, {"require_verification", fmt.Sprintf("%t", policy.RequireVerification)}, {"verification", string(policy.VerificationMode)}}
	return p.publishReplaceableJSON(ctx, KindBackupPolicyRegistry, dTag, tags, map[string]any{"deleted": false, "id": policy.ID.String(), "name": policy.Name, "require_verification": policy.RequireVerification, "verification_mode": string(policy.VerificationMode), "metadata": policy.Metadata, "created_at": formatTime(policy.CreatedAt), "updated_at": formatTime(policy.UpdatedAt)}, "backup.policy.projection", &policy.ID)
}

func (p *Projector) publishBackupRepositoryRegistry(ctx context.Context, repo *domain.BackupRepository) error {
	if repo == nil || repo.Name == "" {
		return nil
	}
	dTag := "backup-repository:" + repo.ID.String()
	tags := gonostr.Tags{{"repository", dTag}, {"repository_id", repo.ID.String()}, {"name", repo.Name}, {"backend", string(repo.Backend)}}
	return p.publishReplaceableJSON(ctx, KindBackupRepositoryRegistry, dTag, tags, map[string]any{"deleted": false, "id": repo.ID.String(), "name": repo.Name, "backend": string(repo.Backend), "repository_uri": repo.RepositoryURI, "credential_profile": repo.CredentialProfile, "metadata": repo.Metadata, "created_at": formatTime(repo.CreatedAt), "updated_at": formatTime(repo.UpdatedAt)}, "backup.repository.projection", &repo.ID)
}

func (p *Projector) publishBackupRunState(ctx context.Context, run *domain.BackupRun) error {
	if run == nil {
		return nil
	}
	dTag := "backup-run:" + run.ID.String()
	restoreEligible := domain.BackupRunRestoreEligible(run)
	content := map[string]any{"deleted": false, "id": run.ID.String(), "recipe_id": run.RecipeID.String(), "repository_id": run.RepositoryID.String(), "policy_id": uuidStringPtr(run.PolicyID), "requested_by": run.RequestedBy, "request_event_id": run.RequestEventID, "request_kind": run.RequestKind, "request_d_tag": run.RequestDTag, "status": string(run.Status), "backend": string(run.Backend), "target_ref": run.TargetRef, "snapshot_created": run.SnapshotCreated, "snapshot_id": run.SnapshotID, "verification_mode": string(run.VerificationMode), "verification_status": string(run.VerificationStatus), "restore_eligible": restoreEligible, "restore_eligibility": string(run.RestoreEligibility), "restore_eligibility_reason": run.RestoreEligibilityReason, "verification_policy_failure": run.VerificationPolicyFailure, "failure_category": string(run.FailureCategory), "publish_summary": run.PublishSummary, "error": run.Error, "metadata": run.Metadata, "started_at": run.StartedAt, "finished_at": run.FinishedAt, "created_at": formatTime(run.CreatedAt), "updated_at": formatTime(run.UpdatedAt)}
	if p.backupSource != nil {
		if verification, err := p.backupSource.GetBackupVerificationByRunID(ctx, run.ID); err == nil && verification != nil {
			content["verification_id"] = verification.ID.String()
			content["verified"] = verification.Verified
			content["verification_mode"] = string(verification.Mode)
			content["verification_error"] = verification.Error
			content["verification"] = map[string]any{"id": verification.ID.String(), "mode": string(verification.Mode), "status": string(verification.Status), "verified": verification.Verified, "evidence": verification.Evidence, "evidence_details": verification.EvidenceDetails, "error": verification.Error}
		}
	}
	tags := gonostr.Tags{{"run", run.ID.String()}, {"recipe_id", run.RecipeID.String()}, {"repository_id", run.RepositoryID.String()}, {"status", string(run.Status)}, {"backend", string(run.Backend)}, {"verification", string(run.VerificationStatus)}, {"restore_eligible", fmt.Sprintf("%t", restoreEligible)}, {"restore_eligibility", string(run.RestoreEligibility)}, {"failure_category", string(run.FailureCategory)}}
	if run.PolicyID != nil {
		tags = append(tags, gonostr.Tag{"policy", run.PolicyID.String()}, gonostr.Tag{"policy_id", run.PolicyID.String()})
	}
	return p.publishReplaceableJSON(ctx, KindBackupRunState, dTag, tags, content, "backup.run_state.projection", &run.ID)
}

func (p *Projector) publishBackupRestoreState(ctx context.Context, restore *domain.BackupRestoreRun) error {
	if restore == nil {
		return nil
	}
	dTag := "backup-restore:" + restore.ID.String()
	pendingApproval := restore.ApprovalStatus == domain.BackupApprovalPending
	content := map[string]any{"deleted": false, "id": restore.ID.String(), "backup_run_id": restore.BackupRunID.String(), "recipe_id": restore.RecipeID.String(), "repository_id": restore.RepositoryID.String(), "policy_id": uuidStringPtr(restore.PolicyID), "snapshot_id": restore.SnapshotID, "restore_target_ref": restore.RestoreTargetRef, "requested_by": restore.RequestedBy, "request_event_id": restore.RequestEventID, "request_kind": restore.RequestKind, "request_d_tag": restore.RequestDTag, "approval_status": string(restore.ApprovalStatus), "approval_required": restore.ApprovalRequired, "approval_requirement": string(restore.ApprovalRequirement), "pending_approval": pendingApproval, "approval_event_id": restore.ApprovalEventID, "approved_by": restore.ApprovedBy, "approved_at": restore.ApprovedAt, "approval_message": restore.ApprovalMessage, "approval_reason_code": restore.ApprovalReasonCode, "approval_reason": restore.ApprovalReason, "status": string(restore.Status), "backend": string(restore.Backend), "verification_status": string(restore.VerificationStatus), "evidence": restore.Evidence, "publish_summary": restore.PublishSummary, "error": restore.Error, "verification_policy_failure": restore.VerificationPolicyFailure, "failure_category": string(restore.FailureCategory), "metadata": restore.Metadata, "started_at": restore.StartedAt, "finished_at": restore.FinishedAt, "created_at": formatTime(restore.CreatedAt), "updated_at": formatTime(restore.UpdatedAt)}
	tags := gonostr.Tags{{"restore", restore.ID.String()}, {"restore_id", restore.ID.String()}, {"run", restore.BackupRunID.String()}, {"backup_run_id", restore.BackupRunID.String()}, {"recipe_id", restore.RecipeID.String()}, {"repository_id", restore.RepositoryID.String()}, {"status", string(restore.Status)}, {"approval", string(restore.ApprovalStatus)}, {"approval_required", fmt.Sprintf("%t", restore.ApprovalRequired)}, {"approval_requirement", string(restore.ApprovalRequirement)}, {"pending_approval", fmt.Sprintf("%t", pendingApproval)}, {"verification", string(restore.VerificationStatus)}, {"backend", string(restore.Backend)}, {"failure_category", string(restore.FailureCategory)}}
	if restore.PolicyID != nil {
		tags = append(tags, gonostr.Tag{"policy", restore.PolicyID.String()}, gonostr.Tag{"policy_id", restore.PolicyID.String()})
	}
	return p.publishReplaceableJSON(ctx, KindBackupRestoreState, dTag, tags, content, "backup.restore_state.projection", &restore.ID)
}

func (p *Projector) publishBackupVerificationState(ctx context.Context, record *domain.BackupVerificationRecord) error {
	if record == nil {
		return nil
	}
	dTag := "backup-verification:" + record.BackupRunID.String()
	tags := gonostr.Tags{{"run", record.BackupRunID.String()}, {"verification_id", record.ID.String()}, {"verification", string(record.Status)}, {"status", string(record.Status)}, {"mode", string(record.Mode)}, {"verified", fmt.Sprintf("%t", record.Verified)}}
	return p.publishReplaceableJSON(ctx, KindBackupVerificationState, dTag, tags, map[string]any{"deleted": false, "id": record.ID.String(), "backup_run_id": record.BackupRunID.String(), "mode": string(record.Mode), "status": string(record.Status), "verified": record.Verified, "evidence": record.Evidence, "evidence_details": record.EvidenceDetails, "error": record.Error, "publish_summary": record.PublishSummary, "verified_at": record.VerifiedAt, "created_at": formatTime(record.CreatedAt), "updated_at": formatTime(record.UpdatedAt)}, "backup.verification_state.projection", &record.ID)
}

func (p *Projector) publishBackupRetentionState(ctx context.Context, run *domain.BackupRetentionRun) error {
	if run == nil {
		return nil
	}
	dTag := "backup-retention:" + run.ID.String()
	content := map[string]any{"deleted": false, "id": run.ID.String(), "repository_id": run.RepositoryID.String(), "policy_id": uuidStringPtr(run.PolicyID), "requested_by": run.RequestedBy, "request_event_id": run.RequestEventID, "request_kind": run.RequestKind, "request_d_tag": run.RequestDTag, "status": string(run.Status), "backend": string(run.Backend), "dry_run": run.DryRun, "evidence": run.Evidence, "publish_summary": run.PublishSummary, "error": run.Error, "failure_category": string(run.FailureCategory), "metadata": run.Metadata, "started_at": run.StartedAt, "finished_at": run.FinishedAt, "created_at": formatTime(run.CreatedAt), "updated_at": formatTime(run.UpdatedAt)}
	tags := gonostr.Tags{{"retention", run.ID.String()}, {"retention_run_id", run.ID.String()}, {"repository_id", run.RepositoryID.String()}, {"status", string(run.Status)}, {"backend", string(run.Backend)}, {"dry_run", fmt.Sprintf("%t", run.DryRun)}, {"failure_category", string(run.FailureCategory)}}
	if run.PolicyID != nil {
		tags = append(tags, gonostr.Tag{"policy", run.PolicyID.String()}, gonostr.Tag{"policy_id", run.PolicyID.String()})
	}
	return p.publishReplaceableJSON(ctx, KindBackupRetentionRegistry, dTag, tags, content, "backup.retention_state.projection", &run.ID)
}

func (p *Projector) publishBackupRuntimeObservation(ctx context.Context) error {
	if p.backupSource == nil {
		return nil
	}
	const pageSize = 500
	now := time.Now().UTC()
	staleTimeout := p.backupStaleTimeout
	if staleTimeout <= 0 {
		staleTimeout = 15 * time.Minute
	}
	staleCutoff := now.Add(-staleTimeout)
	counts := map[string]map[string]int{"runs": {}, "restores": {}, "retention": {}}
	failureCategories := map[string]map[string]int{"runs": {}, "restores": {}, "retention": {}}
	backendHealthFailures := make([]map[string]any, 0)
	pendingApprovals := make([]map[string]any, 0)
	last := map[string]any{}
	for offset := 0; ; offset += pageSize {
		runs, err := p.backupSource.ListBackupRuns(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup runs for runtime observation: %w", err)
		}
		for i := range runs {
			run := runs[i]
			countBackupStatus(counts["runs"], run.Status, run.UpdatedAt, staleCutoff)
			countFailureCategory(failureCategories["runs"], run.FailureCategory)
			if run.Status == domain.RunStatusSucceeded {
				updateLastBackupOutcome(last, "last_successful_run", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{"status": string(run.Status), "repository_id": run.RepositoryID.String(), "recipe_id": run.RecipeID.String(), "snapshot_id": run.SnapshotID, "restore_eligibility": string(run.RestoreEligibility)})
			}
			if run.VerificationStatus == domain.BackupVerificationSucceeded {
				updateLastBackupOutcome(last, "last_verification", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{"status": string(run.VerificationStatus), "mode": string(run.VerificationMode), "repository_id": run.RepositoryID.String(), "recipe_id": run.RecipeID.String()})
			}
			if run.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{"type": "run", "id": run.ID.String(), "repository_id": run.RepositoryID.String(), "error": run.Error, "updated_at": formatTime(run.UpdatedAt)})
			}
		}
		if len(runs) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		restores, err := p.backupSource.ListBackupRestores(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup restores for runtime observation: %w", err)
		}
		for i := range restores {
			restore := restores[i]
			countBackupStatus(counts["restores"], restore.Status, restore.UpdatedAt, staleCutoff)
			if restore.ApprovalStatus == domain.BackupApprovalPending {
				counts["restores"]["pending_approval"]++
				if len(pendingApprovals) < 20 {
					pendingApprovals = append(pendingApprovals, map[string]any{"restore_id": restore.ID.String(), "backup_run_id": restore.BackupRunID.String(), "repository_id": restore.RepositoryID.String(), "requested_by": restore.RequestedBy, "created_at": formatTime(restore.CreatedAt), "approval_requirement": string(restore.ApprovalRequirement)})
				}
			}
			countFailureCategory(failureCategories["restores"], restore.FailureCategory)
			if backupProjectionTerminalStatus(restore.Status) {
				updateLastBackupOutcome(last, "last_restore", restore.ID.String(), restore.FinishedAt, restore.CreatedAt, map[string]any{"status": string(restore.Status), "approval_status": string(restore.ApprovalStatus), "repository_id": restore.RepositoryID.String(), "backup_run_id": restore.BackupRunID.String(), "verification_status": string(restore.VerificationStatus), "failure_category": string(restore.FailureCategory)})
			}
			if restore.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{"type": "restore", "id": restore.ID.String(), "repository_id": restore.RepositoryID.String(), "error": restore.Error, "updated_at": formatTime(restore.UpdatedAt)})
			}
		}
		if len(restores) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		retentions, err := p.backupSource.ListBackupRetentionRuns(ctx, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list backup retention runs for runtime observation: %w", err)
		}
		for i := range retentions {
			run := retentions[i]
			countBackupStatus(counts["retention"], run.Status, run.UpdatedAt, staleCutoff)
			countFailureCategory(failureCategories["retention"], run.FailureCategory)
			if backupProjectionTerminalStatus(run.Status) {
				updateLastBackupOutcome(last, "last_retention", run.ID.String(), run.FinishedAt, run.CreatedAt, map[string]any{"status": string(run.Status), "repository_id": run.RepositoryID.String(), "dry_run": run.DryRun, "failure_category": string(run.FailureCategory)})
			}
			if run.FailureCategory == domain.BackupFailureBackendHealth && len(backendHealthFailures) < 20 {
				backendHealthFailures = append(backendHealthFailures, map[string]any{"type": "retention", "id": run.ID.String(), "repository_id": run.RepositoryID.String(), "error": run.Error, "updated_at": formatTime(run.UpdatedAt)})
			}
		}
		if len(retentions) < pageSize {
			break
		}
	}
	payload := map[string]any{"deleted": false, "scope": "fleet", "generated_at": formatTime(now), "stale_after_seconds": int(staleTimeout.Seconds()), "counts": counts, "last_outcomes": last, "failure_categories": failureCategories, "backend_health_failures": backendHealthFailures, "pending_restore_approvals": pendingApprovals}
	tags := gonostr.Tags{{"scope", "fleet"}, {"status", "summary"}, {"pending_approval", fmt.Sprintf("%d", counts["restores"]["pending_approval"])}}
	return p.publishReplaceableJSON(ctx, KindBackupRuntimeObservationState, "backup-runtime:fleet", tags, payload, "backup.runtime_observation.projection", nil)
}

func countBackupStatus(counts map[string]int, status domain.DeploymentRunStatus, updatedAt time.Time, staleCutoff time.Time) {
	counts[string(status)]++
	if status == domain.RunStatusRunning && updatedAt.Before(staleCutoff) {
		counts["stale"]++
	}
}

func countFailureCategory(counts map[string]int, category domain.BackupFailureCategory) {
	if category != domain.BackupFailureNone {
		counts[string(category)]++
	}
}

func backupProjectionTerminalStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func updateLastBackupOutcome(last map[string]any, key, id string, finishedAt *time.Time, createdAt time.Time, values map[string]any) {
	candidate := createdAt
	if finishedAt != nil && !finishedAt.IsZero() {
		candidate = *finishedAt
	}
	if existing, ok := last[key].(map[string]any); ok {
		if raw, ok := existing["at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, raw); err == nil && !candidate.After(parsed) {
				return
			}
		}
	}
	values["id"] = id
	values["at"] = formatTime(candidate)
	last[key] = values
}

func unitTagValue(id *uuid.UUID) string {
	if id == nil {
		return domain.DefaultDeploymentUnitKey
	}
	return id.String()
}

func uuidStringPtr(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func (p *Projector) publishMLModelRegistry(ctx context.Context, model *domain.MLModel) error {
	if model == nil || model.Slug == "" {
		return nil
	}
	dTag := "model:" + model.Slug
	tags := gonostr.Tags{{"d", dTag}, {"model", dTag}, {"name", model.Name}, {"deleted", "false"}}
	if model.Family != "" {
		tags = append(tags, gonostr.Tag{"family", model.Family})
	}
	for _, modality := range model.Modalities {
		tags = append(tags, gonostr.Tag{"modality", modality})
	}
	for _, task := range model.TaskKinds {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	for _, capability := range model.Capabilities {
		tags = append(tags, gonostr.Tag{"capability", capability})
	}
	if model.License != "" {
		tags = append(tags, gonostr.Tag{"license", model.License})
	}
	return p.publishReplaceableJSON(ctx, KindMLModelRegistry, dTag, tags[1:], model, "ml_model.projection", &model.ID)
}

func (p *Projector) publishMLModelVersionRegistry(ctx context.Context, version *domain.MLModelVersion) error {
	if p.mlSource == nil || version == nil {
		return nil
	}
	model, err := p.mlSource.GetModel(ctx, version.ModelID)
	if err != nil || model == nil || model.Slug == "" {
		if err != nil {
			return err
		}
		return nil
	}
	dTag := fmt.Sprintf("model-version:%s:%s", model.Slug, version.Version)
	tags := gonostr.Tags{{"model", "model:" + model.Slug}, {"model_id", version.ModelID.String()}, {"model_version", dTag}, {"version", version.Version}}
	for _, format := range version.RuntimeRequirements.RequiredFormats {
		tags = append(tags, gonostr.Tag{"format", string(format)})
	}
	for _, runtime := range version.RuntimeRequirements.PreferredRuntimes {
		tags = append(tags, gonostr.Tag{"runtime", string(runtime)})
	}
	return p.publishReplaceableJSON(ctx, KindMLModelVersionRegistry, dTag, tags, version, "ml_model_version.projection", &version.ID)
}

func (p *Projector) publishMLInferenceEndpointRegistry(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	if endpoint == nil {
		return nil
	}
	envName, ok, err := p.environmentNameForMLProjection(ctx, endpoint.EnvironmentID)
	if err != nil || !ok {
		return err
	}
	dTag := fmt.Sprintf("endpoint:%s:%s", endpoint.Name, envName)
	tags := gonostr.Tags{{"endpoint", dTag}, {"endpoint_id", endpoint.ID.String()}, {"environment", envName}, {"environment_id", endpoint.EnvironmentID.String()}, {"name", endpoint.Name}}
	for _, task := range endpoint.TaskKinds {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	if endpoint.Protocol != "" {
		tags = append(tags, gonostr.Tag{"protocol", endpoint.Protocol})
	}
	return p.publishReplaceableJSON(ctx, KindMLInferenceEndpointRegistry, dTag, tags, endpoint, "ml_endpoint.projection", &endpoint.ID)
}

func (p *Projector) publishMLInferenceEndpointState(ctx context.Context, state *domain.MLInferenceState) error {
	if p.mlSource == nil || state == nil {
		return nil
	}
	endpoint, err := p.mlSource.GetInferenceEndpoint(ctx, state.EndpointID)
	if err != nil || endpoint == nil {
		return err
	}
	envName, ok, err := p.environmentNameForMLProjection(ctx, state.EnvironmentID)
	if err != nil || !ok {
		return err
	}
	dTag := fmt.Sprintf("endpoint-state:%s:%s", endpoint.Name, envName)
	tags := gonostr.Tags{{"endpoint", fmt.Sprintf("endpoint:%s:%s", endpoint.Name, envName)}, {"endpoint_id", state.EndpointID.String()}, {"environment", envName}, {"environment_id", state.EnvironmentID.String()}, {"drift_status", string(state.DriftStatus)}, {"gateway_status", string(state.GatewayStatus)}}
	if state.DesiredModelVersionID != nil {
		tags = append(tags, gonostr.Tag{"model_version", state.DesiredModelVersionID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, gonostr.Tag{"deployment", state.DesiredIntentID.String()}, gonostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.ActiveRunID != nil {
		tags = append(tags, gonostr.Tag{"run", state.ActiveRunID.String()})
	}
	if state.RuntimeKind != "" {
		tags = append(tags, gonostr.Tag{"runtime", string(state.RuntimeKind)})
	}
	return p.publishReplaceableJSON(ctx, KindMLInferenceEndpointState, dTag, tags, state, "ml_endpoint_state.projection", &state.EndpointID)
}

func (p *Projector) environmentNameForMLProjection(ctx context.Context, envID uuid.UUID) (string, bool, error) {
	if p.source == nil || envID == uuid.Nil {
		return "", false, nil
	}
	env, err := p.source.GetEnvironment(ctx, envID)
	if err != nil || env == nil || env.Name == "" {
		return "", false, err
	}
	return env.Name, true, nil
}

func (p *Projector) publishMLArtifactProvenanceGraph(ctx context.Context, artifact *domain.MLArtifactRef) error {
	if p.mlSource == nil || artifact == nil {
		return nil
	}
	edges, err := p.mlSource.ListProvenanceEdgesByArtifact(ctx, artifact.ID)
	if err != nil {
		return err
	}
	digest := artifact.SHA256
	if digest == "" {
		digest = artifact.ID.String()
	}
	dTag := "artifact:" + digest
	content := map[string]any{"artifact": artifact, "edges": edges}
	tags := gonostr.Tags{{"artifact", artifact.ID.String()}}
	if artifact.SHA256 != "" {
		tags = append(tags, gonostr.Tag{"sha256", artifact.SHA256})
	}
	if artifact.ModelVersionID != nil {
		tags = append(tags, gonostr.Tag{"model_version", artifact.ModelVersionID.String()})
	}
	if artifact.Format != "" {
		tags = append(tags, gonostr.Tag{"format", string(artifact.Format)})
	}
	return p.publishReplaceableJSON(ctx, KindMLArtifactProvenanceGraph, dTag, tags, content, "ml_artifact_provenance.projection", &artifact.ID)
}

func (p *Projector) publishMLRuntimeCapabilityProfile(ctx context.Context, worker *domain.Worker) error {
	if worker == nil || worker.PubKey == "" {
		return nil
	}
	dTag := fmt.Sprintf("worker:%s:ai-capability", worker.PubKey)
	tags := gonostr.Tags{{"worker", worker.PubKey}, {"role", "worker"}, {"status", string(worker.Status)}}
	for _, runtime := range worker.MLCapabilities.Runtimes {
		tags = append(tags, gonostr.Tag{"runtime", string(runtime)})
	}
	for _, format := range worker.MLCapabilities.ArtifactFormats {
		tags = append(tags, gonostr.Tag{"artifact_format", string(format)})
	}
	for _, task := range worker.MLCapabilities.Tasks {
		tags = append(tags, gonostr.Tag{"task", string(task)})
	}
	for _, accelerator := range worker.MLCapabilities.Accelerators {
		tags = append(tags, gonostr.Tag{"accelerator", accelerator})
	}
	for _, toolchain := range worker.MLCapabilities.Toolchains {
		tags = append(tags, gonostr.Tag{"toolchain", toolchain})
	}
	if worker.Resources != nil && worker.Resources.MemoryGB > 0 {
		tags = append(tags, gonostr.Tag{"ram_gb", fmt.Sprintf("%d", worker.Resources.MemoryGB)})
	}
	for _, accelerator := range worker.Accelerators {
		if accelerator.Model != "" {
			tags = append(tags, gonostr.Tag{"gpu", accelerator.Model})
		}
		if accelerator.MemoryGB > 0 {
			tags = append(tags, gonostr.Tag{"vram_gb", fmt.Sprintf("%d", accelerator.MemoryGB)})
		}
		if accelerator.Driver != "" {
			tags = append(tags, gonostr.Tag{"driver", accelerator.Driver})
		}
	}
	return p.publishReplaceableJSON(ctx, KindMLRuntimeCapabilityProfile, dTag, tags, worker, "ml_runtime_capability.projection", nil)
}

func (p *Projector) publishWorkerAssignmentState(ctx context.Context, state *domain.WorkerAssignmentState) error {
	if state == nil || state.WorkerPubKey == "" {
		return nil
	}
	tags := gonostr.Tags{{"worker", state.WorkerPubKey}, {"assignment_count", fmt.Sprintf("%d", len(state.ActiveAssignments))}}
	for _, assignment := range state.ActiveAssignments {
		if assignment.Type != "" {
			tags = append(tags, gonostr.Tag{"assignment_type", string(assignment.Type)})
		}
		if assignment.WorkloadID != "" {
			tags = append(tags, gonostr.Tag{"workload", assignment.WorkloadID})
		}
		if assignment.Status != "" {
			tags = append(tags, gonostr.Tag{"status", assignment.Status})
		}
		if assignment.Pinned {
			tags = append(tags, gonostr.Tag{"pinned", "true"})
		}
	}
	return p.publishReplaceableJSON(ctx, KindWorkerAssignmentState, state.WorkerPubKey, tags, state, "worker_assignment_state.projection", nil)
}

func (p *Projector) publishWorkerDrainStatus(ctx context.Context, status *domain.WorkerDrainStatus) error {
	if status == nil || status.WorkerPubKey == "" {
		return nil
	}
	tags := gonostr.Tags{{"worker", status.WorkerPubKey}, {"scheduling_state", string(status.SchedulingState)}, {"safe_to_enter_maintenance", fmt.Sprintf("%t", status.SafeToEnterMaintenance)}, {"safe_to_disable", fmt.Sprintf("%t", status.SafeToDisable)}, {"remaining", fmt.Sprintf("%d", len(status.RemainingAssignments))}, {"pinned_blockers", fmt.Sprintf("%d", len(status.PinnedBlockers))}}
	return p.publishReplaceableJSON(ctx, KindWorkerDrainStatus, status.WorkerPubKey, tags, status, "worker_drain_status.projection", nil)
}

func (p *Projector) publishWorkerReadModelsForWorker(ctx context.Context, workerPubKey string) {
	if p.workerReadModelSource == nil || strings.TrimSpace(workerPubKey) == "" {
		return
	}
	assignment, err := p.workerReadModelSource.GetAssignmentState(ctx, workerPubKey)
	if err != nil {
		p.logger.Warn("read worker assignment state for projection failed", zap.String("worker", workerPubKey), zap.Error(err))
	} else if assignment != nil {
		if err := p.publishWorkerAssignmentState(ctx, assignment); err != nil {
			p.logger.Warn("publish worker assignment state failed", zap.String("worker", workerPubKey), zap.Error(err))
		}
	}
	drain, err := p.workerReadModelSource.GetDrainStatus(ctx, workerPubKey)
	if err != nil {
		p.logger.Warn("read worker drain status for projection failed", zap.String("worker", workerPubKey), zap.Error(err))
	} else if drain != nil {
		if err := p.publishWorkerDrainStatus(ctx, drain); err != nil {
			p.logger.Warn("publish worker drain status failed", zap.String("worker", workerPubKey), zap.Error(err))
		}
	}
}

func (p *Projector) publishWorkerReadModelSnapshots(ctx context.Context) (assignmentsPublished, drainsPublished int) {
	if p.workerReadModelSource == nil {
		return
	}
	assignments, err := p.workerReadModelSource.ListAssignmentStates(ctx)
	if err != nil {
		p.logger.Warn("list worker assignment states for projection failed", zap.Error(err))
	} else {
		for i := range assignments {
			if err := p.publishWorkerAssignmentState(ctx, &assignments[i]); err != nil {
				p.logger.Warn("publish worker assignment state failed", zap.String("worker", assignments[i].WorkerPubKey), zap.Error(err))
			} else {
				assignmentsPublished++
			}
		}
	}
	drains, err := p.workerReadModelSource.ListDrainStatuses(ctx)
	if err != nil {
		p.logger.Warn("list worker drain statuses for projection failed", zap.Error(err))
	} else {
		for i := range drains {
			if err := p.publishWorkerDrainStatus(ctx, &drains[i]); err != nil {
				p.logger.Warn("publish worker drain status failed", zap.String("worker", drains[i].WorkerPubKey), zap.Error(err))
			} else {
				drainsPublished++
			}
		}
	}
	return
}

func (p *Projector) publishLLMRouteRegistry(ctx context.Context, route *domain.LLMRoute, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      route.ID.String(),
	}
	if !deleted {
		content["name"] = route.Name
		content["description"] = route.Description
		content["gateway_config"] = route.GatewayConfig
		content["default_placement_policy"] = route.DefaultPlacementPolicy
		content["default_promotion_gate"] = route.DefaultPromotionGate
		content["metadata"] = route.Metadata
		content["created_at"] = formatTime(route.CreatedAt)
		content["updated_at"] = formatTime(route.UpdatedAt)
	} else {
		content["updated_at"] = formatTime(route.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{{"d", canonicalStateDTag("llm", "route", route.ID.String())}, {"route", route.ID.String()}, {"deleted", fmt.Sprintf("%t", deleted)}}
	if !deleted {
		tags = append(tags, gonostr.Tag{"name", route.Name})
		if route.GatewayConfig != nil && route.GatewayConfig.PublicModel != "" {
			tags = append(tags, gonostr.Tag{"model", route.GatewayConfig.PublicModel})
		}
	}
	return p.publishSigned(ctx, KindCASControlState, append(gonostr.Tags{{"domain", "llm"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindLLMRouteRegistry)}}, tags...), string(contentJSON), "llm_route.projection", &route.ID)
}

func (p *Projector) publishLLMRouteState(ctx context.Context, state *domain.LLMRouteState) error {
	content := map[string]any{
		"deleted":          false,
		"route_id":         state.RouteID.String(),
		"environment_id":   state.EnvironmentID.String(),
		"drift_status":     string(state.DriftStatus),
		"gateway_status":   string(state.GatewayStatus),
		"backend_kind":     string(state.BackendKind),
		"backend_endpoint": state.BackendEndpoint,
		"backend_health":   string(state.BackendHealth),
		"gateway_target":   state.GatewayTarget,
		"updated_at":       formatTime(state.UpdatedAt),
	}
	if state.DesiredReleaseID != nil {
		content["desired_release_id"] = state.DesiredReleaseID.String()
	}
	if state.DesiredIntentID != nil {
		content["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.ActiveRunID != nil {
		content["active_run_id"] = state.ActiveRunID.String()
	}
	if state.CurrentObservationID != nil {
		content["current_observation_id"] = state.CurrentObservationID.String()
	}
	if state.LastReconciledAt != nil {
		content["last_reconciled_at"] = formatTime(*state.LastReconciledAt)
	}
	contentJSON, _ := json.Marshal(content)
	dTag := fmt.Sprintf("%s:%s", state.RouteID, state.EnvironmentID)
	tags := gonostr.Tags{
		{"d", dTag},
		{"route", state.RouteID.String()},
		{"environment", state.EnvironmentID.String()},
		{"deleted", "false"},
		{"drift_status", string(state.DriftStatus)},
		{"gateway_status", string(state.GatewayStatus)},
	}
	if state.DesiredReleaseID != nil {
		tags = append(tags, gonostr.Tag{"release", state.DesiredReleaseID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, gonostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.ActiveRunID != nil {
		tags = append(tags, gonostr.Tag{"run", state.ActiveRunID.String()})
	}
	if state.BackendKind != "" {
		tags = append(tags, gonostr.Tag{"backend", string(state.BackendKind)})
	}
	tags[0] = gonostr.Tag{"d", canonicalStateDTag("llm", "state", dTag)}
	return p.publishSigned(ctx, KindCASControlState, append(gonostr.Tags{{"domain", "llm"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindLLMRouteState)}}, tags...), string(contentJSON), "llm_route_state.projection", &state.RouteID)
}

func (p *Projector) publishLLMRouteStateTombstone(ctx context.Context, res events.ResourceData) error {
	routeID, routeOK := parseUUID(res.RouteID)
	envID, envOK := parseUUID(res.EnvironmentID)
	if !routeOK || !envOK {
		return nil
	}
	content, _ := json.Marshal(map[string]any{"deleted": true, "route_id": routeID.String(), "environment_id": envID.String(), "updated_at": formatTime(time.Now().UTC())})
	dTag := fmt.Sprintf("%s:%s", routeID, envID)
	tags := gonostr.Tags{{"d", dTag}, {"route", routeID.String()}, {"environment", envID.String()}, {"deleted", "true"}}
	tags[0] = gonostr.Tag{"d", canonicalStateDTag("llm", "state", dTag)}
	return p.publishSigned(ctx, KindCASControlState, append(gonostr.Tags{{"domain", "llm"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindLLMRouteState)}}, tags...), string(content), "llm_route_state.projection", &routeID)
}

func (p *Projector) publishState(ctx context.Context, state *domain.EnvironmentServiceState) error {
	content := map[string]any{
		"deleted":            false,
		"service_id":         state.ServiceID.String(),
		"environment_id":     state.EnvironmentID.String(),
		"deployment_unit_id": uuidStringPtr(state.DeploymentUnitID),
		"drift_status":       string(state.DriftStatus),
		"updated_at":         formatTime(state.UpdatedAt),
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
		content["last_reconciled_at"] = formatTime(*state.LastReconciledAt)
	}
	// Desired-state metadata (additive — old decoders ignore unknown fields).
	if state.DesiredHash != "" {
		content["desired_hash"] = state.DesiredHash
	}
	observedHash := p.latestObservedHash(ctx, state)
	if observedHash != "" {
		content["observed_hash"] = observedHash
	}
	if state.DesiredRuntimeState != nil {
		if renderer := desiredStateRenderer(state.DesiredRuntimeState); renderer != "" {
			content["renderer"] = renderer
		}
		if target := desiredStateTarget(state.DesiredRuntimeState); target != "" {
			content["target"] = target
		}
	}

	contentJSON, _ := json.Marshal(content)
	dTag := fmt.Sprintf("%s:%s", state.ServiceID, state.EnvironmentID)
	tags := gonostr.Tags{
		{"d", dTag},
		{"service", state.ServiceID.String()},
		{"environment", state.EnvironmentID.String()},
		{"unit", unitTagValue(state.DeploymentUnitID)},
		{"deleted", "false"},
		{"drift_status", string(state.DriftStatus)},
	}
	if state.DesiredArtifactID != nil {
		tags = append(tags, gonostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, gonostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.LastSuccessfulRunID != nil {
		tags = append(tags, gonostr.Tag{"run", state.LastSuccessfulRunID.String()})
	}
	if state.DesiredHash != "" {
		tags = append(tags, gonostr.Tag{"desired_hash", state.DesiredHash})
	}
	if observedHash != "" {
		tags = append(tags, gonostr.Tag{"observed_hash", observedHash})
	}
	tags[0] = gonostr.Tag{"d", canonicalStateDTag("service", "state", dTag)}
	return p.publishSigned(ctx, KindCASControlState, append(gonostr.Tags{{"domain", "service"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindServiceState)}}, tags...), string(contentJSON), "state.projection", &state.ServiceID)
}

func (p *Projector) latestObservedHash(ctx context.Context, state *domain.EnvironmentServiceState) string {
	if p == nil || state == nil || state.CurrentObservationID == nil {
		return ""
	}
	for _, source := range []any{p.snapshotSource(), p.source} {
		obsSource, ok := source.(latestObservationSource)
		if !ok || obsSource == nil {
			continue
		}
		obs, err := obsSource.GetLatestObservation(ctx, state.ServiceID, state.EnvironmentID)
		if err != nil || obs == nil || obs.ID != *state.CurrentObservationID {
			continue
		}
		if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash != "" {
			return obs.NormalizedState.ObservationHash
		}
		if obs.NormalizedHash != "" {
			return obs.NormalizedHash
		}
	}
	return ""
}

func (p *Projector) publishStateTombstone(ctx context.Context, res events.ResourceData) error {
	serviceID, serviceOK := parseUUID(res.ServiceID)
	envID, envOK := parseUUID(res.EnvironmentID)
	if !serviceOK || !envOK {
		return nil
	}
	content, _ := json.Marshal(map[string]any{
		"deleted":        true,
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"updated_at":     formatTime(time.Now().UTC()),
	})
	dTag := fmt.Sprintf("%s:%s", serviceID, envID)
	tags := gonostr.Tags{
		{"d", dTag},
		{"service", serviceID.String()},
		{"environment", envID.String()},
		{"unit", domain.DefaultDeploymentUnitKey},
		{"deleted", "true"},
	}
	tags[0] = gonostr.Tag{"d", canonicalStateDTag("service", "state", dTag)}
	return p.publishSigned(ctx, KindCASControlState, append(gonostr.Tags{{"domain", "service"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", strconv.Itoa(KindServiceState)}}, tags...), string(content), "state.projection", &serviceID)
}

func (p *Projector) publishAudit(ctx context.Context, e events.Event) error {
	legacyKind := auditKindForEvent(e.Type)
	if legacyKind == 0 {
		return nil
	}
	res := resourceFromEvent(e)
	content, _ := json.Marshal(map[string]any{
		"event_type": string(e.Type),
		"entity_id":  e.EntityID,
		"data":       e.Data,
	})
	domainName := auditDomainForEvent(e.Type)
	tags := gonostr.Tags{
		{"domain", domainName},
		{"type", string(e.Type)},
		{"schema", "bahia.audit.v1"},
		{"legacy_kind", strconv.Itoa(legacyKind)},
		{"protected", "true"},
		{"-"},
		{"t", string(e.Type)},
		{"event_type", string(e.Type)},
	}
	if e.EntityID != "" {
		tags = append(tags, gonostr.Tag{"d", e.EntityID})
	}
	tags = appendResourceTags(tags, res)
	tags = appendDNSAuditTags(tags, e.Data)
	entityID := auditEntityID(e.Type, e.EntityID, res)
	return p.publishSigned(ctx, KindCASAudit, tags, string(content), string(e.Type), entityID)
}

func auditDomainForEvent(t events.EventType) string {
	s := string(t)
	if idx := strings.Index(s, "."); idx > 0 {
		return s[:idx]
	}
	return "control_plane"
}

func auditEntityID(t events.EventType, raw string, res events.ResourceData) *uuid.UUID {
	if isLLMEvent(t) {
		return firstParsedUUID(raw, res.RouteID, res.ReleaseID, res.EnvironmentID, res.IntentID, res.RunID)
	}
	return firstParsedUUID(raw, res.ServiceID, res.EnvironmentID, res.IntentID, res.RunID, res.ArtifactID)
}

func isLLMEvent(t events.EventType) bool {
	switch t {
	case events.EventLLMRouteCreated, events.EventLLMRouteUpdated,
		events.EventLLMReleaseRegistered,
		events.EventLLMDeploymentIntentCreated, events.EventLLMDeploymentIntentApproved, events.EventLLMDeploymentIntentRejected,
		events.EventLLMDeploymentRunCreated, events.EventLLMDeploymentRunStatusChanged, events.EventLLMDeploymentRunCompleted,
		events.EventLLMRouteObservation, events.EventLLMRouteStateChanged, events.EventLLMRouteDriftDetected, events.EventLLMGatewayRouteSynced:
		return true
	default:
		return false
	}
}

func (p *Projector) publishSigned(ctx context.Context, kind int, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	ev := gonostr.Event{
		Kind:      kind,
		CreatedAt: gonostr.Now(),
		Tags:      tags,
		Content:   content,
	}
	if err := ev.Sign(p.privateKey); err != nil {
		return fmt.Errorf("sign event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publish event: no relays accepted event kind %d", kind)
	}
	if p.eventRepo != nil {
		tagsJSON, _ := json.Marshal(ev.Tags)
		if _, err := p.eventRepo.Record(ctx, &repository.NostrEventRecord{
			ID:         ev.ID,
			Kind:       ev.Kind,
			PubKey:     ev.PubKey,
			Content:    ev.Content,
			Tags:       tagsJSON,
			Sig:        ev.Sig,
			CreatedAt:  ev.CreatedAt.Time(),
			ReceivedAt: time.Now().UTC(),
			EntityType: entityType,
			EntityID:   entityID,
		}); err != nil {
			p.logger.Warn("failed to record projected Nostr event", zap.String("event_id", ev.ID), zap.Int("kind", ev.Kind), zap.Error(err))
		}
	}
	p.logger.Debug("projected Nostr event published", zap.Int("kind", kind), zap.String("event_id", ev.ID), zap.Int("relays", published))
	return nil
}

func auditKindForEvent(t events.EventType) int {
	switch t {
	case events.EventBuildRegistered:
		return KindBuildRegistered
	case events.EventArtifactRegistered:
		return KindArtifactRegistered
	case events.EventDeploymentIntentCreated:
		return KindDeploymentCreated
	case events.EventDeploymentRunCompleted:
		return KindDeploymentComplete
	case events.EventDriftDetected:
		return KindDriftDetected
	case events.EventRuntimeObservation:
		return KindObservation
	case events.EventServiceCreated, events.EventServiceUpdated, events.EventServiceDeleted:
		return KindServiceRegistryAudit
	case events.EventEnvironmentCreated, events.EventEnvironmentUpdated, events.EventEnvironmentDeleted:
		return KindEnvironmentRegistryAudit
	case events.EventEnvironmentServiceStateChanged:
		return KindStateChangedAudit
	case events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop:
		return KindRuntimeActionAudit
	case events.EventReconcileCompleted:
		return KindReconcileAudit
	case events.EventAdoptionImported:
		return KindAdoptionAudit
	case events.EventDeploymentIntentApproved, events.EventDeploymentIntentRejected:
		return KindDeploymentApprovalAudit
	case events.EventDeploymentRunCreated, events.EventDeploymentRunStatusChanged:
		return KindDeploymentRunAudit
	case events.EventLLMRouteCreated, events.EventLLMRouteUpdated:
		return KindLLMRouteRegistryAudit
	case events.EventLLMReleaseRegistered:
		return KindLLMReleaseRegisteredAudit
	case events.EventLLMDeploymentIntentCreated, events.EventLLMDeploymentIntentApproved, events.EventLLMDeploymentIntentRejected:
		return KindLLMDeploymentAudit
	case events.EventLLMDeploymentRunCreated, events.EventLLMDeploymentRunStatusChanged, events.EventLLMDeploymentRunCompleted:
		return KindLLMRunAudit
	case events.EventLLMRouteObservation, events.EventLLMRouteStateChanged, events.EventLLMRouteDriftDetected:
		return KindLLMRouteStateAudit
	case events.EventLLMGatewayRouteSynced:
		return KindLLMGatewayAudit
	case eventDNSZoneSynced:
		return KindDNSZoneSyncedAudit
	case eventDNSRecordChanged:
		return KindDNSRecordChangedAudit
	case eventDNSDriftDetected:
		return KindDNSDriftDetectedAudit
	case eventDNSEndpointRegistered:
		return KindDNSEndpointRegisteredAudit
	case eventDNSEndpointDeregistered:
		return KindDNSEndpointDeregisteredAudit
	default:
		return 0
	}
}

func resourceFromEvent(e events.Event) events.ResourceData {
	switch data := e.Data.(type) {
	case events.ResourceData:
		return data
	case *events.ResourceData:
		if data != nil {
			return *data
		}
	case *domain.DeploymentIntent:
		if data != nil {
			return events.ResourceData{ServiceID: data.ServiceID.String(), EnvironmentID: data.EnvironmentID.String(), ArtifactID: data.ArtifactID.String(), IntentID: data.ID.String()}
		}
	case domain.DeploymentIntent:
		return events.ResourceData{ServiceID: data.ServiceID.String(), EnvironmentID: data.EnvironmentID.String(), ArtifactID: data.ArtifactID.String(), IntentID: data.ID.String()}
	case *domain.DeploymentRun:
		if data != nil {
			return events.ResourceData{IntentID: data.DeploymentIntentID.String(), RunID: data.ID.String()}
		}
	case domain.DeploymentRun:
		return events.ResourceData{IntentID: data.DeploymentIntentID.String(), RunID: data.ID.String()}
	case *domain.LLMRoute:
		if data != nil {
			return events.ResourceData{RouteID: data.ID.String()}
		}
	case domain.LLMRoute:
		return events.ResourceData{RouteID: data.ID.String()}
	case *domain.LLMRelease:
		if data != nil {
			return events.ResourceData{RouteID: data.RouteID.String(), ReleaseID: data.ID.String()}
		}
	case domain.LLMRelease:
		return events.ResourceData{RouteID: data.RouteID.String(), ReleaseID: data.ID.String()}
	case *domain.LLMDeploymentIntent:
		if data != nil {
			return events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String(), ReleaseID: data.ReleaseID.String(), IntentID: data.ID.String()}
		}
	case domain.LLMDeploymentIntent:
		return events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String(), ReleaseID: data.ReleaseID.String(), IntentID: data.ID.String()}
	case *domain.LLMDeploymentRun:
		if data != nil {
			return events.ResourceData{IntentID: data.DeploymentIntentID.String(), RunID: data.ID.String()}
		}
	case domain.LLMDeploymentRun:
		return events.ResourceData{IntentID: data.DeploymentIntentID.String(), RunID: data.ID.String()}
	case *domain.LLMRouteObservation:
		if data != nil {
			res := events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String()}
			if data.ObservedReleaseID != nil {
				res.ReleaseID = data.ObservedReleaseID.String()
			}
			if data.ObservedRunID != nil {
				res.RunID = data.ObservedRunID.String()
			}
			return res
		}
	case domain.LLMRouteObservation:
		res := events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String()}
		if data.ObservedReleaseID != nil {
			res.ReleaseID = data.ObservedReleaseID.String()
		}
		if data.ObservedRunID != nil {
			res.RunID = data.ObservedRunID.String()
		}
		return res
	case *domain.LLMRouteState:
		if data != nil {
			res := events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String()}
			if data.DesiredReleaseID != nil {
				res.ReleaseID = data.DesiredReleaseID.String()
			}
			if data.DesiredIntentID != nil {
				res.IntentID = data.DesiredIntentID.String()
			}
			if data.ActiveRunID != nil {
				res.RunID = data.ActiveRunID.String()
			}
			return res
		}
	case domain.LLMRouteState:
		res := events.ResourceData{RouteID: data.RouteID.String(), EnvironmentID: data.EnvironmentID.String()}
		if data.DesiredReleaseID != nil {
			res.ReleaseID = data.DesiredReleaseID.String()
		}
		if data.DesiredIntentID != nil {
			res.IntentID = data.DesiredIntentID.String()
		}
		if data.ActiveRunID != nil {
			res.RunID = data.ActiveRunID.String()
		}
		return res
	case *domain.RuntimeObservation:
		if data != nil {
			return events.ResourceData{ServiceID: data.ServiceID.String(), EnvironmentID: data.EnvironmentID.String()}
		}
	case domain.RuntimeObservation:
		return events.ResourceData{ServiceID: data.ServiceID.String(), EnvironmentID: data.EnvironmentID.String()}
	case map[string]string:
		return resourceFromStringMap(data)
	case map[string]any:
		return resourceFromAnyMap(data)
	}
	return resourceFromAnyMap(map[string]any{"entity_id": e.EntityID})
}

func resourceFromStringMap(m map[string]string) events.ResourceData {
	return events.ResourceData{
		ServiceID:     m["service_id"],
		EnvironmentID: m["environment_id"],
		ArtifactID:    m["artifact_id"],
		RouteID:       m["route_id"],
		ReleaseID:     m["release_id"],
		IntentID:      firstString(m["intent_id"], m["deployment_intent_id"]),
		RunID:         firstString(m["run_id"], m["deployment_run_id"]),
	}
}

func resourceFromAnyMap(m map[string]any) events.ResourceData {
	return events.ResourceData{
		ServiceID:     stringify(m["service_id"]),
		EnvironmentID: stringify(m["environment_id"]),
		ArtifactID:    stringify(m["artifact_id"]),
		RouteID:       stringify(m["route_id"]),
		ReleaseID:     stringify(m["release_id"]),
		IntentID:      firstString(stringify(m["intent_id"]), stringify(m["deployment_intent_id"])),
		RunID:         firstString(stringify(m["run_id"]), stringify(m["deployment_run_id"])),
	}
}

func appendDNSAuditTags(tags gonostr.Tags, data any) gonostr.Tags {
	var m map[string]any
	switch value := data.(type) {
	case map[string]any:
		m = value
	case map[string]string:
		m = make(map[string]any, len(value))
		for k, v := range value {
			m[k] = v
		}
	default:
		return tags
	}
	for _, key := range []string{"zone", "backend_ref", "backend", "fqdn", "record_type", "source_coordinate", "operation"} {
		if value := stringify(m[key]); value != "" {
			tagKey := key
			if key == "backend_ref" {
				tagKey = "backend"
			}
			tags = append(tags, gonostr.Tag{tagKey, value})
		}
	}
	return tags
}

func appendResourceTags(tags gonostr.Tags, res events.ResourceData) gonostr.Tags {
	if res.ServiceID != "" {
		tags = append(tags, gonostr.Tag{"service", res.ServiceID})
	}
	if res.EnvironmentID != "" {
		tags = append(tags, gonostr.Tag{"environment", res.EnvironmentID})
	}
	if res.ArtifactID != "" {
		tags = append(tags, gonostr.Tag{"artifact", res.ArtifactID})
	}
	if res.RouteID != "" {
		tags = append(tags, gonostr.Tag{"route", res.RouteID})
	}
	if res.ReleaseID != "" {
		tags = append(tags, gonostr.Tag{"release", res.ReleaseID})
	}
	if res.IntentID != "" {
		tags = append(tags, gonostr.Tag{"intent", res.IntentID})
	}
	if res.RunID != "" {
		tags = append(tags, gonostr.Tag{"run", res.RunID})
	}
	return tags
}

func firstParsedUUID(values ...string) *uuid.UUID {
	for _, value := range values {
		if id, ok := parseUUID(value); ok {
			return &id
		}
	}
	return nil
}

func firstUUID(values ...string) string {
	for _, value := range values {
		if _, ok := parseUUID(value); ok {
			return value
		}
	}
	return ""
}

func parseUUID(raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case uuid.UUID:
		return val.String()
	case *uuid.UUID:
		if val != nil {
			return val.String()
		}
	case fmt.Stringer:
		return val.String()
	}
	return ""
}

func stringifyMapValue(data any, key string) string {
	switch m := data.(type) {
	case map[string]any:
		return stringify(m[key])
	case map[string]string:
		return m[key]
	}
	return ""
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// desiredStateRenderer determines the renderer name from a DesiredServiceSpec
// based on which extension is populated.
func desiredStateRenderer(spec *domain.DesiredServiceSpec) string {
	if spec == nil {
		return ""
	}
	if spec.ComposeExtension != nil {
		return "compose"
	}
	if spec.DockerExtension != nil {
		return "docker"
	}
	if spec.KubernetesExtension != nil {
		return "kubernetes"
	}
	if spec.PodmanExtension != nil {
		return "podman"
	}
	return ""
}

// desiredStateTarget returns the stable service key from a DesiredServiceSpec,
// which identifies the runtime target (container/service name).
func desiredStateTarget(spec *domain.DesiredServiceSpec) string {
	if spec == nil {
		return ""
	}
	return spec.StableServiceKey
}
