package mcp

import (
	"context"
	"encoding/json"
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

type capturePolicyCommandPublisher struct {
	create   *controlplane.PolicyMutationCommand
	update   *controlplane.PolicyMutationCommand
	delete   *controlplane.PolicyMutationCommand
	evaluate *controlplane.PolicyMutationCommand
}

func (p *capturePolicyCommandPublisher) PublishPolicyCreateRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.create = &cmd
	return testPolicyReceipt(controlplane.KindPolicyCreate, cmd.IdempotencyKey), nil
}
func (p *capturePolicyCommandPublisher) PublishPolicyUpdateRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.update = &cmd
	return testPolicyReceipt(controlplane.KindPolicyUpdate, cmd.IdempotencyKey), nil
}
func (p *capturePolicyCommandPublisher) PublishPolicyDeleteRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.delete = &cmd
	return testPolicyReceipt(controlplane.KindPolicyDelete, cmd.IdempotencyKey), nil
}
func (p *capturePolicyCommandPublisher) PublishPolicyEvaluateRequest(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	p.evaluate = &cmd
	return testPolicyReceipt(controlplane.KindPolicyEvaluate, cmd.IdempotencyKey), nil
}

func testPolicyReceipt(kind int, key string) *controlplane.PolicyCommandReceipt {
	if key == "" {
		key = "generated-key"
	}
	return &controlplane.PolicyCommandReceipt{RequestEventID: "policy-event", RequestPubkey: "operator-pubkey", RequestKind: kind, ResultKind: controlplane.KindContextVMMessage, ReadModelKinds: map[string]int{"policy_registry": controlplane.KindCASControlState}, DTag: key, IdempotencyKey: key, Status: "submitted", PublishedRelays: 1}
}

type captureToolApprovalCommandPublisher struct {
	cmd *controlplane.ToolApprovalCommand
}

func (p *captureToolApprovalCommandPublisher) PublishToolApprovalResponse(_ context.Context, cmd controlplane.ToolApprovalCommand) (*controlplane.ToolApprovalCommandReceipt, error) {
	p.cmd = &cmd
	return &controlplane.ToolApprovalCommandReceipt{RequestEventID: "tool-approval-event", RequestPubkey: "operator-pubkey", RequestKind: controlplane.KindToolApprovalResponse, ResultKind: controlplane.KindContextVMMessage, ReadModelKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, IntentID: cmd.IntentID.String(), Action: cmd.Action}, nil
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
	ctx := context.Background()
	server, repo := newTestMCPPolicyServer()
	envID := uuid.New()
	policyID := uuid.New()
	now := time.Now().UTC()
	repo.policies[policyID] = &domain.DeploymentPolicy{ID: policyID, Name: "require-signature", EnvironmentID: &envID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: now, UpdatedAt: now}

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

func TestCallTool_PolicyMutationsPublishSignerFirstRequests(t *testing.T) {
	ctx := context.Background()
	publisher := &capturePolicyCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{PolicyCommandPublisher: publisher})
	envID := uuid.New()
	policyID := uuid.New()
	artifactID := uuid.New()
	serviceID := uuid.New()

	createRes, err := server.CallTool(ctx, "bahia_create_policy", map[string]interface{}{
		"name":            "require-signature",
		"environment_id":  envID.String(),
		"rules":           []interface{}{map[string]interface{}{"type": "require_signature"}},
		"enforcement":     "block",
		"enabled":         true,
		"idempotency_key": "policy:create:test",
	})
	if err != nil || createRes.IsError {
		t.Fatalf("create result=%#v err=%v", createRes, err)
	}
	createPayload := decodeResultMap(t, createRes)
	if int(createPayload["request_kind"].(float64)) != controlplane.KindPolicyCreate {
		t.Fatalf("create request_kind = %v", createPayload["request_kind"])
	}
	if publisher.create == nil || publisher.create.Name != "require-signature" || publisher.create.EnvironmentID == nil || *publisher.create.EnvironmentID != envID || publisher.create.IdempotencyKey != "policy:create:test" {
		t.Fatalf("create command not captured correctly: %#v", publisher.create)
	}

	updateRes, err := server.CallTool(ctx, "bahia_update_policy", map[string]interface{}{"policy_id": policyID.String(), "name": "require-signature-v2", "enforcement": "warn", "idempotency_key": "policy:update:test"})
	if err != nil || updateRes.IsError {
		t.Fatalf("update result=%#v err=%v", updateRes, err)
	}
	if publisher.update == nil || publisher.update.ID != policyID || publisher.update.Name != "require-signature-v2" || publisher.update.Enforcement != "warn" {
		t.Fatalf("update command not captured correctly: %#v", publisher.update)
	}

	evalRes, err := server.CallTool(ctx, "bahia_evaluate_policy", map[string]interface{}{"artifact_id": artifactID.String(), "environment_id": envID.String(), "service_id": serviceID.String(), "idempotency_key": "policy:evaluate:test"})
	if err != nil || evalRes.IsError {
		t.Fatalf("evaluate result=%#v err=%v", evalRes, err)
	}
	if publisher.evaluate == nil || publisher.evaluate.ArtifactID != artifactID || publisher.evaluate.EnvironmentID == nil || *publisher.evaluate.EnvironmentID != envID || publisher.evaluate.ServiceID == nil || *publisher.evaluate.ServiceID != serviceID {
		t.Fatalf("evaluate command not captured correctly: %#v", publisher.evaluate)
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_policy", map[string]interface{}{"policy_id": policyID.String(), "idempotency_key": "policy:delete:test"})
	if err != nil || deleteRes.IsError {
		t.Fatalf("delete result=%#v err=%v", deleteRes, err)
	}
	if publisher.delete == nil || publisher.delete.ID != policyID || publisher.delete.IdempotencyKey != "policy:delete:test" {
		t.Fatalf("delete command not captured correctly: %#v", publisher.delete)
	}
}

func TestCallTool_ToolApprovalMutationsPublishSignerFirstResponses(t *testing.T) {
	ctx := context.Background()
	publisher := &captureToolApprovalCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{ToolApprovalCommandPublisher: publisher})
	intentID := uuid.New()

	res, err := server.CallTool(ctx, "bahia_tool_provision_approve", map[string]interface{}{"intent_id": intentID.String(), "reason": "reviewed", "idempotency_key": "tool:approve:test"})
	if err != nil || res.IsError {
		t.Fatalf("approve result=%#v err=%v", res, err)
	}
	payload := decodeResultMap(t, res)
	if int(payload["request_kind"].(float64)) != controlplane.KindToolApprovalResponse {
		t.Fatalf("approve request_kind = %v", payload["request_kind"])
	}
	if publisher.cmd == nil || publisher.cmd.IntentID != intentID || publisher.cmd.Action != "approve" || publisher.cmd.Reason != "reviewed" || publisher.cmd.IdempotencyKey != "tool:approve:test" {
		t.Fatalf("approval command not captured correctly: %#v", publisher.cmd)
	}

	res, err = server.CallTool(ctx, "bahia_tool_provision_reject", map[string]interface{}{"intent_id": intentID.String(), "reason": "unsafe"})
	if err != nil || res.IsError {
		t.Fatalf("reject result=%#v err=%v", res, err)
	}
	if publisher.cmd == nil || publisher.cmd.Action != "reject" || publisher.cmd.Reason != "unsafe" {
		t.Fatalf("rejection command not captured correctly: %#v", publisher.cmd)
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
