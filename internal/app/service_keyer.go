package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

// openServiceSigner opens a service signer session (a test seam).
var openServiceSigner = servicesigner.Open

// newServiceKeyer is the single construction seam for Bahia's service
// identity. Startup calls it once and injects the signer's Keyer into every
// consumer that signs, AUTHs or NIP-44s as the service. The signer is selected
// by nostr.signer (local, any NIP-46 bunker, or NIP-55L) and must report the
// configured service pubkey. It returns a nil signer when no service identity
// is configured. The returned release func is the caller's single hold on the
// signer session; the session closes when its last holder releases it.
//
// running is the signer of the application a reload candidate will replace.
// When the candidate's signer config is servicesigner.SameSigner as the one
// running was opened with, the candidate takes another hold on running instead
// of opening a second session with the same client key. Otherwise it opens
// its own session and running closes when the replaced application releases
// it (see docs/architecture/service-signer.md, Reload).
//
// Raw-key derivations (secret-store HKDF, legacy O1 OCK, assistant transcript
// keys, confidential-state dedupe HMAC) never read config: they ask the
// Keyer for the optional nostrutil.ServiceKeyMaterialHolder capability via
// nostrutil.RequireServiceKeyMaterial and fail closed with
// nostrutil.ErrServiceKeyMaterialRequired when it is absent. Only the local
// signer (nostrutil.LocalKeyer) provides it.
func newServiceKeyer(cfg *config.Config, admission *nostrout.Admission, logger *zap.Logger, running *serviceSigner) (*serviceSigner, func(), error) {
	if cfg == nil {
		return nil, func() {}, nil
	}
	nostrCfg, err := servicesigner.ResolveClientKeyFile(cfg.Nostr)
	if err != nil {
		return nil, nil, err
	}
	if running != nil && servicesigner.SameSigner(running.config, nostrCfg) {
		if release, ok := running.acquire(); ok {
			if logger != nil {
				logger.Info("service signer config unchanged; reusing the running signer session")
			}
			return running, release, nil
		}
	}
	opts := servicesigner.Options{Admission: admission}
	if logger != nil {
		opts.OnAuthURL = func(authURL string) { logger.Warn(servicesigner.AuthURLMessage, zap.String("url", authURL)) }
	}
	lifetime, cancel := context.WithCancel(context.Background())
	keyer, err := openServiceSigner(lifetime, nostrCfg, opts)
	if errors.Is(err, servicesigner.ErrNotConfigured) {
		cancel()
		return nil, func() {}, nil
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	signer := &serviceSigner{keyer: keyer, config: nostrCfg, holders: 1, close: func() {
		cancel()
		if closer, ok := keyer.(io.Closer); ok {
			_ = closer.Close()
		}
	}}
	return signer, signer.releaser(), nil
}

// serviceSigner is one open service signer session, shared by the running
// application and a reload candidate whose signer config is unchanged. Each
// holder releases it once; the last release closes the session, so the old
// application never closes a session the new one uses, a failed candidate
// leaves it to the old one, and it closes exactly once at final shutdown.
type serviceSigner struct {
	keyer nostr.Keyer
	// config is the signer config the session was opened with, its client
	// key file resolved to the key it held then.
	config config.NostrConfig
	close  func()

	mu      sync.Mutex
	holders int
}

// Keyer returns the service identity, or nil when none is configured.
func (s *serviceSigner) Keyer() nostr.Keyer {
	if s == nil {
		return nil
	}
	return s.keyer
}

// acquire adds a holder. It fails once the session has closed.
func (s *serviceSigner) acquire() (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holders == 0 {
		return nil, false
	}
	s.holders++
	return s.releaser(), true
}

// releaser returns one holder's idempotent release.
func (s *serviceSigner) releaser() func() {
	return sync.OnceFunc(func() {
		s.mu.Lock()
		s.holders--
		last := s.holders == 0
		s.mu.Unlock()
		if last {
			s.close()
		}
	})
}

// legacyO1OrgStateDecryptor reads records in the legacy O1 org-state format,
// whose key is SHA-256 over the raw service key text. It returns nil, nil when
// no service identity is configured. Without in-process key material it
// returns a decryptor that fails every read with the named blocker, plus that
// blocker for the startup log, rather than silently skipping O1 records.
func legacyO1OrgStateDecryptor(serviceKeyer nostr.Keyer) (nostrAdapter.LegacyOrgStateDecryptor, error) {
	if serviceKeyer == nil {
		return nil, nil
	}
	material, err := nostrutil.RequireServiceKeyMaterial(serviceKeyer, "legacy O1 org-state decryption")
	if err != nil {
		return blockedOrgStateDecryptor{err: err}, err
	}
	orgKeySum := sha256.Sum256([]byte("bahia org state key v1\x00" + strings.TrimSpace(material)))
	return controlplane.NewOrgStateEncryptor(controlplane.StaticOrgStateKeyProvider{
		Key: controlplane.OrgStateKey{
			Ref:     "org-state/service-nostr-key",
			Version: "v1",
			Key:     orgKeySum[:],
		},
	}), nil
}

type blockedOrgStateDecryptor struct{ err error }

func (b blockedOrgStateDecryptor) DecryptOrgState(string) ([]byte, error) { return nil, b.err }

// blossomSigner returns the BUD-11 auth signer for the dedicated Blossom
// identity (blossom.private_key), or nil for anonymous access. It is not the
// service identity.
func blossomSigner(cfg config.BlossomConfig) (nostr.Signer, error) {
	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return nil, nil
	}
	signer, err := nostrutil.NewLocalKeyer(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("configuring blossom.private_key signer: %w", err)
	}
	return signer, nil
}
