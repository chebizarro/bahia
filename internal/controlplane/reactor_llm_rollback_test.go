package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type reactorLLMRouteRepo struct {
	routes map[uuid.UUID]*domain.LLMRoute
}

func (r *reactorLLMRouteRepo) Create(context.Context, *domain.LLMRoute) error { return nil }
func (r *reactorLLMRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, ok := r.routes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return route, nil
}
func (r *reactorLLMRouteRepo) GetByName(context.Context, string) (*domain.LLMRoute, error) {
	return nil, repository.ErrNotFound
}
func (r *reactorLLMRouteRepo) List(context.Context, int, int) ([]domain.LLMRoute, error) {
	return nil, nil
}
func (r *reactorLLMRouteRepo) Update(context.Context, *domain.LLMRoute) error { return nil }
func (r *reactorLLMRouteRepo) Delete(context.Context, uuid.UUID) error        { return nil }

type reactorLLMReleaseRepo struct {
	releases map[uuid.UUID]*domain.LLMRelease
}

func (r *reactorLLMReleaseRepo) Create(context.Context, *domain.LLMRelease) error { return nil }
func (r *reactorLLMReleaseRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	release, ok := r.releases[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return release, nil
}
func (r *reactorLLMReleaseRepo) GetByRouteVersion(context.Context, uuid.UUID, string) (*domain.LLMRelease, error) {
	return nil, repository.ErrNotFound
}
func (r *reactorLLMReleaseRepo) ListByRoute(context.Context, uuid.UUID, int, int) ([]domain.LLMRelease, error) {
	return nil, nil
}

type reactorLLMIntentRepo struct {
	intents map[uuid.UUID]*domain.LLMDeploymentIntent
	order   []uuid.UUID
}

func (r *reactorLLMIntentRepo) Create(_ context.Context, intent *domain.LLMDeploymentIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	intent.CreatedAt = time.Now().UTC()
	intent.UpdatedAt = intent.CreatedAt
	r.intents[intent.ID] = intent
	r.order = append([]uuid.UUID{intent.ID}, r.order...)
	return nil
}
func (r *reactorLLMIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	intent, ok := r.intents[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return intent, nil
}
func (r *reactorLLMIntentRepo) ListByRouteEnv(_ context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error) {
	out := make([]domain.LLMDeploymentIntent, 0)
	for _, id := range r.order {
		intent := r.intents[id]
		if intent.RouteID == routeID && intent.EnvironmentID == envID {
			out = append(out, *intent)
		}
	}
	if offset >= len(out) {
		return []domain.LLMDeploymentIntent{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *reactorLLMIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	r.intents[id].Status = status
	return nil
}
func (r *reactorLLMIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	r.intents[id].ApprovalStatus = status
	return nil
}

type reactorLLMStateRepo struct{ state *domain.LLMRouteState }

func (r *reactorLLMStateRepo) Upsert(_ context.Context, state *domain.LLMRouteState) error {
	r.state = state
	return nil
}
func (r *reactorLLMStateRepo) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.LLMRouteState, error) {
	return r.state, nil
}
func (r *reactorLLMStateRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *reactorLLMStateRepo) ListByRoute(context.Context, uuid.UUID) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *reactorLLMStateRepo) ListDrifted(context.Context) ([]domain.LLMRouteState, error) {
	return nil, nil
}
func (r *reactorLLMStateRepo) ListAll(context.Context) ([]domain.LLMRouteState, error) {
	return nil, nil
}

func TestHandleLLMRollbackRequestCreatesRollbackIntent(t *testing.T) {
	ctx := context.Background()
	routeID := uuid.New()
	envID := uuid.New()
	previousReleaseID := uuid.New()
	currentReleaseID := uuid.New()
	currentIntentID := uuid.New()

	routeRepo := &reactorLLMRouteRepo{routes: map[uuid.UUID]*domain.LLMRoute{routeID: {ID: routeID, Name: "chat"}}}
	releaseRepo := &reactorLLMReleaseRepo{releases: map[uuid.UUID]*domain.LLMRelease{
		previousReleaseID: {ID: previousReleaseID, RouteID: routeID, Version: "previous", ModelRef: "hf://previous", ModelSource: domain.ModelSourceHuggingFace},
		currentReleaseID:  {ID: currentReleaseID, RouteID: routeID, Version: "current", ModelRef: "hf://current", ModelSource: domain.ModelSourceHuggingFace},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: false}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	previousIntent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: previousReleaseID, RequestedBy: "alice", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := intentRepo.Create(ctx, previousIntent); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}
	currentIntent := &domain.LLMDeploymentIntent{ID: currentIntentID, RouteID: routeID, EnvironmentID: envID, ReleaseID: currentReleaseID, RequestedBy: "bob", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := intentRepo.Create(ctx, currentIntent); err != nil {
		t.Fatalf("seed current intent: %v", err)
	}
	stateRepo := &reactorLLMStateRepo{state: &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &currentIntentID, DriftStatus: domain.DriftStatusInSync}}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())

	reactor := NewReactor(Config{PrivateKey: nostr.GeneratePrivateKey(), AuthorizedPubkeys: []string{"authorized-pubkey"}}, nil, nostrpool.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithLLMRegistry(llmRegistry))
	reactor.handleLLMRollbackRequest(ctx, &nostr.Event{ID: "rollback-request", PubKey: "authorized-pubkey", Kind: KindLLMRollbackRequest, Content: `{"route_id":"` + routeID.String() + `","environment_id":"` + envID.String() + `","requested_by":"operator"}`})

	if len(intentRepo.intents) != 3 {
		t.Fatalf("expected rollback intent to be created, got %d intents", len(intentRepo.intents))
	}
	rollbackIntent := intentRepo.intents[intentRepo.order[0]]
	if rollbackIntent.ReleaseID != previousReleaseID {
		t.Fatalf("expected rollback release %s, got %s", previousReleaseID, rollbackIntent.ReleaseID)
	}
	if rollbackIntent.SourceKind != domain.SourceKindRollback {
		t.Fatalf("expected rollback source kind, got %s", rollbackIntent.SourceKind)
	}
	if rollbackIntent.RequestedBy != "operator" {
		t.Fatalf("expected requested_by operator, got %q", rollbackIntent.RequestedBy)
	}
	if rollbackIntent.Metadata["nostr_event_id"] != "rollback-request" || rollbackIntent.Metadata["nostr_request_pubkey"] != "authorized-pubkey" {
		t.Fatalf("missing Nostr correlation metadata: %#v", rollbackIntent.Metadata)
	}
	if stateRepo.state.DesiredReleaseID == nil || *stateRepo.state.DesiredReleaseID != previousReleaseID {
		t.Fatalf("expected desired release to roll back, got %#v", stateRepo.state.DesiredReleaseID)
	}
	if stateRepo.state.DesiredIntentID == nil || *stateRepo.state.DesiredIntentID != rollbackIntent.ID {
		t.Fatalf("expected desired intent to point at rollback, got %#v", stateRepo.state.DesiredIntentID)
	}
}

type reactorEnvironmentRepo struct {
	envs map[uuid.UUID]*domain.Environment
}

func (r *reactorEnvironmentRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *reactorEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	env, ok := r.envs[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return env, nil
}
func (r *reactorEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, repository.ErrNotFound
}
func (r *reactorEnvironmentRepo) List(context.Context) ([]domain.Environment, error) { return nil, nil }
func (r *reactorEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *reactorEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *reactorEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }
