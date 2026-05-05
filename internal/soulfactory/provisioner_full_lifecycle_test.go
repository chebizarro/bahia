package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type trackingSigner struct {
	fakeSigner
	suspended []string
	resumed   []string
	revoked   []string
}

func (s *trackingSigner) SuspendAgent(ctx context.Context, pubkey string) error {
	s.suspended = append(s.suspended, pubkey)
	return nil
}

func (s *trackingSigner) ResumeAgent(ctx context.Context, pubkey string) error {
	s.resumed = append(s.resumed, pubkey)
	return nil
}

func (s *trackingSigner) RevokeAgent(ctx context.Context, pubkey string) error {
	s.revoked = append(s.revoked, pubkey)
	return nil
}

func TestFullProvisionerLifecycleActionsPropagateBahiaAndSignerSideEffects(t *testing.T) {
	registry, _, _, intents, observations, _ := newSoulFactoryRegistryHarness()
	envID := uuid.New()
	if err := registry.CreateEnvironment(t.Context(), &domain.Environment{ID: envID, Name: "agents", Protected: false, DeployStrategy: domain.DeployStrategyReplace, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{AgentEnvironmentID: envID.String()}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}

	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	reactor := NewReactor(Config{}, fakeGenerator{}, signer, slog.Default())
	provisioner := NewFullProvisioner(reactor, FullProvisionerConfig{}, integration)
	reactor.provisioner = provisioner

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, Status: domain.SoulStatusActive, NostrPubkey: signer.pubkey, CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID
	if _, err := integration.CreateInitialDeployment(t.Context(), soul, serviceID); err != nil {
		t.Fatalf("CreateInitialDeployment() error = %v", err)
	}
	provisioner.lookupSoul = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }

	if err := provisioner.SuspendSoul(t.Context(), "31951:factory:scout", "maintenance"); err != nil {
		t.Fatalf("SuspendSoul() error = %v", err)
	}
	if soul.Status != domain.SoulStatusSuspended || soul.DeployStatus != "stopped" {
		t.Fatalf("soul after suspend = %+v", soul)
	}
	if len(signer.suspended) != 1 || signer.suspended[0] != soul.NostrPubkey {
		t.Fatalf("suspend signer calls = %+v", signer.suspended)
	}
	if got := latestObservation(observations, serviceID, envID); got == nil || got.HealthStatus != domain.HealthStatusStopped {
		t.Fatalf("latest suspend observation = %+v", got)
	}

	soul.Status = domain.SoulStatusSuspended
	if err := provisioner.ResumeSoul(t.Context(), "scout"); err != nil {
		t.Fatalf("ResumeSoul() error = %v", err)
	}
	if soul.Status != domain.SoulStatusActive || soul.DeployStatus != "deploying" {
		t.Fatalf("soul after resume = %+v", soul)
	}
	if len(signer.resumed) != 1 || signer.resumed[0] != soul.NostrPubkey {
		t.Fatalf("resume signer calls = %+v", signer.resumed)
	}
	if len(intents.intents) != 2 {
		t.Fatalf("intent count after resume = %d, want 2", len(intents.intents))
	}

	if err := provisioner.RedeploySoul(t.Context(), "scout"); err != nil {
		t.Fatalf("RedeploySoul() error = %v", err)
	}
	if soul.DeployStatus != "deploying" {
		t.Fatalf("soul deploy status after redeploy = %q, want deploying", soul.DeployStatus)
	}
	if len(intents.intents) != 3 {
		t.Fatalf("intent count after redeploy = %d, want 3", len(intents.intents))
	}

	if err := provisioner.RevokeSoul(t.Context(), "scout", "retired"); err != nil {
		t.Fatalf("RevokeSoul() error = %v", err)
	}
	if soul.Status != domain.SoulStatusRevoked || soul.DeployStatus != "stopped" {
		t.Fatalf("soul after revoke = %+v", soul)
	}
	if len(signer.revoked) != 1 || signer.revoked[0] != soul.NostrPubkey {
		t.Fatalf("revoke signer calls = %+v", signer.revoked)
	}
}

func TestFullProvisionerResumeRequiresSuspendedSoul(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	reactor := NewReactor(Config{}, fakeGenerator{}, signer, slog.Default())
	provisioner := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	provisioner.lookupSoul = func(context.Context, string) (*domain.AgentSoul, error) {
		return &domain.AgentSoul{AgentID: "scout", Status: domain.SoulStatusActive}, nil
	}

	err := provisioner.ResumeSoul(t.Context(), "scout")
	if err == nil || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("ResumeSoul() error = %v, want cannot resume", err)
	}
}

func TestFullProvisionerReturnsLookupErrors(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	reactor := NewReactor(Config{}, fakeGenerator{}, signer, slog.Default())
	provisioner := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	provisioner.lookupSoul = func(context.Context, string) (*domain.AgentSoul, error) { return nil, errors.New("boom") }

	if err := provisioner.SuspendSoul(t.Context(), "scout", "maintenance"); err == nil || !strings.Contains(err.Error(), "load soul") {
		t.Fatalf("SuspendSoul() error = %v, want load soul failure", err)
	}
}
