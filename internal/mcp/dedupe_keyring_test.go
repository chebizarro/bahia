package mcp

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// dedupeKeyStore delivers one confidential-dedupe system key.
type dedupeKeyStore struct{}

func (dedupeKeyStore) Resolve(context.Context, domain.SecretCredentialRef, secretstore.Scope) (*secretstore.Value, error) {
	return secretstore.NewValue(bytes.Repeat([]byte{0x5a}, secretstore.SystemKeySize), time.Now().Add(time.Minute)), nil
}

func (dedupeKeyStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (dedupeKeyStore) Health(context.Context) error { return nil }

// dedupeKeyring is a loaded keyring holding the confidential-dedupe system
// key the daemon resolves from Signet at startup.
func dedupeKeyring(t testing.TB) *secretstore.Keyring {
	t.Helper()
	ring, err := secretstore.NewKeyring([]secretstore.SystemKeyRef{{
		Purpose: secretstore.PurposeConfidentialDedupe, KeyID: "test",
		Credential: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("d", 64), Version: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Load(context.Background(), dedupeKeyStore{}); err != nil {
		t.Fatal(err)
	}
	return ring
}
