package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
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
	Ping(context.Context) error
	IsConnected() bool
	WaitUntilConnected(context.Context) error
	WaitUntilDisconnected(context.Context) error
	ConfiguredPublicKey() (string, error)
	GetPublicKey(context.Context) (string, error)
	Close() error
}

type soulFactoryRuntime struct {
	reactor    *soulfactory.Reactor
	runner     BackgroundRunner
	connection *signetAdapter.ConnectionManager
	close      func() error
}

var (
	newSoulFactorySignetClient = func(cfg signetAdapter.Config, logger *slog.Logger) (soulFactorySignerClient, error) {
		return signetAdapter.NewClient(cfg, logger)
	}
	newSoulFactoryRuntimeAdapter = func(cfg soulfactory.RuntimeAdapterConfig) (soulfactory.RuntimeAdapter, error) {
		return soulfactory.NewRuntimeAdapter(cfg)
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
	controllerPubkey, err := resolveSoulFactoryControllerPubkey(ctx, sf.SoulFactoryPubkey, signer)
	if err != nil {
		_ = closeSigner()
		return nil, err
	}

	runtimeAdapters, err := buildSoulFactoryRuntimeAdapters(sf.AgentRuntimes, sf.RuntimePubkeys, controllerPubkey, signer, allRelays, slogLogger)
	if err != nil {
		_ = closeSigner()
		return nil, err
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
	concordCommunities, err := loadSoulFactoryConcordCommunities(sf.ConcordCommunities)
	if err != nil {
		_ = closeSigner()
		return nil, err
	}
	var signetEnrollment soulfactory.OpenClawSignetEnrollment
	if sf.OpenClawSignetEnabled {
		signetctl, err := soulfactory.NewContainerSignetctl(soulfactory.SignetctlConfig{
			Container: sf.OpenClawSignetContainer, ConfigPath: sf.OpenClawSignetConfigPath,
			ProvisionerCredentialFile: sf.OpenClawSignetProvisionerFile,
			CredentialOwnerUID:        os.Geteuid(),
		})
		if err != nil {
			_ = closeSigner()
			return nil, fmt.Errorf("configure containerized signetctl: %w", err)
		}
		signetEnrollment, err = soulfactory.NewOpenClawSignetEnrollmentManager(soulfactory.OpenClawSignetEnrollmentConfig{
			StateDir: sf.OpenClawSignetStateDir, ClientKeyDir: sf.OpenClawSignetClientKeyDir,
			FileOwnerUID: os.Geteuid(), PolicyAdmin: signetctl,
			Verifier: soulfactory.NIP46ConnectivityVerifier{},
		})
		if err != nil {
			_ = closeSigner()
			return nil, fmt.Errorf("configure OpenClaw Signet enrollment: %w", err)
		}
	}

	reactor := soulfactory.NewReactor(soulfactory.Config{
		Relays:             sf.Relays,
		AdditionalRelays:   sf.AdditionalRelays,
		SoulFactoryPubkey:  controllerPubkey,
		AuthorizedPubkeys:  sf.AuthorizedPubkeys,
		FleetConfigEnabled: true,
		SignetBunkerURI:    sf.SignetBunkerURI,
		BlossomURL:         firstConfiguredBlossomServer(cfg.Blossom),
		QdrantURL:          cfg.Qdrant.URL,
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
		AgentMemory:            soulFactoryAgentMemoryConfig(sf),
		NIP05Relays:            cfg.SoulFactory.NIP05Relays,
		NIP29Groups:            soulFactoryNIP29Groups(cfg.SoulFactory.NIP29Groups),
		CommunikeysCommunities: soulFactoryCommunikeysCommunities(cfg.SoulFactory.CommunikeysCommunities),
		ConcordCommunities:     concordCommunities,
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
		RuntimeAdapters:         runtimeAdapters,
		SignetEnrollment:        signetEnrollment,
		SignetProvisionerPubkey: sf.OpenClawSignetProvisionerPubkey,
	}, nil)
	if err := reactor.InstallProvisioningEngine(provisioner); err != nil {
		_ = closeSigner()
		return nil, err
	}

	connection := signetAdapter.NewConnectionManager(signer, signetAdapter.ConnectionManagerConfig{
		Name: "soulfactory", AttemptTimeout: sf.StartupTimeout, Logger: slogLogger,
	})
	logger.Info("SoulFactory runtime reactor configured; Signet connection will start asynchronously", zap.Strings("relays", sf.Relays), zap.Strings("additional_relays", sf.AdditionalRelays), zap.Strings("agent_runtimes", sf.AgentRuntimes), zap.String("controller_pubkey", controllerPubkey))
	return &soulFactoryRuntime{
		reactor:    reactor,
		runner:     &soulFactoryRunner{reactor: reactor, signer: signer, controllerPubkey: controllerPubkey},
		connection: connection,
		close:      closeSigner,
	}, nil
}

// buildSoulFactoryRuntimeAdapters instantiates the generic runtime-control
// adapter for every administratively enabled agent runtime target. Startup
// fails on any invalid or failing target; enabled targets are never silently
// omitted from the registry.
func buildSoulFactoryRuntimeAdapters(targets []string, runtimePubkeys map[string][]string, controllerPubkey string, signer soulFactorySignerClient, relays []string, logger *slog.Logger) (map[domain.RuntimeTarget]soulfactory.RuntimeAdapter, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("soul_factory.agent_runtimes is empty; configuration validation must default or reject it before startup")
	}
	adapters := make(map[domain.RuntimeTarget]soulfactory.RuntimeAdapter, len(targets))
	for _, target := range targets {
		target = strings.ToLower(strings.TrimSpace(target))
		if target == "" {
			return nil, fmt.Errorf("soul_factory.agent_runtimes contains an empty runtime target")
		}
		runtimeTarget := domain.RuntimeTarget(target)
		if _, exists := adapters[runtimeTarget]; exists {
			return nil, fmt.Errorf("soul_factory.agent_runtimes contains duplicate runtime target %q", target)
		}
		adapter, err := newSoulFactoryRuntimeAdapter(soulfactory.RuntimeAdapterConfig{
			Target:                runtimeTarget,
			ControllerPubkey:      controllerPubkey,
			TrustedRuntimePubkeys: append([]string(nil), runtimePubkeys[target]...),
			Signer:                signer,
			Relays:                relays,
			Logger:                logger,
		})
		if err != nil {
			return nil, fmt.Errorf("creating SoulFactory runtime adapter for %q: %w", target, err)
		}
		adapters[runtimeTarget] = adapter
	}
	return adapters, nil
}

func soulFactoryNIP29Groups(groups []config.NIP29Group) []soulfactory.NIP29Group {
	out := make([]soulfactory.NIP29Group, 0, len(groups))
	for _, group := range groups {
		out = append(out, soulfactory.NIP29Group{Relay: group.Relay, ID: group.ID})
	}
	return out
}

func soulFactoryCommunikeysCommunities(communities []config.CommunikeysCommunity) []soulfactory.CommunikeysCommunity {
	out := make([]soulfactory.CommunikeysCommunity, 0, len(communities))
	for _, community := range communities {
		out = append(out, soulfactory.CommunikeysCommunity{Pubkey: community.Pubkey, Sections: append([]string(nil), community.Sections...)})
	}
	return out
}

const concordInviteBundleReadLimit = 65536

func loadSoulFactoryConcordCommunities(communities []config.ConcordCommunity) ([]soulfactory.ConcordCommunity, error) {
	out := make([]soulfactory.ConcordCommunity, 0, len(communities))
	for i, community := range communities {
		var bundle []byte
		switch {
		case community.InviteBundleSealedFile != "":
			// Signet-sealed custody stays on disk as ciphertext: it is opened
			// lazily through the bunker, never read into process config.
			out = append(out, soulfactory.ConcordCommunity{
				CommunityID:      community.CommunityID,
				SealedBundlePath: community.InviteBundleSealedFile,
			})
			continue
		case community.InviteBundleEnv != "":
			value, ok := os.LookupEnv(community.InviteBundleEnv)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("load soul_factory.concord_communities[%d]: environment variable %s is unset or empty", i, community.InviteBundleEnv)
			}
			bundle = []byte(value)
		case community.InviteBundleFile != "":
			file, err := os.Open(community.InviteBundleFile)
			if err != nil {
				return nil, fmt.Errorf("load soul_factory.concord_communities[%d] invite bundle file: %w", i, err)
			}
			bundle, err = io.ReadAll(io.LimitReader(file, concordInviteBundleReadLimit))
			closeErr := file.Close()
			if err != nil {
				return nil, fmt.Errorf("read soul_factory.concord_communities[%d] invite bundle file: %w", i, err)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close soul_factory.concord_communities[%d] invite bundle file: %w", i, closeErr)
			}
		default:
			return nil, fmt.Errorf("load soul_factory.concord_communities[%d]: invite bundle secret source is missing", i)
		}
		if len(bundle) == 0 || len(bundle) >= concordInviteBundleReadLimit {
			return nil, fmt.Errorf("load soul_factory.concord_communities[%d]: invite bundle must contain 1 to 65535 bytes", i)
		}
		out = append(out, soulfactory.ConcordCommunity{
			CommunityID:  community.CommunityID,
			InviteBundle: append([]byte(nil), bundle...),
		})
	}
	return out, nil
}

func resolveSoulFactoryControllerPubkey(ctx context.Context, configured string, signer soulFactorySignerClient) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("SoulFactory Signet signer is not configured")
	}
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured != "" {
		if err := validateSoulFactoryHexPubkey(configured); err != nil {
			return "", fmt.Errorf("SoulFactory configured pubkey is invalid: %w", err)
		}
		return configured, nil
	}
	signerPubkey, err := signer.ConfiguredPublicKey()
	if err != nil {
		return "", fmt.Errorf("resolving SoulFactory Signet pubkey: %w", err)
	}
	signerPubkey = strings.ToLower(strings.TrimSpace(signerPubkey))
	if signerPubkey == "" {
		return "", fmt.Errorf("SoulFactory Signet pubkey is empty")
	}
	if err := validateSoulFactoryHexPubkey(signerPubkey); err != nil {
		return "", fmt.Errorf("SoulFactory Signet pubkey is invalid: %w", err)
	}
	return signerPubkey, nil
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

func soulFactoryAgentMemoryConfig(cfg config.SoulFactoryConfig) agentmemory.Config {
	return agentmemory.Config{TaskIDFile: cfg.AgentMemoryTaskIDFile}
}

func firstConfiguredBlossomServer(cfg config.BlossomConfig) string {
	servers := configuredBlossomServers(cfg)
	if len(servers) == 0 {
		return ""
	}
	return servers[0]
}

type soulFactoryRunner struct {
	reactor          *soulfactory.Reactor
	signer           soulFactorySignerClient
	controllerPubkey string
}

func (r *soulFactoryRunner) Name() string { return "soulfactory" }

func (r *soulFactoryRunner) Run(ctx context.Context) error {
	if r == nil || r.reactor == nil {
		return fmt.Errorf("SoulFactory reactor is not configured")
	}
	if r.signer == nil {
		return fmt.Errorf("SoulFactory signer is not configured")
	}
	// Subscription starts only after signing is available. This avoids consuming
	// a request that cannot publish a durable terminal result. On disconnect the
	// subscription is restarted after recovery, replaying unfinished history.
	for {
		if err := r.signer.WaitUntilConnected(ctx); err != nil {
			return nil
		}
		signerPubkey, err := r.signer.GetPublicKey(ctx)
		if err != nil {
			return fmt.Errorf("resolve connected SoulFactory signer pubkey: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(signerPubkey), strings.TrimSpace(r.controllerPubkey)) {
			return fmt.Errorf("SoulFactory configured pubkey %s does not match Signet pubkey %s", r.controllerPubkey, signerPubkey)
		}
		runCtx, cancel := context.WithCancel(ctx)
		result := make(chan error, 1)
		go func() { result <- r.reactor.Run(runCtx) }()
		disconnected := make(chan error, 1)
		go func() { disconnected <- r.signer.WaitUntilDisconnected(runCtx) }()
		select {
		case err := <-result:
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			return err
		case <-disconnected:
			cancel()
			<-result
		case <-ctx.Done():
			cancel()
			<-result
			return nil
		}
	}
}

var _ soulFactorySignerClient = (*signetAdapter.Client)(nil)
var _ BackgroundRunner = (*soulFactoryRunner)(nil)
var _ soulfactory.RuntimeAdapterTransport = (*soulfactory.SoulFactoryRelayBus)(nil)
