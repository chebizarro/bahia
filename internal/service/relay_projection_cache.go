package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// FamilyApplier applies one decoded projection event to a family-specific cache.
type FamilyApplier func(ctx context.Context, event any) error

// RelayProjectionCache applies relay-canonical projection events to local cache repositories.
//
// Events decoded with their source event (every KindCatalog decoder sets it)
// are resolved through nostrutil.Lifecycle (C-12, C-13): replaceable and
// addressable state is latest-wins per (kind, pubkey, d) with the lowest-id
// tie-break, NIP-09 deletions tombstone what they delete and keep it from
// coming back, and NIP-40 expired events are ignored on arrival and
// tombstoned when they expire (Run). Regular events keep the (family, d)
// ordering in the meta repository.
type RelayProjectionCache struct {
	meta      repository.RelayProjectionMetaRepository
	logger    *zap.Logger
	appliers  map[string]FamilyApplier
	lifecycle *nostrutil.Lifecycle
	now       func() time.Time

	mu sync.Mutex
	// applied holds the decoded projection behind each live Lifecycle entry,
	// so a deletion or expiry can replay it as a tombstone.
	applied map[gonostr.ID]any
}

// projectionSource is implemented by decoded projections that carry the
// signed event they were decoded from.
type projectionSource interface {
	SourceEvent() *gonostr.Event
}

func NewRelayProjectionCache(meta repository.RelayProjectionMetaRepository, logger *zap.Logger) *RelayProjectionCache {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RelayProjectionCache{
		meta:      meta,
		logger:    logger,
		appliers:  make(map[string]FamilyApplier),
		lifecycle: nostrutil.NewLifecycle(),
		now:       time.Now,
		applied:   make(map[gonostr.ID]any),
	}
}

// Name identifies the cache's expiry runner.
func (c *RelayProjectionCache) Name() string { return "relay-projection-expiry" }

// Run tombstones applied projections when their NIP-40 expiration passes. It
// waits on a timer for the next expiration and returns when ctx ends.
func (c *RelayProjectionCache) Run(ctx context.Context) error {
	return c.lifecycle.RunExpiry(ctx, c.now, func(expired []nostrutil.Entry) {
		if err := c.remove(ctx, expired); err != nil {
			c.logger.Warn("tombstoning expired relay projections failed", zap.Error(err))
		}
	})
}

func (c *RelayProjectionCache) RegisterApplier(family any, fn FamilyApplier) {
	stream := fmt.Sprint(family)
	if fn == nil {
		delete(c.appliers, stream)
		return
	}
	c.appliers[stream] = fn
}

// ProjectionCacheRepositories groups tier1 and tier2 repositories used by relay projection appliers.
type ProjectionCacheRepositories struct {
	Workers      repository.WorkerRepository
	Services     repository.ServiceRepository
	Environments repository.EnvironmentRepository
	Builds       repository.BuildRepository
	Artifacts    repository.ArtifactRepository
	Policies     repository.DeploymentPolicyRepository
}

func (c *RelayProjectionCache) RegisterTier1Tier2Appliers(repos ProjectionCacheRepositories) {
	if repos.Workers != nil {
		c.RegisterApplier("worker", workerApplier(repos.Workers))
		c.RegisterApplier("continuity", continuityApplier(repos.Workers))
	}
	if repos.Services != nil {
		c.RegisterApplier("service", serviceApplier(repos.Services))
	}
	if repos.Environments != nil {
		c.RegisterApplier("environment", environmentApplier(repos.Environments))
	}
	if repos.Builds != nil {
		c.RegisterApplier("build", buildApplier(repos.Builds))
	}
	if repos.Artifacts != nil {
		c.RegisterApplier("artifact", artifactApplier(repos.Artifacts))
	}
	if repos.Policies != nil {
		c.RegisterApplier("policy", policyApplier(repos.Policies))
	}
}

func (c *RelayProjectionCache) Apply(ctx context.Context, event any) error {
	if c == nil || c.meta == nil {
		return errors.New("relay projection cache requires a meta repository")
	}
	source := sourceEventOf(event)
	var replaced *nostrutil.Entry
	if source != nil {
		decision := c.lifecycle.Observe(source, c.now())
		switch decision.Outcome {
		case nostrutil.OutcomeAccept:
			replaced = decision.Replaced
		case nostrutil.OutcomeDeletion:
			return c.remove(ctx, decision.Removed)
		default:
			c.logger.Debug("skipping relay projection event", zap.String("source_event_id", source.ID.Hex()), zap.Stringer("outcome", decision.Outcome))
			return nil
		}
	}
	projection, err := projectionFields(event)
	if err != nil {
		c.release(source)
		return err
	}

	if source == nil || !nostrutil.IsStateKind(source.Kind) {
		existing, err := c.meta.Get(ctx, projection.stream, projection.entityKey)
		if err != nil {
			c.release(source)
			return fmt.Errorf("getting relay projection meta for %s/%s: %w", projection.stream, projection.entityKey, err)
		}
		if existing != nil && !projectionVersion(projection.updatedAt, projection.sourceEventID).Supersedes(projectionVersion(existing.UpdatedAt, existing.SourceEventID)) {
			c.logger.Debug("skipping stale relay projection event", zap.String("stream", projection.stream), zap.String("entity_key", projection.entityKey), zap.String("source_event_id", projection.sourceEventID))
			return nil
		}
	}

	if applier := c.appliers[projection.stream]; applier != nil {
		if err := applier(ctx, event); err != nil {
			c.release(source)
			return fmt.Errorf("applying relay projection family %s: %w", projection.stream, err)
		}
	}
	c.track(source, replaced, event)

	meta := repository.RelayProjectionMeta{
		Stream:        projection.stream,
		EntityKey:     projection.entityKey,
		UpdatedAt:     projection.updatedAt,
		SourceEventID: projection.sourceEventID,
		Tombstoned:    projection.tombstoned,
	}
	if err := c.meta.Upsert(ctx, meta); err != nil {
		return fmt.Errorf("upserting relay projection meta for %s/%s: %w", projection.stream, projection.entityKey, err)
	}
	return nil
}

func sourceEventOf(event any) *gonostr.Event {
	if carrier, ok := event.(projectionSource); ok {
		return carrier.SourceEvent()
	}
	return nil
}

// projectionVersion orders projections without a source event by the same
// NIP-01 rule: later timestamp, then lowest source event id.
func projectionVersion(at time.Time, sourceEventID string) nostrutil.Version {
	return nostrutil.Version{CreatedAt: gonostr.Timestamp(at.Unix()), ID: sourceEventID}
}

func (c *RelayProjectionCache) release(source *gonostr.Event) {
	if source != nil {
		c.lifecycle.Release(source.ID)
	}
}

// track remembers an applied projection the Lifecycle may later delete or
// expire, and forgets the version it replaced.
func (c *RelayProjectionCache) track(source *gonostr.Event, replaced *nostrutil.Entry, event any) {
	if source == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if replaced != nil {
		delete(c.applied, replaced.ID)
	}
	if nostrutil.IsStateKind(source.Kind) || nostrutil.ExpiresAt(source) > 0 {
		c.applied[source.ID] = event
	}
}

// remove replays each deleted or expired projection as a tombstone through
// its family applier.
func (c *RelayProjectionCache) remove(ctx context.Context, entries []nostrutil.Entry) error {
	var errs []error
	for _, entry := range entries {
		c.mu.Lock()
		event, ok := c.applied[entry.ID]
		delete(c.applied, entry.ID)
		c.mu.Unlock()
		if !ok {
			continue
		}
		tombstone, err := tombstoneOf(event)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		projection, err := projectionFields(tombstone)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if applier := c.appliers[projection.stream]; applier != nil {
			if err := applier(ctx, tombstone); err != nil {
				errs = append(errs, fmt.Errorf("tombstoning relay projection %s/%s: %w", projection.stream, projection.entityKey, err))
				continue
			}
		}
		c.logger.Info("relay projection withdrawn", zap.String("stream", projection.stream), zap.String("entity_key", projection.entityKey), zap.String("source_event_id", projection.sourceEventID))
	}
	return errors.Join(errs...)
}

// tombstoneOf copies a decoded projection with Tombstone set.
func tombstoneOf(event any) (any, error) {
	value := reflect.ValueOf(event)
	isPointer := value.Kind() == reflect.Pointer
	elem := indirect(value)
	if elem.Kind() != reflect.Struct {
		return nil, errors.New("decoded projection event must be a struct or struct pointer")
	}
	copied := reflect.New(elem.Type())
	copied.Elem().Set(elem)
	field := copied.Elem().FieldByName("Tombstone")
	if !field.IsValid() || field.Kind() != reflect.Bool || !field.CanSet() {
		return nil, errors.New("decoded projection event has no Tombstone field")
	}
	field.SetBool(true)
	if isPointer {
		return copied.Interface(), nil
	}
	return copied.Elem().Interface(), nil
}

func workerApplier(repo repository.WorkerRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		workerPayload, ok := projectionPayload(value, "Worker")
		if !ok {
			return nil
		}
		workerField := indirect(workerPayload).FieldByName("Worker")
		if !workerField.IsValid() || workerField.IsNil() {
			return nil
		}
		worker, ok := workerField.Interface().(*domain.Worker)
		if !ok || worker == nil {
			return nil
		}
		if boolField(value, "Tombstone") {
			return repo.UpdateStatus(ctx, worker.PubKey, domain.WorkerStatusOffline)
		}
		if err := repo.Upsert(ctx, worker); err != nil && !errors.Is(err, repository.ErrStaleWrite) {
			return err
		}
		// ErrStaleWrite: the local row already has a newer advertisement than
		// the relay copy, which is the expected case when replaying state this
		// daemon published itself.
		return nil
	}
}

func continuityApplier(repo repository.WorkerRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			StandbyNode *struct {
				WorkerPubKey  string                  `json:"worker_pubkey"`
				ServiceKey    string                  `json:"service_key"`
				Tier          domain.StandbyTier      `json:"tier"`
				ArtifactRef   string                  `json:"artifact_ref"`
				Profiles      []domain.ContinuityMode `json:"profiles"`
				UpdatedAt     time.Time               `json:"updated_at"`
				SourceEventID string                  `json:"source_event_id"`
			} `json:"standby_node"`
		}
		if ok, err := decodeProjectionPayload(value, "Continuity", &payload); err != nil || !ok || payload.StandbyNode == nil {
			return err
		}
		workerPubKey := strings.TrimSpace(payload.StandbyNode.WorkerPubKey)
		serviceKey := strings.TrimSpace(payload.StandbyNode.ServiceKey)
		if workerPubKey == "" || serviceKey == "" {
			return fmt.Errorf("standby node projection requires worker pubkey and service key")
		}
		worker, err := repo.GetByPubKey(ctx, workerPubKey)
		if err != nil {
			return err
		}
		if worker == nil {
			if boolField(value, "Tombstone") {
				return nil
			}
			worker = &domain.Worker{PubKey: workerPubKey, Status: domain.WorkerStatusOffline, SchedulingState: domain.WorkerSchedulingActive}
		}
		if boolField(value, "Tombstone") {
			worker.StandbyAssignments = removeStandbyAssignment(worker.StandbyAssignments, serviceKey)
			return repo.Upsert(ctx, worker)
		}
		assignment := domain.WorkerStandbyAssignment{
			ServiceKey:        serviceKey,
			Tier:              payload.StandbyNode.Tier,
			SupportedProfiles: append([]domain.ContinuityMode(nil), payload.StandbyNode.Profiles...),
			ArtifactRef:       strings.TrimSpace(payload.StandbyNode.ArtifactRef),
			UpdatedAt:         payload.StandbyNode.UpdatedAt,
			SourceEventID:     payload.StandbyNode.SourceEventID,
		}
		worker.StandbyAssignments = upsertStandbyAssignment(worker.StandbyAssignments, assignment)
		return repo.Upsert(ctx, worker)
	}
}

func upsertStandbyAssignment(assignments []domain.WorkerStandbyAssignment, assignment domain.WorkerStandbyAssignment) []domain.WorkerStandbyAssignment {
	for i := range assignments {
		if strings.TrimSpace(assignments[i].ServiceKey) == assignment.ServiceKey {
			assignments[i] = assignment
			return assignments
		}
	}
	return append(assignments, assignment)
}

func removeStandbyAssignment(assignments []domain.WorkerStandbyAssignment, serviceKey string) []domain.WorkerStandbyAssignment {
	out := assignments[:0]
	for _, assignment := range assignments {
		if strings.TrimSpace(assignment.ServiceKey) != serviceKey {
			out = append(out, assignment)
		}
	}
	return out
}

func serviceApplier(repo repository.ServiceRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			Deleted       bool               `json:"deleted"`
			ID            string             `json:"id"`
			Name          string             `json:"name"`
			RepoURL       string             `json:"repo_url"`
			ArtifactRepo  string             `json:"artifact_repo"`
			DefaultBranch string             `json:"default_branch"`
			RuntimeType   domain.RuntimeType `json:"runtime_type"`
			CreatedAt     time.Time          `json:"created_at"`
			UpdatedAt     time.Time          `json:"updated_at"`
		}
		if ok, err := decodeProjectionPayload(value, "Service", &payload); err != nil || !ok {
			return err
		}
		id, err := parseProjectionUUID(payload.ID, "service id")
		if err != nil {
			return err
		}
		if boolField(value, "Tombstone") || payload.Deleted {
			return repo.Delete(ctx, id)
		}
		svc := &domain.Service{ID: id, Name: payload.Name, RepoURL: payload.RepoURL, ArtifactRepo: payload.ArtifactRepo, DefaultBranch: payload.DefaultBranch, RuntimeType: payload.RuntimeType, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt}
		if existing, err := repo.GetByID(ctx, id); err == nil && existing != nil {
			svc.OrgID = existing.OrgID
			svc.Repository = existing.Repository
			svc.RuntimeConfig = existing.RuntimeConfig
			return repo.Update(ctx, svc)
		}
		return repo.Create(ctx, svc)
	}
}

func environmentApplier(repo repository.EnvironmentRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			Deleted        bool                  `json:"deleted"`
			ID             string                `json:"id"`
			Name           string                `json:"name"`
			Protected      bool                  `json:"protected"`
			DeployStrategy domain.DeployStrategy `json:"deploy_strategy"`
			CreatedAt      time.Time             `json:"created_at"`
			UpdatedAt      time.Time             `json:"updated_at"`
		}
		if ok, err := decodeProjectionPayload(value, "Environment", &payload); err != nil || !ok {
			return err
		}
		id, err := parseProjectionUUID(payload.ID, "environment id")
		if err != nil {
			return err
		}
		if boolField(value, "Tombstone") || payload.Deleted {
			return repo.Delete(ctx, id)
		}
		env := &domain.Environment{ID: id, Name: payload.Name, Protected: payload.Protected, DeployStrategy: payload.DeployStrategy, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt}
		if existing, err := repo.GetByID(ctx, id); err == nil && existing != nil {
			env.OrgID = existing.OrgID
			env.LoomWorkerSelector = existing.LoomWorkerSelector
			env.RuntimeConfig = existing.RuntimeConfig
			return repo.Update(ctx, env)
		}
		return repo.Create(ctx, env)
	}
}

func buildApplier(repo repository.BuildRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			Deleted       bool               `json:"deleted"`
			ID            string             `json:"id"`
			ServiceID     string             `json:"service_id"`
			GitSHA        string             `json:"git_sha"`
			GitRef        string             `json:"git_ref"`
			CISystem      string             `json:"ci_system"`
			CIRunID       string             `json:"ci_run_id"`
			LoomJobID     string             `json:"loom_job_id"`
			Status        domain.BuildStatus `json:"status"`
			SourceEventID string             `json:"source_event_id"`
			StartedAt     *time.Time         `json:"started_at"`
			FinishedAt    *time.Time         `json:"finished_at"`
			Metadata      map[string]any     `json:"metadata"`
			CreatedAt     time.Time          `json:"created_at"`
		}
		if ok, err := decodeProjectionPayload(value, "Build", &payload); err != nil || !ok || boolField(value, "Tombstone") || payload.Deleted {
			return err
		}
		id, err := parseProjectionUUID(payload.ID, "build id")
		if err != nil {
			return err
		}
		serviceID, err := parseProjectionUUID(payload.ServiceID, "build service id")
		if err != nil {
			return err
		}
		if existing, err := repo.GetByID(ctx, id); err == nil && existing != nil {
			return repo.UpdateStatus(ctx, id, payload.Status)
		}
		return repo.Create(ctx, &domain.Build{ID: id, ServiceID: serviceID, GitSHA: payload.GitSHA, GitRef: payload.GitRef, CISystem: payload.CISystem, CIRunID: payload.CIRunID, LoomJobID: payload.LoomJobID, Status: payload.Status, SourceEventID: payload.SourceEventID, StartedAt: payload.StartedAt, FinishedAt: payload.FinishedAt, Metadata: payload.Metadata, CreatedAt: payload.CreatedAt})
	}
}

func artifactApplier(repo repository.ArtifactRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			Deleted           bool              `json:"deleted"`
			ID                string            `json:"id"`
			BuildID           string            `json:"build_id"`
			ServiceID         string            `json:"service_id"`
			ImageRepo         string            `json:"image_repo"`
			ImageTag          string            `json:"image_tag"`
			ImageDigest       string            `json:"image_digest"`
			ManifestMediaType string            `json:"manifest_media_type"`
			SizeBytes         *int64            `json:"size_bytes"`
			SBOMURL           string            `json:"sbom_url"`
			SignatureRef      string            `json:"signature_ref"`
			ScanStatus        domain.ScanStatus `json:"scan_status"`
			Metadata          map[string]any    `json:"metadata"`
			CreatedAt         time.Time         `json:"created_at"`
		}
		if ok, err := decodeProjectionPayload(value, "Artifact", &payload); err != nil || !ok || boolField(value, "Tombstone") || payload.Deleted {
			return err
		}
		id, err := parseProjectionUUID(payload.ID, "artifact id")
		if err != nil {
			return err
		}
		buildID, err := parseProjectionUUID(payload.BuildID, "artifact build id")
		if err != nil {
			return err
		}
		serviceID, err := parseProjectionUUID(payload.ServiceID, "artifact service id")
		if err != nil {
			return err
		}
		if existing, err := repo.GetByID(ctx, id); err == nil && existing != nil {
			return nil
		}
		return repo.Create(ctx, &domain.Artifact{ID: id, BuildID: buildID, ServiceID: serviceID, ImageRepo: payload.ImageRepo, ImageTag: payload.ImageTag, ImageDigest: payload.ImageDigest, ManifestMediaType: payload.ManifestMediaType, SizeBytes: payload.SizeBytes, SBOMURL: payload.SBOMURL, SignatureRef: payload.SignatureRef, ScanStatus: payload.ScanStatus, Metadata: payload.Metadata, CreatedAt: payload.CreatedAt})
	}
}

func policyApplier(repo repository.DeploymentPolicyRepository) FamilyApplier {
	return func(ctx context.Context, event any) error {
		value, err := decodedEventValue(event)
		if err != nil {
			return err
		}
		var payload struct {
			Deleted       bool                     `json:"deleted"`
			ID            string                   `json:"id"`
			Name          string                   `json:"name"`
			EnvironmentID *string                  `json:"environment_id"`
			Rules         []domain.PolicyRule      `json:"rules"`
			Enforcement   domain.PolicyEnforcement `json:"enforcement"`
			Enabled       bool                     `json:"enabled"`
			CreatedAt     time.Time                `json:"created_at"`
			UpdatedAt     time.Time                `json:"updated_at"`
		}
		if ok, err := decodeProjectionPayload(value, "Policy", &payload); err != nil || !ok {
			return err
		}
		id, err := parseProjectionUUID(payload.ID, "policy id")
		if err != nil {
			return err
		}
		if boolField(value, "Tombstone") || payload.Deleted {
			return repo.Delete(ctx, id)
		}
		var envID *uuid.UUID
		if payload.EnvironmentID != nil && *payload.EnvironmentID != "" {
			parsed, err := parseProjectionUUID(*payload.EnvironmentID, "policy environment id")
			if err != nil {
				return err
			}
			envID = &parsed
		}
		policy := &domain.DeploymentPolicy{ID: id, Name: payload.Name, EnvironmentID: envID, Rules: payload.Rules, Enforcement: payload.Enforcement, Enabled: payload.Enabled, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt}
		if existing, err := repo.GetByID(ctx, id); err == nil && existing != nil {
			return repo.Update(ctx, policy)
		}
		return repo.Create(ctx, policy)
	}
}

func decodedEventValue(event any) (reflect.Value, error) {
	if event == nil {
		return reflect.Value{}, errors.New("decoded projection event is required")
	}
	value := reflect.ValueOf(event)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}, errors.New("decoded projection event is required")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct || !value.FieldByName("Family").IsValid() {
		return reflect.Value{}, errors.New("decoded projection event type is required")
	}
	return value, nil
}

func decodeProjectionPayload(event reflect.Value, field string, out any) (bool, error) {
	payload, ok := projectionPayload(event, field)
	if !ok {
		return false, nil
	}
	encoded, err := json.Marshal(payload.Interface())
	if err != nil {
		return false, fmt.Errorf("marshal decoded %s payload: %w", field, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return false, fmt.Errorf("decode %s payload: %w", field, err)
	}
	return true, nil
}

func projectionPayload(event reflect.Value, field string) (reflect.Value, bool) {
	payload := event.FieldByName(field)
	if !payload.IsValid() || payload.IsNil() {
		return reflect.Value{}, false
	}
	return payload, true
}

func indirect(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func boolField(value reflect.Value, field string) bool {
	fieldValue := value.FieldByName(field)
	return fieldValue.IsValid() && fieldValue.Kind() == reflect.Bool && fieldValue.Bool()
}

func parseProjectionUUID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return id, nil
}

type relayProjectionFields struct {
	stream        string
	entityKey     string
	updatedAt     time.Time
	sourceEventID string
	tombstoned    bool
}

func projectionFields(event any) (relayProjectionFields, error) {
	if event == nil {
		return relayProjectionFields{}, errors.New("decoded projection event is required")
	}
	value := reflect.ValueOf(event)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return relayProjectionFields{}, errors.New("decoded projection event is required")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return relayProjectionFields{}, errors.New("decoded projection event must be a struct or struct pointer")
	}

	familyField := value.FieldByName("Family")
	dTagField := value.FieldByName("DTag")
	timestampField := value.FieldByName("Timestamp")
	sourceIDField := value.FieldByName("SourceID")
	tombstoneField := value.FieldByName("Tombstone")
	if !familyField.IsValid() || !dTagField.IsValid() || !timestampField.IsValid() || !sourceIDField.IsValid() || !tombstoneField.IsValid() {
		return relayProjectionFields{}, errors.New("decoded projection event is missing required projection fields")
	}

	stream := fmt.Sprint(familyField.Interface())
	entityKey, _ := dTagField.Interface().(string)
	updatedAt, _ := timestampField.Interface().(time.Time)
	sourceID, _ := sourceIDField.Interface().(string)
	tombstone, _ := tombstoneField.Interface().(bool)
	if stream == "" {
		return relayProjectionFields{}, errors.New("decoded projection event family is required")
	}
	if entityKey == "" {
		return relayProjectionFields{}, errors.New("decoded projection event d tag is required")
	}
	if updatedAt.IsZero() {
		return relayProjectionFields{}, errors.New("decoded projection event timestamp is required")
	}
	return relayProjectionFields{stream: stream, entityKey: entityKey, updatedAt: updatedAt, sourceEventID: sourceID, tombstoned: tombstone}, nil
}
