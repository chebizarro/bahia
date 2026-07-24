package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type registrySecretResolverStub map[string]string

func (r registrySecretResolverStub) ResolveSecretWithAudit(_ context.Context, ref string, _ domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	value, ok := r[ref]
	if !ok {
		return "", domain.SecretAccessManifest{}, errors.New("secret not found")
	}
	return value, domain.SecretAccessManifest{}, nil
}

func TestResolveLLMGatewayRouteSpecUsesSecretRefsWithoutMutatingRoute(t *testing.T) {
	const secretRef = "2e9b746d-58c3-4e86-9ca5-a0184bc2918e"
	route := &domain.LLMRoute{Name: "chat", GatewayConfig: &domain.LLMGatewayRouteConfig{
		PublicModel:      "bahia/chat",
		Headers:          map[string]string{"X-Public": "yes"},
		HeaderSecretRefs: map[string]string{"Authorization": secretRef},
	}}

	spec, err := ResolveLLMGatewayRouteSpec(t.Context(), route, "http://backend:8000", registrySecretResolverStub{secretRef: "Bearer route-token"})
	if err != nil {
		t.Fatalf("ResolveLLMGatewayRouteSpec() error = %v", err)
	}
	if spec.Headers["Authorization"] != "Bearer route-token" || spec.Headers["X-Public"] != "yes" {
		t.Fatalf("resolved headers = %#v", spec.Headers)
	}
	if _, exposed := route.GatewayConfig.Headers["Authorization"]; exposed {
		t.Fatal("plaintext secret was written back into the persisted route")
	}
	if route.GatewayConfig.HeaderSecretRefs["Authorization"] != secretRef {
		t.Fatalf("secret ref was mutated: %#v", route.GatewayConfig.HeaderSecretRefs)
	}
}

func TestLLMRegistryListMethodsTolerateMissingLegacyRepositories(t *testing.T) {
	reg := NewLLMRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())

	routes, err := reg.ListRoutes(t.Context(), 25, 0)
	if err != nil {
		t.Fatalf("ListRoutes() error = %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("ListRoutes() returned %d routes, want 0", len(routes))
	}

	allStates, err := reg.ListAllRouteStates(t.Context())
	if err != nil {
		t.Fatalf("ListAllRouteStates() error = %v", err)
	}
	if len(allStates) != 0 {
		t.Fatalf("ListAllRouteStates() returned %d states, want 0", len(allStates))
	}

	envStates, err := reg.ListEnvironmentRouteStates(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("ListEnvironmentRouteStates() error = %v", err)
	}
	if len(envStates) != 0 {
		t.Fatalf("ListEnvironmentRouteStates() returned %d states, want 0", len(envStates))
	}

	routeStates, err := reg.ListRouteStates(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("ListRouteStates() error = %v", err)
	}
	if len(routeStates) != 0 {
		t.Fatalf("ListRouteStates() returned %d states, want 0", len(routeStates))
	}

	drifted, err := reg.ListDriftedRouteStates(t.Context())
	if err != nil {
		t.Fatalf("ListDriftedRouteStates() error = %v", err)
	}
	if len(drifted) != 0 {
		t.Fatalf("ListDriftedRouteStates() returned %d states, want 0", len(drifted))
	}
}

func TestLLMRegistryCreateIntentAndObservationState(t *testing.T) {
	routeID, releaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, releaseID, envID)
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID, RequestedBy: "tester"}
	if err := reg.CreateDeploymentIntent(t.Context(), intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	state, err := reg.GetRouteState(t.Context(), routeID, envID)
	if err != nil || state == nil {
		t.Fatalf("state after intent: %v", err)
	}
	if state.DesiredReleaseID == nil || *state.DesiredReleaseID != releaseID || state.DriftStatus != domain.DriftStatusDeploying {
		t.Fatalf("unexpected desired state: %#v", state)
	}
	obs := &domain.LLMRouteObservation{RouteID: routeID, EnvironmentID: envID, ObservedReleaseID: &releaseID, BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://worker:8000", BackendHealth: domain.HealthStatusHealthy, GatewayStatus: domain.GatewayRouteStatusSynced, GatewayConfigHash: BuildLLMGatewayRouteSpec(repos.routes.byID[routeID], "http://worker:8000").ManagedConfigHash(), Source: "test"}
	if err := reg.RecordObservation(t.Context(), obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}
	state, _ = reg.GetRouteState(t.Context(), routeID, envID)
	if state.DriftStatus != domain.DriftStatusInSync || state.BackendEndpoint != "http://worker:8000" {
		t.Fatalf("expected in-sync observed state, got %#v", state)
	}
}

func TestLLMRegistryRejectIntentRepairsDesiredStateToPreviousDeployment(t *testing.T) {
	routeID, previousReleaseID, pendingReleaseID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, pendingReleaseID, envID)
	repos.envs.byID[envID].Protected = true
	repos.releases.byID[previousReleaseID] = &domain.LLMRelease{ID: previousReleaseID, RouteID: routeID, Version: "v0", ModelRef: "hf/previous", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://prev.example.com"}}
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())

	previous := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: previousReleaseID, EnvironmentID: envID, RequestedBy: "previous", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), previous); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}

	pending := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: pendingReleaseID, EnvironmentID: envID, RequestedBy: "pending", SourceKind: domain.SourceKindManual}
	if err := reg.CreateDeploymentIntent(t.Context(), pending); err != nil {
		t.Fatalf("create pending intent: %v", err)
	}

	if err := reg.RejectDeploymentIntent(t.Context(), pending.ID); err != nil {
		t.Fatalf("reject pending intent: %v", err)
	}

	state, err := reg.GetRouteState(t.Context(), routeID, envID)
	if err != nil || state == nil {
		t.Fatalf("state after rejection: %v %#v", err, state)
	}
	if state.DesiredIntentID == nil || *state.DesiredIntentID != previous.ID {
		t.Fatalf("expected desired intent to repair to previous deployment, got %#v", state)
	}
	if state.DesiredReleaseID == nil || *state.DesiredReleaseID != previousReleaseID {
		t.Fatalf("expected desired release to repair to previous deployment, got %#v", state)
	}
}

func TestLLMRegistryRollbackSelectsPreviousDeployedDifferentRelease(t *testing.T) {
	routeID, previousReleaseID, currentReleaseID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, currentReleaseID, envID)
	repos.releases.byID[previousReleaseID] = &domain.LLMRelease{ID: previousReleaseID, RouteID: routeID, Version: "v0", ModelRef: "hf/previous", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://prev.example.com"}}
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())

	previous := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: previousReleaseID, EnvironmentID: envID, RequestedBy: "previous", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), previous); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}
	current := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: currentReleaseID, EnvironmentID: envID, RequestedBy: "current", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), current); err != nil {
		t.Fatalf("seed current intent: %v", err)
	}
	if err := repos.states.Upsert(t.Context(), &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &current.ID, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed route state: %v", err)
	}

	rollbackIntent, err := reg.RollbackWithMetadata(t.Context(), routeID, envID, "operator", map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollbackIntent.ReleaseID != previousReleaseID {
		t.Fatalf("expected rollback to previous release %s, got %s", previousReleaseID, rollbackIntent.ReleaseID)
	}
	if rollbackIntent.SourceKind != domain.SourceKindRollback {
		t.Fatalf("expected rollback source kind, got %s", rollbackIntent.SourceKind)
	}
	if rollbackIntent.SupersedesIntentID == nil || *rollbackIntent.SupersedesIntentID != current.ID {
		t.Fatalf("expected rollback to supersede current deployed intent, got %#v", rollbackIntent.SupersedesIntentID)
	}
	state, err := reg.GetRouteState(t.Context(), routeID, envID)
	if err != nil || state == nil {
		t.Fatalf("state after rollback: %v %#v", err, state)
	}
	if state.DesiredReleaseID == nil || *state.DesiredReleaseID != previousReleaseID {
		t.Fatalf("expected desired release to move to previous deployment, got %#v", state)
	}
	if state.DesiredIntentID == nil || *state.DesiredIntentID != rollbackIntent.ID {
		t.Fatalf("expected desired intent to point at rollback intent, got %#v", state)
	}
}

func TestLLMRegistryRollbackSelectsMostRecentPriorDifferentDeployment(t *testing.T) {
	routeID, olderReleaseID, targetReleaseID, currentReleaseID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, currentReleaseID, envID)
	repos.releases.byID[olderReleaseID] = &domain.LLMRelease{ID: olderReleaseID, RouteID: routeID, Version: "v0", ModelRef: "hf/older", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://older.example.com"}}
	repos.releases.byID[targetReleaseID] = &domain.LLMRelease{ID: targetReleaseID, RouteID: routeID, Version: "v1", ModelRef: "hf/target", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://target.example.com"}}
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())
	now := time.Now().UTC()

	// Insert out of chronological order to verify rollback selection is based on
	// deployment time, not repository iteration order.
	target := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: targetReleaseID, EnvironmentID: envID, RequestedBy: "target", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed, CreatedAt: now.Add(-2 * time.Hour)}
	if err := repos.intents.Create(t.Context(), target); err != nil {
		t.Fatalf("seed target intent: %v", err)
	}
	older := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: olderReleaseID, EnvironmentID: envID, RequestedBy: "older", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed, CreatedAt: now.Add(-4 * time.Hour)}
	if err := repos.intents.Create(t.Context(), older); err != nil {
		t.Fatalf("seed older intent: %v", err)
	}
	current := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: currentReleaseID, EnvironmentID: envID, RequestedBy: "current", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed, CreatedAt: now.Add(-1 * time.Hour)}
	if err := repos.intents.Create(t.Context(), current); err != nil {
		t.Fatalf("seed current intent: %v", err)
	}
	if err := repos.states.Upsert(t.Context(), &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &current.ID, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed route state: %v", err)
	}

	rollbackIntent, err := reg.RollbackWithMetadata(t.Context(), routeID, envID, "operator", map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollbackIntent.ReleaseID != targetReleaseID {
		t.Fatalf("expected rollback to most recent prior different release %s, got %s", targetReleaseID, rollbackIntent.ReleaseID)
	}
	if rollbackIntent.SupersedesIntentID == nil || *rollbackIntent.SupersedesIntentID != current.ID {
		t.Fatalf("expected rollback to supersede current deployed intent, got %#v", rollbackIntent.SupersedesIntentID)
	}
}

func TestLLMRegistryRollbackSkipsRepeatedCurrentReleaseDeployments(t *testing.T) {
	routeID, previousReleaseID, currentReleaseID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, currentReleaseID, envID)
	repos.releases.byID[previousReleaseID] = &domain.LLMRelease{ID: previousReleaseID, RouteID: routeID, Version: "v0", ModelRef: "hf/previous", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://prev.example.com"}}
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())

	previous := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: previousReleaseID, EnvironmentID: envID, RequestedBy: "previous", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), previous); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}
	currentOld := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: currentReleaseID, EnvironmentID: envID, RequestedBy: "current-old", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), currentOld); err != nil {
		t.Fatalf("seed earlier current intent: %v", err)
	}
	currentNewest := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: currentReleaseID, EnvironmentID: envID, RequestedBy: "current-newest", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), currentNewest); err != nil {
		t.Fatalf("seed newest current intent: %v", err)
	}
	if err := repos.states.Upsert(t.Context(), &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &currentNewest.ID, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed route state: %v", err)
	}

	rollbackIntent, err := reg.RollbackWithMetadata(t.Context(), routeID, envID, "operator", map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollbackIntent.ReleaseID != previousReleaseID {
		t.Fatalf("expected rollback to skip repeated current release and select %s, got %s", previousReleaseID, rollbackIntent.ReleaseID)
	}
	if rollbackIntent.SupersedesIntentID == nil || *rollbackIntent.SupersedesIntentID != currentNewest.ID {
		t.Fatalf("expected rollback to supersede newest current deployment, got %#v", rollbackIntent.SupersedesIntentID)
	}
}

func TestLLMRegistryRollbackFailsClosedWithoutPreviousDeployedDifferentRelease(t *testing.T) {
	routeID, currentReleaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, currentReleaseID, envID)
	reg := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, repos.runs, repos.obs, repos.states, nil, zap.NewNop())

	current := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: currentReleaseID, EnvironmentID: envID, RequestedBy: "current", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := repos.intents.Create(t.Context(), current); err != nil {
		t.Fatalf("seed current intent: %v", err)
	}
	if err := repos.states.Upsert(t.Context(), &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &current.ID, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed route state: %v", err)
	}

	_, err := reg.RollbackWithMetadata(t.Context(), routeID, envID, "operator", map[string]any{"source": "test"})
	if err == nil {
		t.Fatalf("expected rollback failure without previous deployed different release")
	}
	state, stateErr := reg.GetRouteState(t.Context(), routeID, envID)
	if stateErr != nil || state == nil {
		t.Fatalf("state after rollback failure: %v %#v", stateErr, state)
	}
	if state.DesiredReleaseID == nil || *state.DesiredReleaseID != currentReleaseID {
		t.Fatalf("expected desired release to remain unchanged, got %#v", state)
	}
	if len(repos.intents.byID) != 1 {
		t.Fatalf("expected no new rollback intent to be created, got %d intents", len(repos.intents.byID))
	}
}

type llmRegistryFakes struct {
	routes   *fakeLLMRouteRepo
	releases *fakeLLMReleaseRepo
	envs     *fakeEnvRepo
	intents  *fakeLLMIntentRepo
	runs     *fakeLLMRunRepo
	obs      *fakeLLMObsRepo
	states   *fakeLLMStateRepo
}

func newLLMRegistryFakes(routeID, releaseID, envID uuid.UUID) llmRegistryFakes {
	routes := &fakeLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{routeID: &domain.LLMRoute{ID: routeID, Name: "drydock-review", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "drydock.review"}}}}
	releases := &fakeLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{releaseID: &domain.LLMRelease{ID: releaseID, RouteID: routeID, Version: "v1", ModelRef: "hf/model", ModelSource: domain.ModelSourceHuggingFace, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "vllm:latest", HostPort: 8000, ContainerPort: 8000, HealthPath: "/health"}}}}
	envs := &fakeEnvRepo{byID: map[uuid.UUID]*domain.Environment{envID: &domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{"llm_gateway_ref": "default"}}}}
	return llmRegistryFakes{routes: routes, releases: releases, envs: envs, intents: &fakeLLMIntentRepo{byID: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}, runs: &fakeLLMRunRepo{byID: map[uuid.UUID]*domain.LLMDeploymentRun{}}, obs: &fakeLLMObsRepo{}, states: &fakeLLMStateRepo{byKey: map[string]*domain.LLMRouteState{}}}
}

func key(a, b uuid.UUID) string { return a.String() + ":" + b.String() }

type fakeLLMRouteRepo struct {
	byID map[uuid.UUID]*domain.LLMRoute
}

func (r *fakeLLMRouteRepo) Create(context.Context, *domain.LLMRoute) error { return nil }
func (r *fakeLLMRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	return r.byID[id], nil
}
func (r *fakeLLMRouteRepo) GetByName(context.Context, string) (*domain.LLMRoute, error) {
	return nil, nil
}
func (r *fakeLLMRouteRepo) List(context.Context, int, int) ([]domain.LLMRoute, error) {
	return nil, nil
}
func (r *fakeLLMRouteRepo) Update(context.Context, *domain.LLMRoute) error { return nil }
func (r *fakeLLMRouteRepo) Delete(context.Context, uuid.UUID) error        { return nil }

type fakeLLMReleaseRepo struct {
	byID map[uuid.UUID]*domain.LLMRelease
}

func (r *fakeLLMReleaseRepo) Create(context.Context, *domain.LLMRelease) error { return nil }
func (r *fakeLLMReleaseRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	return r.byID[id], nil
}
func (r *fakeLLMReleaseRepo) GetByRouteVersion(context.Context, uuid.UUID, string) (*domain.LLMRelease, error) {
	return nil, nil
}
func (r *fakeLLMReleaseRepo) ListByRoute(context.Context, uuid.UUID, int, int) ([]domain.LLMRelease, error) {
	return nil, nil
}

type fakeEnvRepo struct {
	byID map[uuid.UUID]*domain.Environment
}

func (r *fakeEnvRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *fakeEnvRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.byID[id], nil
}
func (r *fakeEnvRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r *fakeEnvRepo) List(context.Context) ([]domain.Environment, error) { return nil, nil }
func (r *fakeEnvRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *fakeEnvRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *fakeEnvRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type fakeLLMIntentRepo struct {
	byID  map[uuid.UUID]*domain.LLMDeploymentIntent
	order []uuid.UUID
}

func (r *fakeLLMIntentRepo) Create(_ context.Context, i *domain.LLMDeploymentIntent) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	now := time.Now().UTC()
	if i.CreatedAt.IsZero() {
		i.CreatedAt = now
	}
	i.UpdatedAt = now
	cp := *i
	if _, exists := r.byID[i.ID]; !exists {
		r.order = append(r.order, i.ID)
	}
	r.byID[i.ID] = &cp
	return nil
}
func (r *fakeLLMIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	return r.byID[id], nil
}
func (r *fakeLLMIntentRepo) ListByRouteEnv(_ context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error) {
	if limit <= 0 {
		limit = len(r.byID)
	}
	matches := make([]domain.LLMDeploymentIntent, 0, len(r.byID))
	for i := len(r.order) - 1; i >= 0; i-- {
		intent := r.byID[r.order[i]]
		if intent.RouteID == routeID && intent.EnvironmentID == envID {
			matches = append(matches, *intent)
		}
	}
	if offset >= len(matches) {
		return []domain.LLMDeploymentIntent{}, nil
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[offset:end], nil
}
func (r *fakeLLMIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, s domain.DeploymentIntentStatus) error {
	if r.byID[id] == nil {
		return repository.ErrNotFound
	}
	r.byID[id].Status = s
	return nil
}
func (r *fakeLLMIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, s domain.ApprovalStatus) error {
	if r.byID[id] == nil {
		return repository.ErrNotFound
	}
	r.byID[id].ApprovalStatus = s
	return nil
}

type fakeLLMRunRepo struct {
	byID map[uuid.UUID]*domain.LLMDeploymentRun
}

func (r *fakeLLMRunRepo) Create(context.Context, *domain.LLMDeploymentRun) error { return nil }
func (r *fakeLLMRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	return r.byID[id], nil
}
func (r *fakeLLMRunRepo) ListByIntent(context.Context, uuid.UUID) ([]domain.LLMDeploymentRun, error) {
	return nil, nil
}
func (r *fakeLLMRunRepo) EnsureQueuedRunForNextReadyIntent(context.Context) (*domain.LLMDeploymentRun, error) {
	return nil, nil
}
func (r *fakeLLMRunRepo) ClaimNextQueuedRun(context.Context) (*domain.LLMDeploymentRun, error) {
	return nil, nil
}
func (r *fakeLLMRunRepo) RequeueStaleRunning(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (r *fakeLLMRunRepo) Update(context.Context, *domain.LLMDeploymentRun) error { return nil }
func (r *fakeLLMRunRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentRunStatus, *int) error {
	return nil
}

type fakeLLMObsRepo struct{ latest *domain.LLMRouteObservation }

func (r *fakeLLMObsRepo) Create(_ context.Context, o *domain.LLMRouteObservation) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	cp := *o
	r.latest = &cp
	return nil
}
func (r *fakeLLMObsRepo) GetLatest(context.Context, uuid.UUID, uuid.UUID) (*domain.LLMRouteObservation, error) {
	return r.latest, nil
}
func (r *fakeLLMObsRepo) ListByRouteEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.LLMRouteObservation, error) {
	return nil, nil
}

type fakeLLMStateRepo struct {
	byKey map[string]*domain.LLMRouteState
}

func (r *fakeLLMStateRepo) Upsert(_ context.Context, s *domain.LLMRouteState) error {
	cp := *s
	r.byKey[key(s.RouteID, s.EnvironmentID)] = &cp
	return nil
}
func (r *fakeLLMStateRepo) Get(_ context.Context, a, b uuid.UUID) (*domain.LLMRouteState, error) {
	return r.byKey[key(a, b)], nil
}
func (r *fakeLLMStateRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *fakeLLMStateRepo) ListByRoute(context.Context, uuid.UUID) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *fakeLLMStateRepo) ListDrifted(context.Context) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *fakeLLMStateRepo) ListAll(context.Context) ([]domain.LLMRouteState, error) { return nil, nil }
