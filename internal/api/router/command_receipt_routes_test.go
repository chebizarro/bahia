package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type captureRESTServiceCommands struct {
	serviceCreate controlplane.ServiceCreateCommand
	deploy        controlplane.ServiceDeployCommand
	err           error
}

func (p *captureRESTServiceCommands) PublishServiceCreateRequest(_ context.Context, cmd controlplane.ServiceCreateCommand) (*controlplane.ServiceCommandReceipt, error) {
	p.serviceCreate = cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ServiceCommandReceipt{RequestEventID: "service-create-event", RequestPubkey: "rest-pubkey", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindCASControlState, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 2, ServiceName: cmd.Name}, nil
}

func (p *captureRESTServiceCommands) PublishDeployRequest(_ context.Context, cmd controlplane.ServiceDeployCommand) (*controlplane.ServiceCommandReceipt, error) {
	p.deploy = cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ServiceCommandReceipt{RequestEventID: "deploy-event", RequestPubkey: "rest-pubkey", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindDeploymentIntentRegistry, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, ServiceID: cmd.ServiceID.String(), EnvironmentID: cmd.EnvironmentID.String(), ArtifactID: cmd.ArtifactID.String()}, nil
}

type captureRESTLLMCommands struct {
	create controlplane.LLMRouteCreateCommand
	err    error
}

func (p *captureRESTLLMCommands) PublishLLMRouteCreateRequest(_ context.Context, cmd controlplane.LLMRouteCreateCommand) (*controlplane.LLMCommandReceipt, error) {
	p.create = cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.LLMCommandReceipt{RequestEventID: "llm-route-event", RequestPubkey: "rest-pubkey", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindCASControlState, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, TimeoutSeconds: 45}, nil
}

type captureRESTPolicyCommands struct {
	create controlplane.PolicyMutationCommand
	update controlplane.PolicyMutationCommand
	delete controlplane.PolicyMutationCommand
	err    error
}

func (p *captureRESTPolicyCommands) PublishPolicyCreateRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.create = cmd
	if p.err != nil {
		return nil, p.err
	}
	return restPolicyReceipt("policy-create-event", cmd), nil
}
func (p *captureRESTPolicyCommands) PublishPolicyUpdateRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.update = cmd
	if p.err != nil {
		return nil, p.err
	}
	return restPolicyReceipt("policy-update-event", cmd), nil
}
func (p *captureRESTPolicyCommands) PublishPolicyDeleteRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.delete = cmd
	if p.err != nil {
		return nil, p.err
	}
	return restPolicyReceipt("policy-delete-event", cmd), nil
}

func restPolicyReceipt(eventID string, cmd controlplane.PolicyMutationCommand) *controlplane.PolicyCommandReceipt {
	return &controlplane.PolicyCommandReceipt{RequestEventID: eventID, RequestPubkey: "rest-pubkey", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, ReadModelKinds: map[string]int{"policy_registry": controlplane.KindCASControlState}, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, PolicyID: cmd.ID.String(), PolicyName: cmd.Name}
}

func TestTransitionalRESTWritesReturnCommandReceipts(t *testing.T) {
	serviceCommands := &captureRESTServiceCommands{}
	llmCommands := &captureRESTLLMCommands{}
	policyCommands := &captureRESTPolicyCommands{}
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), ServiceCommands: serviceCommands, LLMCommands: llmCommands, PolicyCommands: policyCommands})

	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	policyID := uuid.New()
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		wantEvent   string
		wantDTag    string
		wantTimeout float64
		assertCall  func(t *testing.T)
	}{
		{
			name:      "service create",
			method:    http.MethodPost,
			path:      "/api/v1/services",
			body:      `{"name":"payments-api","artifact_repo":"registry.example/payments","idempotency_key":"svc-create:1"}`,
			wantEvent: "service-create-event",
			wantDTag:  "svc-create:1",
			assertCall: func(t *testing.T) {
				if serviceCommands.serviceCreate.Name != "payments-api" || serviceCommands.serviceCreate.IdempotencyKey != "svc-create:1" {
					t.Fatalf("service create command not captured: %#v", serviceCommands.serviceCreate)
				}
			},
		},
		{
			name:      "deployment intent",
			method:    http.MethodPost,
			path:      "/api/v1/deployments/intents",
			body:      `{"service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + artifactID.String() + `","requested_by":"operator","idempotency_key":"deploy:1"}`,
			wantEvent: "deploy-event",
			wantDTag:  "deploy:1",
			assertCall: func(t *testing.T) {
				if serviceCommands.deploy.ServiceID != serviceID || serviceCommands.deploy.EnvironmentID != envID || serviceCommands.deploy.ArtifactID != artifactID || serviceCommands.deploy.IdempotencyKey != "deploy:1" {
					t.Fatalf("deploy command not captured: %#v", serviceCommands.deploy)
				}
			},
		},
		{
			name:        "llm route create",
			method:      http.MethodPost,
			path:        "/api/v1/llm/routes",
			body:        `{"name":"chat-prod","description":"chat","idempotency_key":"llm-route:1"}`,
			wantEvent:   "llm-route-event",
			wantDTag:    "llm-route:1",
			wantTimeout: 45,
			assertCall: func(t *testing.T) {
				if llmCommands.create.Name != "chat-prod" || llmCommands.create.IdempotencyKey != "llm-route:1" {
					t.Fatalf("llm route command not captured: %#v", llmCommands.create)
				}
			},
		},
		{
			name:      "policy create",
			method:    http.MethodPost,
			path:      "/api/v1/policies",
			body:      `{"name":"require-sbom","rules":[{"type":"require_sbom"}],"enforcement":"block","enabled":true,"idempotency_key":"policy-create:1"}`,
			wantEvent: "policy-create-event",
			wantDTag:  "policy-create:1",
			assertCall: func(t *testing.T) {
				if policyCommands.create.Name != "require-sbom" || len(policyCommands.create.Rules) != 1 || policyCommands.create.IdempotencyKey != "policy-create:1" {
					t.Fatalf("policy create command not captured: %#v", policyCommands.create)
				}
			},
		},
		{
			name:      "policy update",
			method:    http.MethodPut,
			path:      "/api/v1/policies/" + policyID.String(),
			body:      `{"name":"require-sbom","rules":[{"type":"require_sbom"}],"enforcement":"warn","enabled":true,"idempotency_key":"policy-update:1"}`,
			wantEvent: "policy-update-event",
			wantDTag:  "policy-update:1",
			assertCall: func(t *testing.T) {
				if policyCommands.update.ID != policyID || policyCommands.update.Enforcement != string(domain.PolicyEnforcementWarn) || policyCommands.update.IdempotencyKey != "policy-update:1" {
					t.Fatalf("policy update command not captured: %#v", policyCommands.update)
				}
			},
		},
		{
			name:      "policy delete",
			method:    http.MethodDelete,
			path:      "/api/v1/policies/" + policyID.String(),
			body:      `{"idempotency_key":"policy-delete:1"}`,
			wantEvent: "policy-delete-event",
			wantDTag:  "policy-delete:1",
			assertCall: func(t *testing.T) {
				if policyCommands.delete.ID != policyID || policyCommands.delete.IdempotencyKey != "policy-delete:1" {
					t.Fatalf("policy delete command not captured: %#v", policyCommands.delete)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusAccepted {
				t.Fatalf("status=%d, want 202, body=%s", w.Code, w.Body.String())
			}
			data := commandReceiptData(t, w)
			if data["request_event_id"] != tt.wantEvent || data["request_kind"].(float64) != float64(controlplane.KindContextVMMessage) || data["published_relays"].(float64) < 1 {
				t.Fatalf("missing command receipt metadata: %#v", data)
			}
			wantTimeout := tt.wantTimeout
			if wantTimeout == 0 {
				wantTimeout = 30
			}
			if data["status"] != "submitted" || data["timeout_seconds"].(float64) != wantTimeout {
				t.Fatalf("missing submitted receipt fields: %#v", data)
			}
			if tt.wantDTag != "" && data["d_tag"] != tt.wantDTag {
				t.Fatalf("missing d_tag metadata: %#v", data)
			}
			tt.assertCall(t)
		})
	}
}

func TestTransitionalRESTWritePublishRejectionDoesNotReturnSubmittedReceipt(t *testing.T) {
	serviceCommands := &captureRESTServiceCommands{err: errors.New("publish service command ContextVM request: no relay accepted the request; retry after relay reconnect")}
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), ServiceCommands: serviceCommands})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(`{"name":"payments-api","artifact_repo":"registry.example/payments","idempotency_key":"svc-create:reject"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "request_event_id") || strings.Contains(body, "submitted") {
		t.Fatalf("publish rejection must not return submitted receipt: %s", body)
	}
	if !strings.Contains(body, "no relay accepted") {
		t.Fatalf("publish rejection should preserve relay acceptance error: %s", body)
	}
}

func TestLLMRouteCreateRequiresOrgScopedWritePermissionBeforePublish(t *testing.T) {
	const viewerKey = "0000000000000000000000000000000000000000000000000000000000000001"
	viewerPubkey, err := nostr.GetPublicKey(viewerKey)
	if err != nil {
		t.Fatalf("derive viewer pubkey: %v", err)
	}
	orgID := uuid.New()
	lookup := &rbacMemberLookup{members: map[uuid.UUID]map[string]domain.Role{
		orgID: {viewerPubkey: domain.RoleViewer},
	}}
	llmCommands := &captureRESTLLMCommands{}
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		AuthMiddleware: auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config())},
		RBAC:           auth.NewRBAC(lookup),
		LLMCommands:    llmCommands,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	url := srv.URL + "/api/v1/llm/routes?org_id=" + orgID.String()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"name":"chat-prod","idempotency_key":"llm-route:auth"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, viewerKey, http.MethodPost, url))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	if llmCommands.create.Name != "" {
		t.Fatalf("unauthorized LLM route create published command: %#v", llmCommands.create)
	}
}

func TestPolicyWritesResolveTargetOrgBeforePublish(t *testing.T) {
	const adminKey = "0000000000000000000000000000000000000000000000000000000000000001"
	adminPubkey, err := nostr.GetPublicKey(adminKey)
	if err != nil {
		t.Fatalf("derive admin pubkey: %v", err)
	}
	orgA := uuid.New()
	orgB := uuid.New()
	envB := uuid.New()
	policyID := uuid.New()
	envRepo := newMockEnvRepo()
	envRepo.envs[envB] = &domain.Environment{ID: envB, OrgID: orgB, Name: "prod"}
	policyRepo := newMockPolicyHTTPRepo()
	policyRepo.policies[policyID] = &domain.DeploymentPolicy{ID: policyID, Name: "require-sbom", EnvironmentID: &envB, Enabled: true}
	policySvc := service.NewPolicyService(policyRepo, nil, nil, zap.NewNop())
	lookup := &rbacMemberLookup{members: map[uuid.UUID]map[string]domain.Role{
		orgA: {adminPubkey: domain.RoleAdmin},
	}}
	policyCommands := &captureRESTPolicyCommands{}
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		AuthMiddleware: auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config())},
		RBAC:           auth.NewRBAC(lookup),
		Environments:   envRepo,
		Policies:       policySvc,
		PolicyCommands: policyCommands,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/v1/policies", `{"name":"require-sbom","environment_id":"` + envB.String() + `","rules":[{"type":"require_sbom"}],"enabled":true}`},
		{http.MethodPut, "/api/v1/policies/" + policyID.String(), `{"enforcement":"warn"}`},
		{http.MethodDelete, "/api/v1/policies/" + policyID.String(), ``},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			url := srv.URL + tt.path
			req, err := http.NewRequest(tt.method, url, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, adminKey, tt.method, url))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("perform request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", resp.StatusCode)
			}
		})
	}
	if policyCommands.create.Name != "" || policyCommands.update.ID != uuid.Nil || policyCommands.delete.ID != uuid.Nil {
		t.Fatalf("cross-org policy write published command: create=%#v update=%#v delete=%#v", policyCommands.create, policyCommands.update, policyCommands.delete)
	}
}

func commandReceiptData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing response data: %#v", resp)
	}
	return data
}
