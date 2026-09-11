package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestPolicyService_Evaluate_RequireApprovalIsGateNotViolation(t *testing.T) {
	ctx := context.Background()
	svc, policyRepo, _, _ := newTestPolicyService()
	envID := uuid.New()
	policyRepo.Create(ctx, &domain.DeploymentPolicy{
		Name:          "prod-approval",
		EnvironmentID: &envID,
		Enforcement:   domain.PolicyEnforcementBlock,
		Enabled:       true,
		Rules:         []domain.PolicyRule{{Type: domain.RuleRequireApproval}},
	})

	eval, err := svc.Evaluate(ctx, uuid.New(), envID)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !eval.Allowed || eval.Blockers != 0 || eval.Warnings != 0 {
		t.Fatalf("require_approval must not block or warn: %+v", eval)
	}
	if !eval.RequiresApproval {
		t.Fatalf("evaluation did not report RequiresApproval: %+v", eval)
	}
	if len(eval.Results) != 1 || !eval.Results[0].Passed || !eval.Results[0].RequiresApproval {
		t.Fatalf("policy result = %+v", eval.Results)
	}

	other, err := svc.Evaluate(ctx, uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Evaluate(other env) error = %v", err)
	}
	if other.RequiresApproval {
		t.Fatal("environment-scoped require_approval leaked into another environment")
	}
}

func TestPolicyService_DeploymentApprovalRequired(t *testing.T) {
	ctx := context.Background()
	envID := uuid.New()

	tests := []struct {
		name   string
		policy *domain.DeploymentPolicy
		env    uuid.UUID
		want   bool
	}{
		{name: "no policies", env: envID, want: false},
		{
			name:   "global rule",
			policy: &domain.DeploymentPolicy{Name: "g", Enabled: true, Enforcement: domain.PolicyEnforcementWarn, Rules: []domain.PolicyRule{{Type: domain.RuleRequireApproval}}},
			env:    envID, want: true,
		},
		{
			name:   "environment rule matches",
			policy: &domain.DeploymentPolicy{Name: "e", EnvironmentID: &envID, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleRequireApproval}}},
			env:    envID, want: true,
		},
		{
			name:   "environment rule other env",
			policy: &domain.DeploymentPolicy{Name: "e", EnvironmentID: &envID, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleRequireApproval}}},
			env:    uuid.New(), want: false,
		},
		{
			name:   "disabled policy",
			policy: &domain.DeploymentPolicy{Name: "d", Enabled: false, Rules: []domain.PolicyRule{{Type: domain.RuleRequireApproval}}},
			env:    envID, want: false,
		},
		{
			name:   "unrelated rule",
			policy: &domain.DeploymentPolicy{Name: "s", Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}},
			env:    envID, want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, policyRepo, _, _ := newTestPolicyService()
			if tt.policy != nil {
				policyRepo.Create(ctx, tt.policy)
			}
			got, err := svc.DeploymentApprovalRequired(ctx, uuid.New(), tt.env)
			if err != nil {
				t.Fatalf("DeploymentApprovalRequired() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DeploymentApprovalRequired() = %t, want %t", got, tt.want)
			}
		})
	}

	var nilSvc *PolicyService
	if got, err := nilSvc.DeploymentApprovalRequired(ctx, uuid.New(), envID); err != nil || got {
		t.Fatalf("nil service = %t, %v; want false, nil", got, err)
	}
}

type stubApprovalPolicy struct {
	required bool
	err      error
	calls    int
}

func (s *stubApprovalPolicy) DeploymentApprovalRequired(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	s.calls++
	return s.required, s.err
}

func newApprovalGatedRegistry(t *testing.T, policy DeploymentApprovalPolicy) (*RegistryService, *mockIntentRepo, *mockStateRepo) {
	t.Helper()
	intentRepo, stateRepo := newMockIntentRepo(), newMockStateRepo()
	registry := NewRegistryService(
		newMockServiceRepo(), newMockEnvRepo(), newMockBuildRepo(), newMockArtifactRepo(),
		intentRepo, newMockRunRepo(), newMockObsRepo(), stateRepo,
		echoDigestVerifier{}, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentApprovalPolicy(policy),
	)
	return registry, intentRepo, stateRepo
}

func TestCreateDeploymentIntent_RequireApprovalPolicyGatesUnprotectedEnvironment(t *testing.T) {
	ctx := context.Background()
	policy := &stubApprovalPolicy{required: true}
	registry, _, stateRepo := newApprovalGatedRegistry(t, policy)
	svc, env := seedServiceAndEnv(t, registry)
	if env.Protected {
		t.Fatal("fixture expected unprotected environment")
	}
	artifact := seedArtifact(t, registry, svc, "sha256:policy-approval")

	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindManual,
		// Caller-supplied approval must be ignored when policy demands approval.
		Status: domain.IntentStatusApproved, ApprovalStatus: domain.ApprovalStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("CreateDeploymentIntent() error = %v", err)
	}
	if policy.calls != 1 {
		t.Fatalf("approval policy calls = %d, want 1", policy.calls)
	}
	if intent.Status != domain.IntentStatusPending || intent.ApprovalStatus != domain.ApprovalStatusPending {
		t.Fatalf("policy-gated intent not pending: status=%s approval=%s", intent.Status, intent.ApprovalStatus)
	}
	if state, _ := stateRepo.Get(ctx, svc.ID, env.ID); state != nil {
		t.Fatalf("pending policy-gated intent advanced desired state: %+v", state)
	}

	if err := registry.ApproveDeploymentIntent(ctx, intent.ID); err != nil {
		t.Fatalf("ApproveDeploymentIntent() error = %v", err)
	}
	state, err := stateRepo.Get(ctx, svc.ID, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		t.Fatalf("approved intent did not advance desired state: %+v", state)
	}
}

func TestCreateDeploymentIntent_NoApprovalPolicyAutoApproves(t *testing.T) {
	ctx := context.Background()
	registry, _, _ := newApprovalGatedRegistry(t, &stubApprovalPolicy{required: false})
	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:no-policy-approval")

	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindManual,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("CreateDeploymentIntent() error = %v", err)
	}
	if intent.Status != domain.IntentStatusApproved || intent.ApprovalStatus != domain.ApprovalStatusNotRequired {
		t.Fatalf("ungated intent: status=%s approval=%s", intent.Status, intent.ApprovalStatus)
	}
}

func TestCreateDeploymentIntent_ApprovalPolicyErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	registry, intentRepo, stateRepo := newApprovalGatedRegistry(t, &stubApprovalPolicy{err: errors.New("policy store down")})
	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:policy-error")

	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindManual,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err == nil {
		t.Fatal("CreateDeploymentIntent() succeeded despite approval policy error")
	}
	if got, _ := intentRepo.GetByID(ctx, intent.ID); got != nil {
		t.Fatalf("intent persisted despite approval policy error: %+v", got)
	}
	if state, _ := stateRepo.Get(ctx, svc.ID, env.ID); state != nil {
		t.Fatalf("desired state advanced despite approval policy error: %+v", state)
	}
}

func TestCreateDeploymentIntent_RealPolicyServiceRequireApproval(t *testing.T) {
	ctx := context.Background()
	policySvc, policyRepo, _, _ := newTestPolicyService()
	registry, _, _ := newApprovalGatedRegistry(t, policySvc)
	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:real-policy-approval")
	policyRepo.Create(ctx, &domain.DeploymentPolicy{
		Name:          "env-approval",
		EnvironmentID: &env.ID,
		Enforcement:   domain.PolicyEnforcementBlock,
		Enabled:       true,
		Rules:         []domain.PolicyRule{{Type: domain.RuleRequireApproval}},
	})

	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindManual,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("CreateDeploymentIntent() error = %v", err)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
		t.Fatalf("require_approval policy did not gate intent: status=%s approval=%s", intent.Status, intent.ApprovalStatus)
	}
}
