package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

var discoverOperatorRelaysForCLI = func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
	return client.DiscoverOperatorRelays(ctx, cfg)
}

type cliNIP46Signer struct {
	client *signet.Client
	pubkey string
}

// cliEncryptedCapableSigner mirrors the capability pkg/client requires for
// encrypted ContextVM operator requests: NIP-59 sealing needs SignEvent plus
// NIP-44 Encrypt, and the correlated response needs NIP-44 Decrypt. It
// deliberately excludes the deprecated NIP-04 pair, whose absence previously
// rejected this signer and pushed operators toward a raw local nsec.
type cliEncryptedCapableSigner interface {
	canonicalnostr.Signer

	Encrypt(ctx context.Context, plaintext string, recipient canonicalnostr.PubKey) (string, error)
	Decrypt(ctx context.Context, base64ciphertext string, sender canonicalnostr.PubKey) (string, error)
}

// Compile-time guarantee that --encrypted keeps working with a Signet/NIP-46
// bunker signer that holds no local key material.
var _ cliEncryptedCapableSigner = (*cliNIP46Signer)(nil)

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

func (s *cliNIP46Signer) Encrypt(ctx context.Context, plaintext string, recipient canonicalnostr.PubKey) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("NIP-46 signer is not configured")
	}
	return s.client.NIP44Encrypt(ctx, recipient, plaintext)
}

func (s *cliNIP46Signer) Decrypt(ctx context.Context, ciphertext string, sender canonicalnostr.PubKey) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("NIP-46 signer is not configured")
	}
	return s.client.NIP44Decrypt(ctx, sender, ciphertext)
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
