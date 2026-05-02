package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testPolicyRepo struct {
	policies map[uuid.UUID]*domain.DeploymentPolicy
}

func newTestPolicyRepo() *testPolicyRepo {
	return &testPolicyRepo{policies: make(map[uuid.UUID]*domain.DeploymentPolicy)}
}

func (m *testPolicyRepo) Create(_ context.Context, p *domain.DeploymentPolicy) error {
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
	m.policies[p.ID] = p
	return nil
}

func (m *testPolicyRepo) Delete(_ context.Context, id uuid.UUID) error {
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
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{Policies: policySvc})
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
		"bahia_list_policies":    false,
		"bahia_get_policy":       false,
		"bahia_create_policy":    false,
		"bahia_update_policy":    false,
		"bahia_delete_policy":    false,
		"bahia_evaluate_policy":  false,
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

func TestCallTool_PolicyCRUDAndEvaluate(t *testing.T) {
	ctx := context.Background()
	server, _ := newTestMCPPolicyServer()
	envID := uuid.New()

	createRes, err := server.CallTool(ctx, "bahia_create_policy", map[string]interface{}{
		"name":          "require-signature",
		"environment_id": envID.String(),
		"rules": []interface{}{
			map[string]interface{}{"type": "require_signature"},
		},
		"enforcement": "block",
		"enabled":     true,
	})
	if err != nil {
		t.Fatalf("create err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	policyID := createPayload["policy_id"].(string)

	getRes, err := server.CallTool(ctx, "bahia_get_policy", map[string]interface{}{"policy_id": policyID})
	if err != nil {
		t.Fatalf("get err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["name"] != "require-signature" {
		t.Fatalf("unexpected name: %v", getPayload["name"])
	}

	listRes, err := server.CallTool(ctx, "bahia_list_policies", map[string]interface{}{"environment_id": envID.String()})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected 1 policy, got %v", listPayload["total"])
	}

	updateRes, err := server.CallTool(ctx, "bahia_update_policy", map[string]interface{}{
		"policy_id":   policyID,
		"name":        "require-signature-v2",
		"enforcement": "warn",
	})
	if err != nil {
		t.Fatalf("update err: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("update returned error: %s", updateRes.Content[0].Text)
	}

	evalRes, err := server.CallTool(ctx, "bahia_evaluate_policy", map[string]interface{}{
		"artifact_id":    uuid.New().String(),
		"environment_id": envID.String(),
	})
	if err != nil {
		t.Fatalf("evaluate err: %v", err)
	}
	if evalRes.IsError {
		t.Fatalf("evaluate returned error: %s", evalRes.Content[0].Text)
	}
	evalPayload := decodeResultMap(t, evalRes)
	if _, ok := evalPayload["allowed"]; !ok {
		t.Fatalf("expected evaluation payload to include allowed")
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_policy", map[string]interface{}{"policy_id": policyID})
	if err != nil {
		t.Fatalf("delete err: %v", err)
	}
	if deleteRes.IsError {
		t.Fatalf("delete returned error: %s", deleteRes.Content[0].Text)
	}

	getAfterDeleteRes, err := server.CallTool(ctx, "bahia_get_policy", map[string]interface{}{"policy_id": policyID})
	if err != nil {
		t.Fatalf("get after delete err: %v", err)
	}
	if !getAfterDeleteRes.IsError {
		t.Fatalf("expected get after delete to return error")
	}
}

func TestCallTool_GetPolicy_Validation(t *testing.T) {
	server, _ := newTestMCPPolicyServer()

	res, err := server.CallTool(context.Background(), "bahia_get_policy", map[string]interface{}{"policy_id": "not-a-uuid"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected error result")
	}
}

func TestCallTool_GetPolicy_NotConfigured(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(context.Background(), "bahia_get_policy", map[string]interface{}{"policy_id": uuid.New().String()})
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
