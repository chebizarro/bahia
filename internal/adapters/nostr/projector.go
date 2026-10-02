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

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
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

// service.LLMRegistryService satisfies this interface.

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

// SBOMProjectionSource provides published SBOM manifests for projector snapshot republishing.
type SBOMProjectionSource interface {
	ListPublishedManifests(ctx context.Context, limit int) ([]domain.SBOMManifest, error)
}

// Phase 3 W1: WorkerReadModelProjectionSource interface removed — worker
// assignment/drain read models are published directly from the mutation site
// via WorkerReadModelPublisher (bahia-irsry.11.14).

type latestObservationSource interface {
	GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
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

// ProjectionPublisher durably queues and delivers the signed events the
// Projector produces. In production it is the control-plane outbox Publisher,
// which records the row (publish_target=control-plane) before the first relay
// attempt and retries every control-plane relay that has not accepted it.
//
// A nil error means the publish quorum accepted the event. An error wrapping
// nostrutil.ErrPublishIncomplete means the event is durably queued and still
// being retried: the Projector treats it as published for dedupe and never
// re-signs it. Any other error means the event was not queued.
type ProjectionPublisher interface {
	PublishProjection(ctx context.Context, ev gonostr.Event, entityType string, entityID *uuid.UUID) error
}

// Projector republishes Bahia's authoritative DB state into canonical Nostr
// read models and append-only audit events. It is rebuildable: a startup and
// periodic snapshot can repair a cold or wiped sidecar store.
type Projector struct {
	source       ProjectionSource
	mlSource     MLProjectionSource
	workerSource WorkerProjectionSource
	// Phase 3 W1: workerReadModelSource field removed (bahia-irsry.11.14).
	dnsSource            DNSProjectionSource
	dnsZoneSource        DNSZoneProjectionSource
	dnsBackendSource     DNSBackendProjectionSource
	dnsPolicySource      DNSPolicyProjectionSource
	sbomSource           SBOMProjectionSource
	publisher            ProjectionPublisher
	history              ProjectionHistory
	privateKey           string
	enabled              bool
	repairInterval       time.Duration
	logger               *zap.Logger
	systemConfig         *config.Config
	mcpTransport         bool
	dnsPublishMu         sync.Mutex
	dnsPublished         map[string]dnsPublishedEndpoint
	dnsPublishedZones    map[string]dnsPublishedZone
	dnsPublishedBackends map[string]dnsPublishedBackend
	dnsPublishedPolicies map[string]dnsPublishedPolicy
	dnsCacheHydrated     bool

	// F4 warm-start: readiness gate and migrated domain list.
	readiness     ReadinessWaiter
	intentDomains []string

	// Generalized projection dedupe/coalescing/backoff/metrics state; see
	// projection_dedupe.go. Initialized lazily so the constructor literal is
	// untouched.
	projInitOnce sync.Once
	proj         *projectionState
}

// ProjectorOption configures a projector.
type ProjectorOption func(*Projector)

// WithProjectorRepairInterval overrides the periodic snapshot repair interval.
// Use <=0 in tests to disable periodic repair after the startup snapshot.
func WithProjectorRepairInterval(interval time.Duration) ProjectorOption {
	return func(p *Projector) { p.repairInterval = interval }
}

func WithMLProjectionSource(source MLProjectionSource) ProjectorOption {
	return func(p *Projector) { p.mlSource = source }
}

func WithWorkerProjectionSource(source WorkerProjectionSource) ProjectorOption {
	return func(p *Projector) { p.workerSource = source }
}

// Phase 3 W1: WithWorkerReadModelProjectionSource removed (bahia-irsry.11.14).
// Worker read models are published directly from the mutation site.

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

func WithSBOMProjectionSource(source SBOMProjectionSource) ProjectorOption {
	return func(p *Projector) { p.sbomSource = source }
}

func WithSystemDiscoveryConfig(cfg *config.Config, mcpTransportEnabled bool) ProjectorOption {
	return func(p *Projector) {
		p.systemConfig = cfg
		p.mcpTransport = mcpTransportEnabled
	}
}

// ProjectionHistory is the projector's memory of what it published: the
// daemon's own events, newest first. In the daemon it is the author-scoped
// view of the local event store (LocalEventRepository.Authored), which holds
// only the latest version of each addressable coordinate and nothing whose
// delivery was abandoned (bahia-irsry.10.4, audit B-3); PostgreSQL is not
// consulted. Every NostrEventRepository satisfies it.
type ProjectionHistory interface {
	ListByKind(ctx context.Context, kind int, limit int) ([]repository.NostrEventRecord, error)
	FindByTag(ctx context.Context, tagName, tagValue string, kinds []int, limit int) ([]repository.NostrEventRecord, error)
}

// NewProjector creates a canonical Nostr read-model projector. history may be
// nil, in which case the dedupe cache starts cold.
func NewProjector(cfg config.NostrConfig, source ProjectionSource, publisher ProjectionPublisher, history ProjectionHistory, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Projector{
		source:         source,
		publisher:      publisher,
		history:        history,
		privateKey:     cfg.PrivateKey,
		enabled:        cfg.PublishEnabled && cfg.PrivateKey != "" && source != nil && publisher != nil,
		repairInterval: 10 * time.Minute,
		logger:         logger.Named("nostr-projector"),
	}
	for _, opt := range opts {
		opt(p)
	}
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
		// Phase 3 F2/F3: service and environment Created/Updated/Deleted
		// subscriptions removed — their state is published by the intent handlers
		// via PublishBeforeCommit (bahia-irsry.11.3, bahia-irsry.11.4).
		//
		// Phase 3 S2/W1: deployment run event subscriptions removed — their
		// cp-state is published directly from RegistryService (S2), and worker
		// read models are published from the run mutation site (W1, bahia-irsry.11.14).
		events.EventRuntimeObservation,
		events.EventEnvironmentServiceStateChanged,
		events.EventDriftDetected,
		// Phase 3 S1: EventReconcileCompleted removed — reconciler publishes state
		// directly; DNS/observed-deployments refresh is driven by
		// EventEnvironmentServiceStateChanged (B-16, B-17 partial).
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
// snapshots until the context is cancelled. Domains listed in intentDomains
// are warm-started from the daemon's own history (design §5.3) instead of
// re-projected from Postgres; RepublishSnapshot guards skip their legs.
func (p *Projector) Run(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	// Warm-start migrated domains: wait for subscriber EOSE, hydrate the
	// fingerprint cache, and re-publish only stale or missing records.
	p.warmStartMigratedDomains(ctx)
	if ctx.Err() != nil {
		return nil
	}
	// Legacy snapshot for unmigrated domains. Guards inside skip migrated
	// domain legs so they are not re-projected from Postgres.
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

	snapshotSource := p.source
	services, err := snapshotSource.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}

	// Phase 3 F3: environment snapshot republish removed — environment state
	// is now published by the intent handler via PublishBeforeCommit
	// (bahia-irsry.11.4). The listing is kept for observed deployments.
	envs, err := snapshotSource.ListEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}

	// Phase 3 S1: state snapshot republish removed — runtime state is now
	// published directly by the reconciler (bahia-irsry.11.6).
	// Phase 3 S2: build/artifact/intent/run snapshot republish removed —
	// their cp-state is published directly from RegistryService mutation
	// methods (bahia-irsry.11.7).
	// Phase 3 S3: policy state is published directly by PolicyIntentHandler.
	policiesPublished := 0
	// Phase 3 L1: LLM route registry and state records are published
	// directly from the mutation site (intent handler, ContextVM handler,
	// registry service). RepublishSnapshot LLM block removed.
	// Phase 3 M1/W1: ML and worker read-model snapshot legs removed — ML state
	// is published by MLCanonicalPublisher and worker assignment/drain read
	// models by WorkerReadModelPublisher, both from the mutation site
	// (bahia-irsry.11.12, bahia-irsry.11.14).
	// Phase 3 B1: Backup snapshot legs removed. Canonical records are now
	// published by BackupCanonicalPublisher wired to the registry.
	// Phase 3 D1: DNS snapshot legs removed. DNS endpoint/zone/backend/policy
	// records are now published by the DNSCanonicalPublisher wired to the
	// reconciler, triggered by bus events instead of this 10-minute timer.
	sbomRefs, sbomAvailLists := p.publishSBOMSnapshots(ctx)
	p.logger.Info("Nostr projection snapshot republished", zap.Int("services", len(services)), zap.Int("environments", len(envs)), zap.Int("policies", policiesPublished), zap.Int("sbom_references", sbomRefs), zap.Int("sbom_availability_lists", sbomAvailLists))
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
	// Phase 3 W1: deployment run event cases removed — worker assignment/drain
	// read models are published directly from the run mutation site
	// (registry.go, ml_registry.go) instead of reactively here (bahia-irsry.11.14).

	// Phase 3 F2/F3: service and environment handleEvent cases removed.
	// Phase 3 S1: state publication removed — the reconciler publishes state
	// directly via RuntimeStatePublisher; tombstones are published by
	// StateTombstoneHandler (bahia-irsry.11.6).
	case events.EventAdoptionImported:
		p.publishServiceByID(ctx, res.ServiceID)
		p.publishEnvironmentByID(ctx, res.EnvironmentID)
		// Phase 3 L1: LLM handleEvent cases removed — route registry and state
		// records are published directly from the mutation site (intent handler,
		// ContextVM handler, registry service) instead of reactively here.
		// Phase 3 M1: ML model/version/endpoint/intent/observation/state/artifact/provenance
		// handleEvent cases removed — ML state is now published directly from
		// the mutation site via MLCanonicalPublisher. Worker read models
		// for ML runs are refreshed by WorkerReadModelPublisher (W1).
	}
	if shouldRefreshObservedDeploymentsProjection(e.Type) && p.systemConfig != nil && len(p.systemConfig.Nostr.BrowserRelayPolicyRelays()) > 0 {
		if err := p.publishSystemDiscoveryAnnouncement(ctx, p.systemConfig); err != nil {
			p.logger.Warn("publish observed deployments discovery after event failed", zap.String("event_type", string(e.Type)), zap.Error(err))
		}
	}
	// Phase 3 D1: DNS endpoint publishing moved to the reconciler's
	// DNSCanonicalPublisher (B-17 fix). The projector no longer re-derives
	// DNS endpoints on every bus event.
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

// Phase 3 M1: publishMLModelByID, publishMLModelVersionByID,
// publishMLEndpointByID, publishMLStateForIntent, publishMLStateForRun,
// publishMLStateForIDs, publishMLProvenanceByArtifactID,
// publishMLProvenanceFromEvent, publishMLProvenanceForEdge removed.
// ML state is now published directly from the mutation site via MLCanonicalPublisher.

func (p *Projector) publishReplaceableJSON(ctx context.Context, kind int, dTag string, tags gonostr.Tags, value any, entityType string, entityID *uuid.UUID) error {
	content, _ := json.Marshal(value)
	return p.publishControlState(ctx, kind, dTag, false, tags, string(content), entityType, entityID)
}

// publishReplaceableTombstone publishes the deletion marker for a record that
// was projected with publishReplaceableJSON(kind, dTag, ...). Both go through
// controlStateEnvelope, so the tombstone replaces the live event on the relay.
func (p *Projector) publishReplaceableTombstone(ctx context.Context, kind int, dTag string, tags gonostr.Tags, value any, entityType string, entityID *uuid.UUID) error {
	content, _ := json.Marshal(value)
	return p.publishControlState(ctx, kind, dTag, true, tags, string(content), entityType, entityID)
}

// publishControlState signs one projected replaceable record (live or
// tombstone) on the coordinate controlStateEnvelope derives for it.
func (p *Projector) publishControlState(ctx context.Context, legacyKind int, id string, deleted bool, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	wireKind, baseTags := controlStateEnvelope(legacyKind, id, deleted)
	return p.publishSigned(ctx, wireKind, append(baseTags, tags...), content, entityType, entityID)
}

// controlStateEnvelope is the single coordinate builder for projected
// replaceable state. It returns the wire kind and the envelope tags (d, domain,
// schema, legacy_kind, deleted, and the family's single-letter t topic) for the
// record identified by (legacyKind, id).
// Relays replace an addressable event only with a newer event on the exact
// same (kind, pubkey, d) coordinate, so a live record and its tombstone must
// both be built here; deriving either one separately is how deletions ended up
// on a coordinate nobody reads (B-18, B-19).
func controlStateEnvelope(legacyKind int, id string, deleted bool) (wireKind int, tags gonostr.Tags) {
	deletedValue := strconv.FormatBool(deleted)
	domainName, _ := canonicalStateDomain(legacyKind)
	if domainName == "" {
		return legacyKind, gonostr.Tags{{kinds.CASControlStateTagD, id}, {kinds.CASControlStateTagDeleted, deletedValue}}
	}
	return KindCASControlState, gonostr.Tags{
		{kinds.CASControlStateTagD, canonicalStateDTag(legacyKind, id)},
		{kinds.CASControlStateTagDomain, domainName},
		{kinds.CASControlStateTagSchema, controlStateSchema},
		{kinds.CASControlStateTagLegacyKind, strconv.Itoa(legacyKind)},
		{kinds.CASControlStateTagDeleted, deletedValue},
		{"t", cpStateFamilies[legacyKind].topic},
	}
}

const controlStateSchema = kinds.CASControlStateSchema

// cpStateFamily is one projected cp-state family: the 30900 domain and entity
// of its records and the single-letter "t" topic ("<domain>-<entity>") each
// record carries so consumers can REQ it by #t (audit A-27).
type cpStateFamily struct {
	domain string
	entity string
	topic  string
}

// cpStateFamilies is the projector's single table of cp-state families, keyed
// by the catalog kind stamped in legacy_kind. The worker topics are the worker
// contract's (bahia-irsry.9.2).
var cpStateFamilies = map[int]cpStateFamily{
	KindServiceState:                  {"service", "state", kinds.CPStateTopicServiceState},
	KindServiceRegistry:               {"service", "registry", kinds.CPStateTopicServiceRegistry},
	KindEnvironmentRegistry:           {"environment", "registry", kinds.CPStateTopicEnvironmentRegistry},
	KindLLMRouteRegistry:              {"llm", "route", kinds.CPStateTopicLLMRoute},
	KindLLMRouteState:                 {"llm", "state", kinds.CPStateTopicLLMState},
	KindArtifactRegistry:              {"artifact", "registry", kinds.CPStateTopicArtifactRegistry},
	KindDeploymentIntentRegistry:      {"deployment", "intent", kinds.CPStateTopicDeploymentIntent},
	KindDeploymentRunRegistry:         {"deployment", "run", kinds.CPStateTopicDeploymentRun},
	KindBuildRegistry:                 {"build", "registry", kinds.CPStateTopicBuildRegistry},
	KindPolicyRegistry:                {"policy", "registry", kinds.CPStateTopicPolicyRegistry},
	KindPackageRepositoryRegistry:     {"package", "repository", kinds.CPStateTopicPackageRepository},
	KindPackageArtifactRegistry:       {"package", "artifact", kinds.CPStateTopicPackageArtifact},
	KindPackagePromotionRegistry:      {"package", "promotion", kinds.CPStateTopicPackagePromotion},
	KindWorkerState:                   {kinds.WorkerDomain, "state", kinds.WorkerStateTopic},
	KindWorkerAssignmentState:         {kinds.WorkerDomain, "assignment", kinds.WorkerAssignmentTopic},
	KindWorkerDrainStatus:             {kinds.WorkerDomain, "drain", kinds.WorkerDrainTopic},
	KindWorkerEligibilityPreview:      {kinds.WorkerDomain, "eligibility", kinds.WorkerEligibilityTopic},
	KindDNSZoneState:                  {kinds.DNSDomain, "zone", kinds.DNSZoneTopic},
	KindDNSEndpointState:              {kinds.DNSDomain, "endpoint", kinds.DNSEndpointTopic},
	KindDNSPolicyState:                {kinds.DNSDomain, "policy", kinds.DNSPolicyTopic},
	KindDNSBackendState:               {kinds.DNSDomain, "backend", kinds.DNSBackendTopic},
	KindMLModelRegistry:               {"ml", "model", kinds.CPStateTopicMLModel},
	KindMLModelVersionRegistry:        {"ml", "model-version", kinds.CPStateTopicMLModelVersion},
	KindMLDatasetRegistry:             {"ml", "dataset", kinds.CPStateTopicMLDataset},
	KindMLRecipeRegistry:              {"ml", "recipe", kinds.CPStateTopicMLRecipe},
	KindMLRecipeRunState:              {"ml", "recipe-run", kinds.CPStateTopicMLRecipeRun},
	KindMLInferenceEndpointRegistry:   {"ml", "endpoint", kinds.CPStateTopicMLEndpoint},
	KindMLInferenceEndpointState:      {"ml", "endpoint-state", kinds.CPStateTopicMLEndpointState},
	KindMLEvaluationExperimentState:   {"ml", "evaluation", kinds.CPStateTopicMLEvaluation},
	KindMLArtifactProvenanceGraph:     {"ml", "provenance", kinds.CPStateTopicMLProvenance},
	KindMLRuntimeCapabilityProfile:    {"ml", "runtime-capability", kinds.CPStateTopicMLRuntimeCapability},
	KindBackupDefinitionRegistry:      {"backup", "definition", kinds.CPStateTopicBackupDefinition},
	KindBackupPolicyRegistry:          {"backup", "policy", kinds.CPStateTopicBackupPolicy},
	KindBackupRepositoryRegistry:      {"backup", "repository", kinds.CPStateTopicBackupRepository},
	KindBackupRetentionRegistry:       {"backup", "retention", kinds.CPStateTopicBackupRetention},
	KindBackupRecipeRegistry:          {"backup", "recipe", kinds.CPStateTopicBackupRecipe},
	KindBackupRunState:                {"backup", "run", kinds.CPStateTopicBackupRun},
	KindBackupVerificationState:       {"backup", "verification", kinds.CPStateTopicBackupVerification},
	KindBackupRestoreState:            {"backup", "restore", kinds.CPStateTopicBackupRestore},
	KindBackupRuntimeObservationState: {"backup", "runtime", kinds.CPStateTopicBackupRuntimeObservation},
	// Org cp-state families (Phase 3 Wave 5 O1).
	KindOrgRegistry:       {"org", "registry", kinds.CPStateTopicOrgRegistry},
	KindOrgMemberRegistry: {"org", "member", kinds.CPStateTopicOrgMemberRegistry},
	KindOrgInviteRegistry: {"org", "invite", kinds.CPStateTopicOrgInviteRegistry},
}

func canonicalStateDomain(kind int) (domainName string, entity string) {
	family := cpStateFamilies[kind]
	return family.domain, family.entity
}

// canonicalStateDTag is the cp-state d builder. A record is addressed by its
// id, except that worker families prefix it per family (kinds.CPStateFamily
// WorkerDTag, "worker:<entity>:<id>"): assignment and drain are both keyed by
// the worker pubkey, and on one bare-pubkey d each replaced the other on the
// relay (bahia-irsry.36).
func canonicalStateDTag(legacyKind int, id string) string {
	if d, ok := kinds.CPStateFamily(legacyKind).WorkerDTag(id); ok {
		return d
	}
	return id
}

// publishSBOMSnapshots rebuilds canonical SBOM Nostr events (30078 references and
// 30004 availability lists) from published manifests in the persistent repository.
// This ensures SBOM events survive relay sidecar restarts.
func (p *Projector) publishSBOMSnapshots(ctx context.Context) (int, int) {
	if !p.Enabled() || p.sbomSource == nil {
		return 0, 0
	}
	manifests, err := p.sbomSource.ListPublishedManifests(ctx, 1000)
	if err != nil {
		p.logger.Warn("list published SBOM manifests for projection failed", zap.Error(err))
		return 0, 0
	}
	if len(manifests) == 0 {
		return 0, 0
	}

	pubkey, err := publicKeyHexFromPrivateKeyHex(p.privateKey)
	if err != nil {
		p.logger.Warn("derive SBOM projection pubkey failed", zap.Error(err))
		return 0, 0
	}
	attestationSigner, err := sbom.NewNostrDSSESigner(p.privateKey)
	if err != nil {
		p.logger.Warn("configure SBOM projection attestation signer failed", zap.Error(err))
		return 0, 0
	}

	// Publish 30078 reference events and collect entries grouped by subject for availability lists.
	type subjectKey struct {
		Type   domain.SBOMSubjectType
		ID     string
		Digest string
	}
	type subjectGroup struct {
		subject domain.SBOMSubject
		entries []domain.SBOMIndexEntry
	}
	groups := map[subjectKey]*subjectGroup{}
	refsPublished := 0

	for i := range manifests {
		m := &manifests[i]
		// Parse subject digest into algo:hash for the attestation.
		digestParts := strings.SplitN(m.Subject.Digest, ":", 2)
		if len(digestParts) != 2 || digestParts[1] == "" {
			p.logger.Warn("skip SBOM manifest with invalid subject digest",
				zap.String("manifest_id", m.ID.String()),
				zap.String("digest", m.Subject.Digest))
			continue
		}
		digestAlgo, digestHash := digestParts[0], digestParts[1]

		att := &domain.SBOMAttestation{
			Type: "https://in-toto.io/Statement/v1",
			Subject: []domain.AttestationSubject{{
				Name:   m.Subject.DisplayName,
				Digest: map[string]string{digestAlgo: digestHash},
			}},
			PredicateType: predicateTypeForSBOMFormat(m.Format),
			Predicate: domain.SBOMPredicate{
				Format: m.Format,
				Location: domain.SBOMLocation{
					Type:      m.StorageType,
					URI:       m.StorageURI,
					MediaType: m.MediaType,
				},
				Digest:    map[string]string{"sha256": m.PayloadSHA256},
				Generator: m.Generator,
				Timestamp: m.CreatedAt,
				NTIA:      m.NTIA,
			},
		}

		if err := sbom.SignAttestation(ctx, att, attestationSigner); err != nil {
			p.logger.Warn("sign SBOM reference attestation for projection failed",
				zap.String("manifest_id", m.ID.String()), zap.Error(err))
			continue
		}
		createdAt := m.CreatedAt
		ev, _, err := sbom.BuildSBOMReferenceEvent(sbom.BuildSBOMReferenceEventInput{
			Subject:     m.Subject,
			Attestation: att,
			CreatedAt:   &createdAt,
		})
		if err != nil {
			p.logger.Warn("build SBOM reference event for projection failed",
				zap.String("manifest_id", m.ID.String()), zap.Error(err))
			continue
		}

		// Through the dedupe gate: an unchanged reference is neither re-signed
		// nor re-queued on every repair pass.
		if err := p.publishSigned(ctx, int(ev.Kind), ev.Tags, ev.Content, "sbom_reference.projection", &m.ID); err != nil {
			p.logger.Warn("publish SBOM reference event failed",
				zap.String("manifest_id", m.ID.String()), zap.Error(err))
			continue
		}
		refsPublished++

		// Collect entry for the availability list.
		sk := subjectKey{Type: m.Subject.Type, ID: m.Subject.ID, Digest: m.Subject.Digest}
		g, ok := groups[sk]
		if !ok {
			g = &subjectGroup{subject: m.Subject}
			groups[sk] = g
		}
		g.entries = append(g.entries, domain.SBOMIndexEntry{
			SubjectDigest: m.Subject.Digest,
			AttestationID: fmt.Sprintf("%d:%s:%s", sbom.KindSBOMReference, pubkey, m.ReferenceDTag),
			ReferenceDTag: m.ReferenceDTag,
			Format:        m.Format,
			LocationURI:   m.StorageURI,
			StorageType:   m.StorageType,
			PayloadSHA256: m.PayloadSHA256,
			GeneratorID:   m.Generator.ID,
			Timestamp:     m.CreatedAt,
		})
	}

	// Publish 30004 availability lists, one per subject.
	availPublished := 0
	for _, g := range groups {
		// The list's updatedAt is its newest entry, not the repair time, so
		// an unchanged list has identical content and is deduped.
		var updatedAt *time.Time
		for i := range g.entries {
			if ts := g.entries[i].Timestamp; !ts.IsZero() && (updatedAt == nil || ts.After(*updatedAt)) {
				updatedAt = &ts
			}
		}
		ev, _, err := sbom.BuildSBOMAvailabilityListEvent(sbom.BuildSBOMAvailabilityListEventInput{
			Subject:         g.subject,
			Entries:         g.entries,
			PublisherPubkey: pubkey,
			CreatedAt:       updatedAt,
		})
		if err != nil {
			p.logger.Warn("build SBOM availability list for projection failed",
				zap.String("subject_id", g.subject.ID), zap.Error(err))
			continue
		}
		if err := p.publishSigned(ctx, int(ev.Kind), ev.Tags, ev.Content, "sbom_availability.projection", nil); err != nil {
			p.logger.Warn("publish SBOM availability list failed",
				zap.String("subject_id", g.subject.ID), zap.Error(err))
			continue
		}
		availPublished++
	}

	return refsPublished, availPublished
}

// predicateTypeForSBOMFormat returns the in-toto predicate type for an SBOM format.
func predicateTypeForSBOMFormat(format domain.SBOMFormat) domain.SBOMAttestationType {
	switch format {
	case domain.SBOMFormatSPDX:
		return domain.AttestationTypeSPDX
	case domain.SBOMFormatCycloneDX:
		return domain.AttestationTypeCycloneDX
	default:
		return domain.SBOMAttestationType("https://sbom.dev/" + string(format))
	}
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

// dnsStateLegacyKinds are the DNS read-model families the projector derives
// (and therefore must tombstone) itself.
var dnsStateLegacyKinds = []int{KindDNSEndpointState, KindDNSZoneState, KindDNSBackendState, KindDNSPolicyState}

// dnsRetainedRecord is the newest retained event on one live coordinate.
type dnsRetainedRecord struct {
	tags    gonostr.Tags
	content map[string]any
}

// field returns the tag value, falling back to the JSON content field.
func (r dnsRetainedRecord) field(tagName, contentKey string) string {
	if tagName != "" {
		if value := strings.TrimSpace(tagValue(r.tags, tagName)); value != "" {
			return value
		}
	}
	switch value := r.content[contentKey].(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

// liveRetainedControlState returns, per d-tag, the newest retained event this
// projector signed on the live wire coordinate for legacyKind, omitting d-tags
// whose newest event is a tombstone. Ordering matches relay replacement
// semantics: newest created_at wins, ties go to the lowest event id.
func (p *Projector) liveRetainedControlState(ctx context.Context, legacyKind int, servicePubkey string) (map[string]dnsRetainedRecord, error) {
	wireKind, envelope := controlStateEnvelope(legacyKind, "", false)
	legacyValue := tagValue(envelope, "legacy_kind")
	var records []repository.NostrEventRecord
	var err error
	if legacyValue != "" {
		records, err = p.history.FindByTag(ctx, "legacy_kind", legacyValue, []int{wireKind}, projectionHydrateLimit)
	} else {
		records, err = p.history.ListByKind(ctx, wireKind, projectionHydrateLimit)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	seen := map[string]struct{}{}
	live := map[string]dnsRetainedRecord{}
	for _, record := range records {
		if record.Kind != wireKind || (servicePubkey != "" && record.PubKey != servicePubkey) {
			continue
		}
		tags := recordTags(record)
		if legacyValue != "" && tagValue(tags, "legacy_kind") != legacyValue {
			continue
		}
		d := tagValue(tags, kinds.CASControlStateTagD)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		retained := dnsRetainedRecord{tags: tags}
		_ = json.Unmarshal([]byte(record.Content), &retained.content)
		if isTombstoneTags(tags) {
			continue
		}
		if deleted, ok := retained.content["deleted"].(bool); ok && deleted {
			continue
		}
		live[d] = retained
	}
	return live, nil
}

func dnsEndpointTags(endpoint domain.DNSEndpoint) gonostr.Tags {
	tags := gonostr.Tags{{"family", string(endpoint.Family)}, {"health", string(endpoint.Health)}, {"dns", endpoint.FQDN}, {"addr", endpoint.Address}, {"t", "bahia"}}
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

type observedDeploymentDiscovery struct {
	ServiceID           string `json:"service_id"`
	ServiceName         string `json:"service_name,omitempty"`
	EnvironmentID       string `json:"environment_id"`
	EnvironmentName     string `json:"environment_name,omitempty"`
	DeploymentUnitID    string `json:"deployment_unit_id,omitempty"`
	RuntimeType         string `json:"runtime_type,omitempty"`
	RuntimeTarget       string `json:"runtime_target,omitempty"`
	ObservationID       string `json:"observation_id"`
	ObservedVersion     string `json:"observed_version,omitempty"`
	ObservedImageRepo   string `json:"observed_image_repo,omitempty"`
	ObservedImageDigest string `json:"observed_image_digest,omitempty"`
	ObservedContainerID string `json:"observed_container_id,omitempty"`
	ObservedHost        string `json:"observed_host,omitempty"`
	ObservationSource   string `json:"observation_source,omitempty"`
	HealthStatus        string `json:"health_status,omitempty"`
	DriftStatus         string `json:"drift_status,omitempty"`
	ObservedAt          string `json:"observed_at"`
}

func shouldRefreshObservedDeploymentsProjection(eventType events.EventType) bool {
	switch eventType {
	case events.EventServiceCreated, events.EventServiceUpdated, events.EventServiceDeleted,
		events.EventEnvironmentCreated, events.EventEnvironmentUpdated, events.EventEnvironmentDeleted,
		events.EventRuntimeObservation, events.EventEnvironmentServiceStateChanged, events.EventDriftDetected,
		events.EventAdoptionImported, events.EventRuntimeDeploy,
		events.EventRuntimeRestart, events.EventRuntimeStop:
		return true
	default:
		return false
	}
}

func (p *Projector) observedDeployments(ctx context.Context) ([]observedDeploymentDiscovery, error) {
	source := p.source
	services, err := source.ListServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services for observed deployments: %w", err)
	}
	environments, err := source.ListEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environments for observed deployments: %w", err)
	}
	states, err := source.ListAllStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list states for observed deployments: %w", err)
	}

	servicesByID := make(map[uuid.UUID]domain.Service, len(services))
	for _, service := range services {
		servicesByID[service.ID] = service
	}
	environmentsByID := make(map[uuid.UUID]domain.Environment, len(environments))
	for _, environment := range environments {
		environmentsByID[environment.ID] = environment
	}

	deployments := make([]observedDeploymentDiscovery, 0, len(states))
	for i := range states {
		state := &states[i]
		if state.CurrentObservationID == nil {
			continue
		}
		observation, err := source.GetLatestObservation(ctx, state.ServiceID, state.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("get latest observation for service %s in environment %s: %w", state.ServiceID, state.EnvironmentID, err)
		}
		if observation == nil || observation.ID != *state.CurrentObservationID {
			continue
		}

		service := servicesByID[state.ServiceID]
		environment := environmentsByID[state.EnvironmentID]
		deployment := observedDeploymentDiscovery{
			ServiceID:           state.ServiceID.String(),
			ServiceName:         service.Name,
			EnvironmentID:       state.EnvironmentID.String(),
			EnvironmentName:     environment.Name,
			RuntimeType:         string(service.RuntimeType),
			RuntimeTarget:       service.RuntimeTargetName(),
			ObservationID:       observation.ID.String(),
			ObservedVersion:     observation.ObservedVersion,
			ObservedImageRepo:   observation.ObservedImageRepo,
			ObservedImageDigest: observation.ObservedImageDigest,
			ObservedContainerID: observation.ObservedContainerID,
			ObservedHost:        observation.ObservedHost,
			ObservationSource:   observation.Source,
			HealthStatus:        string(observation.HealthStatus),
			DriftStatus:         string(state.DriftStatus),
			ObservedAt:          formatTime(observation.ObservedAt),
		}
		if state.DeploymentUnitID != nil {
			deployment.DeploymentUnitID = state.DeploymentUnitID.String()
		}
		deployments = append(deployments, deployment)
	}

	sort.Slice(deployments, func(i, j int) bool {
		left := strings.ToLower(deployments[i].EnvironmentName) + "\x00" +
			strings.ToLower(deployments[i].ServiceName) + "\x00" +
			deployments[i].EnvironmentID + "\x00" + deployments[i].ServiceID + "\x00" + deployments[i].DeploymentUnitID
		right := strings.ToLower(deployments[j].EnvironmentName) + "\x00" +
			strings.ToLower(deployments[j].ServiceName) + "\x00" +
			deployments[j].EnvironmentID + "\x00" + deployments[j].ServiceID + "\x00" + deployments[j].DeploymentUnitID
		return left < right
	})
	return deployments, nil
}

func (p *Projector) publishSystemDiscoveryAnnouncement(ctx context.Context, cfg *config.Config) error {
	observedDeployments, err := p.observedDeployments(ctx)
	if err != nil {
		return err
	}
	browserRelays := cfg.Nostr.BrowserRelayPolicyRelays()
	encryptedRequestsEnabled := len(browserRelays) > 0 && cfg.Nostr.PrivateKey != ""
	payload := map[string]any{
		"schema":               SystemDiscoverySchema,
		"registries":           discoveryRegistries(cfg),
		"versions":             discoveryVersions(),
		"observed_deployments": observedDeployments,
		"control_plane":        discoveryControlPlane(cfg.LLM.Enabled, p.mcpTransport, p.dnsSource != nil || p.dnsZoneSource != nil || p.dnsBackendSource != nil || p.dnsPolicySource != nil),
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
		"nostr": map[string]any{
			"trusted_relay_monitor_pubkeys": cfg.Nostr.TrustedRelayMonitorPubkeys,
		},
		"assistant": discoveryAssistant(cfg.Assistant),
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
			"relay_read_models":        cfg.Nostr.PublishEnabled,
			"encrypted_nostr_requests": encryptedRequestsEnabled,
			"llm_control_plane":        cfg.LLM.Enabled,
			"direct_nostr_http_auth":   cfg.Auth.Enabled,
			"mcp_transport":            p.mcpTransport,
			"publish_enabled":          cfg.Nostr.PublishEnabled,
		},
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal system discovery: %w", err)
	}
	return p.publishSigned(ctx, kinds.ContextVMServerAnnouncement, systemDiscoveryAnnouncementTags(), string(content), "system.discovery", nil)
}

// discoveryAssistant advertises which assistant workflows can start new turns
// on this deployment. Batch is absent without assistant.llm_model; approving or
// rejecting an existing batch draft does not depend on this list.
func discoveryAssistant(cfg config.AssistantConfig) map[string]any {
	assistant := map[string]any{
		"enabled":             cfg.Enabled,
		"available_workflows": cfg.AvailableWorkflows(),
	}
	if cfg.Enabled {
		assistant["default_workflow"] = cfg.ResolvedDefaultWorkflow()
	}
	return assistant
}

func (p *Projector) publishSystemDiscovery(ctx context.Context) error {
	cfg := p.systemConfig
	if cfg == nil {
		return nil
	}
	browserRelays := cfg.Nostr.BrowserRelayPolicyRelays()
	if len(browserRelays) == 0 {
		if cfg.Nostr.Sidecar.Enabled {
			return fmt.Errorf("system discovery requires nostr.browser_relays when relay sidecar is enabled")
		}
		return nil
	}
	contextVMRelays := cfg.Nostr.ContextVMRelayPolicyRelays()
	serviceRelays := cfg.Nostr.ServiceRelayPolicyRelays()
	if err := p.publishSystemDiscoveryAnnouncement(ctx, cfg); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, BrowserRelaySetDTag, browserRelays); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, ContextVMRelaySetDTag, contextVMRelays); err != nil {
		return err
	}
	if err := p.publishRelaySet(ctx, ServiceRelaySetDTag, serviceRelays); err != nil {
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
	return p.publishSigned(ctx, kinds.RelaySetDiscovery, relaySetTags(dTag, relays), "", "system.discovery.relay_set", nil)
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

// DiscoveryContextVMMethods returns the ContextVM JSON-RPC methods advertised in
// discovery control_plane.methods. Every entry must have a server-side
// RegisterContextVMHandler registration on bahia-server's encrypted transport;
// internal/controlplane tests enforce this. Methods that Bahia's REST/MCP
// command publishers emit without a server-side consumer (llm/*, ml/*,
// package/*, worker/policy-apply, worker/workload-pin) are intentionally not
// advertised. DNS methods are always registered but only advertised when a DNS
// source is configured.
func DiscoveryContextVMMethods(dnsEnabled bool) []string {
	methods := []string{
		"service/deploy-preview",
		"service/deploy",
		"service/route-attach",
		"service/rollback",
		"worker/cordon",
		"worker/uncordon",
		"worker/drain",
		"worker/undrain",
		"worker/maintenance-enter",
		"worker/maintenance-exit",
		"worker/labels-update",
		"worker/cleanup",
		"approval/approve",
		"approval/reject",
		"sbom/generate",
		"sbom/import",
	}
	if dnsEnabled {
		methods = append(methods, "dns/zone-create", "dns/policy-apply", "dns/record-set", "dns/drift-remediate", "dns/override-retire")
	}
	return methods
}

func discoveryControlPlane(llmEnabled, mcpTransportEnabled, dnsEnabled bool) map[string]any {
	capabilities := []string{"service_deployments", "service_registry_read_models", "worker_management", "worker_read_models", "relay_read_models", "encrypted_controlplane.progress_ack"}
	methods := DiscoveryContextVMMethods(dnsEnabled)
	correlationTags := []string{"service", "environment", "artifact", "intent", "run", "worker", "command", "e", "p", "status", "step", "subject", "subject_type"}
	mcpFields := []string{"request_event_id", "request_kind", "service_id", "environment_id", "intent_id", "run_id", "worker_pubkey", "d_tag", "observable_kinds"}
	if llmEnabled {
		capabilities = append(capabilities, "llm_routes", "llm_deployments", "llm_rollback")
		correlationTags = append(correlationTags, "route", "release")
		mcpFields = append(mcpFields, "route_id", "release_id")
	}
	if mcpTransportEnabled {
		capabilities = append(capabilities, "mcp_async_correlation")
	}
	if dnsEnabled {
		capabilities = append(capabilities, "dns_endpoint_catalog")
	}
	// No ml/* ContextVM method has a bahia-server handler; AI/ML discovery
	// advertises read models only until a consumer exists.
	aiMLMethods := []string{}
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
		"capabilities":          []string{"ml_model_registry_read_models", "ml_model_version_read_models", "ml_inference_endpoint_read_models", "ml_provenance_read_models", "ml_runtime_capability_read_models"},
		"correlation_tags":      []string{"model", "model_version", "recipe", "run", "endpoint", "environment", "deployment", "artifact", "worker", "runtime", "e", "p", "status"},
		"contextvm_commands":    len(aiMLMethods) > 0,
		"canonical_observables": true,
		"unsupported_in_d1":     []string{"recipe_execution", "model_import_orchestration", "dataset_import", "evaluation", "benchmark", "fine_tune"},
	}
	return map[string]any{
		"version":               "bahia-controlplane-v1",
		"wire_version":          "contextvm-jsonrpc-v2",
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

// Phase 3 S2: publishBuildRegistry, publishArtifactRegistry,
// publishDeploymentIntentRegistry and publishDeploymentRunRegistry removed —
// their canonical state is published directly from RegistryService mutation
// methods via the shared record builders in control_state_contract.go
// (bahia-irsry.11.7).

// publishServiceRegistry and publishEnvironmentRegistry publish the records
// serviceRegistryRecord and environmentRegistryRecord build, the builders the
// relay-first registry publishes with too (control_state_contract.go).
func (p *Projector) publishServiceRegistry(ctx context.Context, svc *domain.Service, deleted bool) error {
	tags, content := serviceRegistryRecord(svc, deleted)
	return p.publishControlState(ctx, KindServiceRegistry, svc.ID.String(), deleted, tags, content, "service.projection", &svc.ID)
}

func (p *Projector) publishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	units, err := p.environmentRecordUnits(ctx, env, deleted)
	if err != nil {
		return err
	}
	tags, content := environmentRegistryRecord(env, units, deleted)
	return p.publishControlState(ctx, KindEnvironmentRegistry, env.ID.String(), deleted, tags, content, "environment.projection", &env.ID)
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

// Phase 3 M1: publishMLModelRegistry, publishMLModelVersionRegistry,
// publishMLInferenceEndpointRegistry, publishMLInferenceEndpointState,
// environmentNameForMLProjection, publishMLArtifactProvenanceGraph,
// publishMLRuntimeCapabilityProfile removed. ML state is now published
// directly from the mutation site via MLCanonicalPublisher.

// Phase 3 W1: publishWorkerAssignmentState, publishWorkerDrainStatus,
// publishWorkerReadModelsForWorker, and publishWorkerReadModelSnapshots
// removed — worker assignment/drain read models are published directly from
// the mutation site (worker_handlers.go, registry.go, ml_registry.go) via
// WorkerReadModelPublisher (bahia-irsry.11.14).

// Phase 3 S1: publishState removed — the shared record builder
// RuntimeStateRecord in control_state_contract.go replaces it, and the
// reconciler publishes via RuntimeStatePublisher (bahia-irsry.11.6).

// serviceStateDTag is the one coordinate builder for service state: the live
// record and its tombstone both use it, so they share one relay coordinate.
func serviceStateDTag(serviceID, environmentID uuid.UUID) string {
	return fmt.Sprintf("service:%s:environment:%s", serviceID, environmentID)
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

// publishSignedDirect signs one event and hands it to the outbox publisher,
// with no dedupe, coalescing, or backoff. Only publishSigned (the gated choke
// point) calls it. queued reports that the publish quorum has not accepted the
// event yet but the outbox holds it and keeps retrying: the event is kept, so
// the caller must not re-sign it.
func (p *Projector) publishSignedDirect(ctx context.Context, kind int, createdAt gonostr.Timestamp, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) (queued bool, err error) {
	ev := gonostr.Event{
		Kind:      canonicalKind(kind),
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   content,
	}
	if err := signEventWithPrivateKeyHex(&ev, p.privateKey); err != nil {
		return false, err
	}
	err = p.publisher.PublishProjection(ctx, ev, entityType, entityID)
	switch {
	case nostrutil.IsPublishQueued(err):
		p.logger.Debug("projected Nostr event queued for outbox retry", zap.Int("kind", kind), zap.String("event_id", eventIDHex(&ev)), zap.Error(err))
		return true, nil
	case err != nil:
		return false, fmt.Errorf("publish event: %w", err)
	}
	p.logger.Debug("projected Nostr event published", zap.Int("kind", kind), zap.String("event_id", eventIDHex(&ev)))
	return false, nil
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
