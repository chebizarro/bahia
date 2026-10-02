package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	bahiaconfig "github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	dnsagent "github.com/openagentsinc/bahia/internal/dnsagent/agent"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/strutil"
	pkgclient "github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

type config struct {
	ConfigFile        string   `json:"-"`
	PrivateKeyFile    string   `json:"private_key_file"`
	RelayURLs         []string `json:"relay_urls"`
	AuthorizedPubkey  string   `json:"authorized_pubkey"`
	IncludeDir        string   `json:"include_dir"`
	AllowedZones      []string `json:"allowed_zones"`
	FilePrefix        string   `json:"file_prefix"`
	ReloadCommand     string   `json:"reload_command"`
	PreReloadCheck    string   `json:"pre_reload_check"`
	StateFilePath     string   `json:"state_file_path"`
	StorePath         string   `json:"store_path"`
	RequireEncryption bool     `json:"require_encryption"`
	HealthAddr        string   `json:"health_addr"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := loadConfig(args)
	if err != nil {
		return err
	}
	privateKey, err := bahiaconfig.LoadPrivateKey(cfg.PrivateKeyFile, "BAHIA_DNS_AGENT_PRIVATE_KEY")
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}
	normalizedKey, err := pkgclient.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	reload := engine.ReloadConfig{ExplicitCommand: cfg.ReloadCommand}
	if cfg.PreReloadCheck != "" {
		reload.PreReloadCheck = []string{"sh", "-c", cfg.PreReloadCheck}
	}
	eng := engine.New(engine.Config{IncludeDir: cfg.IncludeDir, FilePrefix: cfg.FilePrefix, Reload: reload, Logger: slog.Default()})
	if len(eng.SelectedReloadStrategy()) == 0 {
		return fmt.Errorf("no dnsmasq reload strategy detected; set --reload-command")
	}
	service, err := dnsagent.New(dnsagent.Config{
		Engine:            eng,
		IncludeDir:        cfg.IncludeDir,
		FilePrefix:        cfg.FilePrefix,
		AllowedZones:      cfg.AllowedZones,
		StateFilePath:     cfg.StateFilePath,
		RequireEncryption: cfg.RequireEncryption,
	})
	if err != nil {
		return fmt.Errorf("configure DNS agent: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := localstore.Open(cfg.StorePath)
	if err != nil {
		return fmt.Errorf("open DNS agent event store: %w", err)
	}
	defer store.Close()
	pool := nostradapter.NewRelayPool(cfg.RelayURLs, logger, nostradapter.WithPrivateKey(normalizedKey))
	defer pool.Close()
	transport, err := newRequestTransport(pool, store, normalizedKey, cfg.AuthorizedPubkey, logger)
	if err != nil {
		return err
	}
	service.RegisterHandlers(transport)
	pool.Connect(ctx)

	// Phase 3 D1: zone sync subscriber — subscribe to zone-sync events from
	// the daemon's pubkey and apply them locally. Runs alongside the existing
	// ContextVM transport until the subscription path is proven (C-34).
	zoneSyncEvents := make(chan nostr.Event, 64)
	zoneSyncSub := &nostradapter.StoreBackedSubscriber{
		Pool:           pool,
		Store:          store,
		Logger:         logger,
		ReconcileKinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
	}
	go func() {
		authorPubKey, pkErr := nostrutil.PubKeyFromHex(cfg.AuthorizedPubkey)
		if pkErr != nil {
			logger.Error("invalid daemon pubkey for zone sync", zap.Error(pkErr))
			return
		}
		filter := nostr.Filter{
			Authors: []nostr.PubKey{authorPubKey},
			Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
			Tags:    nostr.TagMap{"t": []string{"dns-zone-sync"}},
		}
		sub, err := zoneSyncSub.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
		if err != nil {
			logger.Error("zone sync subscription failed", zap.Error(err))
			return
		}
		for ev := range sub.Events {
			if ev == nil {
				continue
			}
			select {
			case zoneSyncEvents <- *ev:
			case <-ctx.Done():
				return
			}
		}
		if gaveUp := sub.GaveUp(); gaveUp != nil {
			logger.Error("zone sync subscription gave up", zap.Error(gaveUp))
		}
	}()
	zoneSubscriber := dnsagent.NewZoneSubscriber(service, cfg.AuthorizedPubkey, zoneSyncEvents, logger)
	go func() {
		if err := zoneSubscriber.Run(ctx); err != nil {
			logger.Error("zone subscriber stopped", zap.Error(err))
		}
	}()

	// Phase 3 D1: health status publisher — publish NIP-38 kind 30315 events
	// with NIP-40 expiry so the daemon can read agent health from events
	// instead of ContextVM Health() RPC.
	agentSigner, signerErr := controlplane.NewPrivateKeySigner(normalizedKey)
	if signerErr == nil {
		healthPub := dnsagent.NewHealthPublisher(dnsagent.HealthPublisherConfig{
			Agent:    service,
			Signer:   agentSigner,
			Publish:  func(pubCtx context.Context, ev nostr.Event) error { _, err := pool.Publish(pubCtx, ev); return err },
			Interval: 30 * time.Second,
			Logger:   logger,
		})
		go func() {
			if err := healthPub.Run(ctx); err != nil {
				logger.Error("health publisher stopped", zap.Error(err))
			}
		}()
		logger.Info("DNS agent health publisher started")
	} else {
		logger.Warn("could not create signer for health publisher", zap.Error(signerErr))
	}

	healthServer, healthErr := startHealthServer(ctx, cfg.HealthAddr, service, stop)
	if healthServer != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = healthServer.Shutdown(shutdownCtx)
		}()
	}

	err = supervise(ctx, transport.Run, defaultRestartPolicy(), func(runErr error, wait time.Duration) {
		logger.Warn("ContextVM transport stopped; restarting subscription", zap.Error(runErr), zap.Duration("backoff", wait))
	})
	select {
	case healthServerErr := <-healthErr:
		if healthServerErr != nil {
			return fmt.Errorf("DNS agent health server stopped: %w", healthServerErr)
		}
	default:
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("DNS agent transport stopped: %w", err)
	}
	return nil
}

// newRequestTransport wires the agent's ContextVM request transport to the
// relays through the local event store (bahia-irsry.10.5). Request wraps are
// synced into the store per relay, NIP-59 gift wraps reconciled by id because
// their created_at is backdated, so a restart or reconnect hands the transport
// only wraps it has not seen, and a relay that was down catches up on its own.
// The transport still validates, routes and expires requests (its replay
// window rejects stale ones) and the agent still orders applies by serial.
func newRequestTransport(pool *nostradapter.RelayPool, store *localstore.Store, privateKey, authorizedPubkey string, logger *zap.Logger) (*controlplane.EncryptedRequestTransport, error) {
	signer, err := controlplane.NewPrivateKeySigner(privateKey)
	if err != nil {
		return nil, fmt.Errorf("create DNS agent signer: %w", err)
	}
	responder := controlplane.NewEncryptedResponder(pool, signer, privateKey, logger)
	subscriber := &nostradapter.StoreBackedSubscriber{
		Pool:           pool,
		Store:          store,
		Logger:         logger,
		ReconcileKinds: []nostr.Kind{controlplane.KindContextVMGiftWrap},
	}
	return controlplane.NewEncryptedRequestTransport(subscriber, responder, []string{authorizedPubkey}, logger), nil
}

func startHealthServer(ctx context.Context, addr string, service *dnsagent.Agent, stop context.CancelFunc) (*http.Server, <-chan error) {
	errCh := make(chan error, 1)
	addr = strings.TrimSpace(addr)
	if addr == "" {
		close(errCh)
		return nil, errCh
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.Status())
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		defer close(errCh)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server, errCh
}

func loadConfig(args []string) (config, error) {
	cfg := config{FilePrefix: "bahia-"}
	cfg.ConfigFile = configPath(args)
	if cfg.ConfigFile == "" {
		cfg.ConfigFile = strings.TrimSpace(os.Getenv("BAHIA_DNS_AGENT_CONFIG"))
	}
	if cfg.ConfigFile != "" {
		if err := readConfigFile(cfg.ConfigFile, &cfg); err != nil {
			return cfg, err
		}
	}
	if err := applyEnvironment(&cfg); err != nil {
		return cfg, err
	}

	relays := strings.Join(cfg.RelayURLs, ",")
	zones := strings.Join(cfg.AllowedZones, ",")
	flags := flag.NewFlagSet("bahia-dns-agent", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&cfg.ConfigFile, "config", cfg.ConfigFile, "optional JSON configuration file")
	flags.StringVar(&cfg.PrivateKeyFile, "private-key-file", cfg.PrivateKeyFile, "file containing the DNS agent Nostr private key")
	flags.StringVar(&relays, "relays", relays, "comma-separated Nostr relay URLs")
	flags.StringVar(&cfg.AuthorizedPubkey, "authorized-pubkey", cfg.AuthorizedPubkey, "Bahia service public key authorized to call the agent")
	flags.StringVar(&cfg.IncludeDir, "include-dir", cfg.IncludeDir, "dnsmasq include directory")
	flags.StringVar(&zones, "allowed-zones", zones, "comma-separated DNS zone allowlist")
	flags.StringVar(&cfg.FilePrefix, "file-prefix", cfg.FilePrefix, "prefix for Bahia-owned dnsmasq include files")
	flags.StringVar(&cfg.ReloadCommand, "reload-command", cfg.ReloadCommand, "explicit shell command used to reload dnsmasq")
	flags.StringVar(&cfg.PreReloadCheck, "pre-reload-check", cfg.PreReloadCheck, "optional shell command used to validate dnsmasq before reload")
	flags.StringVar(&cfg.StateFilePath, "state-file", cfg.StateFilePath, "durable DNS agent serial state file")
	flags.StringVar(&cfg.StorePath, "store-path", cfg.StorePath, "local Nostr event store: a rebuildable cache of request events and per-relay sync cursors (default: beside the state file)")
	flags.BoolVar(&cfg.RequireEncryption, "require-encryption", cfg.RequireEncryption, "reject bare kind-25910 requests and require a 1059/21059 envelope")
	flags.StringVar(&cfg.HealthAddr, "health-addr", cfg.HealthAddr, "optional local HTTP address serving /healthz")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	cfg.RelayURLs = strutil.SplitCSV(relays)
	cfg.AllowedZones = strutil.SplitCSV(zones)
	return validateConfig(cfg)
}

func configPath(args []string) string {
	for index, arg := range args {
		if strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-config=") {
			return strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		}
		if (arg == "--config" || arg == "-config") && index+1 < len(args) {
			return strings.TrimSpace(args[index+1])
		}
	}
	return ""
}

func readConfigFile(path string, cfg *config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read DNS agent config %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("decode DNS agent config %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("decode DNS agent config %q: %w", path, err)
	}
	return nil
}

func applyEnvironment(cfg *config) error {
	setStringEnv("BAHIA_DNS_AGENT_PRIVATE_KEY_FILE", &cfg.PrivateKeyFile)
	setStringSliceEnv("BAHIA_DNS_AGENT_RELAYS", &cfg.RelayURLs)
	setStringEnv("BAHIA_DNS_AGENT_AUTHORIZED_PUBKEY", &cfg.AuthorizedPubkey)
	setStringEnv("BAHIA_DNS_AGENT_INCLUDE_DIR", &cfg.IncludeDir)
	setStringSliceEnv("BAHIA_DNS_AGENT_ALLOWED_ZONES", &cfg.AllowedZones)
	setStringEnv("BAHIA_DNS_AGENT_FILE_PREFIX", &cfg.FilePrefix)
	setStringEnv("BAHIA_DNS_AGENT_RELOAD_COMMAND", &cfg.ReloadCommand)
	setStringEnv("BAHIA_DNS_AGENT_PRE_RELOAD_CHECK", &cfg.PreReloadCheck)
	setStringEnv("BAHIA_DNS_AGENT_STATE_FILE", &cfg.StateFilePath)
	setStringEnv("BAHIA_DNS_AGENT_STORE_PATH", &cfg.StorePath)
	setStringEnv("BAHIA_DNS_AGENT_HEALTH_ADDR", &cfg.HealthAddr)
	if value := strings.TrimSpace(os.Getenv("BAHIA_DNS_AGENT_REQUIRE_ENCRYPTION")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse BAHIA_DNS_AGENT_REQUIRE_ENCRYPTION: %w", err)
		}
		cfg.RequireEncryption = parsed
	}
	return nil
}

func validateConfig(cfg config) (config, error) {
	cfg.PrivateKeyFile = strings.TrimSpace(cfg.PrivateKeyFile)
	cfg.IncludeDir = strings.TrimSpace(cfg.IncludeDir)
	cfg.FilePrefix = strings.TrimSpace(cfg.FilePrefix)
	cfg.AuthorizedPubkey = strings.TrimSpace(cfg.AuthorizedPubkey)
	if cfg.PrivateKeyFile == "" {
		return cfg, fmt.Errorf("--private-key-file or BAHIA_DNS_AGENT_PRIVATE_KEY_FILE is required")
	}
	if len(cfg.RelayURLs) == 0 {
		return cfg, fmt.Errorf("at least one relay URL is required")
	}
	for i, relayURL := range cfg.RelayURLs {
		normalized := nostr.NormalizeURL(relayURL)
		if normalized == "" {
			return cfg, fmt.Errorf("invalid relay URL %q", relayURL)
		}
		cfg.RelayURLs[i] = normalized
	}
	pubkey, err := nostr.PubKeyFromHex(cfg.AuthorizedPubkey)
	if err != nil {
		return cfg, fmt.Errorf("invalid authorized Bahia service pubkey: %w", err)
	}
	cfg.AuthorizedPubkey = pubkey.Hex()
	if cfg.IncludeDir == "" {
		return cfg, fmt.Errorf("--include-dir or BAHIA_DNS_AGENT_INCLUDE_DIR is required")
	}
	if len(cfg.AllowedZones) == 0 {
		return cfg, fmt.Errorf("--allowed-zones or BAHIA_DNS_AGENT_ALLOWED_ZONES is required")
	}
	if cfg.FilePrefix == "" {
		cfg.FilePrefix = "bahia-"
	}
	if cfg.StateFilePath == "" {
		cfg.StateFilePath = filepath.Join(cfg.IncludeDir, "."+cfg.FilePrefix+"dns-agent-state.json")
	}
	if cfg.StorePath == "" {
		cfg.StorePath = filepath.Join(filepath.Dir(cfg.StateFilePath), "."+cfg.FilePrefix+"dns-agent-events.bolt")
	}
	return cfg, nil
}

func setStringEnv(name string, target *string) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		*target = value
	}
}

func setStringSliceEnv(name string, target *[]string) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		*target = strutil.SplitCSV(value)
	}
}
