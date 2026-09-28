package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Deployment inventory is a signed canonical CAS control-state (kind 30900)
// read model. Each Bahia environment has one complete snapshot coordinate;
// redacted runtime-target scan aggregates have their own coordinates. No new
// event kind is allocated.
const (
	DeploymentInventoryDomain            = "deployment-inventory"
	DeploymentInventorySchema            = "bahia.deployment-inventory.v1"
	DeploymentInventoryEnvironmentEntity = "environment-inventory"
	DeploymentInventoryTargetScanEntity  = "runtime-target-scan"

	deploymentInventoryEnvironmentDPrefix = DeploymentInventoryDomain + ":environment:"
	deploymentInventoryTargetScanDPrefix  = DeploymentInventoryDomain + ":target-scan:"
	deploymentInventoryHydrateLimit       = 10000

	// Coverage describes which halves of desired/observed state exist. It is
	// time independent; freshness is judged by consumers against
	// freshness.stale_after_seconds so a stopped publisher ages into "stale".
	DeploymentCoverageObserved     = "observed"
	DeploymentCoverageDesiredOnly  = "desired_only"
	DeploymentCoverageObservedOnly = "observed_only"
	DeploymentCoverageUnknown      = "unknown"

	instanceCoverageSupervised    = "supervised"
	instanceCoverageNotSupervised = "not_supervised"

	targetScanComplete    = "complete"
	targetScanUnavailable = "unavailable"
)

var (
	// Allowlists: projected runtime values must match these shapes or they are
	// dropped. They exclude whitespace, quotes, and URL userinfo separators so
	// command lines, env assignments, and credential URLs cannot pass.
	inventoryImageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,511}$`)
	inventoryLabelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,127}$`)
	inventoryCodePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	inventoryDigestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// DeploymentInventorySource supplies the desired-artifact, deployment-unit, and
// managed-instance read models the inventory composes with service state and
// runtime observations.
type DeploymentInventorySource interface {
	GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	GetDeploymentUnit(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error)
	// ListManagedInstances returns per-runtime-target supervisor health. ok is
	// false when instance supervision is not running, so instance coverage is
	// reported as not_supervised rather than as zero instances.
	ListManagedInstances(ctx context.Context) (instances []domain.ManagedInstanceHealth, ok bool, err error)
}

// RepositoryDeploymentInventorySource adapts Bahia repositories to
// DeploymentInventorySource. Nil repositories degrade to explicit absence.
type RepositoryDeploymentInventorySource struct {
	Artifacts           repository.ArtifactRepository
	Units               repository.DeploymentUnitRepository
	Instances           repository.ManagedInstanceHealthRepository
	InstancesSupervised bool
}

func (s RepositoryDeploymentInventorySource) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if s.Artifacts == nil {
		return nil, nil
	}
	return s.Artifacts.GetByID(ctx, id)
}

func (s RepositoryDeploymentInventorySource) GetDeploymentUnit(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	if s.Units == nil {
		return nil, nil
	}
	return s.Units.GetByID(ctx, id)
}

func (s RepositoryDeploymentInventorySource) ListManagedInstances(ctx context.Context) ([]domain.ManagedInstanceHealth, bool, error) {
	if s.Instances == nil || !s.InstancesSupervised {
		return nil, false, nil
	}
	instances, err := s.Instances.ListAllHealth(ctx)
	if err != nil {
		return nil, false, err
	}
	return instances, true, nil
}

// WithBackgroundTargetScanScope declares that background adoption scans
// maintain exactly these runtime-target-scan coordinates. Coordinates outside
// the scope are retired (see retireRuntimeTargetScans).
func WithBackgroundTargetScanScope(scope []service.RuntimeTargetScanScope) ProjectorOption {
	return func(p *Projector) {
		p.targetScanScope = append([]service.RuntimeTargetScanScope(nil), scope...)
		p.targetScanMaintained = true
	}
}

// WithDeploymentInventorySource wires the read models used to enrich the
// deployment inventory with desired artifacts, units, and instances.
func WithDeploymentInventorySource(source DeploymentInventorySource) ProjectorOption {
	return func(p *Projector) { p.inventorySource = source }
}

type deploymentInventoryEnvironmentPayload struct {
	Schema           string                          `json:"schema"`
	Entity           string                          `json:"entity"`
	Complete         bool                            `json:"complete"`
	Environment      deploymentInventoryRef          `json:"environment"`
	Freshness        deploymentInventoryFreshness    `json:"freshness"`
	InstanceCoverage string                          `json:"instance_coverage"`
	Summary          map[string]int                  `json:"summary"`
	Deployments      []deploymentInventoryDeployment `json:"deployments"`
}

type deploymentInventoryTombstonePayload struct {
	Schema      string                 `json:"schema"`
	Entity      string                 `json:"entity"`
	Deleted     bool                   `json:"deleted"`
	Environment deploymentInventoryRef `json:"environment"`
}

type deploymentInventoryRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type deploymentInventoryFreshness struct {
	StaleAfterSeconds int64 `json:"stale_after_seconds"`
}

type deploymentInventoryDeployment struct {
	Key            string                        `json:"key"`
	Service        deploymentInventoryRef        `json:"service"`
	DeploymentUnit deploymentInventoryUnit       `json:"deployment_unit"`
	Runtime        deploymentInventoryRuntime    `json:"runtime"`
	Desired        *deploymentInventoryDesired   `json:"desired,omitempty"`
	Observed       *deploymentInventoryObserved  `json:"observed,omitempty"`
	Coverage       string                        `json:"coverage"`
	DriftStatus    string                        `json:"drift_status"`
	DriftEvaluated bool                          `json:"drift_evaluated"`
	Reconcile      *deploymentInventoryReconcile `json:"reconcile,omitempty"`
	Instances      []deploymentInventoryInstance `json:"instances"`
}

type deploymentInventoryUnit struct {
	ID            string `json:"id,omitempty"`
	Key           string `json:"key"`
	DisplayName   string `json:"display_name,omitempty"`
	OwnershipMode string `json:"ownership_mode,omitempty"`
	ReconcileMode string `json:"reconcile_mode,omitempty"`
	Target        string `json:"target,omitempty"`
	Implicit      bool   `json:"implicit"`
}

type deploymentInventoryRuntime struct {
	Type   string `json:"type,omitempty"`
	Target string `json:"target,omitempty"`
}

type deploymentInventoryDesired struct {
	ArtifactID  string `json:"artifact_id,omitempty"`
	ImageRef    string `json:"image_ref,omitempty"`
	ImageTag    string `json:"image_tag,omitempty"`
	Immutable   bool   `json:"immutable"`
	DesiredHash string `json:"desired_hash,omitempty"`
	IntentID    string `json:"intent_id,omitempty"`
}

type deploymentInventoryObserved struct {
	ObservationID string `json:"observation_id"`
	ImageRepo     string `json:"image_repo,omitempty"`
	ImageDigest   string `json:"image_digest,omitempty"`
	Version       string `json:"version,omitempty"`
	Health        string `json:"health"`
	Source        string `json:"source,omitempty"`
	ObservedAt    string `json:"observed_at"`
}

type deploymentInventoryReconcile struct {
	LastReconciledAt    string `json:"last_reconciled_at,omitempty"`
	FailureReason       string `json:"failure_reason,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	BackoffUntil        string `json:"backoff_until,omitempty"`
}

type deploymentInventoryInstance struct {
	DeploymentUnitID string `json:"deployment_unit_id,omitempty"`
	Target           string `json:"target"`
	Supervisor       string `json:"supervisor,omitempty"`
	Status           string `json:"status"`
	ObservedAt       string `json:"observed_at,omitempty"`
}

type deploymentInventoryTargetScanPayload struct {
	Schema        string                       `json:"schema"`
	Entity        string                       `json:"entity"`
	Environment   string                       `json:"environment"`
	EnvironmentID string                       `json:"environment_id,omitempty"`
	Target        string                       `json:"target"`
	EndpointRef   string                       `json:"endpoint_ref,omitempty"`
	ScanState     string                       `json:"scan_state"`
	ScannedAt     string                       `json:"scanned_at"`
	Freshness     deploymentInventoryFreshness `json:"freshness"`
	Counts        *deploymentInventoryCounts   `json:"counts,omitempty"`
}

type deploymentInventoryCounts struct {
	Total     int `json:"total"`
	Managed   int `json:"managed"`
	Unmanaged int `json:"unmanaged"`
}

func shouldRefreshDeploymentInventory(eventType events.EventType) bool {
	switch eventType {
	case events.EventServiceCreated, events.EventServiceUpdated, events.EventServiceDeleted,
		events.EventEnvironmentCreated, events.EventEnvironmentUpdated, events.EventEnvironmentDeleted,
		events.EventRuntimeObservation, events.EventEnvironmentServiceStateChanged, events.EventDriftDetected,
		events.EventAdoptionImported, events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop,
		events.EventDeploymentRunCompleted,
		// Reconcile failures and backoff are persisted without their own
		// event; the per-cycle completion lets them project within one
		// reconcile interval. Timestamp-only changes are gated below.
		events.EventReconcileCompleted:
		return true
	default:
		return false
	}
}

func deploymentInventoryEnvironmentDTag(environmentID uuid.UUID) string {
	return deploymentInventoryEnvironmentDPrefix + environmentID.String()
}

func deploymentInventoryTargetScanDTag(environment, target string) string {
	return deploymentInventoryTargetScanDPrefix + environment + ":" + target
}

// deploymentInventoryStaleAfter is the longest a published observation may
// age while Bahia is healthy: observations refresh every reconcile interval but
// are republished on material change or at the snapshot repair interval.
func (p *Projector) deploymentInventoryStaleAfter() time.Duration {
	reconcileInterval := config.Defaults().Reconcile.Interval
	if p.systemConfig != nil && p.systemConfig.Reconcile.Interval > 0 {
		reconcileInterval = p.systemConfig.Reconcile.Interval
	}
	budget := 2 * reconcileInterval
	if p.repairInterval > 0 {
		budget += p.repairInterval
	}
	return budget
}

// buildDeploymentInventories composes one complete snapshot per environment.
func (p *Projector) buildDeploymentInventories(ctx context.Context) (map[uuid.UUID]deploymentInventoryEnvironmentPayload, error) {
	source := p.snapshotSource()
	services, err := source.ListServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list services for deployment inventory: %w", err)
	}
	environments, err := source.ListEnvironments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environments for deployment inventory: %w", err)
	}
	states, err := source.ListStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list states for deployment inventory: %w", err)
	}

	servicesByID := make(map[uuid.UUID]domain.Service, len(services))
	for _, svc := range services {
		servicesByID[svc.ID] = svc
	}
	instanceCoverage := instanceCoverageNotSupervised
	instancesByDeployment := map[string][]deploymentInventoryInstance{}
	if p.inventorySource != nil {
		instances, supervised, err := p.inventorySource.ListManagedInstances(ctx)
		if err != nil {
			return nil, fmt.Errorf("list managed instances for deployment inventory: %w", err)
		}
		if supervised {
			instanceCoverage = instanceCoverageSupervised
			for _, instance := range instances {
				key := instance.ServiceID.String() + ":" + instance.EnvironmentID.String()
				projected := deploymentInventoryInstance{
					Target:     safeInventoryTarget(instance.RuntimeTargetName),
					Supervisor: safeInventoryCode(string(instance.SupervisorType)),
					Status:     firstNonEmpty(safeInventoryCode(string(instance.Status)), string(domain.InstanceHealthStatusUnknown)),
					ObservedAt: formatTime(instance.LastObservedAt),
				}
				if instance.DeploymentUnitID != uuid.Nil {
					projected.DeploymentUnitID = instance.DeploymentUnitID.String()
				}
				instancesByDeployment[key] = append(instancesByDeployment[key], projected)
			}
		}
	}

	staleAfter := int64(p.deploymentInventoryStaleAfter() / time.Second)
	out := make(map[uuid.UUID]deploymentInventoryEnvironmentPayload, len(environments))
	for _, env := range environments {
		out[env.ID] = deploymentInventoryEnvironmentPayload{
			Schema:           DeploymentInventorySchema,
			Entity:           DeploymentInventoryEnvironmentEntity,
			Complete:         true,
			Environment:      deploymentInventoryRef{ID: env.ID.String(), Name: env.Name},
			Freshness:        deploymentInventoryFreshness{StaleAfterSeconds: staleAfter},
			InstanceCoverage: instanceCoverage,
			Summary:          map[string]int{},
			Deployments:      []deploymentInventoryDeployment{},
		}
	}

	for i := range states {
		state := &states[i]
		payload, ok := out[state.EnvironmentID]
		if !ok {
			continue
		}
		svc, ok := servicesByID[state.ServiceID]
		if !ok {
			continue
		}
		row, err := p.deploymentInventoryRow(ctx, source, &svc, state)
		if err != nil {
			return nil, err
		}
		row.Instances = instancesByDeployment[state.ServiceID.String()+":"+state.EnvironmentID.String()]
		if row.Instances == nil {
			row.Instances = []deploymentInventoryInstance{}
		}
		sort.Slice(row.Instances, func(a, b int) bool {
			if row.Instances[a].DeploymentUnitID != row.Instances[b].DeploymentUnitID {
				return row.Instances[a].DeploymentUnitID < row.Instances[b].DeploymentUnitID
			}
			return row.Instances[a].Target < row.Instances[b].Target
		})
		payload.Deployments = append(payload.Deployments, row)
		payload.Summary[row.Coverage]++
		out[state.EnvironmentID] = payload
	}
	for id, payload := range out {
		sort.Slice(payload.Deployments, func(a, b int) bool {
			left, right := payload.Deployments[a], payload.Deployments[b]
			if l, r := strings.ToLower(left.Service.Name), strings.ToLower(right.Service.Name); l != r {
				return l < r
			}
			return left.Key < right.Key
		})
		out[id] = payload
	}
	return out, nil
}

func (p *Projector) deploymentInventoryRow(ctx context.Context, source ProjectorSource, svc *domain.Service, state *domain.EnvironmentServiceState) (deploymentInventoryDeployment, error) {
	row := deploymentInventoryDeployment{
		Service:     deploymentInventoryRef{ID: svc.ID.String(), Name: svc.Name},
		Runtime:     deploymentInventoryRuntime{Type: safeInventoryCode(string(svc.RuntimeType)), Target: safeInventoryLabel(svc.RuntimeTargetName())},
		DriftStatus: firstNonEmpty(safeInventoryCode(string(state.DriftStatus)), string(domain.DriftStatusUnknown)),
		DeploymentUnit: deploymentInventoryUnit{
			Key:      domain.DefaultDeploymentUnitKey,
			Implicit: true,
		},
	}
	if state.DeploymentUnitID != nil && *state.DeploymentUnitID != uuid.Nil {
		row.DeploymentUnit = deploymentInventoryUnit{ID: state.DeploymentUnitID.String(), Key: "unresolved"}
		if p.inventorySource != nil {
			unit, err := p.inventorySource.GetDeploymentUnit(ctx, *state.DeploymentUnitID)
			if err != nil {
				return row, fmt.Errorf("get deployment unit %s for deployment inventory: %w", state.DeploymentUnitID, err)
			}
			if unit != nil {
				row.DeploymentUnit = deploymentInventoryUnit{
					ID:            unit.ID.String(),
					Key:           firstNonEmpty(safeInventoryLabel(unit.Key), "unresolved"),
					DisplayName:   safeInventoryDisplayName(unit.DisplayName),
					OwnershipMode: safeInventoryCode(string(unit.OwnershipMode)),
					ReconcileMode: safeInventoryCode(string(unit.ReconcileMode)),
					Target:        safeInventoryLabel(unit.EndpointRef),
					Implicit:      unit.Implicit,
				}
			}
		}
	}
	row.Key = svc.ID.String() + ":" + row.DeploymentUnit.Key

	desired, err := p.deploymentInventoryDesired(ctx, state)
	if err != nil {
		return row, err
	}
	row.Desired = desired

	observation, err := source.GetLatestObservation(ctx, state.ServiceID, state.EnvironmentID)
	if err != nil {
		return row, fmt.Errorf("get latest observation for service %s in environment %s: %w", state.ServiceID, state.EnvironmentID, err)
	}
	if observation != nil {
		row.Observed = &deploymentInventoryObserved{
			ObservationID: observation.ID.String(),
			ImageRepo:     safeInventoryImageRef(observation.ObservedImageRepo),
			ImageDigest:   safeInventoryDigest(observation.ObservedImageDigest),
			Version:       safeInventoryLabel(observation.ObservedVersion),
			Health:        firstNonEmpty(safeInventoryCode(string(observation.HealthStatus)), string(domain.HealthStatusUnknown)),
			Source:        safeInventoryCode(observation.Source),
			ObservedAt:    formatTime(observation.ObservedAt),
		}
		row.DriftEvaluated = state.CurrentObservationID != nil && *state.CurrentObservationID == observation.ID
	}

	switch {
	case row.Desired != nil && row.Observed != nil:
		row.Coverage = DeploymentCoverageObserved
	case row.Desired != nil:
		row.Coverage = DeploymentCoverageDesiredOnly
	case row.Observed != nil:
		row.Coverage = DeploymentCoverageObservedOnly
	default:
		row.Coverage = DeploymentCoverageUnknown
	}

	reconcile := deploymentInventoryReconcile{ConsecutiveFailures: state.ReconcileConsecutiveFailures}
	if state.LastReconciledAt != nil {
		reconcile.LastReconciledAt = formatTime(*state.LastReconciledAt)
	}
	if state.ReconcileBackoffUntil != nil {
		reconcile.BackoffUntil = formatTime(*state.ReconcileBackoffUntil)
	}
	if reason, ok := state.ReconcileFailureMetadata["reason"].(string); ok {
		// Only the reason code is projected; free-form failure messages may
		// carry runtime hosts or command output and stay private.
		reconcile.FailureReason = firstNonEmpty(safeInventoryCode(reason), "reconcile_failed")
	}
	if reconcile != (deploymentInventoryReconcile{}) {
		row.Reconcile = &reconcile
	}
	return row, nil
}

func (p *Projector) deploymentInventoryDesired(ctx context.Context, state *domain.EnvironmentServiceState) (*deploymentInventoryDesired, error) {
	if state.DesiredArtifactID == nil && state.DesiredHash == "" && state.DesiredRuntimeState == nil {
		return nil, nil
	}
	desired := &deploymentInventoryDesired{DesiredHash: safeInventoryDigestOrHash(state.DesiredHash)}
	if state.DesiredIntentID != nil {
		desired.IntentID = state.DesiredIntentID.String()
	}
	if state.DesiredArtifactID != nil && *state.DesiredArtifactID != uuid.Nil {
		desired.ArtifactID = state.DesiredArtifactID.String()
		if p.inventorySource != nil {
			artifact, err := p.inventorySource.GetArtifact(ctx, *state.DesiredArtifactID)
			if err != nil {
				return nil, fmt.Errorf("get desired artifact %s for deployment inventory: %w", state.DesiredArtifactID, err)
			}
			if artifact != nil {
				repo := safeInventoryImageRef(artifact.ImageRepo)
				digest := safeInventoryDigest(artifact.ImageDigest)
				if repo != "" && digest != "" {
					desired.ImageRef = repo + "@" + digest
					desired.Immutable = true
				}
				desired.ImageTag = safeInventoryLabel(artifact.ImageTag)
			}
		}
	}
	if desired.ImageRef == "" && state.DesiredRuntimeState != nil {
		desired.ImageRef = safeInventoryImageRef(state.DesiredRuntimeState.ImageRef)
		_, digest, found := strings.Cut(desired.ImageRef, "@")
		desired.Immutable = found && safeInventoryDigest(digest) != ""
	}
	return desired, nil
}

// requestDeploymentInventoryRefresh coalesces bursts of triggering events: at
// most one rebuild runs at a time, and requests that arrive meanwhile collapse
// into a single follow-up pass.
func (p *Projector) requestDeploymentInventoryRefresh(ctx context.Context) {
	p.inventoryRefreshMu.Lock()
	if p.inventoryRefreshRunning {
		p.inventoryRefreshPending = true
		p.inventoryRefreshMu.Unlock()
		return
	}
	p.inventoryRefreshRunning = true
	p.inventoryRefreshMu.Unlock()
	for {
		if _, _, err := p.publishDeploymentInventorySnapshot(ctx, false); err != nil {
			p.logger.Warn("publish deployment inventory after event failed", zap.Error(err))
		}
		p.inventoryRefreshMu.Lock()
		if !p.inventoryRefreshPending {
			p.inventoryRefreshRunning = false
			p.inventoryRefreshMu.Unlock()
			return
		}
		p.inventoryRefreshPending = false
		p.inventoryRefreshMu.Unlock()
	}
}

// noteDeletedDeploymentInventoryEnvironment ensures a deleted environment's
// coordinate is tombstoned even if hydration did not see its last snapshot.
func (p *Projector) noteDeletedDeploymentInventoryEnvironment(ctx context.Context, environmentID uuid.UUID) {
	p.inventoryMu.Lock()
	defer p.inventoryMu.Unlock()
	if err := p.hydrateDeploymentInventoryPublished(ctx); err != nil {
		p.logger.Warn("hydrate deployment inventory before environment tombstone", zap.Error(err))
	}
	if p.inventoryPublished == nil {
		p.inventoryPublished = map[string]deploymentInventoryRef{}
	}
	p.inventoryPublished[deploymentInventoryEnvironmentDTag(environmentID)] = deploymentInventoryRef{ID: environmentID.String()}
}

// publishDeploymentInventorySnapshot publishes environment snapshots and
// tombstones previously published environment coordinates that no longer
// exist. Event-driven passes (force=false) publish only material changes;
// timestamp-only changes (observed_at, last_reconciled_at) are refreshed by
// the forced repair pass, which bounds the freshness budget.
func (p *Projector) publishDeploymentInventorySnapshot(ctx context.Context, force bool) (published, tombstones int, err error) {
	if !p.Enabled() {
		return 0, 0, nil
	}
	p.inventoryMu.Lock()
	defer p.inventoryMu.Unlock()
	if err := p.hydrateDeploymentInventoryPublished(ctx); err != nil {
		return 0, 0, err
	}
	payloads, err := p.buildDeploymentInventories(ctx)
	if err != nil {
		return 0, 0, err
	}
	ids := make([]uuid.UUID, 0, len(payloads))
	for id := range payloads {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	var failures []string
	next := make(map[string]deploymentInventoryRef, len(payloads))
	for _, id := range ids {
		payload := payloads[id]
		dTag := deploymentInventoryEnvironmentDTag(id)
		next[dTag] = payload.Environment
		material := deploymentInventoryMaterialFingerprint(payload)
		if !force && p.inventoryMaterial[dTag] == material {
			continue
		}
		if err := p.publishDeploymentInventoryEnvironment(ctx, id, payload); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", dTag, err))
			p.logger.Warn("publish deployment inventory failed", zap.String("d_tag", dTag), zap.Error(err))
			continue
		}
		p.inventoryMaterial[dTag] = material
		published++
	}
	for dTag, env := range p.inventoryPublished {
		if _, current := next[dTag]; current {
			continue
		}
		if err := p.publishDeploymentInventoryTombstone(ctx, dTag, env); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", dTag, err))
			p.logger.Warn("publish deployment inventory tombstone failed", zap.String("d_tag", dTag), zap.Error(err))
			next[dTag] = env
			continue
		}
		delete(p.inventoryMaterial, dTag)
		tombstones++
	}
	p.inventoryPublished = next
	if len(failures) > 0 {
		return published, tombstones, fmt.Errorf("deployment inventory projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return published, tombstones, nil
}

func (p *Projector) publishDeploymentInventoryEnvironment(ctx context.Context, environmentID uuid.UUID, payload deploymentInventoryEnvironmentPayload) error {
	content, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal deployment inventory: %w", err)
	}
	status := "ok"
	if len(payload.Deployments) == 0 {
		status = "empty"
	}
	for _, row := range payload.Deployments {
		if row.Coverage != DeploymentCoverageObserved || row.DriftStatus != string(domain.DriftStatusInSync) || row.Observed == nil || row.Observed.Health != string(domain.HealthStatusHealthy) {
			status = "attention"
			break
		}
	}
	tags := gonostr.Tags{
		{kinds.CASControlStateTagD, deploymentInventoryEnvironmentDTag(environmentID)},
		{kinds.CASControlStateTagDomain, DeploymentInventoryDomain},
		{kinds.CASControlStateTagSchema, DeploymentInventorySchema},
		{"entity", DeploymentInventoryEnvironmentEntity},
		{"environment", environmentID.String()},
		{"status", status},
		{"deleted", "false"},
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(content), "deployment_inventory.environment", &environmentID)
}

func (p *Projector) publishDeploymentInventoryTombstone(ctx context.Context, dTag string, env deploymentInventoryRef) error {
	content, err := json.Marshal(deploymentInventoryTombstonePayload{
		Schema:      DeploymentInventorySchema,
		Entity:      DeploymentInventoryEnvironmentEntity,
		Deleted:     true,
		Environment: deploymentInventoryRef{ID: env.ID},
	})
	if err != nil {
		return fmt.Errorf("marshal deployment inventory tombstone: %w", err)
	}
	tags := gonostr.Tags{
		{kinds.CASControlStateTagD, dTag},
		{kinds.CASControlStateTagDomain, DeploymentInventoryDomain},
		{kinds.CASControlStateTagSchema, DeploymentInventorySchema},
		{"entity", DeploymentInventoryEnvironmentEntity},
		{"environment", env.ID},
		{"status", "deleted"},
		{"deleted", "true"},
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(content), "deployment_inventory.environment", nil)
}

// hydrateDeploymentInventoryPublished restores which environment coordinates
// this service last published live, so a restart still tombstones
// environments deleted while it was down.
func (p *Projector) hydrateDeploymentInventoryPublished(ctx context.Context) error {
	if p.inventoryHydrated {
		return nil
	}
	p.inventoryPublished = map[string]deploymentInventoryRef{}
	p.inventoryMaterial = map[string]string{}
	if p.eventRepo == nil {
		p.inventoryHydrated = true
		return nil
	}
	records, err := p.eventRepo.FindByTag(ctx, kinds.CASControlStateTagDomain, DeploymentInventoryDomain, []int{KindCASControlState}, deploymentInventoryHydrateLimit)
	if err != nil {
		return fmt.Errorf("hydrate deployment inventory projection cache: %w", err)
	}
	if len(records) >= deploymentInventoryHydrateLimit {
		// Older coordinates beyond the window cannot be tombstoned after a
		// restart; live EnvironmentDeleted events still tombstone directly.
		p.logger.Warn("deployment inventory hydration window saturated", zap.Int("records", len(records)))
	}
	servicePubkey := ""
	if p.privateKey != "" {
		servicePubkey, err = publicKeyHexFromPrivateKeyHex(p.privateKey)
		if err != nil {
			return fmt.Errorf("derive deployment inventory service pubkey: %w", err)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	seen := map[string]struct{}{}
	for _, record := range records {
		if servicePubkey != "" && record.PubKey != servicePubkey {
			continue
		}
		var tags gonostr.Tags
		if err := json.Unmarshal(record.Tags, &tags); err != nil {
			continue
		}
		dTag := tagValue(tags, kinds.CASControlStateTagD)
		if !strings.HasPrefix(dTag, deploymentInventoryEnvironmentDPrefix) {
			continue
		}
		if _, ok := seen[dTag]; ok {
			continue
		}
		seen[dTag] = struct{}{}
		if tagValue(tags, "deleted") == "true" {
			continue
		}
		p.inventoryPublished[dTag] = deploymentInventoryRef{ID: strings.TrimPrefix(dTag, deploymentInventoryEnvironmentDPrefix)}
	}
	p.inventoryHydrated = true
	return nil
}

// publishRuntimeTargetScans publishes the redacted aggregate of each target of
// a completed adoption scan. Only counts, scan state, and target/environment
// aliases are public; per-instance detail stays on the encrypted adoption/scan
// response to the authorized requester.
//
// Background scans repeat every few minutes, so a coordinate is republished
// only when its material content (counts, scan state, aliases, freshness
// budget, origin) changes, or as a freshness heartbeat once the published
// scanned_at is older than the refresh interval. Unchanged counts therefore
// cost at most one event per coordinate per refresh interval, and consumers
// judge staleness from scanned_at against stale_after_seconds.
func (p *Projector) publishRuntimeTargetScans(ctx context.Context, scan service.AdoptionScanCompleted) error {
	if !p.Enabled() {
		return nil
	}
	p.targetScanMu.Lock()
	defer p.targetScanMu.Unlock()
	if err := p.hydrateTargetScans(ctx); err != nil {
		// Without the retained state every coordinate is treated as new;
		// the dedupe/admission path still bounds what reaches relays.
		p.logger.Warn("hydrate runtime target scan state", zap.Error(err))
	}
	staleAfter := int64(p.targetScanStaleAfter() / time.Second)
	refreshAfter := p.targetScanRefreshAfter()
	scannedAt := scan.CompletedAt.UTC()
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	// Timestamps are published at second resolution; compare what is
	// published so hydrated and live state agree.
	scannedAt = scannedAt.Truncate(time.Second)
	origin := service.AdoptionScanOriginOperator
	if scan.Origin == service.AdoptionScanOriginBackground {
		origin = service.AdoptionScanOriginBackground
	}
	var failures []string
	for _, target := range scan.Targets {
		alias := safeInventoryLabel(target.Target)
		environment := safeInventoryLabel(target.Environment)
		if alias == "" || environment == "" {
			failures = append(failures, "target scan without a safe target/environment alias")
			continue
		}
		payload := deploymentInventoryTargetScanPayload{
			Schema:      DeploymentInventorySchema,
			Entity:      DeploymentInventoryTargetScanEntity,
			Environment: environment,
			Target:      alias,
			EndpointRef: safeInventoryLabel(target.EndpointRef),
			ScanState:   targetScanUnavailable,
			ScannedAt:   formatTime(scannedAt),
			Freshness:   deploymentInventoryFreshness{StaleAfterSeconds: staleAfter},
		}
		if target.EnvironmentID != nil && *target.EnvironmentID != uuid.Nil {
			payload.EnvironmentID = target.EnvironmentID.String()
		}
		if target.Available {
			payload.ScanState = targetScanComplete
			payload.Counts = &deploymentInventoryCounts{Total: target.Total, Managed: target.Managed, Unmanaged: target.Unmanaged}
		}
		dTag := deploymentInventoryTargetScanDTag(environment, alias)
		tags := gonostr.Tags{
			{kinds.CASControlStateTagD, dTag},
			{kinds.CASControlStateTagDomain, DeploymentInventoryDomain},
			{kinds.CASControlStateTagSchema, DeploymentInventorySchema},
			{"entity", DeploymentInventoryTargetScanEntity},
			{"target", alias},
			{"status", payload.ScanState},
			{"origin", origin},
			{"deleted", "false"},
		}
		material := targetScanMaterialFingerprint(payload, origin)
		if previous, ok := p.targetScans[dTag]; ok && !previous.deleted {
			if scannedAt.Before(previous.scannedAt) {
				// An older result finishing late never overwrites a newer one.
				continue
			}
			if previous.material == material && scannedAt.Sub(previous.scannedAt) < refreshAfter {
				p.projection().count(projectionFamily(KindCASControlState, tags), func(m *ProjectionFamilyMetrics) { m.Deduped++ })
				continue
			}
		}
		content, err := json.Marshal(payload)
		if err != nil {
			failures = append(failures, fmt.Sprintf("marshal %s: %v", alias, err))
			continue
		}
		if err := p.publishSigned(ctx, KindCASControlState, tags, string(content), "deployment_inventory.target_scan", nil); err != nil {
			failures = append(failures, fmt.Sprintf("publish %s: %v", alias, err))
			continue
		}
		p.targetScans[dTag] = targetScanRecord{material: material, scannedAt: scannedAt, origin: origin, environment: environment, target: alias, endpointRef: payload.EndpointRef}
	}
	if len(failures) > 0 {
		return fmt.Errorf("runtime target scan projection completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// targetScanRecord is the last published state of one target-scan coordinate.
type targetScanRecord struct {
	material    string
	scannedAt   time.Time
	origin      string
	environment string
	target      string
	endpointRef string
	deleted     bool
}

type deploymentInventoryTargetScanTombstonePayload struct {
	Schema      string `json:"schema"`
	Entity      string `json:"entity"`
	Deleted     bool   `json:"deleted"`
	Environment string `json:"environment"`
	Target      string `json:"target"`
}

// targetScanMaterialFingerprint hashes a target-scan aggregate without its
// scan time, so repeated scans with unchanged counts are not material.
func targetScanMaterialFingerprint(payload deploymentInventoryTargetScanPayload, origin string) string {
	payload.ScannedAt = ""
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(append(encoded, []byte("\x00"+origin)...))
	return hex.EncodeToString(sum[:])
}

// targetScanRefreshAfter is the heartbeat interval: an unchanged aggregate is
// republished once its published scanned_at is at least this old.
func (p *Projector) targetScanRefreshAfter() time.Duration {
	if p.repairInterval > 0 {
		return p.repairInterval
	}
	return defaultProjectorRepairInterval
}

// targetScanStaleAfter is the freshness budget published with target-scan
// aggregates. With background scanning, a healthy target's published
// scanned_at lags by at most the heartbeat plus one scan period: the interval
// and jitter, up to one timeout waiting for the previous cycle to finish, and
// one timeout scanning. The budget allows a whole missed period on top.
// Without background scanning, the inventory budget applies (operator scans
// age into stale).
func (p *Projector) targetScanStaleAfter() time.Duration {
	budget := p.deploymentInventoryStaleAfter()
	if p.systemConfig == nil || !p.systemConfig.Adoption.BackgroundScanEnabled() {
		return budget
	}
	bg := p.systemConfig.Adoption.BackgroundScan
	background := p.targetScanRefreshAfter() + 2*(bg.Interval+bg.Jitter+bg.Timeout)
	if background > budget {
		return background
	}
	return budget
}

// retireRuntimeTargetScans tombstones target-scan coordinates that no
// longer describe a configured target. It runs with every snapshot repair
// (startup and each repair interval), whatever the background scan mode:
//
//   - a coordinate last published by a background scan that is outside the
//     background scope (target removed, or background scanning disabled) is
//     retired immediately, because nothing will refresh it;
//   - a coordinate whose endpoint alias is no longer in runtime.endpoints is
//     retired, because its target was removed from configuration;
//   - while background scanning maintains the scope, an operator ad-hoc
//     aggregate of an unconfigured target is retired once it is stale.
//
// Without background scanning, operator aggregates of configured endpoints
// are kept and age into stale, as before.
func (p *Projector) retireRuntimeTargetScans(ctx context.Context, now time.Time) (int, error) {
	if !p.Enabled() {
		return 0, nil
	}
	p.targetScanMu.Lock()
	defer p.targetScanMu.Unlock()
	if err := p.hydrateTargetScans(ctx); err != nil {
		return 0, err
	}
	keep := make(map[string]struct{}, len(p.targetScanScope))
	for _, scope := range p.targetScanScope {
		environment, target := safeInventoryLabel(scope.Environment), safeInventoryLabel(scope.Target)
		if environment != "" && target != "" {
			keep[deploymentInventoryTargetScanDTag(environment, target)] = struct{}{}
		}
	}
	var endpoints map[string]struct{}
	if p.systemConfig != nil {
		endpoints = make(map[string]struct{}, len(p.systemConfig.Runtime.Endpoints))
		for ref := range p.systemConfig.Runtime.Endpoints {
			if label := safeInventoryLabel(ref); label != "" {
				endpoints[label] = struct{}{}
			}
		}
	}
	staleAfter := p.targetScanStaleAfter()
	dTags := make([]string, 0, len(p.targetScans))
	for dTag := range p.targetScans {
		dTags = append(dTags, dTag)
	}
	sort.Strings(dTags)
	retired := 0
	var failures []string
	for _, dTag := range dTags {
		record := p.targetScans[dTag]
		if record.deleted {
			continue
		}
		if _, maintained := keep[dTag]; maintained {
			continue
		}
		_, endpointConfigured := endpoints[record.endpointRef]
		switch {
		case record.origin == service.AdoptionScanOriginBackground:
		case endpoints != nil && record.endpointRef != "" && !endpointConfigured:
		case p.targetScanMaintained && now.Sub(record.scannedAt) > staleAfter:
		default:
			continue
		}
		if err := p.publishTargetScanTombstone(ctx, dTag, record); err != nil {
			failures = append(failures, fmt.Sprintf("tombstone %s: %v", dTag, err))
			continue
		}
		record.deleted = true
		p.targetScans[dTag] = record
		retired++
	}
	if len(failures) > 0 {
		return retired, fmt.Errorf("runtime target scan retirement completed with %d failure(s): %s", len(failures), strings.Join(failures, "; "))
	}
	return retired, nil
}

func (p *Projector) publishTargetScanTombstone(ctx context.Context, dTag string, record targetScanRecord) error {
	content, err := json.Marshal(deploymentInventoryTargetScanTombstonePayload{
		Schema:      DeploymentInventorySchema,
		Entity:      DeploymentInventoryTargetScanEntity,
		Deleted:     true,
		Environment: record.environment,
		Target:      record.target,
	})
	if err != nil {
		return fmt.Errorf("marshal runtime target scan tombstone: %w", err)
	}
	tags := gonostr.Tags{
		{kinds.CASControlStateTagD, dTag},
		{kinds.CASControlStateTagDomain, DeploymentInventoryDomain},
		{kinds.CASControlStateTagSchema, DeploymentInventorySchema},
		{"entity", DeploymentInventoryTargetScanEntity},
		{"target", record.target},
		{"status", "deleted"},
		{"deleted", "true"},
	}
	return p.publishSigned(ctx, KindCASControlState, tags, string(content), "deployment_inventory.target_scan", nil)
}

// hydrateTargetScans restores the last published state of each target-scan
// coordinate from this service's retained events, so a restart neither
// republishes unchanged counts nor forgets coordinates it must retire. It
// queries by entity tag, giving target scans their own hydration window. On
// failure the live state is kept and hydration is retried on the next call;
// once it succeeds, live state recorded meanwhile wins over retained events.
func (p *Projector) hydrateTargetScans(ctx context.Context) error {
	if p.targetScans == nil {
		p.targetScans = map[string]targetScanRecord{}
	}
	if p.targetScansHydrated {
		return nil
	}
	if p.eventRepo == nil {
		p.targetScansHydrated = true
		return nil
	}
	records, err := p.eventRepo.FindByTag(ctx, "entity", DeploymentInventoryTargetScanEntity, []int{KindCASControlState}, deploymentInventoryHydrateLimit)
	if err != nil {
		return fmt.Errorf("hydrate runtime target scan state: %w", err)
	}
	if len(records) >= deploymentInventoryHydrateLimit {
		p.logger.Warn("runtime target scan hydration window saturated", zap.Int("records", len(records)))
	}
	servicePubkey := ""
	if p.privateKey != "" {
		servicePubkey, err = publicKeyHexFromPrivateKeyHex(p.privateKey)
		if err != nil {
			return fmt.Errorf("derive runtime target scan service pubkey: %w", err)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	hydrated := map[string]targetScanRecord{}
	for _, record := range records {
		if servicePubkey != "" && record.PubKey != servicePubkey {
			continue
		}
		var tags gonostr.Tags
		if err := json.Unmarshal(record.Tags, &tags); err != nil {
			p.logger.Warn("skip retained runtime target scan with malformed tags", zap.String("event_id", record.ID), zap.Error(err))
			continue
		}
		dTag := tagValue(tags, kinds.CASControlStateTagD)
		if !strings.HasPrefix(dTag, deploymentInventoryTargetScanDPrefix) || tagValue(tags, "entity") != DeploymentInventoryTargetScanEntity {
			continue
		}
		if _, seen := hydrated[dTag]; seen {
			continue
		}
		if tagValue(tags, "deleted") == "true" {
			hydrated[dTag] = targetScanRecord{deleted: true}
			continue
		}
		var payload deploymentInventoryTargetScanPayload
		if err := json.Unmarshal([]byte(record.Content), &payload); err != nil {
			p.logger.Warn("skip retained runtime target scan with malformed content", zap.String("event_id", record.ID), zap.String("d_tag", dTag), zap.Error(err))
			continue
		}
		scannedAt, err := time.Parse(time.RFC3339Nano, payload.ScannedAt)
		if err != nil {
			// The event's own signing time bounds the scan time from above;
			// using it keeps staleness and heartbeat judgements conservative.
			p.logger.Warn("retained runtime target scan has no valid scanned_at; using its created_at", zap.String("event_id", record.ID), zap.String("d_tag", dTag), zap.Error(err))
			scannedAt = record.CreatedAt
		}
		origin := tagValue(tags, "origin")
		if origin != service.AdoptionScanOriginBackground {
			// Events published before scan origins existed came from
			// operator scans.
			origin = service.AdoptionScanOriginOperator
		}
		hydrated[dTag] = targetScanRecord{
			material:    targetScanMaterialFingerprint(payload, origin),
			scannedAt:   scannedAt.UTC(),
			origin:      origin,
			environment: payload.Environment,
			target:      payload.Target,
			endpointRef: payload.EndpointRef,
		}
	}
	for dTag, record := range hydrated {
		if _, live := p.targetScans[dTag]; !live {
			p.targetScans[dTag] = record
		}
	}
	p.targetScansHydrated = true
	return nil
}

func adoptionScanCompletedPayload(data any) (service.AdoptionScanCompleted, bool) {
	switch v := data.(type) {
	case service.AdoptionScanCompleted:
		return v, true
	case *service.AdoptionScanCompleted:
		if v != nil {
			return *v, true
		}
	}
	return service.AdoptionScanCompleted{}, false
}

func safeInventoryImageRef(value string) string {
	value = strings.TrimSpace(value)
	if !inventoryImageRefPattern.MatchString(value) {
		return ""
	}
	return value
}

func safeInventoryLabel(value string) string {
	value = strings.TrimSpace(value)
	if !inventoryLabelPattern.MatchString(value) {
		return ""
	}
	return value
}

// safeInventoryTarget keeps an instance distinguishable even when its runtime
// target name falls outside the label allowlist (for example a systemd
// template unit "app@1.service"): it is replaced by a stable, non-reversible
// alias instead of being dropped.
func safeInventoryTarget(value string) string {
	if label := safeInventoryLabel(value); label != "" {
		return label
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return "target-" + hex.EncodeToString(sum[:6])
}

// deploymentInventoryMaterialFingerprint hashes a snapshot without the
// timestamps and rotating observation ids that advance on every reconcile pass.
func deploymentInventoryMaterialFingerprint(payload deploymentInventoryEnvironmentPayload) string {
	material := payload
	material.Deployments = make([]deploymentInventoryDeployment, len(payload.Deployments))
	for i, row := range payload.Deployments {
		if row.Observed != nil {
			observed := *row.Observed
			observed.ObservedAt = ""
			observed.ObservationID = ""
			row.Observed = &observed
		}
		if row.Reconcile != nil {
			reconcile := *row.Reconcile
			reconcile.LastReconciledAt = ""
			row.Reconcile = &reconcile
		}
		instances := make([]deploymentInventoryInstance, len(row.Instances))
		for j, instance := range row.Instances {
			instance.ObservedAt = ""
			instances[j] = instance
		}
		row.Instances = instances
		material.Deployments[i] = row
	}
	encoded, _ := json.Marshal(material)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func safeInventoryCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !inventoryCodePattern.MatchString(value) {
		return ""
	}
	return value
}

func safeInventoryDigest(value string) string {
	value = domain.NormalizeImageDigest(value)
	if !inventoryDigestPattern.MatchString(value) {
		return ""
	}
	return value
}

func safeInventoryDigestOrHash(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if digest := safeInventoryDigest(value); digest != "" {
		return digest
	}
	if len(value) == 64 && strings.Trim(value, "0123456789abcdef") == "" {
		return value
	}
	return ""
}

func safeInventoryDisplayName(value string) string {
	value = strings.TrimSpace(domain.SanitizeEvidence(value))
	if len(value) > 128 || strings.ContainsAny(value, "\n\r\t=") {
		return ""
	}
	return value
}

// publicAuditData allowlists audit payloads that would otherwise expose raw
// runtime detail. A runtime observation carries the raw Docker host fallback,
// container identity, and normalized env/command/volume state; runtime action
// payloads carry free-form apply warnings; adoption imports carry container
// identity. Their public audits keep only identity, image, health, hash, and
// code fields.
func publicAuditData(e events.Event) any {
	switch e.Type {
	case events.EventRuntimeObservation:
		return publicObservationAuditData(e.Data)
	case events.EventRuntimeDeploy, events.EventRuntimeRestart, events.EventRuntimeStop:
		return allowlistAuditMap(e.Data, runtimeActionAuditKeys)
	case events.EventAdoptionImported:
		return allowlistAuditMap(e.Data, adoptionImportedAuditKeys)
	default:
		return e.Data
	}
}

var (
	runtimeActionAuditKeys    = []string{"service_id", "environment_id", "service", "environment", "runtime_target", "observation_id", "health_status", "artifact_id", "desired_hash", "environment_revision", "renderer", "execution_mode", "failure_reason"}
	adoptionImportedAuditKeys = []string{"service_id", "environment_id", "artifact_id", "target_name", "status"}
)

func allowlistAuditMap(data any, keys []string) any {
	values, ok := data.(map[string]any)
	if !ok {
		// Non-map payloads are not produced for these events; publish
		// nothing rather than an unreviewed shape.
		return nil
	}
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		value, present := values[key]
		if !present || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			if safe := safeInventoryLabel(v); safe != "" {
				out[key] = safe
			}
		case uuid.UUID:
			out[key] = v.String()
		case fmt.Stringer:
			if safe := safeInventoryLabel(v.String()); safe != "" {
				out[key] = safe
			}
		}
	}
	return out
}

func publicObservationAuditData(data any) any {
	var obs *domain.RuntimeObservation
	switch v := data.(type) {
	case *domain.RuntimeObservation:
		obs = v
	case domain.RuntimeObservation:
		obs = &v
	default:
		return nil
	}
	if obs == nil {
		return nil
	}
	summary := map[string]any{
		"id":                    obs.ID.String(),
		"service_id":            obs.ServiceID.String(),
		"environment_id":        obs.EnvironmentID.String(),
		"observed_image_repo":   safeInventoryImageRef(obs.ObservedImageRepo),
		"observed_image_digest": safeInventoryDigest(obs.ObservedImageDigest),
		"observed_version":      safeInventoryLabel(obs.ObservedVersion),
		"health_status":         safeInventoryCode(string(obs.HealthStatus)),
		"source":                safeInventoryCode(obs.Source),
		"normalized_hash":       safeInventoryDigestOrHash(obs.NormalizedHash),
		"observed_at":           formatTime(obs.ObservedAt),
	}
	if obs.DeploymentUnitID != nil {
		summary["deployment_unit_id"] = obs.DeploymentUnitID.String()
	}
	return summary
}
