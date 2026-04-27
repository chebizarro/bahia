package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// --- Mock repos ---

type mockPolicyRepo struct {
	policies map[uuid.UUID]*domain.DeploymentPolicy
}

func newMockPolicyRepo() *mockPolicyRepo {
	return &mockPolicyRepo{policies: make(map[uuid.UUID]*domain.DeploymentPolicy)}
}

func (m *mockPolicyRepo) Create(_ context.Context, p *domain.DeploymentPolicy) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	m.policies[p.ID] = p
	return nil
}

func (m *mockPolicyRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	p, ok := m.policies[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return p, nil
}

func (m *mockPolicyRepo) GetByName(_ context.Context, name string) (*domain.DeploymentPolicy, error) {
	for _, p := range m.policies {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockPolicyRepo) List(_ context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	var result []domain.DeploymentPolicy
	for _, p := range m.policies {
		if enabledOnly && !p.Enabled {
			continue
		}
		result = append(result, *p)
	}
	return result, nil
}

func (m *mockPolicyRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error) {
	var result []domain.DeploymentPolicy
	for _, p := range m.policies {
		if p.EnvironmentID != nil && *p.EnvironmentID == envID && p.Enabled {
			result = append(result, *p)
		}
	}
	return result, nil
}

func (m *mockPolicyRepo) ListGlobal(_ context.Context) ([]domain.DeploymentPolicy, error) {
	var result []domain.DeploymentPolicy
	for _, p := range m.policies {
		if p.EnvironmentID == nil && p.Enabled {
			result = append(result, *p)
		}
	}
	return result, nil
}

func (m *mockPolicyRepo) Update(_ context.Context, p *domain.DeploymentPolicy) error {
	m.policies[p.ID] = p
	return nil
}

func (m *mockPolicyRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.policies, id)
	return nil
}

type mockSigRepo struct {
	hasSig bool
}

func (m *mockSigRepo) Create(_ context.Context, _ *domain.ArtifactSignature) error { return nil }
func (m *mockSigRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, repository.ErrNotFound
}
func (m *mockSigRepo) ListByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (m *mockSigRepo) ListVerifiedByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (m *mockSigRepo) HasVerifiedSignature(_ context.Context, _ uuid.UUID) (bool, error) {
	return m.hasSig, nil
}

type mockSBOMRepoForPolicy struct {
	sbom *domain.ArtifactSBOM
	pkgs []domain.SBOMPackage
}

func (m *mockSBOMRepoForPolicy) CreateSBOM(_ context.Context, _ *domain.ArtifactSBOM) error {
	return nil
}
func (m *mockSBOMRepoForPolicy) GetSBOMByID(_ context.Context, _ uuid.UUID) (*domain.ArtifactSBOM, error) {
	if m.sbom == nil {
		return nil, repository.ErrNotFound
	}
	return m.sbom, nil
}
func (m *mockSBOMRepoForPolicy) GetSBOMByArtifact(_ context.Context, _ uuid.UUID) (*domain.ArtifactSBOM, error) {
	if m.sbom == nil {
		return nil, repository.ErrNotFound
	}
	return m.sbom, nil
}
func (m *mockSBOMRepoForPolicy) GetSBOMByHash(_ context.Context, _ string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (m *mockSBOMRepoForPolicy) CreatePackages(_ context.Context, _ []domain.SBOMPackage) error {
	return nil
}
func (m *mockSBOMRepoForPolicy) ListPackagesBySBOM(_ context.Context, _ uuid.UUID) ([]domain.SBOMPackage, error) {
	return m.pkgs, nil
}
func (m *mockSBOMRepoForPolicy) SearchPackagesByName(_ context.Context, _ string, _ int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

// --- Tests ---

func newTestPolicyService() (*PolicyService, *mockPolicyRepo, *mockSigRepo, *mockSBOMRepoForPolicy) {
	policyRepo := newMockPolicyRepo()
	sigRepo := &mockSigRepo{}
	sbomRepo := &mockSBOMRepoForPolicy{}
	svc := NewPolicyService(policyRepo, sigRepo, sbomRepo, zap.NewNop())
	return svc, policyRepo, sigRepo, sbomRepo
}

func TestPolicyService_Evaluate_NoPolicies(t *testing.T) {
	svc, _, _, _ := newTestPolicyService()

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed when no policies exist")
	}
}

func TestPolicyService_Evaluate_RequireSignature_Pass(t *testing.T) {
	svc, policyRepo, sigRepo, _ := newTestPolicyService()
	sigRepo.hasSig = true

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "require-sig",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed when signature exists")
	}
	if eval.Blockers != 0 {
		t.Errorf("blockers = %d, want 0", eval.Blockers)
	}
}

func TestPolicyService_Evaluate_RequireSignature_Block(t *testing.T) {
	svc, policyRepo, sigRepo, _ := newTestPolicyService()
	sigRepo.hasSig = false

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "require-sig",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Allowed {
		t.Error("expected blocked when no signature")
	}
	if eval.Blockers != 1 {
		t.Errorf("blockers = %d, want 1", eval.Blockers)
	}
}

func TestPolicyService_Evaluate_RequireSignature_Warn(t *testing.T) {
	svc, policyRepo, sigRepo, _ := newTestPolicyService()
	sigRepo.hasSig = false

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "require-sig-warn",
		Enforcement: domain.PolicyEnforcementWarn,
		Enabled:     true,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed (warn only)")
	}
	if eval.Warnings != 1 {
		t.Errorf("warnings = %d, want 1", eval.Warnings)
	}
}

func TestPolicyService_Evaluate_RequireSBOM(t *testing.T) {
	svc, policyRepo, _, sbomRepo := newTestPolicyService()

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "require-sbom",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSBOM}},
	})

	// No SBOM → blocked.
	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Allowed {
		t.Error("expected blocked when no SBOM")
	}

	// Add SBOM → pass.
	sbomRepo.sbom = &domain.ArtifactSBOM{ID: uuid.New()}
	eval, err = svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed when SBOM exists")
	}
}

func TestPolicyService_Evaluate_MaxCriticalVulns(t *testing.T) {
	svc, policyRepo, _, sbomRepo := newTestPolicyService()

	sbomRepo.sbom = &domain.ArtifactSBOM{
		ID:            uuid.New(),
		CriticalCount: 3,
	}

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "max-crit",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules: []domain.PolicyRule{
			{Type: domain.RuleMaxCriticalVulns, Params: map[string]any{"max": 0}},
		},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Allowed {
		t.Error("expected blocked: 3 critical vulns > max 0")
	}
}

func TestPolicyService_Evaluate_BlockPackage(t *testing.T) {
	svc, policyRepo, _, sbomRepo := newTestPolicyService()

	sbomID := uuid.New()
	sbomRepo.sbom = &domain.ArtifactSBOM{ID: sbomID}
	sbomRepo.pkgs = []domain.SBOMPackage{
		{Name: "safe-pkg", Version: "1.0"},
		{Name: "log4j-core", Version: "2.14.1"},
	}

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "block-log4j",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules: []domain.PolicyRule{
			{Type: domain.RuleBlockPackage, Params: map[string]any{"package": "log4j-core"}},
		},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Allowed {
		t.Error("expected blocked: log4j-core is in SBOM")
	}
	if len(eval.Results[0].Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(eval.Results[0].Violations))
	}
	if eval.Results[0].Violations[0].Rule != domain.RuleBlockPackage {
		t.Errorf("violation rule = %q", eval.Results[0].Violations[0].Rule)
	}
}

func TestPolicyService_Evaluate_MultipleRules(t *testing.T) {
	svc, policyRepo, sigRepo, sbomRepo := newTestPolicyService()

	sigRepo.hasSig = true
	sbomRepo.sbom = &domain.ArtifactSBOM{
		ID:            uuid.New(),
		CriticalCount: 0,
		HighCount:     5,
	}

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        "strict-policy",
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules: []domain.PolicyRule{
			{Type: domain.RuleRequireSignature},
			{Type: domain.RuleRequireSBOM},
			{Type: domain.RuleMaxCriticalVulns, Params: map[string]any{"max": 0}},
			{Type: domain.RuleMaxHighVulns, Params: map[string]any{"max": 10}},
		},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed: all rules pass")
	}
}

func TestPolicyService_Evaluate_EnvSpecificPolicy(t *testing.T) {
	svc, policyRepo, sigRepo, _ := newTestPolicyService()
	sigRepo.hasSig = false

	envID := uuid.New()
	otherEnvID := uuid.New()

	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:          "prod-sig-required",
		EnvironmentID: &envID,
		Enforcement:   domain.PolicyEnforcementBlock,
		Enabled:       true,
		Rules:         []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
	})

	// Evaluated against the same env → blocked.
	eval, err := svc.Evaluate(context.Background(), uuid.New(), envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Allowed {
		t.Error("expected blocked for env with policy")
	}

	// Evaluated against different env → allowed (no matching policy).
	eval, err = svc.Evaluate(context.Background(), uuid.New(), otherEnvID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eval.Allowed {
		t.Error("expected allowed for env without policy")
	}
}

func TestPolicyService_CRUD(t *testing.T) {
	svc, _, _, _ := newTestPolicyService()
	ctx := context.Background()

	p := &domain.DeploymentPolicy{
		Name:        "test-policy",
		Enforcement: domain.PolicyEnforcementWarn,
		Enabled:     true,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSBOM}},
	}

	// Create.
	if err := svc.CreatePolicy(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get.
	got, err := svc.GetPolicy(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "test-policy" {
		t.Errorf("name = %q", got.Name)
	}

	// List.
	list, err := svc.ListPolicies(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(list))
	}

	// Update.
	p.Name = "updated-policy"
	if err := svc.UpdatePolicy(ctx, p); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = svc.GetPolicy(ctx, p.ID)
	if got.Name != "updated-policy" {
		t.Errorf("updated name = %q", got.Name)
	}

	// Delete.
	if err := svc.DeletePolicy(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = svc.ListPolicies(ctx, false)
	if len(list) != 0 {
		t.Error("expected 0 policies after delete")
	}
}

func TestGetIntParam(t *testing.T) {
	if getIntParam(nil, "max", 5) != 5 {
		t.Error("nil params should return default")
	}
	if getIntParam(map[string]any{"max": 10}, "max", 5) != 10 {
		t.Error("int param should return value")
	}
	if getIntParam(map[string]any{"max": float64(10)}, "max", 5) != 10 {
		t.Error("float64 param should return value")
	}
	if getIntParam(map[string]any{"max": "not-a-number"}, "max", 5) != 5 {
		t.Error("invalid type should return default")
	}
}

func TestGetStringParam(t *testing.T) {
	if getStringParam(nil, "status", "clean") != "clean" {
		t.Error("nil params should return default")
	}
	if getStringParam(map[string]any{"status": "warning"}, "status", "clean") != "warning" {
		t.Error("string param should return value")
	}
}
