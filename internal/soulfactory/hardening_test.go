package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeGenerator struct{}

func (fakeGenerator) Generate(context.Context, domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	return &domain.SoulGeneratorOutput{
		SoulMD:       "# Soul",
		IdentityMD:   "# Identity",
		AllowedKinds: []int{1, domain.KindSoulAction},
	}, nil
}

type fakeSigner struct {
	secret string
	pubkey string
}

func newFakeSigner(t *testing.T) fakeSigner {
	t.Helper()
	secret := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secret)
	if err != nil {
		t.Fatalf("get public key: %v", err)
	}
	return fakeSigner{secret: secret, pubkey: pubkey}
}

func (s fakeSigner) Sign(ctx context.Context, event *nostr.Event) error {
	return event.Sign(s.secret)
}

func (s fakeSigner) ProvisionAgent(ctx context.Context, agentID string, allowedKinds []int) (pubkey, npub, bunkerURI string, err error) {
	npub, err = nip19.EncodePublicKey(s.pubkey)
	if err != nil {
		return "", "", "", err
	}
	return s.pubkey, npub, "bunker://test", nil
}

func (s fakeSigner) RevokeAgent(ctx context.Context, pubkey string) error  { return nil }
func (s fakeSigner) SuspendAgent(ctx context.Context, pubkey string) error { return nil }
func (s fakeSigner) ResumeAgent(ctx context.Context, pubkey string) error  { return nil }

func TestDefaultReactorProvisioningFailsClosed(t *testing.T) {
	reactor := NewReactor(Config{}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	run := &domain.ProvisioningRun{ID: domain.NewUUID(), Steps: []domain.ProvisioningStepResult{}}
	_, err := reactor.provisioner.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "agent", Brief: "brief"}, run)
	if !errors.Is(err, ErrSoulFactoryUnavailable) {
		t.Fatalf("Provision() error = %v, want ErrSoulFactoryUnavailable", err)
	}
}

func TestLegacyProvisionerCannotProduceSuccess(t *testing.T) {
	reactor := NewReactor(Config{}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	legacy := NewProvisioner(reactor)
	run := &domain.ProvisioningRun{ID: domain.NewUUID(), Steps: []domain.ProvisioningStepResult{}}
	_, err := legacy.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "agent", Brief: "brief"}, run)
	if !errors.Is(err, ErrSoulFactoryUnavailable) {
		t.Fatalf("legacy Provision() error = %v, want ErrSoulFactoryUnavailable", err)
	}
	if len(run.Steps) == 0 || run.Steps[0].Status != domain.StepStatusFailed {
		t.Fatalf("legacy run steps = %+v, want failed step", run.Steps)
	}
}

func TestFullProvisionerSkipsUnconfiguredOptionalSteps(t *testing.T) {
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	reactor.provisioner = full
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       "request",
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "agent", Name: "Agent", Brief: "brief", Tier: domain.SoulTierStandard}, run)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if soul.QdrantCollection != "" || soul.WorkspaceRepoURL != "" || soul.SoulBlobHash != "" {
		t.Fatalf("soul contains fake optional outputs: %+v", soul)
	}
	wantSkipped := map[domain.ProvisioningStep]bool{
		domain.StepAvatar:    true,
		domain.StepQdrant:    true,
		domain.StepMemory:    true,
		domain.StepWorkspace: true,
	}
	for _, step := range run.Steps {
		if wantSkipped[step.Name] && step.Status != domain.StepStatusSkipped {
			t.Fatalf("step %s status = %s, want skipped", step.Name, step.Status)
		}
	}
}

func TestMCPServerReturnsConfigurationErrorInsteadOfMockData(t *testing.T) {
	server := NewMCPServer(NewReactor(Config{}, fakeGenerator{}, newFakeSigner(t), slog.Default()), nil, slog.Default())
	for _, tool := range []string{"soul_factory_list_souls", "soul_factory_list_templates"} {
		result, err := server.CallTool(t.Context(), tool, map[string]interface{}{})
		if err != nil {
			t.Fatalf("CallTool(%s) error = %v", tool, err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("CallTool(%s) = %+v, want MCP error result", tool, result)
		}
		if got := result.Content[0].Text; got == "" || got == "[]" {
			t.Fatalf("CallTool(%s) error text = %q, want explicit configuration failure", tool, got)
		}
	}
}
