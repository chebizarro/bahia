package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

type cliOperatorClient interface {
	Close()
	DeployServiceRuntimeNostr(context.Context, string, string, *string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	RestartServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	StopServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error)
	ScanAdoptionNostr(context.Context, client.AdoptionScanRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionPreview, error)
	ImportAdoptionNostr(context.Context, client.AdoptionImportRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionImportResult, error)
}

var newCLIOperatorClient = func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
	return client.NewOperatorControlPlaneClient(cfg)
}

var discoverOperatorRelaysForCLI = func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
	return client.DiscoverOperatorRelays(ctx, cfg)
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
	if strings.TrimSpace(key) == "" {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator signer", RequestAccepted: false, Cause: fmt.Errorf("provide --nsec, --privkey, BAHIA_NOSTR_NSEC, or BAHIA_NOSTR_PRIVATE_KEY for signer-first operator requests")}
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, &client.ControlPlaneRequestError{Phase: "resolve operator relays", RequestAccepted: false, Cause: err}
	}
	op, err := newCLIOperatorClient(client.OperatorControlPlaneConfig{Relays: relays, PrivateKey: key, ServicePubkey: resolveOperatorServicePubkey(cmd)})
	if err != nil {
		return nil, &client.ControlPlaneRequestError{Phase: "configure operator Nostr client", RequestAccepted: false, Cause: err}
	}
	return op, nil
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
