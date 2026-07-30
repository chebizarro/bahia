package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

type cliOperatorClient interface {
	Close()
	DeployServiceRuntimeNostr(context.Context, string, string, *string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	CreateDeploymentIntentNostr(context.Context, string, string, string, string, func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error)
	RollbackDeploymentNostr(context.Context, string, string, string, func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error)
	RestartServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	StopServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	ScanAdoptionNostr(context.Context, client.AdoptionScanRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionPreview, error)
	ImportAdoptionNostr(context.Context, client.AdoptionImportRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionImportResult, error)
	PublishPolicyCreateNostr(context.Context, controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
}

var newCLIOperatorClient = func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
	return client.NewOperatorControlPlaneClient(cfg)
}

var discoverOperatorRelaysForCLI = func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
	return client.DiscoverOperatorRelays(ctx, cfg)
}

type cliNIP46Signer struct {
	client *signet.Client
	pubkey string
}

func (s *cliNIP46Signer) GetPublicKey(_ context.Context) (canonicalnostr.PubKey, error) {
	if s == nil || s.pubkey == "" {
		return canonicalnostr.PubKey{}, fmt.Errorf("NIP-46 signer public key is not configured")
	}
	pubkey, err := canonicalnostr.PubKeyFromHex(s.pubkey)
	if err != nil {
		return canonicalnostr.PubKey{}, fmt.Errorf("parse NIP-46 signer public key: %w", err)
	}
	return pubkey, nil
}

func (s *cliNIP46Signer) SignEvent(ctx context.Context, event *canonicalnostr.Event) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("NIP-46 signer is not configured")
	}
	return s.client.Sign(ctx, event)
}

var newCLINIP46Signer = func(ctx context.Context, bunkerURI, clientKey string) (canonicalnostr.Signer, string, func() error, error) {
	signetClient, err := signet.NewClient(signet.Config{
		BunkerURI:       bunkerURI,
		ClientSecretKey: clientKey,
		RequireReal:     true,
	}, slog.Default())
	if err != nil {
		return nil, "", nil, err
	}
	if err := signetClient.Connect(ctx); err != nil {
		_ = signetClient.Close()
		return nil, "", nil, err
	}
	pubkey, err := signetClient.GetPublicKey(ctx)
	if err != nil {
		_ = signetClient.Close()
		return nil, "", nil, err
	}
	return &cliNIP46Signer{client: signetClient, pubkey: pubkey}, pubkey, signetClient.Close, nil
}

func runDeploymentIntentNostr(cmd *cobra.Command, serviceID, envID, artifactID, requestedBy string) (*client.DeploymentCommandResult, error) {
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	return op.CreateDeploymentIntentNostr(cmd.Context(), serviceID, envID, artifactID, requestedBy, operatorStatusCallback(cmd, "deploy"))
}

func runRollbackIntentNostr(cmd *cobra.Command, serviceID, envID, requestedBy string) (*client.DeploymentCommandResult, error) {
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	return op.RollbackDeploymentNostr(cmd.Context(), serviceID, envID, requestedBy, operatorStatusCallback(cmd, "rollback"))
}

func runRuntimeActionNostrFirst(cmd *cobra.Command, action, serviceID, envID string, artifactID *string, fallback func(context.Context) (*client.RuntimeActionResult, error)) (*client.RuntimeActionResult, error) {
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	defer op.Close()

	statusCallback := operatorStatusCallback(cmd, action)
	var result *client.RuntimeActionResult
	switch action {
	case "deploy":
		result, err = op.DeployServiceRuntimeNostr(cmd.Context(), serviceID, envID, artifactID, statusCallback)
	case "restart":
		result, err = op.RestartServiceRuntimeNostr(cmd.Context(), serviceID, envID, statusCallback)
	case "stop":
		result, err = op.StopServiceRuntimeNostr(cmd.Context(), serviceID, envID, statusCallback)
	default:
		err = &client.ControlPlaneRequestError{Phase: "validate runtime action", RequestAccepted: false, Cause: fmt.Errorf("unsupported runtime action %q", action)}
	}
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	return result, nil
}

func runAdoptionScanNostrFirst(cmd *cobra.Command, req client.AdoptionScanRequest, rawTargetUsed bool, fallback func(context.Context) ([]client.AdoptionPreview, error)) ([]client.AdoptionPreview, error) {
	if rawTargetUsed {
		if !operatorHTTPFallback {
			return nil, rawTargetRequiresFallbackError()
		}
		return fallback(cmd.Context())
	}
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	defer op.Close()
	result, err := op.ScanAdoptionNostr(cmd.Context(), req, operatorStatusCallback(cmd, "adoption scan"))
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	return result, nil
}

func runPolicyCreateNostrFirst(cmd *cobra.Command, req controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	return op.PublishPolicyCreateNostr(cmd.Context(), req)
}

func runAdoptionImportNostrFirst(cmd *cobra.Command, req client.AdoptionImportRequest, rawTargetUsed bool, fallback func(context.Context) ([]client.AdoptionImportResult, error)) ([]client.AdoptionImportResult, error) {
	if rawTargetUsed {
		if !operatorHTTPFallback {
			return nil, rawTargetRequiresFallbackError()
		}
		return fallback(cmd.Context())
	}
	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	defer op.Close()
	result, err := op.ImportAdoptionNostr(cmd.Context(), req, operatorStatusCallback(cmd, "adoption import"))
	if err != nil {
		return fallbackOrError(cmd, err, fallback)
	}
	return result, nil
}

func buildCLIOperatorClient(cmd *cobra.Command) (cliOperatorClient, error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator signer", RequestAccepted: false, Cause: err}
	}
	bunkerURI, clientKey, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator signer", RequestAccepted: false, Cause: err}
	}
	if strings.TrimSpace(key) != "" && bunkerURI != "" {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator signer", RequestAccepted: false, Cause: fmt.Errorf("configure either a NIP-46 bunker signer or a local private key, not both")}
	}
	if strings.TrimSpace(key) == "" && bunkerURI == "" {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator signer", RequestAccepted: false, Cause: fmt.Errorf("provide --nostr-bunker-file with --nostr-client-key-file (or BAHIA_NOSTR_BUNKER_FILE/BAHIA_NOSTR_BUNKER_URI with BAHIA_NOSTR_CLIENT_KEY_FILE/BAHIA_NOSTR_CLIENT_PRIVATE_KEY); local-key inputs remain compatibility-only")}
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator relays", RequestAccepted: false, Cause: err}
	}
	cfg := client.OperatorControlPlaneConfig{Relays: relays, ServicePubkey: resolveOperatorServicePubkey(cmd)}
	if bunkerURI != "" {
		signer, pubkey, closeSigner, signerErr := newCLINIP46Signer(cmd.Context(), bunkerURI, clientKey)
		if signerErr != nil {
			return nil, &client.ControlPlaneRequestError{Phase: "connect operator NIP-46 signer", RequestAccepted: false, Cause: signerErr}
		}
		cfg.Signer = signer
		cfg.Pubkey = pubkey
		cfg.CloseSigner = closeSigner
	} else {
		cfg.PrivateKey = key
	}
	op, err := newCLIOperatorClient(cfg)
	if err != nil {
		if cfg.CloseSigner != nil {
			_ = cfg.CloseSigner()
		}
		return nil, &client.ControlPlaneRequestError{Phase: "configure operator Nostr client", RequestAccepted: false, Cause: err}
	}
	return op, nil
}

func resolveNIP46OperatorInput(cmd *cobra.Command) (string, string, error) {
	bunkerURI, err := resolveSecretInput(cmd, "nostr-bunker-file", nostrBunkerFile, "BAHIA_NOSTR_BUNKER_FILE", "BAHIA_NOSTR_BUNKER_URI", "NIP-46 bunker URI")
	if err != nil {
		return "", "", err
	}
	clientKey, err := resolveSecretInput(cmd, "nostr-client-key-file", nostrClientKeyFile, "BAHIA_NOSTR_CLIENT_KEY_FILE", "BAHIA_NOSTR_CLIENT_PRIVATE_KEY", "NIP-46 client key")
	if err != nil {
		return "", "", err
	}
	if (bunkerURI == "") != (clientKey == "") {
		return "", "", fmt.Errorf("NIP-46 operator signing requires both a bunker URI and a persistent client key")
	}
	if bunkerURI != "" {
		bunkerURI, err = addBunkerRelays(bunkerURI, resolveBunkerRelays(cmd))
		if err != nil {
			return "", "", err
		}
	}
	return bunkerURI, clientKey, nil
}

func resolveBunkerRelays(cmd *cobra.Command) []string {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed("nostr-bunker-relay") {
			return normalizeRelayList(nostrBunkerRelays)
		}
	}
	return normalizeRelayList(strings.Split(os.Getenv("BAHIA_NOSTR_BUNKER_RELAYS"), ","))
}

func addBunkerRelays(bunkerURI string, relays []string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(bunkerURI))
	if err != nil || parsed.Scheme != "bunker" || parsed.Host == "" {
		return "", fmt.Errorf("invalid NIP-46 bunker URI")
	}
	query := parsed.Query()
	for _, relay := range relays {
		query.Add("relay", relay)
	}
	if len(query["relay"]) == 0 {
		return "", fmt.Errorf("NIP-46 bunker URI has no relay; provide --nostr-bunker-relay or BAHIA_NOSTR_BUNKER_RELAYS")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func resolveSecretInput(cmd *cobra.Command, flagName, flagValue, fileEnv, valueEnv, label string) (string, error) {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed(flagName) {
			path := strings.TrimSpace(flagValue)
			if path == "" {
				return "", fmt.Errorf("--%s requires a file path or - for stdin", flagName)
			}
			return readNostrPrivateKeyInput(cmd, path)
		}
	}
	filePath := strings.TrimSpace(os.Getenv(fileEnv))
	value := strings.TrimSpace(os.Getenv(valueEnv))
	if filePath != "" && value != "" {
		return "", fmt.Errorf("specify only one of %s or %s", fileEnv, valueEnv)
	}
	if filePath != "" {
		return readNostrPrivateKeyInput(cmd, filePath)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s environment input must be a single line", label)
	}
	return value, nil
}

func resolveOperatorRelays(cmd *cobra.Command) ([]string, error) {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed("relay") {
			relays := normalizeRelayList(operatorRelays)
			if len(relays) == 0 {
				return nil, fmt.Errorf("--relay was provided but no relay URL was usable")
			}
			return relays, nil
		}
	}

	if envRelays := normalizeRelayList(strings.Split(os.Getenv("BAHIA_NOSTR_RELAYS"), ",")); len(envRelays) > 0 {
		return envRelays, nil
	}

	bootstrapRelays := resolveOperatorBootstrapRelays(cmd)
	trustedPubkeys := resolveOperatorTrustedServicePubkeys(cmd)
	if len(bootstrapRelays) == 0 && len(trustedPubkeys) == 0 {
		return nil, fmt.Errorf("no operator relays configured; pass --relay, set BAHIA_NOSTR_RELAYS, or configure trusted bootstrap discovery with BAHIA_NOSTR_BOOTSTRAP_RELAYS plus BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS or BAHIA_NOSTR_SERVICE_PUBKEY")
	}
	if len(bootstrapRelays) == 0 {
		return nil, fmt.Errorf("operator bootstrap discovery requires at least one bootstrap relay; pass --bootstrap-relay or set BAHIA_NOSTR_BOOTSTRAP_RELAYS")
	}
	if len(trustedPubkeys) == 0 {
		return nil, fmt.Errorf("operator bootstrap discovery requires at least one trusted service pubkey; pass --trusted-service-pubkey or set BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS or BAHIA_NOSTR_SERVICE_PUBKEY")
	}
	ctx := context.Background()
	if cmd != nil {
		ctx = cmd.Context()
	}
	relays, err := discoverOperatorRelaysForCLI(ctx, client.OperatorRelayDiscoveryConfig{BootstrapRelays: bootstrapRelays, TrustedServicePubkeys: trustedPubkeys})
	if err != nil {
		return nil, err
	}
	if len(relays) == 0 {
		return nil, fmt.Errorf("trusted operator bootstrap discovery returned no usable relay URLs")
	}
	return relays, nil
}

func resolveOperatorBootstrapRelays(cmd *cobra.Command) []string {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed("bootstrap-relay") {
			return normalizeRelayList(operatorBootstrapRelays)
		}
	}
	return normalizeRelayList(strings.Split(os.Getenv("BAHIA_NOSTR_BOOTSTRAP_RELAYS"), ","))
}

func resolveOperatorTrustedServicePubkeys(cmd *cobra.Command) []string {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed("trusted-service-pubkey") {
			return normalizeRelayList(operatorTrustedServicePubkeys)
		}
	}
	if envTrusted := normalizeRelayList(strings.Split(os.Getenv("BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS"), ",")); len(envTrusted) > 0 {
		return envTrusted
	}
	if servicePubkey := strings.TrimSpace(resolveOperatorServicePubkey(cmd)); servicePubkey != "" {
		return []string{servicePubkey}
	}
	return nil
}

func resolveOperatorServicePubkey(cmd *cobra.Command) string {
	if cmd != nil && cmd.Root() != nil {
		flags := cmd.Root().PersistentFlags()
		if flags != nil && flags.Changed("service-pubkey") {
			return strings.TrimSpace(operatorServicePubkey)
		}
	}
	if servicePubkey := strings.TrimSpace(os.Getenv("BAHIA_NOSTR_SERVICE_PUBKEY")); servicePubkey != "" {
		return servicePubkey
	}
	return strings.TrimSpace(operatorServicePubkey)
}

func normalizeRelayList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, relay := range strings.Split(value, ",") {
			relay = strings.TrimSpace(relay)
			if relay == "" {
				continue
			}
			key := strings.TrimRight(relay, "/")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, relay)
		}
	}
	return out
}

func fallbackOrError[T any](cmd *cobra.Command, err error, fallback func(context.Context) (T, error)) (T, error) {
	var zero T
	if !operatorHTTPFallback || fallback == nil || !isPreAcceptanceOperatorFailure(err) {
		return zero, err
	}
	if outputFormat == "table" && cmd != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "→ signer-first operator request unavailable before relay acceptance; using explicit HTTP fallback: %v\n", err)
	}
	return fallback(cmd.Context())
}

func isPreAcceptanceOperatorFailure(err error) bool {
	var reqErr *client.ControlPlaneRequestError
	return errors.As(err, &reqErr) && !reqErr.RequestAccepted
}

func operatorStatusCallback(cmd *cobra.Command, label string) func(client.OperatorStatusEvent) {
	if outputFormat != "table" {
		return nil
	}
	return func(status client.OperatorStatusEvent) {
		message := strings.TrimSpace(status.Message)
		if message == "" {
			message = firstNonEmpty(status.Step, status.Status)
		}
		if message == "" {
			message = "status update"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "→ %s: %s\n", label, message)
	}
}

func rawTargetRequiresFallbackError() error {
	return fmt.Errorf("--raw-target is compatibility-only and requires explicit --http-fallback; use --target endpoint refs for signer-first adoption")
}

func getEnvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
