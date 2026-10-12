package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/servicesigner"
)

// keyEnvRefused names the variable whose presence makes a local signer refuse
// to start: the leaked key is read only from a file, never from argv or the
// environment.
const keyEnvRefused = "BAHIA_LEAK_DELETE_PRIVATE_KEY"

// maxSecretFileBytes bounds a bunker URI file, like config.LoadPrivateKey.
const maxSecretFileBytes = 4096

// signerFlags select how the tool signs as the leaked identity: the same
// three methods as nostr.signer, configured from files so no secret appears in
// argv.
type signerFlags struct {
	method        string
	keyFile       string
	bunkerURIFile string
	clientKeyFile string
	nip55lBus     string
	nip55lAppID   string
	timeout       time.Duration
}

func (f *signerFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&f.method, "signer", "", "signer holding the leaked key: local, nip46 or nip55l")
	flags.StringVar(&f.keyFile, "key-file", "", "local: file holding the leaked private key (64 hex)")
	flags.StringVar(&f.bunkerURIFile, "bunker-uri-file", "", "nip46: file holding the bunker:// URI")
	flags.StringVar(&f.clientKeyFile, "client-key-file", "", "nip46: absolute path of the dedicated NIP-46 client key file")
	flags.StringVar(&f.nip55lBus, "nip55l-bus", "", "nip55l: D-Bus address (empty: session bus)")
	flags.StringVar(&f.nip55lAppID, "nip55l-app-id", "", "nip55l: application id shown to the signer (empty: bahia)")
	flags.DurationVar(&f.timeout, "signer-timeout", 0, "bound on connecting to the signer and on each request (0: signer default)")
}

func (f signerFlags) configured() bool { return strings.TrimSpace(f.method) != "" }

// nostrConfig builds the configuration servicesigner.Open reads, pinned to the
// leaked identity: Open fails unless the signer reports exactly oldPubkey.
func (f signerFlags) nostrConfig(oldPubkey nostr.PubKey) (config.NostrConfig, error) {
	method := strings.ToLower(strings.TrimSpace(f.method))
	cfg := config.NostrConfig{
		PublicKey: oldPubkey.Hex(),
		Signer:    config.NostrSignerConfig{Method: method, Timeout: f.timeout},
	}
	unexpected := func(names ...string) error {
		for _, name := range names {
			if name != "" {
				return fmt.Errorf("--signer=%s does not take %s", method, name)
			}
		}
		return nil
	}
	set := func(value, name string) string {
		if value != "" {
			return name
		}
		return ""
	}
	switch method {
	case config.NostrSignerLocal:
		if err := unexpected(set(f.bunkerURIFile, "--bunker-uri-file"), set(f.clientKeyFile, "--client-key-file"), set(f.nip55lBus, "--nip55l-bus"), set(f.nip55lAppID, "--nip55l-app-id")); err != nil {
			return config.NostrConfig{}, err
		}
		key, err := config.LoadPrivateKey(f.keyFile, keyEnvRefused)
		if err != nil {
			return config.NostrConfig{}, fmt.Errorf("--key-file: %w", err)
		}
		cfg.PrivateKey = key
	case config.NostrSignerNIP46:
		if err := unexpected(set(f.keyFile, "--key-file"), set(f.nip55lBus, "--nip55l-bus"), set(f.nip55lAppID, "--nip55l-app-id")); err != nil {
			return config.NostrConfig{}, err
		}
		uri, err := readSecretFile(f.bunkerURIFile)
		if err != nil {
			return config.NostrConfig{}, fmt.Errorf("--bunker-uri-file: %w", err)
		}
		if !filepath.IsAbs(f.clientKeyFile) {
			return config.NostrConfig{}, errors.New("--client-key-file must be an absolute path")
		}
		cfg.Signer.BunkerURI = uri
		cfg.Signer.ClientSecretKeyFile = f.clientKeyFile
	case config.NostrSignerNIP55L:
		if err := unexpected(set(f.keyFile, "--key-file"), set(f.bunkerURIFile, "--bunker-uri-file"), set(f.clientKeyFile, "--client-key-file")); err != nil {
			return config.NostrConfig{}, err
		}
		cfg.Signer.NIP55L = config.NostrSignerNIP55LConfig{BusAddress: f.nip55lBus, AppID: f.nip55lAppID}
	default:
		return config.NostrConfig{}, fmt.Errorf("--signer must be local, nip46 or nip55l, not %q", f.method)
	}
	return cfg, nil
}

// openSigner opens the configured signer through the service signer seam. Open
// verifies that the signer reports oldPubkey before returning it, so nothing
// else runs with a signer for any other identity. The session lives as long
// as ctx.
func (f signerFlags) openSigner(ctx context.Context, oldPubkey nostr.PubKey, admission *nostrout.Admission, stderr io.Writer) (nostr.Keyer, error) {
	cfg, err := f.nostrConfig(oldPubkey)
	if err != nil {
		return nil, err
	}
	keyer, err := servicesigner.Open(ctx, cfg, servicesigner.Options{
		Admission: admission,
		OnAuthURL: func(authURL string) { fmt.Fprintf(stderr, "%s: %s\n", servicesigner.AuthURLMessage, authURL) },
	})
	if err != nil {
		return nil, fmt.Errorf("open signer for the leaked identity %s: %w", oldPubkey.Hex(), err)
	}
	return keyer, nil
}

func closeSigner(keyer nostr.Keyer) {
	if closer, ok := keyer.(io.Closer); ok {
		_ = closer.Close()
	}
}

func readSecretFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("a file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSecretFileBytes {
		return "", fmt.Errorf("file exceeds %d bytes", maxSecretFileBytes)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("file is empty")
	}
	return value, nil
}

// parsePubkey accepts 64-hex or npub.
func parsePubkey(value string) (nostr.PubKey, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "npub1") {
		prefix, decoded, err := nip19.Decode(value)
		if err != nil || prefix != "npub" {
			return nostr.PubKey{}, fmt.Errorf("invalid npub %q", value)
		}
		pubkey, ok := decoded.(nostr.PubKey)
		if !ok {
			return nostr.PubKey{}, fmt.Errorf("invalid npub %q", value)
		}
		return pubkey, nil
	}
	pubkey, err := nostr.PubKeyFromHex(value)
	if err != nil || pubkey == (nostr.PubKey{}) {
		return nostr.PubKey{}, fmt.Errorf("invalid pubkey %q: want 64 hex characters or npub", value)
	}
	return pubkey, nil
}
