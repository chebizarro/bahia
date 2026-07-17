package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
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
	relays := flag.String("relays", env("SOULFACTORY_RELAYS", ""), "comma-separated OpenClaw runtime/control relays for capability, request, and result events; not ngit repository publication relays")
	privateKeyFile := flag.String("private-key-file", env("OPENCLAW_SOULFACTORY_PRIVATE_KEY_FILE", ""), "file containing the OpenClaw sidecar Nostr private key (env OPENCLAW_SOULFACTORY_PRIVATE_KEY remains supported)")
	trustedControllers := flag.String("trusted-controller-pubkeys", env("SOULFACTORY_CONTROLLER_PUBKEYS", ""), "comma-separated trusted SoulFactory controller pubkeys")
	identifier := flag.String("identifier", env("OPENCLAW_SOULFACTORY_IDENTIFIER", "openclaw-soulfactory-sidecar"), "kind:30317 d-tag identifier")
	command := flag.String("command", env("OPENCLAW_SOULFACTORY_COMMAND", ""), "local OpenClaw control command; receives invocation JSON on stdin and returns outcome JSON on stdout")
	methods := flag.String("methods", env("OPENCLAW_SOULFACTORY_METHODS", ""), "comma-separated SoulFactory runtime-control methods advertised and accepted by the command driver; defaults to the wrapper-supported method set")
	workdir := flag.String("workdir", env("OPENCLAW_SOULFACTORY_WORKDIR", ""), "optional command working directory")
	readRelays := flag.String("read-relays", env("OPENCLAW_SOULFACTORY_READ_RELAYS", ""), "comma-separated read relay hints in capability announcements")
	writeRelays := flag.String("write-relays", env("OPENCLAW_SOULFACTORY_WRITE_RELAYS", ""), "comma-separated write relay hints in capability announcements")
	controlRelays := flag.String("control-relays", env("OPENCLAW_SOULFACTORY_CONTROL_RELAYS", ""), "comma-separated control relay hints in capability announcements")
	storePath := flag.String("idempotency-store", env("OPENCLAW_SOULFACTORY_IDEMPOTENCY_STORE", defaultStorePath()), "durable JSON idempotency store path")
	flag.Var(&args, "arg", "argument to append to the OpenClaw control command; repeatable")
	flag.Parse()

	if len(args) == 0 {
		args = splitCSV(env("OPENCLAW_SOULFACTORY_ARGS", ""))
	}
	if strings.TrimSpace(*command) == "" {
		fatalf("-command or OPENCLAW_SOULFACTORY_COMMAND is required so the owned sidecar can drive a local OpenClaw control surface")
	}
	privateKey, err := loadPrivateKey(*privateKeyFile, env("OPENCLAW_SOULFACTORY_PRIVATE_KEY", ""))
	if err != nil {
		fatalf("load private key: %v", err)
	}
	normalizedKey, err := pkgclient.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		fatalf("invalid private key: %v", err)
	}
	secret, err := nostr.SecretKeyFromHex(normalizedKey)
	if err != nil {
		fatalf("derive runtime pubkey: %v", err)
	}
	runtimePubkey := secret.Public().Hex()
	relayList := splitCSV(*relays)
	if len(relayList) == 0 {
		fatalf("at least one relay is required")
	}
	controllers := splitCSV(*trustedControllers)
	if len(controllers) == 0 {
		fatalf("at least one trusted controller pubkey is required")
	}
	store, err := soulfactory.NewFileOpenClawIdempotencyStore(*storePath)
	if err != nil {
		fatalf("open idempotency store: %v", err)
	}

	sidecar, err := soulfactory.NewOpenClawSidecar(soulfactory.OpenClawSidecarConfig{
		RuntimePubkey:            runtimePubkey,
		Signer:                   cliSigner{privateKey: normalizedKey},
		TrustedControllerPubkeys: controllers,
		Identifier:               *identifier,
		Relays:                   relayList,
		RelayHints: domain.SoulRelayPolicySpec{
			Read:    splitCSV(*readRelays),
			Write:   splitCSV(*writeRelays),
			Control: splitCSV(*controlRelays),
		},
		Driver: soulfactory.OpenClawCommandDriver{
			Command:     *command,
			Args:        args,
			Dir:         strings.TrimSpace(*workdir),
			MethodsList: splitCSV(*methods),
		},
		IdempotencyStore: store,
		Logger:           slog.Default(),
	})
	if err != nil {
		fatalf("configure sidecar: %v", err)
	}
	defer sidecar.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sidecar.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fatalf("sidecar stopped: %v", err)
	}
}

const maxPrivateKeyFileBytes = 4096

func loadPrivateKey(path, environmentValue string) (string, error) {
	path = strings.TrimSpace(path)
	environmentValue = strings.TrimSpace(environmentValue)
	if path != "" && environmentValue != "" {
		return "", fmt.Errorf("configure only one of -private-key-file or OPENCLAW_SOULFACTORY_PRIVATE_KEY")
	}
	if path == "" {
		return environmentValue, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, maxPrivateKeyFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxPrivateKeyFileBytes {
		return "", fmt.Errorf("private key file exceeds %d bytes", maxPrivateKeyFileBytes)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("private key file is empty")
	}
	return key, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func defaultStorePath() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cacheDir) != "" {
		return filepath.Join(cacheDir, "bahia", "openclaw-soulfactory-sidecar-idempotency.json")
	}
	return filepath.Join(os.TempDir(), "bahia-openclaw-soulfactory-sidecar-idempotency.json")
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
