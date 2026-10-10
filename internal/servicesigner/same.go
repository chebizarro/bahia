package servicesigner

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/config"
)

// SameSigner reports whether a and b select the same service signer session,
// so a config reload can keep the running signer instead of opening a second
// session with the same client key. They are the same when the effective
// method (config.NostrConfig.ServiceSignerMethod), the trimmed
// nostr.private_key, the case-insensitive nostr.public_key and every
// nostr.signer setting are equal.
//
// SameSigner does no I/O. nostr.signer.client_secret_key_file names a key
// whose content can be rotated under an unchanged path, so a config that
// still names a key file is never the same signer: comparing paths would keep
// a rotated-out client key. Resolve both sides with ResolveClientKeyFile first
// to compare the keys themselves.
func SameSigner(a, b config.NostrConfig) bool {
	method := a.ServiceSignerMethod()
	if method != b.ServiceSignerMethod() || a.Signer.ClientSecretKeyFile != "" || b.Signer.ClientSecretKeyFile != "" {
		return false
	}
	signerA, signerB := a.Signer, b.Signer
	signerA.Method, signerB.Method = method, method
	return signerA == signerB &&
		strings.TrimSpace(a.PrivateKey) == strings.TrimSpace(b.PrivateKey) &&
		strings.EqualFold(strings.TrimSpace(a.PublicKey), strings.TrimSpace(b.PublicKey))
}

// ResolveClientKeyFile returns cfg with nostr.signer.client_secret_key_file
// replaced by the client key the file holds now. Open the resolved config so
// the session uses exactly the key SameSigner compared; a key file rotated
// after that is picked up by the next reload, never silently kept or ignored.
func ResolveClientKeyFile(cfg config.NostrConfig) (config.NostrConfig, error) {
	path := cfg.Signer.ClientSecretKeyFile
	if path == "" {
		return cfg, nil
	}
	loaded, err := config.LoadPrivateKey(path, "")
	if err != nil {
		return config.NostrConfig{}, fmt.Errorf("read nostr.signer.client_secret_key_file: %w", err)
	}
	cfg.Signer.ClientSecretKey = strings.ToLower(strings.TrimSpace(loaded))
	cfg.Signer.ClientSecretKeyFile = ""
	return cfg, nil
}
