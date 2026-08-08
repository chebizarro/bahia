package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/agentmemory"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/adapters/qdrant"
	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
	"go.uber.org/zap"
)

type soulFactorySignerClient interface {
	soulfactory.Signer
	Connect(context.Context) error
	GetPublicKey(context.Context) (string, error)
	Close() error
}

type soulFactoryRuntime struct {
	reactor *soulfactory.Reactor
	runner  BackgroundRunner
	close   func() error
}

var (
	newSoulFactorySignetClient = func(cfg signetAdapter.Config, logger *slog.Logger) (soulFactorySignerClient, error) {
		return signetAdapter.NewClient(cfg, logger)
	}
	newSoulFactoryOpenClawRuntimeAdapter = func(cfg soulfactory.RuntimeAdapterConfig) (soulfactory.RuntimeAdapter, error) {
		return soulfactory.NewOpenClawRuntimeAdapter(cfg)
	}
	newSoulFactorySoulGenerator = func(cfg llm.Config, logger *slog.Logger) soulfactory.SoulGenerator {
		return llm.NewSoulGenerator(cfg, logger)
	}
)

func buildSoulFactoryRuntime(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*soulFactoryRuntime, error) {
	if cfg == nil || !cfg.SoulFactory.Enabled {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	slogLogger := slog.Default()
	sf := cfg.SoulFactory
	// Soul read models and drafts are browser-facing state. Always include the
	// configured browser relays in the SoulFactory publish set so a valid
	// service-relay projection cannot silently disappear from the gallery.
	sf.AdditionalRelays = mergeRelayLists(sf.AdditionalRelays, cfg.Nostr.BrowserRelayPolicyRelays())
	allRelays := mergeSoulFactoryRelays(sf)

	signer, err := newSoulFactorySignetClient(signetAdapter.Config{
		BunkerURI:       sf.SignetBunkerURI,
		Relays:          allRelays,
		ClientSecretKey: sf.SignetClientSecretKey,
		ConnectTimeout:  sf.StartupTimeout,
		RequireReal:     !cfg.DevMode,
		AllowMock:       cfg.DevMode,
	}, slogLogger)
	if err != nil {
		return nil, fmt.Errorf("creating SoulFactory Signet client: %w", err)
	}
	closeSigner := func() error { return signer.Close() }
	startupCtx, cancelStartup := context.WithTimeout(ctx, sf.StartupTimeout)
	defer cancelStartup()
	// The NIP-46 client retains its Connect context for the lifetime of its
	// response subscription. Its own ConnectTimeout bounds the handshake; pass
	// the application context here so the subscription survives startup.
	if err := signer.Connect(ctx); err != nil {
		_ = closeSigner()
		return nil, fmt.Errorf("connecting SoulFactory Signet client: %w", err)
	}

	controllerPubkey, err := resolveSoulFactoryControllerPubkey(startupCtx, sf.SoulFactoryPubkey, signer)
	if err != nil {
		_ = closeSigner()
		return nil, err
	}

	runtimeAdapter, err := newSoulFactoryOpenClawRuntimeAdapter(soulfactory.RuntimeAdapterConfig{
		Target:           domain.RuntimeTargetOpenClaw,
		ControllerPubkey: controllerPubkey,
		Signer:           signer,
		Relays:           allRelays,
		Logger:           slogLogger,
	})
	if err != nil {
		_ = closeSigner()
		return nil, fmt.Errorf("creating SoulFactory OpenClaw runtime adapter: %w", err)
	}

	generator := newSoulFactorySoulGenerator(llm.Config{
		APIKey:  sf.LLMAPIKey,
		APIURL:  sf.LLMBaseURL,
		Model:   sf.LLMModel,
		Timeout: sf.LLMTimeout,
	}, slogLogger)
	if generator == nil {
		_ = closeSigner()
		return nil, fmt.Errorf("creating SoulFactory generator: generator is not configured")
	}

	reactor := soulfactory.NewReactor(soulfactory.Config{
		Relays:            sf.Relays,
		AdditionalRelays:  sf.AdditionalRelays,
		SoulFactoryPubkey: controllerPubkey,
		AuthorizedPubkeys: sf.AuthorizedPubkeys,
		SignetBunkerURI:   sf.SignetBunkerURI,
		BlossomURL:        firstConfiguredBlossomServer(cfg.Blossom),
		QdrantURL:         cfg.Qdrant.URL,
	}, generator, signer, slogLogger)
	provisioner := soulfactory.NewFullProvisioner(reactor, soulfactory.FullProvisionerConfig{
		Blossom: blossom.Config{
			Servers:       configuredBlossomServers(cfg.Blossom),
			MaxRetries:    cfg.Blossom.MaxRetries,
			RetryDelay:    cfg.Blossom.RetryDelay,
			Timeout:       cfg.Blossom.Timeout,
			PrivateKeyHex: cfg.Blossom.PrivateKey,
		},
		Qdrant: qdrant.Config{
			URL:                       cfg.Qdrant.URL,
			Timeout:                   cfg.Qdrant.Timeout,
			APIKey:                    cfg.Qdrant.APIKey,
			AuthHeaderName:            cfg.Qdrant.AuthHeaderName,
			AllowUnauthenticatedLocal: cfg.Qdrant.AllowUnauthenticatedLocal,
		},
		AgentMemory: agentmemory.Config{},
		NIP05Relays: cfg.SoulFactory.NIP05Relays,
		NIP29Groups: soulFactoryNIP29Groups(cfg.SoulFactory.NIP29Groups),
		Workspace: soulfactory.WorkspaceConfig{
			GiteaURL:              sf.WorkspaceGiteaURL,
			TemplateDir:           sf.WorkspaceTemplateDir,
			OpenClawRelays:        allRelays,
			OpenClawControllers:   []string{controllerPubkey},
			OpenClawModel:         sf.LLMModel,
			OpenClawPrivateKeyRef: sf.WorkspacePrivateKeyRef,
			AgentMemoryMCPURLRef:  sf.WorkspaceAgentMemoryMCPURLRef,
			NgitRelays:            allRelays,
			GatewayPort:           sf.WorkspaceGatewayPort,
		},
		RuntimeAdapters: map[domain.RuntimeTarget]soulfactory.RuntimeAdapter{
			domain.RuntimeTargetOpenClaw: runtimeAdapter,
		},
	}, nil)
	if err := reactor.InstallProvisioningEngine(provisioner); err != nil {
		_ = closeSigner()
		return nil, err
	}

	logger.Info("SoulFactory OpenClaw reactor configured", zap.Strings("relays", sf.Relays), zap.Strings("additional_relays", sf.AdditionalRelays), zap.String("controller_pubkey", controllerPubkey))
	return &soulFactoryRuntime{
		reactor: reactor,
		runner:  &soulFactoryRunner{reactor: reactor},
		close:   closeSigner,
	}, nil
}

func soulFactoryNIP29Groups(groups []config.NIP29Group) []soulfactory.NIP29Group {
	out := make([]soulfactory.NIP29Group, 0, len(groups))
	for _, group := range groups {
		out = append(out, soulfactory.NIP29Group{Relay: group.Relay, ID: group.ID})
	}
	return out
}

func resolveSoulFactoryControllerPubkey(ctx context.Context, configured string, signer soulFactorySignerClient) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("SoulFactory Signet signer is not configured")
	}
	signerPubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving SoulFactory Signet pubkey: %w", err)
	}
	signerPubkey = strings.ToLower(strings.TrimSpace(signerPubkey))
	configured = strings.ToLower(strings.TrimSpace(configured))
	if signerPubkey == "" {
		return "", fmt.Errorf("SoulFactory Signet pubkey is empty")
	}
	if err := validateSoulFactoryHexPubkey(signerPubkey); err != nil {
		return "", fmt.Errorf("SoulFactory Signet pubkey is invalid: %w", err)
	}
	if configured == "" {
		return signerPubkey, nil
	}
	if err := validateSoulFactoryHexPubkey(configured); err != nil {
		return "", fmt.Errorf("SoulFactory configured pubkey is invalid: %w", err)
	}
	if signerPubkey != configured {
		return "", fmt.Errorf("SoulFactory configured pubkey %s does not match Signet pubkey %s", configured, signerPubkey)
	}
	return configured, nil
}

func validateSoulFactoryHexPubkey(pubkey string) error {
	if len(pubkey) != 64 {
		return fmt.Errorf("expected 64 hex characters")
	}
	if _, err := hex.DecodeString(pubkey); err != nil {
		return fmt.Errorf("expected valid hex: %w", err)
	}
	return nil
}

func mergeSoulFactoryRelays(cfg config.SoulFactoryConfig) []string {
	return mergeRelayLists(cfg.Relays, cfg.AdditionalRelays)
}

func mergeRelayLists(relayLists ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, relays := range relayLists {
		for _, relay := range relays {
			relay = strings.TrimSpace(relay)
			if relay == "" {
				continue
			}
			if _, exists := seen[relay]; exists {
				continue
			}
			seen[relay] = struct{}{}
			out = append(out, relay)
		}
	}
	return out
}

func configuredBlossomServers(cfg config.BlossomConfig) []string {
	servers := append([]string{}, cfg.Servers...)
	if len(servers) == 0 && strings.TrimSpace(cfg.URL) != "" {
		servers = append(servers, strings.TrimSpace(cfg.URL))
	}
	return servers
}

func firstConfiguredBlossomServer(cfg config.BlossomConfig) string {
	servers := configuredBlossomServers(cfg)
	if len(servers) == 0 {
		return ""
	}
	return servers[0]
}

type soulFactoryRunner struct {
	reactor *soulfactory.Reactor
}

func (r *soulFactoryRunner) Name() string { return "soulfactory" }

func (r *soulFactoryRunner) Run(ctx context.Context) error {
	if r == nil || r.reactor == nil {
		return fmt.Errorf("SoulFactory reactor is not configured")
	}
	return r.reactor.Run(ctx)
}

var _ soulFactorySignerClient = (*signetAdapter.Client)(nil)
var _ BackgroundRunner = (*soulFactoryRunner)(nil)
var _ soulfactory.RuntimeAdapterTransport = (*soulfactory.SoulFactoryRelayBus)(nil)
