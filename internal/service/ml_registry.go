package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	EventMLModelChanged      events.EventType = "ml_model.changed"
	EventMLVersionChanged    events.EventType = "ml_model_version.changed"
	EventMLEndpointChanged   events.EventType = "ml_endpoint.changed"
	EventMLIntentChanged     events.EventType = "ml_deployment_intent.changed"
	EventMLRunChanged        events.EventType = "ml_deployment_run.changed"
	EventMLObservation       events.EventType = "ml_inference.observation"
	EventMLStateChanged      events.EventType = "ml_inference_state.changed"
	EventMLRecipeChanged     events.EventType = "ml_recipe.changed"
	EventMLBackfillCompleted events.EventType = "ml_llm_backfill.completed"
	EventMLParityChecked     events.EventType = "ml_llm_parity.checked"
)

// MLRegistryService owns canonical generic ML registry and lifecycle state.
type MLRegistryService struct {
	repo      repository.MLRegistryRepository
	publisher events.Publisher
	logger    *zap.Logger
}

func NewMLRegistryService(repo repository.MLRegistryRepository, publisher events.Publisher, logger *zap.Logger) *MLRegistryService {
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MLRegistryService{repo: repo, publisher: publisher, logger: logger}
}

func (s *MLRegistryService) CreateOrUpdateModel(ctx context.Context, model *domain.MLModel) error {
	if err := domain.ValidateMLModel(model); err != nil {
		return err
	}
	model.Slug = strings.TrimSpace(model.Slug)
	model.Name = strings.TrimSpace(model.Name)
	if err := s.repo.UpsertModel(ctx, model); err != nil {
		return err
	}
	s.publish(ctx, EventMLModelChanged, model.ID.String(), map[string]any{"model_id": model.ID.String(), "slug": model.Slug})
	return nil
}

func (s *MLRegistryService) GetModel(ctx context.Context, id uuid.UUID) (*domain.MLModel, error) {
	return s.repo.GetModel(ctx, id)
}

func (s *MLRegistryService) GetModelBySlug(ctx context.Context, slug string) (*domain.MLModel, error) {
	return s.repo.GetModelBySlug(ctx, slug)
}

func (s *MLRegistryService) ListModels(ctx context.Context, task domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error) {
	if task != "" && !task.IsValid() {
		return nil, fmt.Errorf("%w: ML task kind %q is not valid", domain.ErrInvalidValue, task)
	}
	if limit <= 0 {
		limit = 100
	}
	return s.repo.ListModels(ctx, task, limit, offset)
}

func (s *MLRegistryService) CreateOrUpdateModelVersion(ctx context.Context, version *domain.MLModelVersion) error {
	if err := domain.ValidateMLModelVersion(version); err != nil {
		return err
	}
	model, err := s.repo.GetModel(ctx, version.ModelID)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("ML model %s not found: %w", version.ModelID, repository.ErrNotFound)
	}
	if err := s.repo.UpsertModelVersion(ctx, version); err != nil {
		return err
	}
	s.publish(ctx, EventMLVersionChanged, version.ID.String(), map[string]any{"model_id": version.ModelID.String(), "model_version_id": version.ID.String(), "version": version.Version})
	return nil
}

func (s *MLRegistryService) GetModelVersion(ctx context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	return s.repo.GetModelVersion(ctx, id)
}

func (s *MLRegistryService) ListModelVersions(ctx context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.repo.ListModelVersions(ctx, modelID, limit, offset)
}

func (s *MLRegistryService) CreateOrUpdateArtifactRef(ctx context.Context, artifact *domain.MLArtifactRef) error {
	if err := domain.ValidateMLArtifactRef(artifact); err != nil {
		return err
	}
	return s.repo.UpsertArtifactRef(ctx, artifact)
}

func (s *MLRegistryService) CreateOrUpdateRecipe(ctx context.Context, recipe *domain.MLRecipe) error {
	if err := ApplyValidatedRecipeYAML(recipe); err != nil {
		return err
	}
	if err := s.repo.UpsertRecipe(ctx, recipe); err != nil {
		return err
	}
	s.publish(ctx, EventMLRecipeChanged, recipe.ID.String(), map[string]any{"recipe_id": recipe.ID.String(), "name": recipe.Name, "version": recipe.Version})
	return nil
}

func (s *MLRegistryService) CreateOrUpdateInferenceEndpoint(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	if endpoint == nil {
		return fmt.Errorf("ML inference endpoint is required")
	}
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	if endpoint.Name == "" {
		return fmt.Errorf("%w: endpoint name must not be empty", domain.ErrEmptyField)
	}
	if endpoint.EnvironmentID == uuid.Nil {
		return fmt.Errorf("%w: environment_id must not be empty", domain.ErrNilUUID)
	}
	for _, task := range endpoint.TaskKinds {
		if !task.IsValid() {
			return fmt.Errorf("%w: ML task kind %q is not valid", domain.ErrInvalidValue, task)
		}
	}
	if err := s.repo.UpsertInferenceEndpoint(ctx, endpoint); err != nil {
		return err
	}
	s.publish(ctx, EventMLEndpointChanged, endpoint.ID.String(), map[string]any{"endpoint_id": endpoint.ID.String(), "environment_id": endpoint.EnvironmentID.String()})
	return nil
}

func (s *MLRegistryService) GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	return s.repo.GetInferenceEndpoint(ctx, id)
}

func (s *MLRegistryService) CreateDeploymentIntent(ctx context.Context, intent *domain.MLDeploymentIntent) error {
	if intent == nil {
		return fmt.Errorf("ML deployment intent is required")
	}
	endpoint, err := s.repo.GetInferenceEndpoint(ctx, intent.EndpointID)
	if err != nil {
		return err
	}
	if endpoint == nil {
		return fmt.Errorf("ML endpoint %s not found: %w", intent.EndpointID, repository.ErrNotFound)
	}
	if endpoint.EnvironmentID != intent.EnvironmentID {
		return fmt.Errorf("ML endpoint %s is not in environment %s", intent.EndpointID, intent.EnvironmentID)
	}
	version, err := s.repo.GetModelVersion(ctx, intent.ModelVersionID)
	if err != nil {
		return err
	}
	if version == nil {
		return fmt.Errorf("ML model version %s not found: %w", intent.ModelVersionID, repository.ErrNotFound)
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
	if intent.ApprovalStatus == "" {
		intent.ApprovalStatus = domain.ApprovalStatusNotRequired
	}
	if intent.Status == "" {
		intent.Status = domain.IntentStatusApproved
	}
	if err := s.repo.UpsertDeploymentIntent(ctx, intent); err != nil {
		return err
	}
	state := &domain.MLInferenceState{EndpointID: intent.EndpointID, EnvironmentID: intent.EnvironmentID, DesiredModelVersionID: &intent.ModelVersionID, DesiredIntentID: &intent.ID, DriftStatus: domain.DriftStatusDeploying, GatewayStatus: domain.GatewayRouteStatusPending, BackendHealth: domain.HealthStatusUnknown}
	if err := s.repo.UpsertInferenceState(ctx, state); err != nil {
		return fmt.Errorf("upserting ML inference state: %w", err)
	}
	s.publish(ctx, EventMLIntentChanged, intent.ID.String(), map[string]any{"endpoint_id": intent.EndpointID.String(), "environment_id": intent.EnvironmentID.String(), "model_version_id": intent.ModelVersionID.String(), "intent_id": intent.ID.String()})
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *MLRegistryService) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	return s.repo.GetDeploymentIntent(ctx, id)
}

func (s *MLRegistryService) ListDeploymentIntents(ctx context.Context, endpointID, envID uuid.UUID, limit, offset int) ([]domain.MLDeploymentIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.repo.ListDeploymentIntents(ctx, endpointID, envID, limit, offset)
}

func (s *MLRegistryService) CreateOrUpdateDeploymentRun(ctx context.Context, run *domain.MLDeploymentRun) error {
	if run == nil {
		return fmt.Errorf("ML deployment run is required")
	}
	if run.Status == "" {
		run.Status = domain.RunStatusQueued
	}
	if err := domain.ValidateDeploymentRunStatus(run.Status); err != nil {
		return err
	}
	if err := s.repo.UpsertDeploymentRun(ctx, run); err != nil {
		return err
	}
	s.publish(ctx, EventMLRunChanged, run.ID.String(), map[string]any{"intent_id": run.DeploymentIntentID.String(), "run_id": run.ID.String(), "status": string(run.Status)})
	return nil
}

func (s *MLRegistryService) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if !isTerminalRunStatus(status) {
		return fmt.Errorf("cannot complete ML run with non-terminal status: %s", status)
	}
	run, err := s.repo.GetDeploymentRun(ctx, id)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("ML deployment run %s not found: %w", id, repository.ErrNotFound)
	}
	intent, err := s.repo.GetDeploymentIntent(ctx, run.DeploymentIntentID)
	if err != nil {
		return err
	}
	if intent == nil {
		return fmt.Errorf("ML deployment intent %s not found: %w", run.DeploymentIntentID, repository.ErrNotFound)
	}
	now := time.Now().UTC()
	run.Status = status
	run.ExitCode = exitCode
	run.FinishedAt = &now
	if err := s.repo.UpsertDeploymentRun(ctx, run); err != nil {
		return err
	}
	if status == domain.RunStatusSucceeded {
		intent.Status = domain.IntentStatusDeployed
	} else {
		intent.Status = domain.IntentStatusFailed
	}
	if err := s.repo.UpsertDeploymentIntent(ctx, intent); err != nil {
		return err
	}
	state, err := s.repo.GetInferenceState(ctx, intent.EndpointID, intent.EnvironmentID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &domain.MLInferenceState{EndpointID: intent.EndpointID, EnvironmentID: intent.EnvironmentID}
	}
	if status == domain.RunStatusSucceeded {
		state.ActiveRunID = &run.ID
		state.DesiredModelVersionID = &intent.ModelVersionID
		state.DesiredIntentID = &intent.ID
	} else if state.DesiredIntentID != nil && *state.DesiredIntentID == intent.ID {
		state.DriftStatus = domain.DriftStatusDrifted
		state.GatewayStatus = domain.GatewayRouteStatusError
	}
	if err := s.repo.UpsertInferenceState(ctx, state); err != nil {
		return err
	}
	s.publish(ctx, EventMLRunChanged, run.ID.String(), map[string]any{"intent_id": intent.ID.String(), "run_id": run.ID.String(), "status": string(status)})
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *MLRegistryService) RecordObservation(ctx context.Context, obs *domain.MLInferenceObservation) error {
	if obs == nil {
		return fmt.Errorf("ML inference observation is required")
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
	previousLatest, err := s.repo.GetLatestInferenceObservation(ctx, obs.EndpointID, obs.EnvironmentID)
	if err != nil {
		return err
	}
	if err := s.repo.UpsertInferenceObservation(ctx, obs); err != nil {
		return err
	}
	s.publish(ctx, EventMLObservation, obs.ID.String(), obs)
	if previousLatest != nil && obs.ObservedAt.Before(previousLatest.ObservedAt) {
		return nil
	}
	state, err := s.repo.GetInferenceState(ctx, obs.EndpointID, obs.EnvironmentID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &domain.MLInferenceState{EndpointID: obs.EndpointID, EnvironmentID: obs.EnvironmentID}
	}
	state.CurrentObservationID = &obs.ID
	state.RuntimeKind = obs.RuntimeKind
	state.BackendEndpoint = obs.BackendEndpoint
	state.BackendHealth = obs.BackendHealth
	state.GatewayStatus = obs.GatewayStatus
	state.GatewayTarget = obs.GatewayTarget
	now := time.Now().UTC()
	state.LastReconciledAt = &now
	var desiredIntent *domain.MLDeploymentIntent
	if state.DesiredIntentID != nil {
		desiredIntent, _ = s.repo.GetDeploymentIntent(ctx, *state.DesiredIntentID)
	}
	state.DriftStatus = computeMLDriftStatus(state, obs, desiredIntent)
	if err := s.repo.UpsertInferenceState(ctx, state); err != nil {
		return err
	}
	s.publishStateChanged(ctx, state)
	return nil
}

func (s *MLRegistryService) GetInferenceState(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	return s.repo.GetInferenceState(ctx, endpointID, envID)
}

func (s *MLRegistryService) ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error) {
	return s.repo.ListInferenceStates(ctx)
}

func computeMLDriftStatus(state *domain.MLInferenceState, obs *domain.MLInferenceObservation, desiredIntent *domain.MLDeploymentIntent) domain.DriftStatus {
	if state == nil || state.DesiredModelVersionID == nil {
		return domain.DriftStatusUnknown
	}
	if obs.ObservedModelVersionID != nil && *obs.ObservedModelVersionID == *state.DesiredModelVersionID && obs.BackendHealth == domain.HealthStatusHealthy && obs.GatewayStatus == domain.GatewayRouteStatusSynced {
		return domain.DriftStatusInSync
	}
	if desiredIntent != nil {
		switch desiredIntent.Status {
		case domain.IntentStatusPending, domain.IntentStatusApproved, domain.IntentStatusDeploying:
			return domain.DriftStatusDeploying
		}
	}
	return domain.DriftStatusDrifted
}

func (s *MLRegistryService) publish(ctx context.Context, typ events.EventType, entityID string, data any) {
	s.publisher.Publish(ctx, events.Event{Type: typ, EntityID: entityID, Data: data})
}

func (s *MLRegistryService) publishStateChanged(ctx context.Context, state *domain.MLInferenceState) {
	if state == nil {
		return
	}
	data := map[string]any{"endpoint_id": state.EndpointID.String(), "environment_id": state.EnvironmentID.String()}
	if state.DesiredModelVersionID != nil {
		data["model_version_id"] = state.DesiredModelVersionID.String()
	}
	if state.DesiredIntentID != nil {
		data["intent_id"] = state.DesiredIntentID.String()
	}
	if state.ActiveRunID != nil {
		data["run_id"] = state.ActiveRunID.String()
	}
	s.publish(ctx, EventMLStateChanged, state.EndpointID.String()+":"+state.EnvironmentID.String(), data)
}

// LLMBackfillSource is implemented by LLMRegistryService and test fakes. It avoids
// expanding legacy repository interfaces while still permitting deterministic backfill.
type LLMBackfillSource interface {
	ListRoutes(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error)
	ListReleases(ctx context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error)
	ListDeploymentIntents(ctx context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error)
	ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.LLMDeploymentRun, error)
	GetLatestObservation(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteObservation, error)
	ListAllRouteStates(ctx context.Context) ([]domain.LLMRouteState, error)
}

type LLMBackfillReport struct {
	RoutesBackfilled       int      `json:"routes_backfilled"`
	ReleasesBackfilled     int      `json:"releases_backfilled"`
	EndpointsBackfilled    int      `json:"endpoints_backfilled"`
	IntentsBackfilled      int      `json:"intents_backfilled"`
	RunsBackfilled         int      `json:"runs_backfilled"`
	ObservationsBackfilled int      `json:"observations_backfilled"`
	StatesBackfilled       int      `json:"states_backfilled"`
	Warnings               []string `json:"warnings,omitempty"`
}

type LLMParityReport struct {
	RoutesCompared   int      `json:"routes_compared"`
	ReleasesCompared int      `json:"releases_compared"`
	StatesCompared   int      `json:"states_compared"`
	Mismatches       []string `json:"mismatches,omitempty"`
}

func (r LLMParityReport) OK() bool { return len(r.Mismatches) == 0 }

func (s *MLRegistryService) BackfillLLMCompatibility(ctx context.Context, source LLMBackfillSource) (*LLMBackfillReport, error) {
	if source == nil {
		return nil, fmt.Errorf("LLM backfill source is required")
	}
	report := &LLMBackfillReport{}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		routes, err := source.ListRoutes(ctx, pageSize, offset)
		if err != nil {
			return report, fmt.Errorf("list LLM routes: %w", err)
		}
		if len(routes) == 0 {
			break
		}
		for i := range routes {
			model := LLMRouteToMLModel(&routes[i])
			if err := s.CreateOrUpdateModel(ctx, model); err != nil {
				return report, err
			}
			report.RoutesBackfilled++
			releases, err := source.ListReleases(ctx, routes[i].ID, pageSize, 0)
			if err != nil {
				return report, fmt.Errorf("list LLM releases: %w", err)
			}
			for j := range releases {
				version := LLMReleaseToMLModelVersion(&releases[j])
				if err := s.CreateOrUpdateModelVersion(ctx, version); err != nil {
					return report, err
				}
				report.ReleasesBackfilled++
			}
		}
		if len(routes) < pageSize {
			break
		}
	}
	states, err := source.ListAllRouteStates(ctx)
	if err != nil {
		return report, fmt.Errorf("list LLM route states: %w", err)
	}
	for i := range states {
		endpoint := LLMStateToMLInferenceEndpoint(&states[i])
		if err := s.CreateOrUpdateInferenceEndpoint(ctx, endpoint); err != nil {
			return report, err
		}
		report.EndpointsBackfilled++
		intents, err := source.ListDeploymentIntents(ctx, states[i].RouteID, states[i].EnvironmentID, pageSize, 0)
		if err != nil {
			return report, fmt.Errorf("list LLM deployment intents: %w", err)
		}
		for j := range intents {
			intent := LLMIntentToMLDeploymentIntent(&intents[j])
			if err := s.repo.UpsertDeploymentIntent(ctx, intent); err != nil {
				return report, err
			}
			report.IntentsBackfilled++
			runs, err := source.ListDeploymentRuns(ctx, intents[j].ID)
			if err != nil {
				return report, fmt.Errorf("list LLM deployment runs: %w", err)
			}
			for k := range runs {
				run := LLMRunToMLDeploymentRun(&runs[k])
				if err := s.repo.UpsertDeploymentRun(ctx, run); err != nil {
					return report, err
				}
				report.RunsBackfilled++
			}
		}
		obs, err := source.GetLatestObservation(ctx, states[i].RouteID, states[i].EnvironmentID)
		if err != nil {
			return report, fmt.Errorf("get latest LLM observation: %w", err)
		}
		if obs != nil {
			mlObs := LLMObservationToMLInferenceObservation(obs)
			if err := s.repo.UpsertInferenceObservation(ctx, mlObs); err != nil {
				return report, err
			}
			report.ObservationsBackfilled++
		}
		mlState := LLMStateToMLInferenceState(&states[i])
		if err := s.repo.UpsertInferenceState(ctx, mlState); err != nil {
			return report, err
		}
		report.StatesBackfilled++
	}
	s.publish(ctx, EventMLBackfillCompleted, "llm", report)
	return report, nil
}

func (s *MLRegistryService) CheckLLMCompatibilityParity(ctx context.Context, source LLMBackfillSource) (*LLMParityReport, error) {
	if source == nil {
		return nil, fmt.Errorf("LLM parity source is required")
	}
	report := &LLMParityReport{}
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		routes, err := source.ListRoutes(ctx, pageSize, offset)
		if err != nil {
			return report, err
		}
		if len(routes) == 0 {
			break
		}
		for i := range routes {
			route := routes[i]
			model, err := s.repo.GetModel(ctx, route.ID)
			if err != nil {
				return report, err
			}
			if model == nil {
				report.Mismatches = append(report.Mismatches, fmt.Sprintf("route %s missing ML model", route.ID))
				continue
			}
			if model.Slug != route.Name {
				report.Mismatches = append(report.Mismatches, fmt.Sprintf("route %s slug mismatch: %s != %s", route.ID, model.Slug, route.Name))
			}
			report.RoutesCompared++
			releases, err := source.ListReleases(ctx, route.ID, pageSize, 0)
			if err != nil {
				return report, err
			}
			for j := range releases {
				version, err := s.repo.GetModelVersion(ctx, releases[j].ID)
				if err != nil {
					return report, err
				}
				if version == nil {
					report.Mismatches = append(report.Mismatches, fmt.Sprintf("release %s missing ML model version", releases[j].ID))
					continue
				}
				if version.ModelID != releases[j].RouteID || version.Version != releases[j].Version {
					report.Mismatches = append(report.Mismatches, fmt.Sprintf("release %s ML version mismatch", releases[j].ID))
				}
				report.ReleasesCompared++
			}
		}
		if len(routes) < pageSize {
			break
		}
	}
	states, err := source.ListAllRouteStates(ctx)
	if err != nil {
		return report, err
	}
	for i := range states {
		state := states[i]
		mlState, err := s.repo.GetInferenceState(ctx, state.RouteID, state.EnvironmentID)
		if err != nil {
			return report, err
		}
		if mlState == nil {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("state %s/%s missing ML state", state.RouteID, state.EnvironmentID))
			continue
		}
		if !uuidPtrEqual(mlState.DesiredModelVersionID, state.DesiredReleaseID) {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("state %s/%s desired release mismatch", state.RouteID, state.EnvironmentID))
		}
		if !uuidPtrEqual(mlState.DesiredIntentID, state.DesiredIntentID) {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("state %s/%s desired intent mismatch", state.RouteID, state.EnvironmentID))
		}
		if mlState.DriftStatus != state.DriftStatus {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf("state %s/%s drift mismatch", state.RouteID, state.EnvironmentID))
		}
		report.StatesCompared++
	}
	s.publish(ctx, EventMLParityChecked, "llm", report)
	return report, nil
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func LLMRouteToMLModel(route *domain.LLMRoute) *domain.MLModel {
	model := &domain.MLModel{ID: route.ID, Slug: route.Name, Name: route.Name, Description: route.Description, Modalities: []string{"text"}, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Metadata: map[string]any{"llm_compat": true}, CreatedAt: route.CreatedAt, UpdatedAt: route.UpdatedAt}
	if route.GatewayConfig != nil {
		model.Metadata["gateway_config"] = route.GatewayConfig
	}
	if route.DefaultPlacementPolicy != nil {
		model.Metadata["default_placement_policy"] = route.DefaultPlacementPolicy
	}
	if route.DefaultPromotionGate != nil {
		model.Metadata["default_promotion_gate"] = route.DefaultPromotionGate
	}
	if route.Metadata != nil {
		model.Metadata["llm_metadata"] = route.Metadata
	}
	return model
}

func LLMReleaseToMLModelVersion(release *domain.LLMRelease) *domain.MLModelVersion {
	runtimes := make([]domain.MLRuntimeKind, 0, len(release.BackendPreferences))
	for _, kind := range release.BackendPreferences {
		runtimes = append(runtimes, domain.MLRuntimeKind(kind))
	}
	metadata := map[string]any{"llm_compat": true, "estimated_vram_gb": release.EstimatedVRAMGB}
	if release.RuntimeBackend != nil {
		metadata["runtime_backend"] = release.RuntimeBackend
	}
	if release.ExternalBackend != nil {
		metadata["external_backend"] = release.ExternalBackend
	}
	if release.PlacementPolicy != nil {
		metadata["placement_policy"] = release.PlacementPolicy
	}
	if release.PromotionGate != nil {
		metadata["promotion_gate"] = release.PromotionGate
	}
	if release.Metadata != nil {
		metadata["llm_metadata"] = release.Metadata
	}
	return &domain.MLModelVersion{ID: release.ID, ModelID: release.RouteID, Version: release.Version, Source: domain.MLSourceRef{Kind: release.ModelSource, URI: release.ModelRef, Revision: release.ModelRevision}, RuntimeRequirements: domain.MLRuntimeRequirements{PreferredRuntimes: runtimes, MinVRAMGB: release.EstimatedVRAMGB}, Metadata: metadata, CreatedAt: release.CreatedAt}
}

func MLModelToLLMRoute(model *domain.MLModel) *domain.LLMRoute {
	if model == nil {
		return nil
	}
	route := &domain.LLMRoute{ID: model.ID, Name: model.Slug, Description: model.Description, Metadata: map[string]any{}, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt}
	if model.Metadata != nil {
		_ = decodeMetadataField(model.Metadata, "gateway_config", &route.GatewayConfig)
		_ = decodeMetadataField(model.Metadata, "default_placement_policy", &route.DefaultPlacementPolicy)
		_ = decodeMetadataField(model.Metadata, "default_promotion_gate", &route.DefaultPromotionGate)
		if raw, ok := model.Metadata["llm_metadata"].(map[string]any); ok {
			route.Metadata = raw
		}
	}
	defaultRouteGatewayConfig(route)
	return route
}

func MLModelVersionToLLMRelease(version *domain.MLModelVersion) *domain.LLMRelease {
	if version == nil {
		return nil
	}
	prefs := make([]domain.LLMBackendKind, 0, len(version.RuntimeRequirements.PreferredRuntimes))
	for _, runtime := range version.RuntimeRequirements.PreferredRuntimes {
		prefs = append(prefs, domain.LLMBackendKind(runtime))
	}
	release := &domain.LLMRelease{ID: version.ID, RouteID: version.ModelID, Version: version.Version, ModelRef: version.Source.URI, ModelSource: version.Source.Kind, ModelRevision: version.Source.Revision, BackendPreferences: prefs, Metadata: map[string]any{}, CreatedAt: version.CreatedAt}
	if version.RuntimeRequirements.MinVRAMGB > 0 {
		release.EstimatedVRAMGB = version.RuntimeRequirements.MinVRAMGB
	}
	if version.Metadata != nil {
		_ = decodeMetadataField(version.Metadata, "runtime_backend", &release.RuntimeBackend)
		_ = decodeMetadataField(version.Metadata, "external_backend", &release.ExternalBackend)
		_ = decodeMetadataField(version.Metadata, "placement_policy", &release.PlacementPolicy)
		_ = decodeMetadataField(version.Metadata, "promotion_gate", &release.PromotionGate)
		if raw, ok := version.Metadata["llm_metadata"].(map[string]any); ok {
			release.Metadata = raw
		}
		if n, ok := version.Metadata["estimated_vram_gb"].(float64); ok {
			release.EstimatedVRAMGB = int(n)
		}
		if n, ok := version.Metadata["estimated_vram_gb"].(int); ok {
			release.EstimatedVRAMGB = n
		}
	}
	return release
}

func LLMStateToMLInferenceEndpoint(state *domain.LLMRouteState) *domain.MLInferenceEndpoint {
	return &domain.MLInferenceEndpoint{ID: state.RouteID, Name: state.RouteID.String(), EnvironmentID: state.EnvironmentID, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Protocol: "openai-compatible", Metadata: map[string]any{"llm_compat": true}}
}

func LLMIntentToMLDeploymentIntent(intent *domain.LLMDeploymentIntent) *domain.MLDeploymentIntent {
	return &domain.MLDeploymentIntent{ID: intent.ID, EndpointID: intent.RouteID, EnvironmentID: intent.EnvironmentID, ModelVersionID: intent.ReleaseID, RequestedBy: intent.RequestedBy, SourceKind: intent.SourceKind, ApprovalStatus: intent.ApprovalStatus, Status: intent.Status, SupersedesIntentID: intent.SupersedesIntentID, ApprovalMetadata: intent.ApprovalMetadata, Metadata: copyMetadataWithCompat(intent.Metadata), CreatedAt: intent.CreatedAt, ApprovedAt: intent.ApprovedAt, UpdatedAt: intent.UpdatedAt}
}

func LLMRunToMLDeploymentRun(run *domain.LLMDeploymentRun) *domain.MLDeploymentRun {
	return &domain.MLDeploymentRun{ID: run.ID, DeploymentIntentID: run.DeploymentIntentID, RuntimeKind: domain.MLRuntimeKind(run.BackendKind), EndpointRef: run.EndpointRef, WorkerPubkey: run.WorkerPubkey, WorkerName: run.WorkerName, BackendEndpoint: run.BackendEndpoint, Status: run.Status, ExitCode: run.ExitCode, StdoutRef: run.StdoutRef, StderrRef: run.StderrRef, Metadata: copyMetadataWithCompat(run.Metadata), StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
}

func LLMObservationToMLInferenceObservation(obs *domain.LLMRouteObservation) *domain.MLInferenceObservation {
	return &domain.MLInferenceObservation{ID: obs.ID, EndpointID: obs.RouteID, EnvironmentID: obs.EnvironmentID, ObservedModelVersionID: obs.ObservedReleaseID, ObservedRunID: obs.ObservedRunID, RuntimeKind: domain.MLRuntimeKind(obs.BackendKind), BackendEndpoint: obs.BackendEndpoint, BackendHealth: obs.BackendHealth, GatewayStatus: obs.GatewayStatus, GatewayTarget: obs.GatewayTarget, GatewayConfigHash: obs.GatewayConfigHash, Source: obs.Source, Metadata: copyMetadataWithCompat(obs.Metadata), ObservedAt: obs.ObservedAt}
}

func LLMStateToMLInferenceState(state *domain.LLMRouteState) *domain.MLInferenceState {
	return &domain.MLInferenceState{EndpointID: state.RouteID, EnvironmentID: state.EnvironmentID, DesiredModelVersionID: state.DesiredReleaseID, DesiredIntentID: state.DesiredIntentID, ActiveRunID: state.ActiveRunID, CurrentObservationID: state.CurrentObservationID, DriftStatus: state.DriftStatus, GatewayStatus: state.GatewayStatus, RuntimeKind: domain.MLRuntimeKind(state.BackendKind), BackendEndpoint: state.BackendEndpoint, BackendHealth: state.BackendHealth, GatewayTarget: state.GatewayTarget, LastReconciledAt: state.LastReconciledAt, UpdatedAt: state.UpdatedAt}
}

func MLIntentToLLMDeploymentIntent(intent *domain.MLDeploymentIntent) *domain.LLMDeploymentIntent {
	if intent == nil {
		return nil
	}
	return &domain.LLMDeploymentIntent{ID: intent.ID, RouteID: intent.EndpointID, EnvironmentID: intent.EnvironmentID, ReleaseID: intent.ModelVersionID, RequestedBy: intent.RequestedBy, SourceKind: intent.SourceKind, ApprovalStatus: intent.ApprovalStatus, Status: intent.Status, SupersedesIntentID: intent.SupersedesIntentID, ApprovalMetadata: intent.ApprovalMetadata, Metadata: intent.Metadata, CreatedAt: intent.CreatedAt, ApprovedAt: intent.ApprovedAt, UpdatedAt: intent.UpdatedAt}
}

func MLRunToLLMDeploymentRun(run *domain.MLDeploymentRun) *domain.LLMDeploymentRun {
	if run == nil {
		return nil
	}
	return &domain.LLMDeploymentRun{ID: run.ID, DeploymentIntentID: run.DeploymentIntentID, BackendKind: domain.LLMBackendKind(run.RuntimeKind), EndpointRef: run.EndpointRef, WorkerPubkey: run.WorkerPubkey, WorkerName: run.WorkerName, BackendEndpoint: run.BackendEndpoint, Status: run.Status, ExitCode: run.ExitCode, StdoutRef: run.StdoutRef, StderrRef: run.StderrRef, Metadata: run.Metadata, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
}

func MLObservationToLLMRouteObservation(obs *domain.MLInferenceObservation) *domain.LLMRouteObservation {
	if obs == nil {
		return nil
	}
	return &domain.LLMRouteObservation{ID: obs.ID, RouteID: obs.EndpointID, EnvironmentID: obs.EnvironmentID, ObservedReleaseID: obs.ObservedModelVersionID, ObservedRunID: obs.ObservedRunID, BackendKind: domain.LLMBackendKind(obs.RuntimeKind), BackendEndpoint: obs.BackendEndpoint, BackendHealth: obs.BackendHealth, GatewayStatus: obs.GatewayStatus, GatewayTarget: obs.GatewayTarget, GatewayConfigHash: obs.GatewayConfigHash, Source: obs.Source, Metadata: obs.Metadata, ObservedAt: obs.ObservedAt}
}

func MLStateToLLMRouteState(state *domain.MLInferenceState) *domain.LLMRouteState {
	if state == nil {
		return nil
	}
	return &domain.LLMRouteState{RouteID: state.EndpointID, EnvironmentID: state.EnvironmentID, DesiredReleaseID: state.DesiredModelVersionID, DesiredIntentID: state.DesiredIntentID, ActiveRunID: state.ActiveRunID, CurrentObservationID: state.CurrentObservationID, DriftStatus: state.DriftStatus, GatewayStatus: state.GatewayStatus, BackendKind: domain.LLMBackendKind(state.RuntimeKind), BackendEndpoint: state.BackendEndpoint, BackendHealth: state.BackendHealth, GatewayTarget: state.GatewayTarget, LastReconciledAt: state.LastReconciledAt, UpdatedAt: state.UpdatedAt}
}

func decodeMetadataField[T any](metadata map[string]any, key string, target **T) error {
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var decoded T
	if err := json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	*target = &decoded
	return nil
}

func copyMetadataWithCompat(in map[string]any) map[string]any {
	out := map[string]any{"llm_compat": true}
	for k, v := range in {
		out[k] = v
	}
	return out
}
