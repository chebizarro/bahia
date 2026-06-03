package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type mutableLLMRouteRepo struct {
	byID   map[uuid.UUID]*domain.LLMRoute
	byName map[string]*domain.LLMRoute
}

func (r *mutableLLMRouteRepo) Create(_ context.Context, route *domain.LLMRoute) error {
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	now := time.Now().UTC()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = now
	}
	route.UpdatedAt = now
	cp := *route
	r.byID[cp.ID] = &cp
	if cp.Name != "" {
		r.byName[cp.Name] = &cp
	}
	return nil
}

func (r *mutableLLMRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return route, nil
}

func (r *mutableLLMRouteRepo) GetByName(_ context.Context, name string) (*domain.LLMRoute, error) {
	route, ok := r.byName[name]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return route, nil
}

func (r *mutableLLMRouteRepo) List(context.Context, int, int) ([]domain.LLMRoute, error) {
	out := make([]domain.LLMRoute, 0, len(r.byID))
	for _, route := range r.byID {
		out = append(out, *route)
	}
	return out, nil
}

func (r *mutableLLMRouteRepo) Update(_ context.Context, route *domain.LLMRoute) error {
	cp := *route
	r.byID[cp.ID] = &cp
	if cp.Name != "" {
		r.byName[cp.Name] = &cp
	}
	return nil
}

func (r *mutableLLMRouteRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.byID, id)
	return nil
}

type mutableLLMReleaseRepo struct {
	byID map[uuid.UUID]*domain.LLMRelease
}

func (r *mutableLLMReleaseRepo) Create(_ context.Context, release *domain.LLMRelease) error {
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	if release.CreatedAt.IsZero() {
		release.CreatedAt = time.Now().UTC()
	}
	cp := *release
	r.byID[cp.ID] = &cp
	return nil
}

func (r *mutableLLMReleaseRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	release, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return release, nil
}

func (r *mutableLLMReleaseRepo) GetByRouteVersion(_ context.Context, routeID uuid.UUID, version string) (*domain.LLMRelease, error) {
	for _, release := range r.byID {
		if release.RouteID == routeID && release.Version == version {
			return release, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *mutableLLMReleaseRepo) ListByRoute(_ context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error) {
	out := make([]domain.LLMRelease, 0)
	for _, release := range r.byID {
		if release.RouteID == routeID {
			out = append(out, *release)
		}
	}
	return out, nil
}

func TestHandleLLMRouteCreatePublishesCorrelatedResult(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{}, byName: map[string]*domain.LLMRoute{}}
	llmRegistry := service.NewLLMRegistryService(routeRepo, nil, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMRouteCreate, `{"name":"chat-prod","description":"chat completions","gateway_config":{"public_model":"bahia/chat"}}`, nil)

	reactor.handleLLMRouteCreate(ctx, request)

	if len(routeRepo.byID) != 1 {
		t.Fatalf("expected one route, got %d", len(routeRepo.byID))
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "success")
	assertSignedEvent(t, result)

	payload := decodeLLMJSONMap(t, result.Content)
	if payload["status"] != "success" {
		t.Fatalf("unexpected result payload: %#v", payload)
	}
	routeID := uuid.MustParse(payload["route_id"].(string))
	assertReactorTag(t, result.Tags, "route", routeID.String())
	if stored := routeRepo.byID[routeID]; stored == nil || stored.Name != "chat-prod" {
		t.Fatalf("stored route mismatch: %#v", stored)
	}
}

func TestHandleLLMReleaseRegisterPublishesCorrelatedResult(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}},
	}, byName: map[string]*domain.LLMRoute{}}
	releaseRepo := &mutableLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{}}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMReleaseRegister, `{"version":"v1","model_ref":"hf://example/model","model_source":"huggingface","external_backend":{"base_url":"https://llm.example.com"}}`, nostr.Tags{{"route", routeID.String()}})

	reactor.handleLLMReleaseRegister(ctx, request)

	if len(releaseRepo.byID) != 1 {
		t.Fatalf("expected one release, got %d", len(releaseRepo.byID))
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "success")
	assertReactorTag(t, result.Tags, "route", routeID.String())
	assertSignedEvent(t, result)

	payload := decodeLLMJSONMap(t, result.Content)
	if payload["route_id"] != routeID.String() || payload["status"] != "success" {
		t.Fatalf("unexpected result payload: %#v", payload)
	}
	releaseID := uuid.MustParse(payload["release_id"].(string))
	assertReactorTag(t, result.Tags, "release", releaseID.String())
	if stored := releaseRepo.byID[releaseID]; stored == nil || stored.RouteID != routeID || stored.Version != "v1" {
		t.Fatalf("stored release mismatch: %#v", stored)
	}
}

func TestHandleLLMDeployRequestPublishesAcceptedStatus(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}},
	}, byName: map[string]*domain.LLMRoute{}}
	releaseRepo := &mutableLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{
		releaseID: {ID: releaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://example/model", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://llm.example.com"}},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: false}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	stateRepo := &reactorLLMStateRepo{}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMDeployRequest, `{}`, nostr.Tags{{"route", routeID.String()}, {"environment", envID.String()}, {"release", releaseID.String()}})

	reactor.handleLLMDeployRequest(ctx, request)

	if len(intentRepo.order) != 1 {
		t.Fatalf("expected one intent, got %d", len(intentRepo.order))
	}
	intent := intentRepo.intents[intentRepo.order[0]]
	if intent == nil || intent.RouteID != routeID || intent.EnvironmentID != envID || intent.ReleaseID != releaseID {
		t.Fatalf("stored intent mismatch: %#v", intent)
	}
	if intent.RequestedBy != requestPubkey {
		t.Fatalf("requested_by = %q, want requester pubkey %q", intent.RequestedBy, requestPubkey)
	}
	if intent.Metadata["nostr_event_id"] != request.ID || intent.Metadata["nostr_request_pubkey"] != requestPubkey {
		t.Fatalf("missing Nostr metadata: %#v", intent.Metadata)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	status := capture.events[0]
	if status.Kind != KindNIP38Status {
		t.Fatalf("status kind = %d, want %d", status.Kind, KindNIP38Status)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, status.Tags, "e", request.ID)
	assertReactorTag(t, status.Tags, "p", requestPubkey)
	assertReactorTag(t, status.Tags, "status", "processing")
	assertReactorTag(t, status.Tags, "step", "accepted")
	assertReactorTag(t, status.Tags, "route", routeID.String())
	assertReactorTag(t, status.Tags, "environment", envID.String())
	assertReactorTag(t, status.Tags, "release", releaseID.String())
	assertReactorTag(t, status.Tags, "intent", intent.ID.String())
	assertSignedEvent(t, status)

	payload := decodeLLMJSONMap(t, status.Content)
	if payload["status"] != "processing" || payload["step"] != "accepted" {
		t.Fatalf("unexpected status payload: %#v", payload)
	}
}

func TestHandleLLMRollbackRequestPublishesAcceptedStatus(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	envID := uuid.New()
	previousReleaseID := uuid.New()
	currentReleaseID := uuid.New()
	currentIntentID := uuid.New()
	currentIntent := &domain.LLMDeploymentIntent{ID: currentIntentID, RouteID: routeID, EnvironmentID: envID, ReleaseID: currentReleaseID, RequestedBy: "current", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	routeRepo := &reactorLLMRouteRepo{routes: map[uuid.UUID]*domain.LLMRoute{routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}}}}
	releaseRepo := &reactorLLMReleaseRepo{releases: map[uuid.UUID]*domain.LLMRelease{
		previousReleaseID: {ID: previousReleaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://previous", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://prev.example.com"}},
		currentReleaseID:  {ID: currentReleaseID, RouteID: routeID, Version: "v2", ModelRef: "hf://current", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://current.example.com"}},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: false}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	if err := intentRepo.Create(ctx, &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: previousReleaseID, RequestedBy: "previous", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}
	if err := intentRepo.Create(ctx, currentIntent); err != nil {
		t.Fatalf("seed current intent: %v", err)
	}
	stateRepo := &reactorLLMStateRepo{state: &domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &currentReleaseID, DesiredIntentID: &currentIntentID, DriftStatus: domain.DriftStatusInSync}}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMRollbackRequest, `{}`, nostr.Tags{{"route", routeID.String()}, {"environment", envID.String()}})

	reactor.handleLLMRollbackRequest(ctx, request)

	if len(intentRepo.order) != 3 {
		t.Fatalf("expected rollback intent to be created, got %d intents", len(intentRepo.order))
	}
	intent := intentRepo.intents[intentRepo.order[0]]
	if intent == nil || intent.RouteID != routeID || intent.EnvironmentID != envID || intent.ReleaseID != previousReleaseID {
		t.Fatalf("stored rollback intent mismatch: %#v", intent)
	}
	if intent.RequestedBy != requestPubkey {
		t.Fatalf("requested_by = %q, want requester pubkey %q", intent.RequestedBy, requestPubkey)
	}
	if intent.Metadata["nostr_event_id"] != request.ID || intent.Metadata["nostr_request_pubkey"] != requestPubkey {
		t.Fatalf("missing Nostr metadata: %#v", intent.Metadata)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	status := capture.events[0]
	if status.Kind != KindNIP38Status {
		t.Fatalf("status kind = %d, want %d", status.Kind, KindNIP38Status)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, status.Tags, "e", request.ID)
	assertReactorTag(t, status.Tags, "p", requestPubkey)
	assertReactorTag(t, status.Tags, "status", "processing")
	assertReactorTag(t, status.Tags, "step", "accepted")
	assertReactorTag(t, status.Tags, "route", routeID.String())
	assertReactorTag(t, status.Tags, "environment", envID.String())
	assertReactorTag(t, status.Tags, "release", previousReleaseID.String())
	assertReactorTag(t, status.Tags, "intent", intent.ID.String())
	assertSignedEvent(t, status)

	payload := decodeLLMJSONMap(t, status.Content)
	if payload["status"] != "processing" || payload["step"] != "accepted" {
		t.Fatalf("unexpected status payload: %#v", payload)
	}
}

func TestHandleLLMDeploymentApprovalApprovesPendingIntent(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}},
	}, byName: map[string]*domain.LLMRoute{}}
	releaseRepo := &mutableLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{
		releaseID: {ID: releaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://example/model", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://llm.example.com"}},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: true}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	stateRepo := &reactorLLMStateRepo{}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: "alice", SourceKind: domain.SourceKindManual}
	if err := llmRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMDeploymentApproval, `{}`, nostr.Tags{{"intent", intent.ID.String()}, {"decision", "approve"}})

	reactor.handleLLMDeploymentApproval(ctx, request)

	stored := intentRepo.intents[intent.ID]
	if stored.ApprovalStatus != domain.ApprovalStatusApproved || stored.Status != domain.IntentStatusApproved {
		t.Fatalf("approved intent state mismatch: %#v", stored)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "success")
	assertReactorTag(t, result.Tags, "result", "approve")
	assertReactorTag(t, result.Tags, "route", routeID.String())
	assertReactorTag(t, result.Tags, "environment", envID.String())
	assertReactorTag(t, result.Tags, "release", releaseID.String())
	assertReactorTag(t, result.Tags, "intent", intent.ID.String())
	assertSignedEvent(t, result)

	payload := decodeLLMJSONMap(t, result.Content)
	if payload["status"] != "approve" || payload["intent_id"] != intent.ID.String() {
		t.Fatalf("unexpected approval payload: %#v", payload)
	}
}

func TestHandleLLMDeploymentApprovalRejectsPendingIntentAndRepairsState(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	envID := uuid.New()
	previousReleaseID := uuid.New()
	pendingReleaseID := uuid.New()
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}},
	}, byName: map[string]*domain.LLMRoute{}}
	releaseRepo := &mutableLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{
		previousReleaseID: {ID: previousReleaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://example/model-prev", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://prev.example.com"}},
		pendingReleaseID:  {ID: pendingReleaseID, RouteID: routeID, Version: "v2", ModelRef: "hf://example/model-next", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://next.example.com"}},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: true}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	stateRepo := &reactorLLMStateRepo{}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	previousIntent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: previousReleaseID, RequestedBy: "alice", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusDeployed}
	if err := intentRepo.Create(ctx, previousIntent); err != nil {
		t.Fatalf("seed previous intent: %v", err)
	}
	pendingIntent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: pendingReleaseID, RequestedBy: "bob", SourceKind: domain.SourceKindManual}
	if err := llmRegistry.CreateDeploymentIntent(ctx, pendingIntent); err != nil {
		t.Fatalf("create pending intent: %v", err)
	}
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMDeploymentApproval, `{}`, nostr.Tags{{"intent", pendingIntent.ID.String()}, {"decision", "reject"}})

	reactor.handleLLMDeploymentApproval(ctx, request)

	stored := intentRepo.intents[pendingIntent.ID]
	if stored.ApprovalStatus != domain.ApprovalStatusRejected || stored.Status != domain.IntentStatusRejected {
		t.Fatalf("rejected intent state mismatch: %#v", stored)
	}
	if stateRepo.state == nil || stateRepo.state.DesiredIntentID == nil || *stateRepo.state.DesiredIntentID != previousIntent.ID {
		t.Fatalf("desired intent was not repaired to previous deployment: %#v", stateRepo.state)
	}
	if stateRepo.state.DesiredReleaseID == nil || *stateRepo.state.DesiredReleaseID != previousReleaseID {
		t.Fatalf("desired release was not repaired: %#v", stateRepo.state)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "success")
	assertReactorTag(t, result.Tags, "result", "reject")
	assertReactorTag(t, result.Tags, "intent", pendingIntent.ID.String())
	assertSignedEvent(t, result)

	payload := decodeLLMJSONMap(t, result.Content)
	if payload["status"] != "reject" || payload["intent_id"] != pendingIntent.ID.String() {
		t.Fatalf("unexpected rejection payload: %#v", payload)
	}
}

func TestHandleLLMDeploymentApprovalRejectsInvalidDecision(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	routeRepo := &mutableLLMRouteRepo{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: {ID: routeID, Name: "chat-prod", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "bahia/chat"}},
	}, byName: map[string]*domain.LLMRoute{}}
	releaseRepo := &mutableLLMReleaseRepo{byID: map[uuid.UUID]*domain.LLMRelease{
		releaseID: {ID: releaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://example/model", ModelSource: domain.ModelSourceHuggingFace, ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://llm.example.com"}},
	}}
	envRepo := &reactorEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Protected: true}}}
	intentRepo := &reactorLLMIntentRepo{intents: map[uuid.UUID]*domain.LLMDeploymentIntent{}, order: []uuid.UUID{}}
	stateRepo := &reactorLLMStateRepo{}
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, envRepo, intentRepo, nil, nil, stateRepo, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: "alice", SourceKind: domain.SourceKindManual}
	if err := llmRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	reactor := newLLMRequestTestReactor(t, Config{AuthorizedPubkeys: []string{requestPubkey}}, capture, llmRegistry)
	request := signedLLMRequest(t, requestKey, KindLLMDeploymentApproval, `{}`, nostr.Tags{{"intent", intent.ID.String()}, {"decision", "later"}})

	reactor.handleLLMDeploymentApproval(ctx, request)

	stored := intentRepo.intents[intent.ID]
	if stored.ApprovalStatus != domain.ApprovalStatusPending || stored.Status != domain.IntentStatusPending {
		t.Fatalf("invalid decision should not mutate intent: %#v", stored)
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", requestPubkey)
	assertReactorTag(t, result.Tags, "status", "error")
	assertReactorTag(t, result.Tags, "step", "validation_error")
	assertReactorTag(t, result.Tags, "intent", intent.ID.String())
	assertSignedEvent(t, result)

	var response ContextVMJSONRPCResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("decode invalid-decision ContextVM response: %v", err)
	}
	if response.Error == nil || response.Error.Message == "" {
		t.Fatalf("unexpected invalid-decision response: %#v", response)
	}
}

func newLLMRequestTestReactor(t *testing.T, cfg Config, capture *captureNostrPublisher, llmRegistry *service.LLMRegistryService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(cfg, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithLLMRegistry(llmRegistry))
}

func signedLLMRequest(t *testing.T, privateKey string, kind int, content string, tags nostr.Tags) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := event.Sign(privateKey); err != nil {
		t.Fatalf("sign request event: %v", err)
	}
	return event
}

func decodeLLMJSONMap(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if result, ok := payload["result"].(map[string]any); ok {
		return result
	}
	return payload
}
