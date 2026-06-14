package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	sigs   []domain.ArtifactSignature
}

func (m *mockSigRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	sig.NormalizeVerificationStatus()
	m.sigs = append(m.sigs, *sig)
	return nil
}
func (m *mockSigRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, repository.ErrNotFound
}
func (m *mockSigRepo) ListByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	return m.sigs, nil
}
func (m *mockSigRepo) ListVerifiedByArtifact(_ context.Context, _ uuid.UUID) ([]domain.ArtifactSignature, error) {
	var out []domain.ArtifactSignature
	for _, sig := range m.sigs {
		sig.NormalizeVerificationStatus()
		if sig.VerificationStatus == domain.SignatureStatusVerified {
			out = append(out, sig)
		}
	}
	return out, nil
}
func (m *mockSigRepo) HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error) {
	if m.hasSig {
		return true, nil
	}
	sigs, err := m.ListVerifiedByArtifact(ctx, artifactID)
	return len(sigs) > 0, err
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

type mockSBOMAttestationProvider struct {
	att      *domain.SBOMAttestation
	sbomData []byte
	attErr   error
	dataErr  error
}

func (m *mockSBOMAttestationProvider) GetAttestationForArtifact(_ context.Context, _ uuid.UUID) (*domain.SBOMAttestation, error) {
	if m.attErr != nil {
		return nil, m.attErr
	}
	return m.att, nil
}

func (m *mockSBOMAttestationProvider) GetSBOMDataForArtifact(_ context.Context, _ uuid.UUID) ([]byte, error) {
	if m.dataErr != nil {
		return nil, m.dataErr
	}
	return m.sbomData, nil
}

// --- Tests ---

func newTestPolicyService() (*PolicyService, *mockPolicyRepo, *mockSigRepo, *mockSBOMRepoForPolicy) {
	policyRepo := newMockPolicyRepo()
	sigRepo := &mockSigRepo{}
	sbomRepo := &mockSBOMRepoForPolicy{}
	svc := NewPolicyService(policyRepo, sigRepo, sbomRepo, zap.NewNop())
	return svc, policyRepo, sigRepo, sbomRepo
}

func newTestPolicyServiceWithOptions(opts ...PolicyServiceOption) (*PolicyService, *mockPolicyRepo, *mockSigRepo, *mockSBOMRepoForPolicy) {
	policyRepo := newMockPolicyRepo()
	sigRepo := &mockSigRepo{}
	sbomRepo := &mockSBOMRepoForPolicy{}
	svc := NewPolicyService(policyRepo, sigRepo, sbomRepo, zap.NewNop(), opts...)
	return svc, policyRepo, sigRepo, sbomRepo
}

func evaluateBlockingRule(t *testing.T, svc *PolicyService, policyRepo *mockPolicyRepo, sbomRepo *mockSBOMRepoForPolicy, rule domain.PolicyRule) *domain.PolicyEvaluation {
	t.Helper()
	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{
		Name:        string(rule.Type),
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
		Rules:       []domain.PolicyRule{rule},
	})

	eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eval.Results) != 1 {
		t.Fatalf("expected 1 policy result, got %d", len(eval.Results))
	}
	_ = sbomRepo
	return eval
}

func requireAllowed(t *testing.T, eval *domain.PolicyEvaluation) {
	t.Helper()
	if !eval.Allowed {
		t.Fatalf("expected policy to pass, got violations: %+v", eval.Results[0].Violations)
	}
	if eval.Blockers != 0 {
		t.Fatalf("blockers = %d, want 0", eval.Blockers)
	}
	if !eval.Results[0].Passed {
		t.Fatalf("policy result passed = false, violations: %+v", eval.Results[0].Violations)
	}
}

func requireBlocked(t *testing.T, eval *domain.PolicyEvaluation, rule domain.PolicyRuleType, msgContains string) {
	t.Helper()
	if eval.Allowed {
		t.Fatal("expected policy to block")
	}
	if eval.Blockers != 1 {
		t.Fatalf("blockers = %d, want 1", eval.Blockers)
	}
	violations := eval.Results[0].Violations
	if len(violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(violations))
	}
	if violations[0].Rule != rule {
		t.Fatalf("violation rule = %q, want %q", violations[0].Rule, rule)
	}
	if msgContains != "" && !strings.Contains(violations[0].Message, msgContains) {
		t.Fatalf("violation message = %q, want containing %q", violations[0].Message, msgContains)
	}
}

func testSBOMAttestation(subjectHash, generatorID string, ntia *domain.NTIACompliance) *domain.SBOMAttestation {
	att := &domain.SBOMAttestation{
		Type:          "https://in-toto.io/Statement/v1",
		PredicateType: domain.AttestationTypeSPDX,
		Predicate: domain.SBOMPredicate{
			Format:    domain.SBOMFormatSPDX,
			Generator: domain.SBOMGenerator{ID: generatorID},
			NTIA:      ntia,
		},
	}
	if subjectHash != "" {
		att.Subject = []domain.AttestationSubject{{
			Name:   "artifact",
			Digest: map[string]string{"sha256": subjectHash},
		}}
	}
	return att
}

func compliantNTIA() *domain.NTIACompliance {
	return &domain.NTIACompliance{
		HasSupplierName:     true,
		HasComponentName:    true,
		HasComponentVersion: true,
		HasUniqueID:         true,
		HasRelationship:     true,
		HasAuthor:           true,
		HasTimestamp:        true,
		IsCompliant:         true,
	}
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

func TestPolicyService_Evaluate_RequireSignature_DiscoveredOnlyBlocks(t *testing.T) {
	svc, policyRepo, sigRepo, _ := newTestPolicyService()
	sigRepo.sigs = []domain.ArtifactSignature{{
		ArtifactID:         uuid.New(),
		SignatureType:      domain.SignatureCosign,
		SignatureRef:       "sha256:cosign-referrer",
		VerificationStatus: domain.SignatureStatusDiscovered,
	}}

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
		t.Error("expected blocked when only discovered signatures exist")
	}
	if eval.Blockers != 1 {
		t.Errorf("blockers = %d, want 1", eval.Blockers)
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

func TestPolicyService_Evaluate_UsesLatestSecurityCountsBeforeSBOMFallback(t *testing.T) {
	artifactID := uuid.New()
	svc, policyRepo, _, sbomRepo := newTestPolicyService()
	sbomRepo.sbom = &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: artifactID, HighCount: 0}
	target, err := domain.NewSBOMSecurityTarget(domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String(), Digest: "sha256:artifact"}, domain.SBOMFormatSPDX, strings.Repeat("a", 64), "sbom:ref:test")
	if err != nil {
		t.Fatal(err)
	}
	target.ID = uuid.New()
	securityRepo := newMemorySecurityRepo(target, nil)
	securityRepo.latest[target.TargetKeyHash] = &domain.SecurityTargetLatest{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, RunID: uuid.New(), Status: domain.SecurityScanCompleted, FindingCount: 2, SeverityCounts: domain.SecuritySeverityCounts{High: 2}, ScannedAt: time.Now().UTC()}
	svc.security = securityRepo
	policyRepo.Create(context.Background(), &domain.DeploymentPolicy{Name: "max-high", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleMaxHighVulns, Params: map[string]any{"max": 0}}}})

	eval, err := svc.Evaluate(context.Background(), artifactID, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	requireBlocked(t, eval, domain.RuleMaxHighVulns, "latest Security OSV scan")
}

func TestPolicyService_Evaluate_SecurityOSVScanStates(t *testing.T) {
	t.Run("no scan blocks by default", func(t *testing.T) {
		svc, policyRepo, _, _ := newTestPolicyService()
		svc.security = newMemorySecurityRepo(domain.SecurityTarget{}, nil)
		policyRepo.Create(context.Background(), &domain.DeploymentPolicy{Name: "security", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan}}})
		eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		requireBlocked(t, eval, domain.RuleSecurityOSVScan, "no completed")
	})
	t.Run("no scan can pass explicitly", func(t *testing.T) {
		svc, policyRepo, _, _ := newTestPolicyService()
		svc.security = newMemorySecurityRepo(domain.SecurityTarget{}, nil)
		policyRepo.Create(context.Background(), &domain.DeploymentPolicy{Name: "security", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"no_scan": "pass"}}}})
		eval, err := svc.Evaluate(context.Background(), uuid.New(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		requireAllowed(t, eval)
	})
	t.Run("stale scan warns when policy enforcement warns", func(t *testing.T) {
		artifactID := uuid.New()
		svc, policyRepo, _, _ := newTestPolicyService()
		target, err := domain.NewSBOMSecurityTarget(domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String(), Digest: "sha256:artifact"}, domain.SBOMFormatSPDX, strings.Repeat("a", 64), "sbom:ref:test")
		if err != nil {
			t.Fatal(err)
		}
		target.ID = uuid.New()
		securityRepo := newMemorySecurityRepo(target, nil)
		securityRepo.latest[target.TargetKeyHash] = &domain.SecurityTargetLatest{TargetID: target.ID, TargetKeyHash: target.TargetKeyHash, RunID: uuid.New(), Status: domain.SecurityScanCompleted, ScannedAt: time.Now().UTC().Add(-2 * time.Hour)}
		svc.security = securityRepo
		policyRepo.Create(context.Background(), &domain.DeploymentPolicy{Name: "security", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"freshness_seconds": 60, "stale": "warn"}}}})
		eval, err := svc.Evaluate(context.Background(), artifactID, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if !eval.Allowed || eval.Warnings != 1 {
			t.Fatalf("expected warn-only stale scan, got %+v", eval)
		}
	})
}

func TestPolicyService_CreatePolicyValidatesAndDerivesSecuritySchedules(t *testing.T) {
	svc, _, _, _ := newTestPolicyService()
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	if err != nil {
		t.Fatal(err)
	}
	target.ID = uuid.New()
	securityRepo := newMemorySecurityRepo(target, nil)
	svc.security = securityRepo
	policy := &domain.DeploymentPolicy{Name: "security", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"interval_seconds": 3600, "target_key_hash": target.TargetKeyHash}}}}
	if err := svc.CreatePolicy(context.Background(), policy); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if len(securityRepo.schedules) != 1 {
		t.Fatalf("expected one derived schedule, got %d", len(securityRepo.schedules))
	}
	var scheduleID uuid.UUID
	var preservedDue time.Time
	for id, schedule := range securityRepo.schedules {
		scheduleID = id
		preservedDue = time.Now().UTC().Add(6 * time.Hour)
		schedule.NextDueAt = preservedDue
	}
	if err := svc.DeriveSecurityScanSchedules(context.Background()); err != nil {
		t.Fatalf("derive schedules: %v", err)
	}
	if !securityRepo.schedules[scheduleID].NextDueAt.Equal(preservedDue) {
		t.Fatalf("schedule derivation reset next_due_at: got %s want %s", securityRepo.schedules[scheduleID].NextDueAt, preservedDue)
	}
	policy.Rules[0].Params["enabled"] = false
	if err := svc.UpdatePolicy(context.Background(), policy); err != nil {
		t.Fatalf("disable security policy rule: %v", err)
	}
	if securityRepo.schedules[scheduleID].Enabled {
		t.Fatal("disabled security_osv_scan rule left schedule enabled")
	}
	bad := &domain.DeploymentPolicy{Name: "bad", Rules: []domain.PolicyRule{{Type: domain.RuleSecurityOSVScan, Params: map[string]any{"stale": "maybe"}}}}
	if err := svc.CreatePolicy(context.Background(), bad); err == nil {
		t.Fatal("expected invalid stale behavior to be rejected")
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

func TestPolicyService_Evaluate_SBOMSubjectDigestMatch(t *testing.T) {
	t.Run("passes when subject digest matches artifact digest", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMSubjectDigestMatch,
			Params: map[string]any{"artifact_digest": "sha256:abc123"},
		})

		requireAllowed(t, eval)
	})

	t.Run("blocks when subject digest differs", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("def456", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMSubjectDigestMatch,
			Params: map[string]any{"artifact_digest": "sha256:abc123"},
		})

		requireBlocked(t, eval, domain.RuleSBOMSubjectDigestMatch, "does not match")
	})

	t.Run("blocks when artifact digest parameter is missing", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMSubjectDigestMatch})

		requireBlocked(t, eval, domain.RuleSBOMSubjectDigestMatch, "artifact_digest parameter required")
	})

	t.Run("blocks when expected digest format is invalid", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMSubjectDigestMatch,
			Params: map[string]any{"artifact_digest": "not-a-digest"},
		})

		requireBlocked(t, eval, domain.RuleSBOMSubjectDigestMatch, "does not match")
	})

	t.Run("blocks when attestation provider is missing", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMSubjectDigestMatch,
			Params: map[string]any{"artifact_digest": "sha256:abc123"},
		})

		requireBlocked(t, eval, domain.RuleSBOMSubjectDigestMatch, "attestation provider not configured")
	})

	t.Run("blocks when provider cannot load attestation", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{attErr: errors.New("relay unavailable")}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMSubjectDigestMatch,
			Params: map[string]any{"artifact_digest": "sha256:abc123"},
		})

		requireBlocked(t, eval, domain.RuleSBOMSubjectDigestMatch, "failed to get attestation")
	})
}

func TestPolicyService_Evaluate_SBOMParseability(t *testing.T) {
	cases := []struct {
		name        string
		sbomData    []byte
		dataErr     error
		wantAllowed bool
		wantMessage string
	}{
		{
			name:        "passes for valid SPDX JSON",
			sbomData:    []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0"}`),
			wantAllowed: true,
		},
		{
			name:        "passes for valid CycloneDX JSON",
			sbomData:    []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5"}`),
			wantAllowed: true,
		},
		{
			name:        "blocks empty SBOM data",
			sbomData:    nil,
			wantMessage: "empty SBOM data",
		},
		{
			name:        "blocks invalid JSON",
			sbomData:    []byte(`not json`),
			wantMessage: "invalid JSON",
		},
		{
			name:        "blocks unknown SBOM format",
			sbomData:    []byte(`{"name":"unknown"}`),
			wantMessage: "unknown SBOM format",
		},
		{
			name:        "blocks provider errors",
			dataErr:     errors.New("blob missing"),
			wantMessage: "failed to get SBOM data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockSBOMAttestationProvider{sbomData: tc.sbomData, dataErr: tc.dataErr}
			svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

			eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMParseability})

			if tc.wantAllowed {
				requireAllowed(t, eval)
			} else {
				requireBlocked(t, eval, domain.RuleSBOMParseability, tc.wantMessage)
			}
		})
	}

	t.Run("blocks when attestation provider is missing", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMParseability})

		requireBlocked(t, eval, domain.RuleSBOMParseability, "attestation provider not configured")
	})
}

func TestPolicyService_Evaluate_SBOMNTIAMinFields(t *testing.T) {
	t.Run("passes when NTIA compliance data is complete", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMNTIAMinFields})

		requireAllowed(t, eval)
	})

	t.Run("blocks when NTIA data is missing", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", nil)}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMNTIAMinFields})

		requireBlocked(t, eval, domain.RuleSBOMNTIAMinFields, "missing NTIA compliance data")
	})

	t.Run("blocks and reports missing NTIA fields", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", &domain.NTIACompliance{
			HasComponentName: true,
			HasUniqueID:      true,
			IsCompliant:      false,
		})}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMNTIAMinFields})

		requireBlocked(t, eval, domain.RuleSBOMNTIAMinFields, "supplier_name")
		if !strings.Contains(eval.Results[0].Violations[0].Message, "component_version") {
			t.Fatalf("violation message = %q, want component_version", eval.Results[0].Violations[0].Message)
		}
	})

	t.Run("blocks when attestation provider is missing", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMNTIAMinFields})

		requireBlocked(t, eval, domain.RuleSBOMNTIAMinFields, "attestation provider not configured")
	})

	t.Run("blocks provider errors", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{attErr: errors.New("attestation missing")}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMNTIAMinFields})

		requireBlocked(t, eval, domain.RuleSBOMNTIAMinFields, "failed to get attestation")
	})
}

func TestPolicyService_Evaluate_SBOMTrustedGenerator(t *testing.T) {
	t.Run("passes when generator is trusted by rule params", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "syft", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMTrustedGenerator,
			Params: map[string]any{"trusted_generators": []string{"syft", "trivy"}},
		})

		requireAllowed(t, eval)
	})

	t.Run("passes when generator is trusted by service config", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "trivy", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(
			WithAttestationProvider(provider),
			WithTrustedGenerators([]string{"trivy"}),
		)

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMTrustedGenerator})

		requireAllowed(t, eval)
	})

	t.Run("passes with JSON decoded trusted generator params", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "cdxgen", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMTrustedGenerator,
			Params: map[string]any{"trusted_generators": []interface{}{"cdxgen"}},
		})

		requireAllowed(t, eval)
	})

	t.Run("blocks unidentified generator", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMTrustedGenerator})

		requireBlocked(t, eval, domain.RuleSBOMTrustedGenerator, "not identified")
	})

	t.Run("blocks untrusted generator", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{att: testSBOMAttestation("abc123", "unknown", compliantNTIA())}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMTrustedGenerator,
			Params: map[string]any{"trusted_generators": []string{"syft"}},
		})

		requireBlocked(t, eval, domain.RuleSBOMTrustedGenerator, "not in trusted list")
	})

	t.Run("blocks when attestation provider is missing", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMTrustedGenerator})

		requireBlocked(t, eval, domain.RuleSBOMTrustedGenerator, "attestation provider not configured")
	})

	t.Run("blocks provider errors", func(t *testing.T) {
		provider := &mockSBOMAttestationProvider{attErr: errors.New("attestation unavailable")}
		svc, policyRepo, _, sbomRepo := newTestPolicyServiceWithOptions(WithAttestationProvider(provider))

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMTrustedGenerator})

		requireBlocked(t, eval, domain.RuleSBOMTrustedGenerator, "failed to get attestation")
	})
}

func TestPolicyService_Evaluate_SBOMFormat(t *testing.T) {
	t.Run("passes when no format restriction is configured", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{Type: domain.RuleSBOMFormat})

		requireAllowed(t, eval)
	})

	t.Run("passes when SBOM format is allowed", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()
		sbomRepo.sbom = &domain.ArtifactSBOM{ID: uuid.New(), Format: domain.SBOMFormatSPDX}

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMFormat,
			Params: map[string]any{"formats": []string{"spdx", "cyclonedx"}},
		})

		requireAllowed(t, eval)
	})

	t.Run("passes with JSON decoded format params", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()
		sbomRepo.sbom = &domain.ArtifactSBOM{ID: uuid.New(), Format: domain.SBOMFormatCycloneDX}

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMFormat,
			Params: map[string]any{"formats": []interface{}{"cyclonedx"}},
		})

		requireAllowed(t, eval)
	})

	t.Run("blocks when required SBOM is missing", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMFormat,
			Params: map[string]any{"formats": []string{"spdx"}},
		})

		requireBlocked(t, eval, domain.RuleSBOMFormat, "failed to get SBOM")
	})

	t.Run("blocks when SBOM format is not allowed", func(t *testing.T) {
		svc, policyRepo, _, sbomRepo := newTestPolicyService()
		sbomRepo.sbom = &domain.ArtifactSBOM{ID: uuid.New(), Format: domain.SBOMFormatCycloneDX}

		eval := evaluateBlockingRule(t, svc, policyRepo, sbomRepo, domain.PolicyRule{
			Type:   domain.RuleSBOMFormat,
			Params: map[string]any{"formats": []string{"spdx"}},
		})

		requireBlocked(t, eval, domain.RuleSBOMFormat, "not in allowed formats")
	})
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
