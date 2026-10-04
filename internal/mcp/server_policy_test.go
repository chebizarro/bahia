package mcp

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testPolicyRepo struct {
	mutations int
	policies  map[uuid.UUID]*domain.DeploymentPolicy
}

func newTestPolicyRepo() *testPolicyRepo {
	return &testPolicyRepo{policies: make(map[uuid.UUID]*domain.DeploymentPolicy)}
}

func (m *testPolicyRepo) Create(_ context.Context, p *domain.DeploymentPolicy) error {
	m.mutations++
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	m.policies[p.ID] = p
	return nil
}

func (m *testPolicyRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	p, ok := m.policies[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return p, nil
}

func (m *testPolicyRepo) GetByName(_ context.Context, name string) (*domain.DeploymentPolicy, error) {
	for _, p := range m.policies {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testPolicyRepo) List(_ context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	out := make([]domain.DeploymentPolicy, 0, len(m.policies))
	for _, p := range m.policies {
		if enabledOnly && !p.Enabled {
			continue
		}
		out = append(out, *p)
	}
	return out, nil
}

func (m *testPolicyRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error) {
	out := make([]domain.DeploymentPolicy, 0)
	for _, p := range m.policies {
		if p.Enabled && p.EnvironmentID != nil && *p.EnvironmentID == envID {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (m *testPolicyRepo) ListGlobal(_ context.Context) ([]domain.DeploymentPolicy, error) {
	out := make([]domain.DeploymentPolicy, 0)
	for _, p := range m.policies {
		if p.Enabled && p.EnvironmentID == nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (m *testPolicyRepo) Update(_ context.Context, p *domain.DeploymentPolicy) error {
	m.mutations++
	m.policies[p.ID] = p
	return nil
}

func (m *testPolicyRepo) Delete(_ context.Context, id uuid.UUID) error {
	m.mutations++
	delete(m.policies, id)
	return nil
}

type testSigRepo struct{ hasSig bool }

func (m *testSigRepo) Create(_ context.Context, _ *domain.ArtifactSignature) error { return nil }
func (m *testSigRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, repository.ErrNotFound
}
func (m *testSigRepo) ListByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (m *testSigRepo) ListVerifiedByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (m *testSigRepo) HasVerifiedSignature(_ context.Context, _ uuid.UUID) (bool, error) {
	return m.hasSig, nil
}

type testSBOMRepo struct{}

func (m *testSBOMRepo) CreateSBOM(_ context.Context, _ *domain.ArtifactSBOM) error { return nil }
func (m *testSBOMRepo) GetSBOMByID(_ context.Context, _ uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (m *testSBOMRepo) GetSBOMByArtifact(_ context.Context, _ uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (m *testSBOMRepo) GetSBOMByHash(_ context.Context, _ string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (m *testSBOMRepo) CreatePackages(_ context.Context, _ []domain.SBOMPackage) error { return nil }
func (m *testSBOMRepo) ListPackagesBySBOM(_ context.Context, _ uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (m *testSBOMRepo) SearchPackagesByName(_ context.Context, _ string, _ int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

func newTestMCPPolicyServer() (*Server, *testPolicyRepo) {
	policyRepo := newTestPolicyRepo()
	policySvc := service.NewPolicyService(policyRepo, &testSigRepo{hasSig: true}, &testSBOMRepo{}, zap.NewNop())
	server := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{Policies: policySvc})
	return server, policyRepo
}

func decodeResultMap(t *testing.T, result *ToolResult) map[string]interface{} {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("missing result content")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return payload
}

func TestGetTools_IncludesPolicyCRUDAndEvaluate(t *testing.T) {
	server, _ := newTestMCPPolicyServer()
	tools := server.GetTools()

	required := map[string]bool{
		"bahia_list_policies":   false,
		"bahia_get_policy":      false,
		"bahia_create_policy":   false,
		"bahia_update_policy":   false,
		"bahia_delete_policy":   false,
		"bahia_evaluate_policy": false,
	}

	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}

	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_PolicyReadToolsUseDurableReadModels(t *testing.T) {
	ctx := authorizedMCPContext()
	server, repo := newTestMCPPolicyServer()
	envID := uuid.New()
	policyID := uuid.New()
	now := time.Now().UTC()
	repo.policies[policyID] = &domain.DeploymentPolicy{ID: policyID, Name: "require-signature", EnvironmentID: &envID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: now, UpdatedAt: now}
	attachCanonicalMCPFixture(t, server).publishPolicy(t, repo.policies[policyID])

	getRes, err := server.CallTool(ctx, "bahia_get_policy", map[string]interface{}{"policy_id": policyID.String()})
	if err != nil || getRes.IsError {
		t.Fatalf("get result=%#v err=%v", getRes, err)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["name"] != "require-signature" {
		t.Fatalf("unexpected name: %v", getPayload["name"])
	}

	listRes, err := server.CallTool(ctx, "bahia_list_policies", map[string]interface{}{"environment_id": envID.String()})
	if err != nil || listRes.IsError {
		t.Fatalf("list result=%#v err=%v", listRes, err)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected 1 policy, got %v", listPayload["total"])
	}
}

func TestCallTool_GetPolicy_Validation(t *testing.T) {
	server, _ := newTestMCPPolicyServer()

	res, err := server.CallTool(authorizedMCPContext(), "bahia_get_policy", map[string]interface{}{"policy_id": "not-a-uuid"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result")
	}
}

func TestCallTool_GetPolicy_NotConfigured(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(authorizedMCPContext(), "bahia_get_policy", map[string]interface{}{"policy_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result")
	}
}

func TestPolicyToMap_StableTimestamps(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	policy := &domain.DeploymentPolicy{
		ID:          uuid.New(),
		Name:        "x",
		Enforcement: domain.PolicyEnforcementWarn,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	got := policyToMap(policy)
	if got["created_at"] == "" || got["updated_at"] == "" {
		t.Fatalf("expected timestamps in map")
	}
}

func TestCallTool_EvaluatePolicyUsesIntentAndPublishesDecision(t *testing.T) {
	f := newRealMCPIntentFixture(t, "policy", false)
	environmentID, artifactID := uuid.New(), uuid.New()
	f.canonical.publishEnvironment(t, &domain.Environment{ID: environmentID, OrgID: f.orgID, Name: "prod"})
	policyID := uuid.New()
	f.policies.policies[policyID] = &domain.DeploymentPolicy{
		ID: policyID, Name: "require signature", EnvironmentID: &environmentID,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
		Enforcement: domain.PolicyEnforcementBlock, Enabled: true,
	}
	policySvc := service.NewPolicyService(f.policies, &testSigRepo{hasSig: false}, &testSBOMRepo{}, zap.NewNop())
	var statuses []nostr.Event
	signer, err := controlplane.NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	status := controlplane.NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		statuses = append(statuses, ev)
		return nil
	}, signer, zap.NewNop())
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{f.actor}, zap.NewNop()), f.canonical.store, status,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"policy": true}}, zap.NewNop())
	processor.RegisterHandler("policy", controlplane.NewPolicyIntentHandler(controlplane.PolicyIntentHandlerConfig{Policies: policySvc, Evaluator: policySvc, Logger: zap.NewNop()}))
	f.server.intentProc = processor
	args := map[string]interface{}{"artifact_id": artifactID.String(), "environment_id": environmentID.String(), "idempotency_key": "eval-1"}
	result, err := f.server.CallTool(f.ctx, "bahia_evaluate_policy", args)
	if err != nil || result.IsError {
		t.Fatalf("evaluate: %#v %v", result, err)
	}
	body := decodeResultMap(t, result)
	if body["status"] != "accepted" || int(body["status_kind"].(float64)) != controlplane.KindNIP38Status {
		t.Fatalf("receipt: %#v", body)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d", len(statuses))
	}
	var payload struct {
		Evaluation domain.PolicyEvaluation `json:"evaluation"`
	}
	if err := json.Unmarshal([]byte(statuses[0].Content), &payload); err != nil {
		t.Fatal(err)
	}
	want, err := policySvc.Evaluate(context.Background(), artifactID, environmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Evaluation, *want) {
		t.Fatalf("decision mismatch: got %#v want %#v", payload.Evaluation, *want)
	}
	if payload.Evaluation.Allowed || payload.Evaluation.Blockers != 1 {
		t.Fatalf("wrong decision: %#v", payload.Evaluation)
	}
	if body["status_coordinate"] != "intent-status:"+f.actor+":evaluation:"+artifactID.String()+":"+environmentID.String() {
		t.Fatalf("status coordinate: %#v", body)
	}

	result, err = f.server.CallTool(f.ctx, "bahia_evaluate_policy", args)
	if err != nil || result.IsError || len(statuses) != 1 {
		t.Fatalf("retry should deduplicate: %#v %v statuses=%d", result, err, len(statuses))
	}
	result, err = f.server.CallTool(f.strangerCtx, "bahia_evaluate_policy", args)
	if err != nil || !result.IsError || len(statuses) != 1 {
		t.Fatalf("non-operator should be rejected: %#v %v statuses=%d", result, err, len(statuses))
	}
	args["service_id"] = "" // Legacy optional context field was allowed to be empty.
	args["idempotency_key"] = "eval-empty-service"
	result, err = f.server.CallTool(f.ctx, "bahia_evaluate_policy", args)
	if err != nil || result.IsError || len(statuses) != 2 {
		t.Fatalf("empty optional service_id should not change evaluation: %#v %v statuses=%d", result, err, len(statuses))
	}
}
