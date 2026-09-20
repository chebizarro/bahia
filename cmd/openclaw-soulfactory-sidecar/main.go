package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
	"github.com/openagentsinc/bahia/internal/strutil"
	pkgclient "github.com/openagentsinc/bahia/pkg/client"
)

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*f = append(*f, value)
	}
	return nil
}

type cliSigner struct{ privateKey string }

func (s cliSigner) Sign(_ context.Context, event *nostr.Event) error {
	secret, err := nostr.SecretKeyFromHex(s.privateKey)
	if err != nil {
		return err
	}
	return event.Sign(secret)
}

func main() {
	var args repeatedFlag
	relays := flag.String("relays", strutil.Env("SOULFACTORY_RELAYS", ""), "comma-separated OpenClaw runtime/control relays for capability, request, and result events; not ngit repository publication relays")
	privateKeyFile := flag.String("private-key-file", strutil.Env("OPENCLAW_SOULFACTORY_PRIVATE_KEY_FILE", ""), "file containing the OpenClaw sidecar Nostr private key")
	trustedControllers := flag.String("trusted-controller-pubkeys", strutil.Env("SOULFACTORY_CONTROLLER_PUBKEYS", ""), "legacy comma-separated one-time seed used only when persisted controller policy is absent")
	controllerPolicyPath := flag.String("controller-policy-file", strutil.Env("OPENCLAW_SOULFACTORY_CONTROLLER_POLICY_FILE", ""), "persisted SoulFactory controller policy file; defaults beside the idempotency store")
	identifier := flag.String("identifier", strutil.Env("OPENCLAW_SOULFACTORY_IDENTIFIER", "openclaw-soulfactory-sidecar"), "kind:30317 d-tag identifier")
	command := flag.String("command", strutil.Env("OPENCLAW_SOULFACTORY_COMMAND", ""), "local OpenClaw control command; receives invocation JSON on stdin and returns outcome JSON on stdout")
	methods := flag.String("methods", strutil.Env("OPENCLAW_SOULFACTORY_METHODS", ""), "comma-separated SoulFactory runtime-control methods advertised and accepted by the command driver; defaults to the wrapper-supported method set")
	workdir := flag.String("workdir", strutil.Env("OPENCLAW_SOULFACTORY_WORKDIR", ""), "optional command working directory")
	readRelays := flag.String("read-relays", strutil.Env("OPENCLAW_SOULFACTORY_READ_RELAYS", ""), "comma-separated read relay hints in capability announcements")
	writeRelays := flag.String("write-relays", strutil.Env("OPENCLAW_SOULFACTORY_WRITE_RELAYS", ""), "comma-separated write relay hints in capability announcements")
	controlRelays := flag.String("control-relays", strutil.Env("OPENCLAW_SOULFACTORY_CONTROL_RELAYS", ""), "comma-separated control relay hints in capability announcements")
	storePath := flag.String("idempotency-store", strutil.Env("OPENCLAW_SOULFACTORY_IDEMPOTENCY_STORE", defaultStorePath()), "durable JSON idempotency store path")
	healthAddr := flag.String("health-addr", strutil.Env("OPENCLAW_SOULFACTORY_HEALTH_ADDR", "127.0.0.1:8081"), "HTTP address for /health and /ready")
	flag.Var(&args, "arg", "argument to append to the OpenClaw control command; repeatable")
	flag.Parse()

	if len(args) == 0 {
		args = strutil.SplitCSV(strutil.Env("OPENCLAW_SOULFACTORY_ARGS", ""))
	}
	if strings.TrimSpace(*command) == "" {
		fmt.Fprintf(os.Stderr, "-command or OPENCLAW_SOULFACTORY_COMMAND is required so the owned sidecar can drive a local OpenClaw control surface\n")
		os.Exit(1)
	}
	privateKey, err := config.LoadPrivateKey(*privateKeyFile, "OPENCLAW_SOULFACTORY_PRIVATE_KEY")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load private key: %v\n", err)
		os.Exit(1)
	}
	normalizedKey, err := pkgclient.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid private key: %v\n", err)
		os.Exit(1)
	}
	secret, err := nostr.SecretKeyFromHex(normalizedKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive runtime pubkey: %v\n", err)
		os.Exit(1)
	}
	runtimePubkey := secret.Public().Hex()
	relayList := strutil.SplitCSV(*relays)
	if len(relayList) == 0 {
		fmt.Fprintf(os.Stderr, "at least one relay is required\n")
		os.Exit(1)
	}
	if strings.TrimSpace(*controllerPolicyPath) == "" {
		*controllerPolicyPath = filepath.Join(filepath.Dir(*storePath), "openclaw-soulfactory-controller-policy.json")
	}
	controllerPolicy, seeded, err := soulfactory.NewFileOpenClawControllerPolicy(
		*controllerPolicyPath, strutil.SplitCSV(*trustedControllers))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open controller policy: %v\n", err)
		os.Exit(1)
	}
	if seeded {
		slog.Info("seeded persisted SoulFactory controller policy from legacy configuration")
	}
	store, err := soulfactory.NewFileOpenClawIdempotencyStore(*storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open idempotency store: %v\n", err)
		os.Exit(1)
	}

	sidecar, err := soulfactory.NewOpenClawSidecar(soulfactory.OpenClawSidecarConfig{
		RuntimePubkey:    runtimePubkey,
		Signer:           cliSigner{privateKey: normalizedKey},
		ControllerPolicy: controllerPolicy,
		Identifier:       *identifier,
		Relays:           relayList,
		RelayHints: domain.SoulRelayPolicySpec{
			Read:    strutil.SplitCSV(*readRelays),
			Write:   strutil.SplitCSV(*writeRelays),
			Control: strutil.SplitCSV(*controlRelays),
		},
		Driver: soulfactory.OpenClawCommandDriver{
			Command:     *command,
			Args:        args,
			Dir:         strings.TrimSpace(*workdir),
			MethodsList: strutil.SplitCSV(*methods),
		},
		IdempotencyStore: store,
		Logger:           slog.Default(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure sidecar: %v\n", err)
		os.Exit(1)
	}
	defer sidecar.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := sidecar.ReloadControllerPolicy(); err != nil {
					slog.Error("reload SoulFactory controller policy", "error", err)
					continue
				}
				if err := sidecar.PublishCapability(ctx); err != nil {
					slog.Error("publish capability after controller policy reload", "error", err)
				}
			}
		}
	}()
	healthServer := newHealthServer(*healthAddr, sidecar)
	healthErr := make(chan error, 1)
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			healthErr <- err
			stop()
		}
	}()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = healthServer.Shutdown(shutdownCtx)
	runErr := sidecar.Run(ctx)
	select {
	case err := <-healthErr:
		fmt.Fprintf(os.Stderr, "sidecar health server stopped: %v\n", err)
		os.Exit(1)
	default:
	}
	if err := runErr; err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "sidecar stopped: %v\n", err)
		os.Exit(1)
	}
}

type readinessProvider interface {
	Readiness() soulfactory.OpenClawSidecarReadiness
}

func newHealthServer(addr string, sidecar readinessProvider) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"alive": true})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		state := sidecar.Readiness()
		w.Header().Set("Content-Type", "application/json")
		if !state.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(state)
	})
	return &http.Server{Addr: strings.TrimSpace(addr), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

func defaultStorePath() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cacheDir) != "" {
		return filepath.Join(cacheDir, "bahia", "openclaw-soulfactory-sidecar-idempotency.json")
	}
	return filepath.Join(os.TempDir(), "bahia-openclaw-soulfactory-sidecar-idempotency.json")
}
