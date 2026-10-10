package app

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// newServiceKeyer is the single construction seam for Bahia's service
// identity. Startup calls it once and injects the result into every consumer
// that signs, AUTHs or NIP-44s as the service. It returns nil, nil when no
// service identity is configured.
//
// Raw-key derivations (secret-store HKDF, legacy O1 OCK, assistant transcript
// keys, confidential-state dedupe HMAC) never read config: they ask the
// returned Keyer for the optional nostrutil.ServiceKeyMaterialHolder
// capability via nostrutil.RequireServiceKeyMaterial and fail closed with
// nostrutil.ErrServiceKeyMaterialRequired when it is absent.
func newServiceKeyer(cfg *config.Config) (nostr.Keyer, error) {
	if cfg == nil || strings.TrimSpace(cfg.Nostr.PrivateKey) == "" {
		return nil, nil
	}
	local, err := nostrutil.NewLocalKeyer(cfg.Nostr.PrivateKey)
	if err != nil {
		return nil, err
	}
	return local, nil
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
