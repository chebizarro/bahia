package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testLLMRouteRepo struct {
	routes map[uuid.UUID]*domain.LLMRoute
}

func newTestLLMRouteRepo() *testLLMRouteRepo {
	return &testLLMRouteRepo{routes: make(map[uuid.UUID]*domain.LLMRoute)}
}

func (r *testLLMRouteRepo) Create(_ context.Context, route *domain.LLMRoute) error {
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	now := time.Now().UTC()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = now
	}
	route.UpdatedAt = now
	r.routes[route.ID] = route
	return nil
}

func (r *testLLMRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, ok := r.routes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return route, nil
}

func (r *testLLMRouteRepo) GetByName(_ context.Context, name string) (*domain.LLMRoute, error) {
	for _, route := range r.routes {
		if route.Name == name {
			return route, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *testLLMRouteRepo) List(_ context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	out := make([]domain.LLMRoute, 0, len(r.routes))
	for _, route := range r.routes {
		out = append(out, *route)
	}
	if offset >= len(out) {
		return []domain.LLMRoute{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *testLLMRouteRepo) Update(_ context.Context, route *domain.LLMRoute) error {
	route.UpdatedAt = time.Now().UTC()
	r.routes[route.ID] = route
	return nil
}

func (r *testLLMRouteRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.routes, id)
	return nil
}

type testLLMReleaseRepo struct {
	releases map[uuid.UUID]*domain.LLMRelease
}

func newTestLLMReleaseRepo() *testLLMReleaseRepo {
	return &testLLMReleaseRepo{releases: make(map[uuid.UUID]*domain.LLMRelease)}
}

func (r *testLLMReleaseRepo) Create(_ context.Context, release *domain.LLMRelease) error {
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	if release.CreatedAt.IsZero() {
		release.CreatedAt = time.Now().UTC()
	}
	r.releases[release.ID] = release
	return nil
}

func (r *testLLMReleaseRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	release, ok := r.releases[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return release, nil
}

func (r *testLLMReleaseRepo) GetByRouteVersion(_ context.Context, routeID uuid.UUID, version string) (*domain.LLMRelease, error) {
	for _, release := range r.releases {
		if release.RouteID == routeID && release.Version == version {
			return release, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *testLLMReleaseRepo) ListByRoute(_ context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error) {
	out := make([]domain.LLMRelease, 0)
	for _, release := range r.releases {
		if release.RouteID == routeID {
			out = append(out, *release)
		}
	}
	if offset >= len(out) {
		return []domain.LLMRelease{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type captureLLMCommandPublisher struct {
	routeCreate     *controlplane.LLMRouteCreateCommand
	releaseRegister *controlplane.LLMReleaseRegisterCommand
	deploy          *controlplane.LLMDeployCommand
	approval        *controlplane.LLMApprovalCommand
	rollback        *controlplane.LLMRollbackCommand
}

func (p *captureLLMCommandPublisher) PublishLLMRouteCreateRequest(_ context.Context, cmd controlplane.LLMRouteCreateCommand) (*controlplane.LLMCommandReceipt, error) {
	p.routeCreate = &cmd
	return &controlplane.LLMCommandReceipt{RequestEventID: "route-create-event", RequestPubkey: "mcp-pubkey", RequestKind: controlplane.KindLLMRouteCreate, ResultKind: controlplane.KindLLMRouteCreateResult, RegistryKind: controlplane.KindLLMRouteRegistry, StateKind: controlplane.KindLLMRouteState, PublishedRelays: 1}, nil
}

func (p *captureLLMCommandPublisher) PublishLLMReleaseRegisterRequest(_ context.Context, cmd controlplane.LLMReleaseRegisterCommand) (*controlplane.LLMCommandReceipt, error) {
	p.releaseRegister = &cmd
	return &controlplane.LLMCommandReceipt{RequestEventID: "release-register-event", RequestPubkey: "mcp-pubkey", RequestKind: controlplane.KindLLMReleaseRegister, ResultKind: controlplane.KindLLMReleaseRegisterResult, RegistryKind: controlplane.KindLLMRouteRegistry, StateKind: controlplane.KindLLMRouteState, PublishedRelays: 1, RouteID: cmd.RouteID.String()}, nil
}

func (p *captureLLMCommandPublisher) PublishLLMDeployRequest(_ context.Context, cmd controlplane.LLMDeployCommand) (*controlplane.LLMCommandReceipt, error) {
	p.deploy = &cmd
	return &controlplane.LLMCommandReceipt{RequestEventID: "deploy-event", RequestPubkey: "mcp-pubkey", RequestKind: controlplane.KindLLMDeployRequest, StatusKind: controlplane.KindLLMDeploymentStatus, ResultKind: controlplane.KindLLMDeploymentResult, RegistryKind: controlplane.KindLLMRouteRegistry, StateKind: controlplane.KindLLMRouteState, PublishedRelays: 1, RouteID: cmd.RouteID.String(), EnvironmentID: cmd.EnvironmentID.String(), ReleaseID: cmd.ReleaseID.String()}, nil
}

func (p *captureLLMCommandPublisher) PublishLLMApprovalRequest(_ context.Context, cmd controlplane.LLMApprovalCommand) (*controlplane.LLMCommandReceipt, error) {
	p.approval = &cmd
	return &controlplane.LLMCommandReceipt{RequestEventID: cmd.Decision + "-event", RequestPubkey: "mcp-pubkey", RequestKind: controlplane.KindLLMDeploymentApproval, StatusKind: controlplane.KindLLMDeploymentStatus, ResultKind: controlplane.KindLLMDeploymentResult, RegistryKind: controlplane.KindLLMRouteRegistry, StateKind: controlplane.KindLLMRouteState, PublishedRelays: 1, IntentID: cmd.IntentID.String(), Decision: cmd.Decision}, nil
}

func (p *captureLLMCommandPublisher) PublishLLMRollbackRequest(_ context.Context, cmd controlplane.LLMRollbackCommand) (*controlplane.LLMCommandReceipt, error) {
	p.rollback = &cmd
	return &controlplane.LLMCommandReceipt{RequestEventID: "rollback-event", RequestPubkey: "mcp-pubkey", RequestKind: controlplane.KindLLMRollbackRequest, StatusKind: controlplane.KindLLMDeploymentStatus, ResultKind: controlplane.KindLLMDeploymentResult, RegistryKind: controlplane.KindLLMRouteRegistry, StateKind: controlplane.KindLLMRouteState, PublishedRelays: 1, RouteID: cmd.RouteID.String(), EnvironmentID: cmd.EnvironmentID.String()}, nil
}

func newTestLLMRegistryServer() (*Server, *testLLMRouteRepo, *testLLMReleaseRepo) {
	routeRepo := newTestLLMRouteRepo()
	releaseRepo := newTestLLMReleaseRepo()
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{LLMRegistry: llmRegistry})
	return server, routeRepo, releaseRepo
}

func TestGetTools_IncludesLLMTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{
		"bahia_llm_create_route":       false,
		"bahia_llm_update_route":       false,
		"bahia_llm_register_release":   false,
		"bahia_llm_list_routes":        false,
		"bahia_llm_list_releases":      false,
		"bahia_llm_deploy":             false,
		"bahia_llm_approve_deployment": false,
		"bahia_llm_reject_deployment":  false,
		"bahia_llm_rollback":           false,
	}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing LLM tool %s", name)
		}
	}
}

func TestCallTool_LLMRouteReleaseToolsPublishCanonicalNostrRequests(t *testing.T) {
	ctx := context.Background()
	publisher := &captureLLMCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{LLMCommandPublisher: publisher})
	routeID := uuid.New()

	createRes, err := server.CallTool(ctx, "bahia_llm_create_route", map[string]interface{}{
		"name":           "chat",
		"description":    "chat completions",
		"gateway_config": map[string]interface{}{"public_model": "bahia/chat"},
	})
	if err != nil {
		t.Fatalf("create route call err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create route returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	if createPayload["request_kind"].(float64) != float64(controlplane.KindLLMRouteCreate) || createPayload["result_kind"].(float64) != float64(controlplane.KindLLMRouteCreateResult) {
		t.Fatalf("unexpected create route payload: %#v", createPayload)
	}
	if _, ok := createPayload["status_kind"]; ok {
		t.Fatalf("route-create payload should not include status_kind: %#v", createPayload)
	}
	if publisher.routeCreate == nil || publisher.routeCreate.Name != "chat" || publisher.routeCreate.Description != "chat completions" {
		t.Fatalf("unexpected captured route-create command: %#v", publisher.routeCreate)
	}

	releaseRes, err := server.CallTool(ctx, "bahia_llm_register_release", map[string]interface{}{
		"route_id":     routeID.String(),
		"version":      "v1",
		"model_ref":    "hf://example/model",
		"model_source": domain.ModelSourceHuggingFace,
		"external_backend": map[string]interface{}{
			"base_url": "https://llm.example.com",
		},
	})
	if err != nil {
		t.Fatalf("register release call err: %v", err)
	}
	if releaseRes.IsError {
		t.Fatalf("register release returned error: %s", releaseRes.Content[0].Text)
	}
	releasePayload := decodeResultMap(t, releaseRes)
	if releasePayload["request_kind"].(float64) != float64(controlplane.KindLLMReleaseRegister) || releasePayload["result_kind"].(float64) != float64(controlplane.KindLLMReleaseRegisterResult) {
		t.Fatalf("unexpected release-register payload: %#v", releasePayload)
	}
	if _, ok := releasePayload["status_kind"]; ok {
		t.Fatalf("release-register payload should not include status_kind: %#v", releasePayload)
	}
	if publisher.releaseRegister == nil || publisher.releaseRegister.RouteID != routeID || publisher.releaseRegister.Version != "v1" || publisher.releaseRegister.ModelRef != "hf://example/model" {
		t.Fatalf("unexpected captured release-register command: %#v", publisher.releaseRegister)
	}
}

func TestCallTool_LLMUpdateRoutePersistsRegistryMetadata(t *testing.T) {
	ctx := context.Background()
	server, routeRepo, _ := newTestLLMRegistryServer()
	route := &domain.LLMRoute{ID: uuid.New(), Name: "chat", Description: "before"}
	if err := routeRepo.Create(ctx, route); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	updateRes, err := server.CallTool(ctx, "bahia_llm_update_route", map[string]interface{}{
		"route_id":    route.ID.String(),
		"description": "updated",
	})
	if err != nil {
		t.Fatalf("update route call err: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("update route returned error: %s", updateRes.Content[0].Text)
	}
	if routeRepo.routes[route.ID].Description != "updated" {
		t.Fatalf("expected route description to update")
	}
}

func TestCallTool_LLMAsyncToolsPublishCanonicalNostrRequests(t *testing.T) {
	ctx := context.Background()
	publisher := &captureLLMCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{LLMCommandPublisher: publisher})
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	intentID := uuid.New()

	deployRes, err := server.CallTool(ctx, "bahia_llm_deploy", map[string]interface{}{"route_id": routeID.String(), "environment_id": envID.String(), "release_id": releaseID.String(), "requested_by": "alice"})
	if err != nil {
		t.Fatalf("deploy call err: %v", err)
	}
	if deployRes.IsError {
		t.Fatalf("deploy returned error: %s", deployRes.Content[0].Text)
	}
	deployPayload := decodeResultMap(t, deployRes)
	if deployPayload["request_kind"].(float64) != float64(controlplane.KindLLMDeployRequest) || deployPayload["request_event_id"] != "deploy-event" {
		t.Fatalf("unexpected deploy correlation payload: %#v", deployPayload)
	}
	if publisher.deploy == nil || publisher.deploy.RouteID != routeID || publisher.deploy.EnvironmentID != envID || publisher.deploy.ReleaseID != releaseID || publisher.deploy.RequestedBy != "alice" {
		t.Fatalf("unexpected captured deploy command: %#v", publisher.deploy)
	}

	approveRes, err := server.CallTool(ctx, "bahia_llm_approve_deployment", map[string]interface{}{"intent_id": intentID.String()})
	if err != nil {
		t.Fatalf("approve call err: %v", err)
	}
	if approveRes.IsError {
		t.Fatalf("approve returned error: %s", approveRes.Content[0].Text)
	}
	approvePayload := decodeResultMap(t, approveRes)
	if approvePayload["request_kind"].(float64) != float64(controlplane.KindLLMDeploymentApproval) || approvePayload["decision"] != "approve" {
		t.Fatalf("unexpected approve payload: %#v", approvePayload)
	}

	rejectRes, err := server.CallTool(ctx, "bahia_llm_reject_deployment", map[string]interface{}{"intent_id": intentID.String()})
	if err != nil {
		t.Fatalf("reject call err: %v", err)
	}
	if rejectRes.IsError {
		t.Fatalf("reject returned error: %s", rejectRes.Content[0].Text)
	}
	rejectPayload := decodeResultMap(t, rejectRes)
	if rejectPayload["request_kind"].(float64) != float64(controlplane.KindLLMDeploymentApproval) || rejectPayload["decision"] != "reject" {
		t.Fatalf("unexpected reject payload: %#v", rejectPayload)
	}

	rollbackRes, err := server.CallTool(ctx, "bahia_llm_rollback", map[string]interface{}{"route_id": routeID.String(), "environment_id": envID.String(), "requested_by": "operator"})
	if err != nil {
		t.Fatalf("rollback call err: %v", err)
	}
	if rollbackRes.IsError {
		t.Fatalf("rollback returned error: %s", rollbackRes.Content[0].Text)
	}
	rollbackPayload := decodeResultMap(t, rollbackRes)
	if rollbackPayload["request_kind"].(float64) != float64(controlplane.KindLLMRollbackRequest) || rollbackPayload["status_kind"].(float64) != float64(controlplane.KindLLMDeploymentStatus) || rollbackPayload["result_kind"].(float64) != float64(controlplane.KindLLMDeploymentResult) {
		t.Fatalf("unexpected rollback correlation payload: %#v", rollbackPayload)
	}
	if publisher.rollback == nil || publisher.rollback.RouteID != routeID || publisher.rollback.EnvironmentID != envID || publisher.rollback.RequestedBy != "operator" {
		t.Fatalf("unexpected captured rollback command: %#v", publisher.rollback)
	}
}
