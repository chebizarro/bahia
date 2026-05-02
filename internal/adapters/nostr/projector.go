package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
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
	GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error)
	GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error)
}

// ProjectionPublisher publishes signed Nostr events to relay-visible storage.
type ProjectionPublisher interface {
	Publish(ctx context.Context, ev gonostr.Event) (int, error)
}

// Projector republishes Bahia's authoritative DB state into canonical Nostr
// read models and append-only audit events. It is rebuildable: a startup and
// periodic snapshot can repair a cold or wiped sidecar store.
type Projector struct {
	source         ProjectionSource
	publisher      ProjectionPublisher
	eventRepo      repository.NostrEventRepository
	privateKey     string
	enabled        bool
	repairInterval time.Duration
	logger         *zap.Logger
}

// ProjectorOption configures a projector.
type ProjectorOption func(*Projector)

// WithProjectorRepairInterval overrides the periodic snapshot repair interval.
// Use <=0 in tests to disable periodic repair after the startup snapshot.
func WithProjectorRepairInterval(interval time.Duration) ProjectorOption {
	return func(p *Projector) { p.repairInterval = interval }
}

// NewProjector creates a canonical Nostr read-model projector.
func NewProjector(cfg config.NostrConfig, source ProjectionSource, publisher ProjectionPublisher, eventRepo repository.NostrEventRepository, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Projector{
		source:         source,
		publisher:      publisher,
		eventRepo:      eventRepo,
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

// RepublishSnapshot republishes all replaceable read models from authoritative
// state. It is safe to run repeatedly; latest replaceable events win by d-tag.
func (p *Projector) RepublishSnapshot(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	services, err := p.source.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for i := range services {
		if err := p.publishServiceRegistry(ctx, &services[i], false); err != nil {
			p.logger.Warn("publish service registry projection failed", zap.String("service_id", services[i].ID.String()), zap.Error(err))
		}
	}

	envs, err := p.source.ListEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	for i := range envs {
		if err := p.publishEnvironmentRegistry(ctx, &envs[i], false); err != nil {
			p.logger.Warn("publish environment registry projection failed", zap.String("environment_id", envs[i].ID.String()), zap.Error(err))
		}
	}

	states, err := p.source.ListAllStates(ctx)
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
	p.logger.Info("Nostr projection snapshot republished", zap.Int("services", len(services)), zap.Int("environments", len(envs)), zap.Int("states", len(states)))
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
	case events.EventDeploymentIntentCreated, events.EventDeploymentIntentApproved, events.EventDeploymentIntentRejected:
		if id, ok := parseUUID(firstString(res.IntentID, e.EntityID)); ok {
			p.publishStateForIntent(ctx, id)
		}
	case events.EventDeploymentRunCreated, events.EventDeploymentRunStatusChanged, events.EventDeploymentRunCompleted:
		if id, ok := parseUUID(firstString(res.RunID, e.EntityID)); ok {
			p.publishStateForRun(ctx, id)
		} else if id, ok := parseUUID(res.IntentID); ok {
			p.publishStateForIntent(ctx, id)
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
		{"d", svc.ID.String()},
		{"deleted", fmt.Sprintf("%t", deleted)},
	}
	if !deleted {
		tags = append(tags,
			gonostr.Tag{"name", svc.Name},
			gonostr.Tag{"runtime", string(svc.RuntimeType)},
		)
	}
	return p.publishSigned(ctx, KindServiceRegistry, tags, string(contentJSON), "service.projection", &svc.ID)
}

func (p *Projector) publishEnvironmentRegistry(ctx context.Context, env *domain.Environment, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      env.ID.String(),
	}
	if !deleted {
		content["name"] = env.Name
		content["protected"] = env.Protected
		content["deploy_strategy"] = string(env.DeployStrategy)
		content["created_at"] = formatTime(env.CreatedAt)
		content["updated_at"] = formatTime(env.UpdatedAt)
	} else {
		content["updated_at"] = formatTime(env.UpdatedAt)
	}
	contentJSON, _ := json.Marshal(content)
	tags := gonostr.Tags{
		{"d", env.ID.String()},
		{"deleted", fmt.Sprintf("%t", deleted)},
	}
	if !deleted {
		tags = append(tags,
			gonostr.Tag{"name", env.Name},
			gonostr.Tag{"protected", fmt.Sprintf("%t", env.Protected)},
		)
	}
	return p.publishSigned(ctx, KindEnvironmentRegistry, tags, string(contentJSON), "environment.projection", &env.ID)
}

func (p *Projector) publishState(ctx context.Context, state *domain.EnvironmentServiceState) error {
	content := map[string]any{
		"deleted":        false,
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"updated_at":     formatTime(state.UpdatedAt),
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

	contentJSON, _ := json.Marshal(content)
	dTag := fmt.Sprintf("%s:%s", state.ServiceID, state.EnvironmentID)
	tags := gonostr.Tags{
		{"d", dTag},
		{"service", state.ServiceID.String()},
		{"environment", state.EnvironmentID.String()},
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
	return p.publishSigned(ctx, KindServiceState, tags, string(contentJSON), "state.projection", &state.ServiceID)
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
		{"deleted", "true"},
	}
	return p.publishSigned(ctx, KindServiceState, tags, string(content), "state.projection", &serviceID)
}

func (p *Projector) publishAudit(ctx context.Context, e events.Event) error {
	kind := auditKindForEvent(e.Type)
	if kind == 0 {
		return nil
	}
	res := resourceFromEvent(e)
	content, _ := json.Marshal(map[string]any{
		"event_type": string(e.Type),
		"entity_id":  e.EntityID,
		"data":       e.Data,
	})
	tags := gonostr.Tags{
		{"t", string(e.Type)},
		{"event_type", string(e.Type)},
	}
	if e.EntityID != "" {
		tags = append(tags, gonostr.Tag{"d", e.EntityID})
	}
	tags = appendResourceTags(tags, res)
	entityID := firstParsedUUID(e.EntityID, res.ServiceID, res.EnvironmentID, res.IntentID, res.RunID, res.ArtifactID)
	return p.publishSigned(ctx, kind, tags, string(content), string(e.Type), entityID)
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
	if p.eventRepo != nil {
		tagsJSON, _ := json.Marshal(ev.Tags)
		if err := p.eventRepo.Record(ctx, &repository.NostrEventRecord{
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
		IntentID:      firstString(m["intent_id"], m["deployment_intent_id"]),
		RunID:         firstString(m["run_id"], m["deployment_run_id"]),
	}
}

func resourceFromAnyMap(m map[string]any) events.ResourceData {
	return events.ResourceData{
		ServiceID:     stringify(m["service_id"]),
		EnvironmentID: stringify(m["environment_id"]),
		ArtifactID:    stringify(m["artifact_id"]),
		IntentID:      firstString(stringify(m["intent_id"]), stringify(m["deployment_intent_id"])),
		RunID:         firstString(stringify(m["run_id"]), stringify(m["deployment_run_id"])),
	}
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

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
