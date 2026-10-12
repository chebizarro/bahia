package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/relayguard"
)

// SecretStoreBackendSignet selects Signet's generic secret store.
const SecretStoreBackendSignet = "signet"

const defaultSecretStoreRequestTimeout = 30 * time.Second

// SecretStoreConfig selects the external store that holds secret values
// (docs/architecture/secret-store.md). An empty backend leaves secret values
// in Bahia's legacy store and refuses reference-only secret intents.
type SecretStoreConfig struct {
	// Backend is "" or "signet".
	Backend string                  `koanf:"backend" yaml:"backend" secret:"false"`
	Signet  SecretStoreSignetConfig `koanf:"signet" yaml:"signet"`
}

// SecretStoreSignetConfig addresses Signet's credential management plane.
type SecretStoreSignetConfig struct {
	// Pubkey is Signet's management identity (its [nostr] bunker_pubkey).
	Pubkey string `koanf:"pubkey" yaml:"pubkey" secret:"false"`
	// Relays carry the gift-wrapped requests and Signet's replies. They are
	// this store's own explicit allowlist: relays Signet and the daemon both
	// use, never a well-known public relay. They need not be control-plane
	// relays (nostr.controlled_relays); the store never publishes the
	// control plane.
	Relays []string `koanf:"relays" yaml:"relays" secret:"false"`
	// ClientKeyFile is an absolute path to the dedicated requester key (64
	// hex) Signet authorizes for delivery. It must not be the service key;
	// startup refuses a key whose pubkey is the service pubkey.
	ClientKeyFile string `koanf:"client_key_file" yaml:"client_key_file" secret:"false"`
	// Owner is the Signet agent id that owns Bahia's credentials.
	Owner string `koanf:"owner" yaml:"owner" secret:"false"`
	// RequestTimeout bounds each Signet round trip. Zero uses 30s.
	RequestTimeout time.Duration `koanf:"request_timeout" yaml:"request_timeout" secret:"false"`
}

func (s *SecretStoreConfig) validate() error {
	s.Backend = strings.ToLower(strings.TrimSpace(s.Backend))
	sig := &s.Signet
	sig.Pubkey = strings.ToLower(strings.TrimSpace(sig.Pubkey))
	sig.Relays = normalizeRelayList(sig.Relays)
	sig.ClientKeyFile = strings.TrimSpace(sig.ClientKeyFile)
	sig.Owner = strings.TrimSpace(sig.Owner)
	switch s.Backend {
	case "":
		if sig.Pubkey != "" || len(sig.Relays) > 0 || sig.ClientKeyFile != "" || sig.Owner != "" || sig.RequestTimeout != 0 {
			return fmt.Errorf("config validation failed: secret_store.signet settings require secret_store.backend=signet")
		}
		return nil
	case SecretStoreBackendSignet:
	default:
		return fmt.Errorf("config validation failed: secret_store.backend must be empty or %q", SecretStoreBackendSignet)
	}
	if !isHexKey(sig.Pubkey) {
		return fmt.Errorf("config validation failed: secret_store.signet.pubkey must be 64 hex characters")
	}
	if len(sig.Relays) == 0 {
		return fmt.Errorf("config validation failed: secret_store.signet.relays must name at least one relay")
	}
	for _, relay := range sig.Relays {
		u, err := url.Parse(relay)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
			return fmt.Errorf("config validation failed: secret_store.signet.relays: %q is not a ws:// or wss:// URL", relay)
		}
		// Secret requests and deliveries never touch a public relay, even
		// encrypted; nostr.allowed_public_relays does not apply here.
		if relayguard.IsWellKnownPublic(relay) {
			return fmt.Errorf("config validation failed: secret_store.signet.relays: %q is a public relay; secret delivery uses only relays you operate", relay)
		}
	}
	if sig.ClientKeyFile == "" || !filepath.IsAbs(sig.ClientKeyFile) {
		return fmt.Errorf("config validation failed: secret_store.signet.client_key_file must be an absolute path")
	}
	if sig.Owner == "" || len(sig.Owner) > 128 {
		return fmt.Errorf("config validation failed: secret_store.signet.owner must name the Signet agent that owns Bahia's credentials (at most 128 characters)")
	}
	if sig.RequestTimeout < 0 {
		return fmt.Errorf("config validation failed: secret_store.signet.request_timeout must not be negative")
	}
	if sig.RequestTimeout == 0 {
		sig.RequestTimeout = defaultSecretStoreRequestTimeout
	}
	return nil
}
