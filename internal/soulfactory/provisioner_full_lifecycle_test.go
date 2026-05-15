package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
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

func TestLifecycleHandlerActionsPropagateBahiaAndSignerSideEffects(t *testing.T) {
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
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}}, fakeGenerator{}, signer, slog.Default())
	_ = NewFullProvisioner(reactor, FullProvisionerConfig{}, integration)

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, Status: domain.SoulStatusActive, NostrPubkey: signer.pubkey, CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID
	if _, err := integration.CreateInitialDeployment(t.Context(), soul, serviceID); err != nil {
		t.Fatalf("CreateInitialDeployment() error = %v", err)
	}
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }
	handler := reactor.lifecycle()

	if err := handler.HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "suspend-side-effects", nostr.Tags{{"soul", "31951:factory:scout"}, {"action", string(domain.SoulActionSuspend)}, {"reason", "maintenance"}}, "")); err != nil {
		t.Fatalf("HandleAction(suspend) error = %v", err)
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
	if err := handler.HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "resume-side-effects", nostr.Tags{{"soul", "scout"}, {"action", string(domain.SoulActionResume)}}, "")); err != nil {
		t.Fatalf("HandleAction(resume) error = %v", err)
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

	if err := handler.HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "redeploy-side-effects", nostr.Tags{{"soul", "scout"}, {"action", string(domain.SoulActionRedeploy)}}, "")); err != nil {
		t.Fatalf("HandleAction(redeploy) error = %v", err)
	}
	if soul.DeployStatus != "deploying" {
		t.Fatalf("soul deploy status after redeploy = %q, want deploying", soul.DeployStatus)
	}
	if len(intents.intents) != 3 {
		t.Fatalf("intent count after redeploy = %d, want 3", len(intents.intents))
	}

	if err := handler.HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "revoke-side-effects", nostr.Tags{{"soul", "scout"}, {"action", string(domain.SoulActionRevoke)}, {"reason", "retired"}}, "")); err != nil {
		t.Fatalf("HandleAction(revoke) error = %v", err)
	}
	if soul.Status != domain.SoulStatusRevoked || soul.DeployStatus != "stopped" {
		t.Fatalf("soul after revoke = %+v", soul)
	}
	if len(signer.revoked) != 1 || signer.revoked[0] != soul.NostrPubkey {
		t.Fatalf("revoke signer calls = %+v", signer.revoked)
	}
}

func TestLifecycleHandlerResumeRequiresSuspendedSoul(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}}, fakeGenerator{}, signer, slog.Default())
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) {
		return &domain.AgentSoul{AgentID: "scout", Status: domain.SoulStatusActive}, nil
	}

	err := reactor.lifecycle().HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "resume-active", nostr.Tags{{"soul", "scout"}, {"action", string(domain.SoulActionResume)}}, ""))
	if err == nil || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("HandleAction(resume) error = %v, want cannot resume", err)
	}
}

func TestLifecycleHandlerReturnsLookupErrors(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}}, fakeGenerator{}, signer, slog.Default())
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return nil, errors.New("boom") }

	if err := reactor.lifecycle().HandleAction(t.Context(), buildActionEvent(t, signer.fakeSigner, "lookup-error", nostr.Tags{{"soul", "scout"}, {"action", string(domain.SoulActionSuspend)}}, "")); err == nil || !strings.Contains(err.Error(), "lookup soul") {
		t.Fatalf("HandleAction(suspend) error = %v, want lookup soul failure", err)
	}
}
