// Package servicesigner builds Bahia's service identity signer from
// nostr.signer configuration. The identity is a standard nostr.Keyer whatever
// holds the key: process memory (local), any NIP-46 bunker (nip46) or a local
// NIP-55L D-Bus signer (nip55l). Single-writer fencing is the signer's job;
// Bahia authenticates with its dedicated client identity and never calls
// signer-specific or management methods for its own identity.
package servicesigner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nip55l"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// The local signer must be nostrutil.LocalKeyer: legacy raw-key derivations
// find their key material through its ServiceKeyMaterialHolder capability.
var _ BinaryCipher = nostrutil.LocalKeyer{}

// AuthURLMessage is the operator-facing message for a bunker's out-of-band
// authorization URL.
const AuthURLMessage = "NIP-46 bunker requires out-of-band authorization of Bahia's client key"

// ErrNotConfigured reports that no service identity is configured: neither
// nostr.private_key nor nostr.signer.method is set.
var ErrNotConfigured = errors.New("no service signer configured (nostr.private_key or nostr.signer)")

const (
	defaultTimeout       = 30 * time.Second
	defaultNIP55LTimeout = 330 * time.Second
)

// BinaryCipher is the optional binary-safe NIP-44 capability. NIP-46 carries
// params as JSON strings, so arbitrary bytes cannot ride Encrypt/Decrypt;
// these methods move only the plaintext side as bytes while the ciphertext is
// an ordinary NIP-44 v2 payload. A backend without the capability returns an
// error wrapping errors.ErrUnsupported; it never falls back to a raw key.
type BinaryCipher interface {
	EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error)
	DecryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error)
}

// NIP55LConfig is what the NIP-55L constructor receives.
type NIP55LConfig struct {
	ServicePubkey nostr.PubKey
	AppID         string
	// BusAddress is a D-Bus address; empty uses the session bus.
	BusAddress  string
	CallTimeout time.Duration
}

// Options carries the process dependencies Open cannot read from config.
type Options struct {
	// Admission gates every NIP-46 request publication. Nil uses the
	// process-wide controller.
	Admission *nostrout.Admission
	// NewNIP55L overrides the NIP-55L D-Bus keyer constructor (tests). Nil
	// uses internal/adapters/nip55l.
	NewNIP55L func(context.Context, NIP55LConfig) (nostr.Keyer, error)
	// OnAuthURL receives the URL a NIP-46 bunker asks the operator to open to
	// authorize Bahia's client key. Nil logs it at warn level via slog.Default.
	OnAuthURL func(authURL string)
}

// Open builds the configured service signer and verifies that it reports the
// configured service pubkey (nostr.public_key, or the local key's own pubkey
// when it is unset). A mismatch is fatal: it would silently change Bahia's
// identity. A remote signer's session lives as long as ctx.
func Open(ctx context.Context, cfg config.NostrConfig, opts Options) (nostr.Keyer, error) {
	method := cfg.ServiceSignerMethod()
	timeout := cfg.Signer.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
		if method == config.NostrSignerNIP55L {
			// Unvalidated configs (tools, tests) still honour the NIP-55L
			// approval window.
			timeout = defaultNIP55LTimeout
		}
	}
	var expected nostr.PubKey
	if cfg.PublicKey != "" {
		pubkey, err := nostr.PubKeyFromHex(cfg.PublicKey)
		if err != nil || pubkey == nostr.ZeroPK {
			return nil, errors.New("nostr.public_key must be a valid 64-character hex pubkey")
		}
		expected = pubkey
	}
	if method != "" && method != config.NostrSignerLocal && expected == nostr.ZeroPK {
		return nil, fmt.Errorf("nostr.signer.method=%s requires nostr.public_key", method)
	}

	var signer nostr.Keyer
	var err error
	switch method {
	case "":
		return nil, ErrNotConfigured
	case config.NostrSignerLocal:
		signer, err = nostrutil.NewLocalKeyer(cfg.PrivateKey)
	case config.NostrSignerNIP46:
		onAuthURL := opts.OnAuthURL
		if onAuthURL == nil {
			onAuthURL = func(authURL string) { slog.Warn(AuthURLMessage, "url", authURL) }
		}
		signer, err = openNIP46(ctx, cfg.Signer, expected, timeout, opts.Admission, onAuthURL)
	case config.NostrSignerNIP55L:
		newNIP55L := opts.NewNIP55L
		if newNIP55L == nil {
			newNIP55L = openNIP55L
		}
		signer, err = newNIP55L(ctx, NIP55LConfig{ServicePubkey: expected, AppID: cfg.Signer.NIP55L.AppID, BusAddress: cfg.Signer.NIP55L.BusAddress, CallTimeout: timeout})
		if err == nil && signer == nil {
			err = errors.New("NIP-55L constructor returned no signer")
		}
	default:
		return nil, fmt.Errorf("unknown nostr.signer.method %q", method)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s service signer: %w", method, err)
	}
	if err := verifyIdentity(ctx, signer, expected, timeout); err != nil {
		closeSigner(signer)
		return nil, fmt.Errorf("verify %s service signer: %w", method, err)
	}
	return signer, nil
}

func verifyIdentity(ctx context.Context, signer nostr.Keyer, expected nostr.PubKey, timeout time.Duration) error {
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actual, err := signer.GetPublicKey(verifyCtx)
	if err != nil {
		return fmt.Errorf("read signer pubkey: %w", err)
	}
	if actual == nostr.ZeroPK || (expected != nostr.ZeroPK && actual != expected) {
		return fmt.Errorf("signer pubkey %s differs from configured service pubkey %s", actual.Hex(), expected.Hex())
	}
	return nil
}

func closeSigner(signer nostr.Keyer) {
	switch s := signer.(type) {
	case *nip46Keyer:
		s.cancel()
	case io.Closer:
		_ = s.Close()
	}
}

// openNIP55L builds the D-Bus keyer. It never returns a typed-nil
// *nip55l.Keyer as a non-nil nostr.Keyer.
func openNIP55L(ctx context.Context, cfg NIP55LConfig) (nostr.Keyer, error) {
	keyer, err := nip55l.New(ctx, nip55l.Config{
		ServicePubkey: cfg.ServicePubkey,
		AppID:         cfg.AppID,
		BusAddress:    cfg.BusAddress,
		CallTimeout:   cfg.CallTimeout,
	})
	if err != nil {
		return nil, err
	}
	return keyer, nil
}
