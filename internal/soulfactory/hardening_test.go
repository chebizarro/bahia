package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/adapters/agentmemory"
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
	secret := nostr.Generate()
	return fakeSigner{secret: secret.Hex(), pubkey: secret.Public().Hex()}
}

func (s fakeSigner) Sign(ctx context.Context, event *nostr.Event) error {
	secret, err := nostr.SecretKeyFromHex(s.secret)
	if err != nil {
		return err
	}
	return event.Sign(secret)
}

func (s fakeSigner) ProvisionAgent(ctx context.Context, agentID string, allowedKinds []int) (pubkey, npub, bunkerURI string, err error) {
	pubkeyValue, err := nostr.PubKeyFromHex(s.pubkey)
	if err != nil {
		return "", "", "", err
	}
	npub = nip19.EncodeNpub(pubkeyValue)
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

func TestDefaultReactorProvisioningPublishesOnlyErrorWithoutEngine(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{
		AuthorizedPubkeys: []string{signer.pubkey},
		SoulFactoryPubkey: signer.pubkey,
	}, fakeGenerator{}, signer, slog.Default())
	reactor.relayBus = newEOSEOnlyRelayBus(t)
	capture := attachPublishCapture(reactor)
	request := buildProvisioningEvent(t, signer.pubkey, "no-engine", nostr.Tags{{"agent-id", "agent"}}, `{"brief":"brief"}`)

	reactor.handleProvisioningRequest(t.Context(), request)

	if got := len(capture.eventsByKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("agent soul publication count = %d, want 0", got)
	}
	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("provisioning result count = %d, want 1", len(results))
	}
	if got := findTag(results[0], "status"); got != "error" {
		t.Fatalf("result status = %q, want error", got)
	}
	if got := findTag(results[0], "step"); got != string(domain.StepGenerate) {
		t.Fatalf("result step = %q, want %q", got, domain.StepGenerate)
	}
	if results[0].Content != ErrSoulFactoryUnavailable.Error() {
		t.Fatalf("result content = %q, want %q", results[0].Content, ErrSoulFactoryUnavailable.Error())
	}
}

func TestFullProvisionerSkipsUnconfiguredOptionalSteps(t *testing.T) {
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	reactor.provisioner = full
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("request").Hex(),
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

type failingAgentMemory struct{ seedErr error }

func (*failingAgentMemory) Configured() bool { return true }
func (*failingAgentMemory) RegisterAgent(context.Context, string, string, map[string]interface{}) error {
	return nil
}
func (m *failingAgentMemory) SeedMemory(context.Context, string, []agentmemory.MemoryEntry) error {
	return m.seedErr
}

func TestFullProvisionerFailsMemoryStepWhenSeedingFails(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	full.agentMemory = &failingAgentMemory{seedErr: errors.New("memory store unavailable")}
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("request-memory-failure").Hex(),
		RequesterPubkey: signer.pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}

	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "agent", Name: "Agent", Brief: "brief", Tier: domain.SoulTierStandard}, run)
	if err == nil || !strings.Contains(err.Error(), "seed agent memory") {
		t.Fatalf("Provision() error = %v, want memory seed failure", err)
	}
	if soul != nil {
		t.Fatalf("Provision() soul = %+v, want nil on failed memory step", soul)
	}
	last := run.Steps[len(run.Steps)-1]
	if last.Name != domain.StepMemory || last.Status != domain.StepStatusFailed {
		t.Fatalf("last step = %+v, want failed memory", last)
	}
}

func TestFullProvisionerFailsDeployStepWhenNIP05RegistrationFails(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)

	invalidDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	full := NewFullProvisioner(reactor, FullProvisionerConfig{NIP05: NIP05Config{
		Domain:       "example.test",
		WellKnownDir: invalidDir,
	}}, nil)
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("request-nip05-failure").Hex(),
		RequesterPubkey: signer.pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}

	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "agent", Name: "Agent", Brief: "brief", Tier: domain.SoulTierStandard}, run)
	if err == nil || !strings.Contains(err.Error(), "NIP-05 registration") {
		t.Fatalf("Provision() error = %v, want NIP-05 registration failure", err)
	}
	if soul != nil {
		t.Fatalf("Provision() soul = %+v, want nil on failed deploy step", soul)
	}
	if len(run.Steps) == 0 {
		t.Fatal("Provision() recorded no steps")
	}
	last := run.Steps[len(run.Steps)-1]
	if last.Name != domain.StepDeploy || last.Status != domain.StepStatusFailed {
		t.Fatalf("last step = %+v, want failed deploy", last)
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
