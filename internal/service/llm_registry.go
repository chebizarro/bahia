package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLMRegistryService owns canonical DB-first lifecycle state for LLM routes.
type LLMRegistryService struct {
	routes       repository.LLMRouteRepository
	releases     repository.LLMReleaseRepository
	environments repository.EnvironmentRepository
	intents      repository.LLMDeploymentIntentRepository
	runs         repository.LLMDeploymentRunRepository
	observations repository.LLMRouteObservationRepository
	state        repository.LLMRouteStateRepository
	ml           *MLRegistryService
	publisher    events.Publisher
	logger       *zap.Logger
}

func NewLLMRegistryService(
	routes repository.LLMRouteRepository,
	releases repository.LLMReleaseRepository,
	environments repository.EnvironmentRepository,
	intents repository.LLMDeploymentIntentRepository,
	runs repository.LLMDeploymentRunRepository,
	observations repository.LLMRouteObservationRepository,
	state repository.LLMRouteStateRepository,
	publisher events.Publisher,
	logger *zap.Logger,
) *LLMRegistryService {
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LLMRegistryService{routes: routes, releases: releases, environments: environments, intents: intents, runs: runs, observations: observations, state: state, publisher: publisher, logger: logger}
}

// NewMLBackedLLMRegistryService creates an LLM compatibility facade over the generic ML registry.
// The legacy constructor remains unchanged so existing LLM behavior can continue during cutover.
func NewMLBackedLLMRegistryService(ml *MLRegistryService, environments repository.EnvironmentRepository, publisher events.Publisher, logger *zap.Logger) *LLMRegistryService {
	svc := NewLLMRegistryService(nil, nil, environments, nil, nil, nil, nil, publisher, logger)
	svc.ml = ml
	return svc
}

// WithMLRegistry enables the optional ML-backed compatibility facade path.
func (s *LLMRegistryService) WithMLRegistry(ml *MLRegistryService) *LLMRegistryService {
	s.ml = ml
	return s
}

func (s *LLMRegistryService) mlBacked() bool {
	return s != nil && s.ml != nil
}

func (s *LLMRegistryService) CreateRoute(ctx context.Context, route *domain.LLMRoute) error {
	if s.mlBacked() {
		if route == nil {
			return fmt.Errorf("LLM route is required")
		}
		route.Name = strings.TrimSpace(route.Name)
		if err := domain.ValidateLLMRouteName(route.Name); err != nil {
			return err
		}
		defaultRouteGatewayConfig(route)
		model := LLMRouteToMLModel(route)
		if err := s.ml.CreateOrUpdateModel(ctx, model); err != nil {
			return err
		}
		route.ID = model.ID
		return nil
	}
	if route == nil {
		return fmt.Errorf("LLM route is required")
	}
	route.Name = strings.TrimSpace(route.Name)
	if err := domain.ValidateLLMRouteName(route.Name); err != nil {
		return err
	}
	if err := domain.ValidateLLMPromotionGateConfig(route.DefaultPromotionGate); err != nil {
		return err
	}
	defaultRouteGatewayConfig(route)
	if err := s.routes.Create(ctx, route); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMRouteCreated, route.ID.String(), events.ResourceData{RouteID: route.ID.String()})
	return nil
}

func (s *LLMRegistryService) GetRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	if s.mlBacked() {
		model, err := s.ml.GetModel(ctx, id)
		if err != nil {
			return nil, err
		}
		return MLModelToLLMRoute(model), nil
	}
	return s.routes.GetByID(ctx, id)
}

func (s *LLMRegistryService) GetRouteByName(ctx context.Context, name string) (*domain.LLMRoute, error) {
	if s.mlBacked() {
		model, err := s.ml.GetModelBySlug(ctx, name)
		if err != nil {
			return nil, err
		}
		return MLModelToLLMRoute(model), nil
	}
	return s.routes.GetByName(ctx, name)
}

func (s *LLMRegistryService) ListRoutes(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	if limit <= 0 {
		limit = 100
	}
	if s.mlBacked() {
		models, err := s.ml.ListModels(ctx, domain.MLTaskKindChatCompletions, limit, offset)
		if err != nil {
			return nil, err
		}
		routes := make([]domain.LLMRoute, 0, len(models))
		for i := range models {
			routes = append(routes, *MLModelToLLMRoute(&models[i]))
		}
		return routes, nil
	}
	return s.routes.List(ctx, limit, offset)
}

func (s *LLMRegistryService) UpdateRoute(ctx context.Context, route *domain.LLMRoute) error {
	if s.mlBacked() {
		if route == nil {
			return fmt.Errorf("LLM route is required")
		}
		defaultRouteGatewayConfig(route)
		return s.ml.CreateOrUpdateModel(ctx, LLMRouteToMLModel(route))
	}
	if route == nil {
		return fmt.Errorf("LLM route is required")
	}
	existing, err := s.routes.GetByID(ctx, route.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("LLM route %s not found: %w", route.ID, repository.ErrNotFound)
	}
	route.Name = existing.Name // route names are immutable.
	if err := domain.ValidateLLMPromotionGateConfig(route.DefaultPromotionGate); err != nil {
		return err
	}
	defaultRouteGatewayConfig(route)
	if err := s.routes.Update(ctx, route); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMRouteUpdated, route.ID.String(), events.ResourceData{RouteID: route.ID.String()})
	return nil
}

func (s *LLMRegistryService) CreateRelease(ctx context.Context, release *domain.LLMRelease) error {
	if s.mlBacked() {
		if release == nil {
			return fmt.Errorf("LLM release is required")
		}
		if err := domain.ValidateLLMReleaseConfig(release); err != nil {
			return err
		}
		version := LLMReleaseToMLModelVersion(release)
		if err := s.ml.CreateOrUpdateModelVersion(ctx, version); err != nil {
			return err
		}
		release.ID = version.ID
		return nil
	}
	if release == nil {
		return fmt.Errorf("LLM release is required")
	}
	if err := domain.ValidateLLMReleaseConfig(release); err != nil {
		return err
	}
	route, err := s.routes.GetByID(ctx, release.RouteID)
	if err != nil {
		return err
	}
	if route == nil {
		return fmt.Errorf("LLM route %s not found", release.RouteID)
	}
	if err := s.releases.Create(ctx, release); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMReleaseRegistered, release.ID.String(), events.ResourceData{RouteID: release.RouteID.String(), ReleaseID: release.ID.String()})
	return nil
}

func (s *LLMRegistryService) GetRelease(ctx context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	if s.mlBacked() {
		version, err := s.ml.GetModelVersion(ctx, id)
		if err != nil {
			return nil, err
		}
		return MLModelVersionToLLMRelease(version), nil
	}
	return s.releases.GetByID(ctx, id)
}

func (s *LLMRegistryService) ListReleases(ctx context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error) {
	if limit <= 0 {
		limit = 100
	}
	if s.mlBacked() {
		versions, err := s.ml.ListModelVersions(ctx, routeID, limit, offset)
		if err != nil {
			return nil, err
		}
		releases := make([]domain.LLMRelease, 0, len(versions))
		for i := range versions {
			releases = append(releases, *MLModelVersionToLLMRelease(&versions[i]))
		}
		return releases, nil
	}
	return s.releases.ListByRoute(ctx, routeID, limit, offset)
}

func (s *LLMRegistryService) CreateDeploymentIntent(ctx context.Context, intent *domain.LLMDeploymentIntent) error {
	if s.mlBacked() {
		if intent == nil {
			return fmt.Errorf("LLM deployment intent is required")
		}
		_, release, env, err := s.loadRouteReleaseEnvironment(ctx, intent.RouteID, intent.ReleaseID, intent.EnvironmentID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(intent.RequestedBy) == "" {
			return fmt.Errorf("requested_by must not be empty")
		}
		if intent.SourceKind == "" {
			intent.SourceKind = domain.SourceKindManual
		}
		if !env.Protected {
			intent.ApprovalStatus = domain.ApprovalStatusNotRequired
			intent.Status = domain.IntentStatusApproved
		} else if intent.ApprovalStatus == domain.ApprovalStatusApproved {
			intent.Status = domain.IntentStatusApproved
		} else {
			intent.ApprovalStatus = domain.ApprovalStatusPending
			intent.Status = domain.IntentStatusPending
		}
		endpoint := &domain.MLInferenceEndpoint{ID: intent.RouteID, Name: intent.RouteID.String(), EnvironmentID: intent.EnvironmentID, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Protocol: "openai-compatible", Metadata: map[string]any{"llm_compat": true}}
		if err := s.ml.CreateOrUpdateInferenceEndpoint(ctx, endpoint); err != nil {
			return err
		}
		mlIntent := LLMIntentToMLDeploymentIntent(intent)
		if len(release.BackendPreferences) > 0 {
			mlIntent.RuntimePreference = domain.MLRuntimeKind(release.BackendPreferences[0])
		}
		if err := s.ml.CreateDeploymentIntent(ctx, mlIntent); err != nil {
			return err
		}
		intent.ID = mlIntent.ID
		intent.CreatedAt = mlIntent.CreatedAt
		intent.UpdatedAt = mlIntent.UpdatedAt
		return nil
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent is required")
	}
	route, release, env, err := s.loadRouteReleaseEnvironment(ctx, intent.RouteID, intent.ReleaseID, intent.EnvironmentID)
	if err != nil {
		return err
	}
	if release.RouteID != route.ID {
		return fmt.Errorf("release %s does not belong to route %s", release.ID, route.ID)
	}
	if strings.TrimSpace(intent.RequestedBy) == "" {
		return fmt.Errorf("requested_by must not be empty")
	}
	if intent.SourceKind == "" {
		intent.SourceKind = domain.SourceKindManual
	}
	if err := domain.ValidateSourceKind(intent.SourceKind); err != nil {
		return err
	}
	if !env.Protected {
		intent.ApprovalStatus = domain.ApprovalStatusNotRequired
		intent.Status = domain.IntentStatusApproved
	} else if intent.ApprovalStatus == domain.ApprovalStatusApproved {
		intent.Status = domain.IntentStatusApproved
	} else {
		intent.ApprovalStatus = domain.ApprovalStatusPending
		intent.Status = domain.IntentStatusPending
	}
	if err := s.intents.Create(ctx, intent); err != nil {
		return err
	}
	state := &domain.LLMRouteState{
		RouteID:          intent.RouteID,
		EnvironmentID:    intent.EnvironmentID,
		DesiredReleaseID: &intent.ReleaseID,
		DesiredIntentID:  &intent.ID,
		DriftStatus:      domain.DriftStatusDeploying,
		GatewayStatus:    domain.GatewayRouteStatusPending,
		BackendHealth:    domain.HealthStatusUnknown,
	}
	if err := s.state.Upsert(ctx, state); err != nil {
		return fmt.Errorf("upserting LLM route state: %w", err)
	}
	s.publish(ctx, events.EventLLMDeploymentIntentCreated, intent.ID.String(), events.ResourceData{RouteID: intent.RouteID.String(), ReleaseID: intent.ReleaseID.String(), EnvironmentID: intent.EnvironmentID.String(), IntentID: intent.ID.String()})
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *LLMRegistryService) ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	if s.mlBacked() {
		mlIntent, err := s.ml.GetDeploymentIntent(ctx, id)
		if err != nil {
			return err
		}
		if mlIntent == nil {
			return fmt.Errorf("LLM deployment intent %s not found: %w", id, repository.ErrNotFound)
		}
		intent := MLIntentToLLMDeploymentIntent(mlIntent)
		if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
			return fmt.Errorf("cannot approve LLM intent %s: approval=%s status=%s", id, intent.ApprovalStatus, intent.Status)
		}
		now := time.Now().UTC()
		mlIntent.ApprovalStatus = domain.ApprovalStatusApproved
		mlIntent.Status = domain.IntentStatusApproved
		mlIntent.ApprovedAt = &now
		if err := s.ml.repo.UpsertDeploymentIntent(ctx, mlIntent); err != nil {
			return err
		}
		s.publish(ctx, events.EventLLMDeploymentIntentApproved, id.String(), events.ResourceData{RouteID: intent.RouteID.String(), ReleaseID: intent.ReleaseID.String(), EnvironmentID: intent.EnvironmentID.String(), IntentID: id.String()})
		return nil
	}
	intent, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent %s not found: %w", id, repository.ErrNotFound)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot approve LLM intent %s: approval=%s status=%s", id, intent.ApprovalStatus, intent.Status)
	}
	if err := s.intents.UpdateApproval(ctx, id, domain.ApprovalStatusApproved); err != nil {
		return err
	}
	if err := s.intents.UpdateStatus(ctx, id, domain.IntentStatusApproved); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMDeploymentIntentApproved, id.String(), events.ResourceData{RouteID: intent.RouteID.String(), ReleaseID: intent.ReleaseID.String(), EnvironmentID: intent.EnvironmentID.String(), IntentID: id.String()})
	return nil
}

func (s *LLMRegistryService) RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	if s.mlBacked() {
		mlIntent, err := s.ml.GetDeploymentIntent(ctx, id)
		if err != nil {
			return err
		}
		if mlIntent == nil {
			return fmt.Errorf("LLM deployment intent %s not found: %w", id, repository.ErrNotFound)
		}
		intent := MLIntentToLLMDeploymentIntent(mlIntent)
		if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
			return fmt.Errorf("cannot reject LLM intent %s: approval=%s status=%s", id, intent.ApprovalStatus, intent.Status)
		}
		mlIntent.ApprovalStatus = domain.ApprovalStatusRejected
		mlIntent.Status = domain.IntentStatusRejected
		if err := s.ml.repo.UpsertDeploymentIntent(ctx, mlIntent); err != nil {
			return err
		}
		if err := s.repairStateAfterRejectedIntent(ctx, intent); err != nil {
			return err
		}
		s.publish(ctx, events.EventLLMDeploymentIntentRejected, id.String(), events.ResourceData{RouteID: intent.RouteID.String(), ReleaseID: intent.ReleaseID.String(), EnvironmentID: intent.EnvironmentID.String(), IntentID: id.String()})
		return nil
	}
	intent, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent %s not found: %w", id, repository.ErrNotFound)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot reject LLM intent %s: approval=%s status=%s", id, intent.ApprovalStatus, intent.Status)
	}
	if err := s.intents.UpdateApproval(ctx, id, domain.ApprovalStatusRejected); err != nil {
		return err
	}
	if err := s.intents.UpdateStatus(ctx, id, domain.IntentStatusRejected); err != nil {
		return err
	}
	if err := s.repairStateAfterRejectedIntent(ctx, intent); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMDeploymentIntentRejected, id.String(), events.ResourceData{RouteID: intent.RouteID.String(), ReleaseID: intent.ReleaseID.String(), EnvironmentID: intent.EnvironmentID.String(), IntentID: id.String()})
	return nil
}

func (s *LLMRegistryService) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	if s.mlBacked() {
		intent, err := s.ml.GetDeploymentIntent(ctx, id)
		if err != nil || intent == nil {
			return nil, err
		}
		return MLIntentToLLMDeploymentIntent(intent), nil
	}
	return s.intents.GetByID(ctx, id)
}

func (s *LLMRegistryService) ListDeploymentIntents(ctx context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error) {
	if limit <= 0 {
		limit = 50
	}
	if s.mlBacked() {
		intents, err := s.ml.ListDeploymentIntents(ctx, routeID, envID, limit, offset)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMDeploymentIntent, 0, len(intents))
		for i := range intents {
			out = append(out, *MLIntentToLLMDeploymentIntent(&intents[i]))
		}
		return out, nil
	}
	return s.intents.ListByRouteEnv(ctx, routeID, envID, limit, offset)
}

func (s *LLMRegistryService) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	if s.mlBacked() {
		run, err := s.ml.repo.GetDeploymentRun(ctx, id)
		if err != nil || run == nil {
			return nil, err
		}
		return MLRunToLLMDeploymentRun(run), nil
	}
	return s.runs.GetByID(ctx, id)
}

func (s *LLMRegistryService) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.LLMDeploymentRun, error) {
	if s.mlBacked() {
		runs, err := s.ml.repo.ListDeploymentRuns(ctx, intentID)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMDeploymentRun, 0, len(runs))
		for i := range runs {
			out = append(out, *MLRunToLLMDeploymentRun(&runs[i]))
		}
		return out, nil
	}
	return s.runs.ListByIntent(ctx, intentID)
}

func (s *LLMRegistryService) MarkDeploymentRunCreated(ctx context.Context, run *domain.LLMDeploymentRun) error {
	if s.mlBacked() {
		if run == nil {
			return nil
		}
		mlRun := LLMRunToMLDeploymentRun(run)
		if err := s.ml.CreateOrUpdateDeploymentRun(ctx, mlRun); err != nil {
			return err
		}
		run.ID = mlRun.ID
		run.CreatedAt = mlRun.CreatedAt
		run.UpdatedAt = mlRun.UpdatedAt
		return nil
	}
	if run == nil {
		return nil
	}
	if err := s.intents.UpdateStatus(ctx, run.DeploymentIntentID, domain.IntentStatusDeploying); err != nil {
		return err
	}
	data := map[string]any{"intent_id": run.DeploymentIntentID.String(), "run_id": run.ID.String(), "status": string(run.Status)}
	if intent, err := s.intents.GetByID(ctx, run.DeploymentIntentID); err == nil && intent != nil {
		data["route_id"] = intent.RouteID.String()
		data["release_id"] = intent.ReleaseID.String()
		data["environment_id"] = intent.EnvironmentID.String()
	}
	s.publish(ctx, events.EventLLMDeploymentRunCreated, run.ID.String(), data)
	s.publish(ctx, events.EventLLMDeploymentRunStatusChanged, run.ID.String(), data)
	return nil
}

func (s *LLMRegistryService) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if s.mlBacked() {
		return s.ml.CompleteDeploymentRun(ctx, id, status, exitCode)
	}
	if !isTerminalRunStatus(status) {
		return fmt.Errorf("cannot complete LLM run with non-terminal status: %s", status)
	}
	run, err := s.runs.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("LLM deployment run %s not found: %w", id, repository.ErrNotFound)
	}
	if run.Status != domain.RunStatusQueued && run.Status != domain.RunStatusRunning {
		return fmt.Errorf("cannot complete LLM run %s: status is already %s", id, run.Status)
	}
	if err := s.runs.UpdateStatus(ctx, id, status, exitCode); err != nil {
		return err
	}
	intent, err := s.intents.GetByID(ctx, run.DeploymentIntentID)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent %s not found", run.DeploymentIntentID)
	}
	intentStatus := domain.IntentStatusFailed
	if status == domain.RunStatusSucceeded {
		intentStatus = domain.IntentStatusDeployed
	}
	if status == domain.RunStatusCancelled {
		intentStatus = domain.IntentStatusFailed
	}
	if err := s.intents.UpdateStatus(ctx, intent.ID, intentStatus); err != nil {
		return err
	}
	state, err := s.state.Get(ctx, intent.RouteID, intent.EnvironmentID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &domain.LLMRouteState{RouteID: intent.RouteID, EnvironmentID: intent.EnvironmentID}
	}
	if status == domain.RunStatusSucceeded {
		state.ActiveRunID = &id
		state.DesiredReleaseID = &intent.ReleaseID
		state.DesiredIntentID = &intent.ID
	} else if state.DesiredIntentID != nil && *state.DesiredIntentID == intent.ID {
		state.DriftStatus = domain.DriftStatusDrifted
		state.GatewayStatus = domain.GatewayRouteStatusError
	}
	if err := s.state.Upsert(ctx, state); err != nil {
		return err
	}
	runData := map[string]any{"status": string(status), "route_id": intent.RouteID.String(), "release_id": intent.ReleaseID.String(), "environment_id": intent.EnvironmentID.String(), "intent_id": intent.ID.String(), "run_id": id.String()}
	s.publish(ctx, events.EventLLMDeploymentRunCompleted, id.String(), runData)
	s.publish(ctx, events.EventLLMDeploymentRunStatusChanged, id.String(), runData)
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *LLMRegistryService) Rollback(ctx context.Context, routeID, envID uuid.UUID, requestedBy string) (*domain.LLMDeploymentIntent, error) {
	return s.RollbackWithMetadata(ctx, routeID, envID, requestedBy, nil)
}

func (s *LLMRegistryService) RollbackWithMetadata(ctx context.Context, routeID, envID uuid.UUID, requestedBy string, metadata map[string]any) (*domain.LLMDeploymentIntent, error) {
	state, err := s.GetRouteState(ctx, routeID, envID)
	if err != nil {
		return nil, err
	}
	if state == nil || state.DesiredReleaseID == nil {
		return nil, fmt.Errorf("no LLM route state exists for this route/environment")
	}
	intents, err := s.ListDeploymentIntents(ctx, routeID, envID, 50, 0)
	if err != nil {
		return nil, err
	}
	selection, err := selectRollbackTarget(*state.DesiredReleaseID, intents)
	if err != nil {
		return nil, err
	}
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: selection.targetReleaseID, RequestedBy: requestedBy, SourceKind: domain.SourceKindRollback, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved, SupersedesIntentID: selection.supersedesIntentID, Metadata: metadata}
	if err := s.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, err
	}
	return intent, nil
}

var errNoPreviousDeployedDifferentRelease = fmt.Errorf("no previous successfully deployed LLM release to roll back to")

type rollbackTargetSelection struct {
	targetReleaseID    uuid.UUID
	supersedesIntentID *uuid.UUID
}

func selectRollbackTarget(currentDesiredReleaseID uuid.UUID, intents []domain.LLMDeploymentIntent) (*rollbackTargetSelection, error) {
	deployed := deployedIntentsNewestFirst(intents)
	if len(deployed) == 0 {
		return nil, errNoPreviousDeployedDifferentRelease
	}
	supersedesIntentID := deployed[0].ID
	targetReleaseID, ok := selectPreviousDeployedDifferentRelease(currentDesiredReleaseID, deployed)
	if !ok {
		return nil, errNoPreviousDeployedDifferentRelease
	}
	return &rollbackTargetSelection{targetReleaseID: targetReleaseID, supersedesIntentID: &supersedesIntentID}, nil
}

func deployedIntentsNewestFirst(intents []domain.LLMDeploymentIntent) []domain.LLMDeploymentIntent {
	deployed := make([]domain.LLMDeploymentIntent, 0, len(intents))
	for i := range intents {
		if intents[i].Status == domain.IntentStatusDeployed {
			deployed = append(deployed, intents[i])
		}
	}
	return deployed
}

func selectPreviousDeployedDifferentRelease(currentDesiredReleaseID uuid.UUID, deployedIntentsNewestFirst []domain.LLMDeploymentIntent) (uuid.UUID, bool) {
	for i := range deployedIntentsNewestFirst {
		if deployedIntentsNewestFirst[i].ReleaseID == currentDesiredReleaseID {
			continue
		}
		return deployedIntentsNewestFirst[i].ReleaseID, true
	}
	return uuid.Nil, false
}

func (s *LLMRegistryService) RecordObservation(ctx context.Context, obs *domain.LLMRouteObservation) error {
	if s.mlBacked() {
		if obs == nil {
			return fmt.Errorf("LLM route observation is required")
		}
		return s.ml.RecordObservation(ctx, LLMObservationToMLInferenceObservation(obs))
	}
	if obs == nil {
		return fmt.Errorf("LLM route observation is required")
	}
	previousState, err := s.state.Get(ctx, obs.RouteID, obs.EnvironmentID)
	if err != nil {
		return err
	}
	previousDrift := domain.DriftStatusUnknown
	if previousState != nil {
		previousDrift = previousState.DriftStatus
	}
	previousLatest, err := s.observations.GetLatest(ctx, obs.RouteID, obs.EnvironmentID)
	if err != nil {
		return err
	}
	if obs.BackendHealth == "" {
		obs.BackendHealth = domain.HealthStatusUnknown
	}
	if obs.GatewayStatus == "" {
		obs.GatewayStatus = domain.GatewayRouteStatusUnknown
	}
	if obs.Source == "" {
		obs.Source = "api"
	}
	if err := s.observations.Create(ctx, obs); err != nil {
		return err
	}
	s.publish(ctx, events.EventLLMRouteObservation, obs.ID.String(), obs)
	if previousLatest != nil && obs.ObservedAt.Before(previousLatest.ObservedAt) {
		return nil
	}
	state := previousState
	if state == nil {
		state = &domain.LLMRouteState{RouteID: obs.RouteID, EnvironmentID: obs.EnvironmentID}
	}
	state.CurrentObservationID = &obs.ID
	state.BackendKind = obs.BackendKind
	state.BackendEndpoint = obs.BackendEndpoint
	state.BackendHealth = obs.BackendHealth
	state.GatewayStatus = obs.GatewayStatus
	state.GatewayTarget = obs.GatewayTarget
	now := time.Now().UTC()
	state.LastReconciledAt = &now
	state.DriftStatus = s.computeDriftStatus(ctx, state, obs)
	if err := s.state.Upsert(ctx, state); err != nil {
		return err
	}
	if previousDrift != domain.DriftStatusDrifted && state.DriftStatus == domain.DriftStatusDrifted {
		s.publish(ctx, events.EventLLMRouteDriftDetected, obs.RouteID.String()+":"+obs.EnvironmentID.String(), events.ResourceData{RouteID: obs.RouteID.String(), EnvironmentID: obs.EnvironmentID.String()})
	}
	if obs.GatewayStatus == domain.GatewayRouteStatusSynced {
		s.publish(ctx, events.EventLLMGatewayRouteSynced, obs.RouteID.String()+":"+obs.EnvironmentID.String(), events.ResourceData{RouteID: obs.RouteID.String(), EnvironmentID: obs.EnvironmentID.String()})
	}
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *LLMRegistryService) GetLatestObservation(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteObservation, error) {
	if s.mlBacked() {
		obs, err := s.ml.repo.GetLatestInferenceObservation(ctx, routeID, envID)
		if err != nil || obs == nil {
			return nil, err
		}
		return MLObservationToLLMRouteObservation(obs), nil
	}
	return s.observations.GetLatest(ctx, routeID, envID)
}

func (s *LLMRegistryService) GetRouteState(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error) {
	if s.mlBacked() {
		state, err := s.ml.GetInferenceState(ctx, routeID, envID)
		if err != nil || state == nil {
			return nil, err
		}
		return MLStateToLLMRouteState(state), nil
	}
	return s.state.Get(ctx, routeID, envID)
}

func (s *LLMRegistryService) ListEnvironmentRouteStates(ctx context.Context, envID uuid.UUID) ([]domain.LLMRouteState, error) {
	if s.mlBacked() {
		states, err := s.ListAllRouteStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMRouteState, 0, len(states))
		for i := range states {
			if states[i].EnvironmentID == envID {
				out = append(out, states[i])
			}
		}
		return out, nil
	}
	return s.state.ListByEnvironment(ctx, envID)
}

func (s *LLMRegistryService) ListRouteStates(ctx context.Context, routeID uuid.UUID) ([]domain.LLMRouteState, error) {
	if s.mlBacked() {
		states, err := s.ListAllRouteStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMRouteState, 0, len(states))
		for i := range states {
			if states[i].RouteID == routeID {
				out = append(out, states[i])
			}
		}
		return out, nil
	}
	return s.state.ListByRoute(ctx, routeID)
}

func (s *LLMRegistryService) ListAllRouteStates(ctx context.Context) ([]domain.LLMRouteState, error) {
	if s.mlBacked() {
		states, err := s.ml.ListInferenceStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMRouteState, 0, len(states))
		for i := range states {
			out = append(out, *MLStateToLLMRouteState(&states[i]))
		}
		return out, nil
	}
	return s.state.ListAll(ctx)
}

func (s *LLMRegistryService) ListDriftedRouteStates(ctx context.Context) ([]domain.LLMRouteState, error) {
	if s.mlBacked() {
		states, err := s.ListAllRouteStates(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]domain.LLMRouteState, 0, len(states))
		for i := range states {
			if states[i].DriftStatus == domain.DriftStatusDrifted {
				out = append(out, states[i])
			}
		}
		return out, nil
	}
	return s.state.ListDrifted(ctx)
}

// LLM projector source aliases keep projection wiring explicit without changing the HTTP-facing service API.
func (s *LLMRegistryService) ListLLMRoutes(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	return s.ListRoutes(ctx, limit, offset)
}

func (s *LLMRegistryService) GetLLMRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	return s.GetRoute(ctx, id)
}

func (s *LLMRegistryService) ListAllLLMRouteStates(ctx context.Context) ([]domain.LLMRouteState, error) {
	return s.ListAllRouteStates(ctx)
}

func (s *LLMRegistryService) GetLLMRouteState(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error) {
	return s.GetRouteState(ctx, routeID, envID)
}

func (s *LLMRegistryService) GetLLMDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	return s.GetDeploymentIntent(ctx, id)
}

func (s *LLMRegistryService) GetLLMDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	return s.GetDeploymentRun(ctx, id)
}

func (s *LLMRegistryService) loadRouteReleaseEnvironment(ctx context.Context, routeID, releaseID, envID uuid.UUID) (*domain.LLMRoute, *domain.LLMRelease, *domain.Environment, error) {
	route, err := s.GetRoute(ctx, routeID)
	if err != nil {
		return nil, nil, nil, err
	}
	if route == nil {
		return nil, nil, nil, fmt.Errorf("LLM route %s not found", routeID)
	}
	release, err := s.GetRelease(ctx, releaseID)
	if err != nil {
		return nil, nil, nil, err
	}
	if release == nil {
		return nil, nil, nil, fmt.Errorf("LLM release %s not found", releaseID)
	}
	env, err := s.environments.GetByID(ctx, envID)
	if err != nil {
		return nil, nil, nil, err
	}
	if env == nil {
		return nil, nil, nil, fmt.Errorf("environment %s not found", envID)
	}
	return route, release, env, nil
}

func (s *LLMRegistryService) repairStateAfterRejectedIntent(ctx context.Context, rejected *domain.LLMDeploymentIntent) error {
	state, err := s.GetRouteState(ctx, rejected.RouteID, rejected.EnvironmentID)
	if err != nil || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != rejected.ID {
		return err
	}
	intents, err := s.ListDeploymentIntents(ctx, rejected.RouteID, rejected.EnvironmentID, 50, 0)
	if err != nil {
		return err
	}
	state.DesiredReleaseID = nil
	state.DesiredIntentID = nil
	state.DriftStatus = domain.DriftStatusUnknown
	state.GatewayStatus = domain.GatewayRouteStatusUnknown
	for i := range intents {
		candidate := intents[i]
		if candidate.ID == rejected.ID {
			continue
		}
		switch candidate.Status {
		case domain.IntentStatusDeployed, domain.IntentStatusDeploying, domain.IntentStatusApproved:
			state.DesiredReleaseID = &candidate.ReleaseID
			state.DesiredIntentID = &candidate.ID
		}
		if state.DesiredIntentID != nil {
			break
		}
	}
	if s.mlBacked() {
		if err := s.ml.repo.UpsertInferenceState(ctx, LLMStateToMLInferenceState(state)); err != nil {
			return err
		}
	} else if err := s.state.Upsert(ctx, state); err != nil {
		return err
	}
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *LLMRegistryService) computeDriftStatus(ctx context.Context, state *domain.LLMRouteState, obs *domain.LLMRouteObservation) domain.DriftStatus {
	if state == nil || state.DesiredReleaseID == nil {
		return domain.DriftStatusUnknown
	}
	if obs.ObservedReleaseID != nil && *obs.ObservedReleaseID == *state.DesiredReleaseID && obs.BackendHealth == domain.HealthStatusHealthy && obs.GatewayStatus == domain.GatewayRouteStatusSynced {
		route, err := s.routes.GetByID(ctx, obs.RouteID)
		if err == nil && route != nil {
			desiredHash := BuildLLMGatewayRouteSpec(route, obs.BackendEndpoint).ManagedConfigHash()
			if obs.GatewayConfigHash == "" || obs.GatewayConfigHash == desiredHash {
				return domain.DriftStatusInSync
			}
		}
	}
	if state.DesiredIntentID != nil {
		intent, err := s.intents.GetByID(ctx, *state.DesiredIntentID)
		if err == nil && intent != nil {
			switch intent.Status {
			case domain.IntentStatusPending, domain.IntentStatusApproved, domain.IntentStatusDeploying:
				return domain.DriftStatusDeploying
			}
		}
	}
	return domain.DriftStatusDrifted
}

func (s *LLMRegistryService) publish(ctx context.Context, typ events.EventType, entityID string, data any) {
	s.publisher.Publish(ctx, events.Event{Type: typ, EntityID: entityID, Data: data})
}

func (s *LLMRegistryService) publishStateChanged(ctx context.Context, state *domain.LLMRouteState) {
	if state == nil {
		return
	}
	data := events.ResourceData{RouteID: state.RouteID.String(), EnvironmentID: state.EnvironmentID.String()}
	if state.DesiredReleaseID != nil {
		data.ReleaseID = state.DesiredReleaseID.String()
	}
	if state.DesiredIntentID != nil {
		data.IntentID = state.DesiredIntentID.String()
	}
	if state.ActiveRunID != nil {
		data.RunID = state.ActiveRunID.String()
	}
	s.publish(ctx, events.EventLLMRouteStateChanged, state.RouteID.String()+":"+state.EnvironmentID.String(), data)
}

func defaultRouteGatewayConfig(route *domain.LLMRoute) {
	if route.GatewayConfig == nil {
		route.GatewayConfig = &domain.LLMGatewayRouteConfig{}
	}
	if route.GatewayConfig.PublicModel == "" {
		route.GatewayConfig.PublicModel = route.Name
	}
}

// BuildLLMGatewayRouteSpec creates the Bahia-owned gateway route spec for a backend endpoint.
func BuildLLMGatewayRouteSpec(route *domain.LLMRoute, backendEndpoint string) llmadapter.GatewayRouteSpec {
	name := ""
	publicModel := ""
	cfg := (*domain.LLMGatewayRouteConfig)(nil)
	if route != nil {
		name = route.Name
		cfg = route.GatewayConfig
	}
	if cfg != nil {
		publicModel = cfg.PublicModel
	}
	if publicModel == "" {
		publicModel = name
	}
	spec := llmadapter.GatewayRouteSpec{RouteName: name, PublicModel: publicModel, TargetURL: backendEndpoint}
	if cfg != nil {
		spec.Path = cfg.Path
		spec.TimeoutSeconds = cfg.TimeoutSeconds
		spec.Headers = cfg.Headers
	}
	return spec
}
