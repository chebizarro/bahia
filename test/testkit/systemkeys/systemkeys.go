// Package systemkeys gives tests the daemon's system keyring: one key of a
// purpose, resolved through a store that enforces the system scope by label
// as Signet does, as the daemon resolves it at startup.
package systemkeys

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// KeyID is the key id of every key this package configures.
const KeyID = "test"

// store delivers a key of fill bytes for any credential labelled for the
// scope it is asked under.
type store struct {
	label string
	fill  byte
}

func (s store) Resolve(_ context.Context, _ domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error) {
	if !scope.Admits(s.label) {
		return nil, secretstore.ErrDenied
	}
	return secretstore.NewValue(bytes.Repeat([]byte{s.fill}, secretstore.SystemKeySize), time.Now().Add(time.Minute)), nil
}

func (store) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (store) Health(context.Context) error { return nil }

func (s store) ResolveAll(ctx context.Context, reqs []secretstore.Request) ([]*secretstore.Value, error) {
	values := make([]*secretstore.Value, 0, len(reqs))
	for _, req := range reqs {
		value, err := s.Resolve(ctx, req.Ref, req.Scope)
		if err != nil {
			for _, v := range values {
				v.Zeroize()
			}
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// Unloaded configures one key of purpose without resolving it.
func Unloaded(purpose string) *secretstore.Keyring {
	ring, err := secretstore.NewKeyring([]secretstore.SystemKeyRef{{
		Purpose: purpose, KeyID: KeyID,
		Credential: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("d", 64), Version: 1},
	}})
	if err != nil {
		panic(err) // a fixed, valid reference
	}
	return ring
}

// Load resolves ring's key of purpose as fill bytes.
func Load(ring *secretstore.Keyring, purpose string, fill byte) error {
	return ring.Load(context.Background(), store{label: secretstore.SystemLabelNamespace + "/" + purpose + "/" + KeyID, fill: fill})
}

// Keyring is a loaded keyring whose key of purpose repeats fill.
func Keyring(purpose string, fill byte) *secretstore.Keyring {
	ring := Unloaded(purpose)
	if err := Load(ring, purpose, fill); err != nil {
		panic(err) // the store answers every request for this label
	}
	return ring
}
