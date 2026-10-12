package nostr

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

func signEventWithPrivateKeyHex(ev *gonostr.Event, privateKeyHex string) error {
	if err := nostrutil.SignEventWithHexKey(ev, privateKeyHex); err != nil {
		return fmt.Errorf("signing nostr event: %w", err)
	}
	return nil
}

func publicKeyHexFromPrivateKeyHex(privateKeyHex string) (string, error) {
	return nostrutil.PublicKeyHexFromPrivateKeyHex(privateKeyHex)
}

// mustLocalKeyer is the local-mode service Keyer for a test key.
func mustLocalKeyer(privateKeyHex string) nostrutil.LocalKeyer {
	keyer, err := nostrutil.NewLocalKeyer(privateKeyHex)
	if err != nil {
		panic(err)
	}
	return keyer
}

// withTestServiceKey gives a hand-built projector the local service identity
// NewProjector would derive from an injected LocalKeyer.
func withTestServiceKey(p *Projector, privateKeyHex string) *Projector {
	keyer := mustLocalKeyer(privateKeyHex)
	p.signer = keyer
	if p.servicePubkey == "" {
		pubkey, _ := publicKeyHexFromPrivateKeyHex(privateKeyHex)
		p.servicePubkey = pubkey
	}
	p.stateHashKeys = testStateHashKeyring(testStateHashKeyFill)
	return p
}

// testStateHashKeyFill is the byte the default test confidential-dedupe key
// repeats.
const testStateHashKeyFill = 0x5a

// keyringTestStore delivers one system key to any system scope.
type keyringTestStore struct{ key []byte }

func (s keyringTestStore) Resolve(_ context.Context, _ domain.SecretCredentialRef, _ secretstore.Scope) (*secretstore.Value, error) {
	return secretstore.NewValue(append([]byte(nil), s.key...), time.Now().Add(time.Minute)), nil
}

func (keyringTestStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (keyringTestStore) Health(context.Context) error { return nil }

// testStateHashKeyring is a loaded keyring whose confidential-dedupe key
// repeats fill.
func testStateHashKeyring(fill byte) *secretstore.Keyring {
	ring := unloadedStateHashKeyring()
	if err := ring.Load(context.Background(), keyringTestStore{key: bytes.Repeat([]byte{fill}, secretstore.SystemKeySize)}); err != nil {
		panic(err)
	}
	return ring
}

// unloadedStateHashKeyring configures a confidential-dedupe key that has not
// been resolved.
func unloadedStateHashKeyring() *secretstore.Keyring {
	ring, err := secretstore.NewKeyring([]secretstore.SystemKeyRef{{
		Purpose: secretstore.PurposeConfidentialDedupe, KeyID: "test",
		Credential: domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat("d", 64), Version: 1},
	}})
	if err != nil {
		panic(err)
	}
	return ring
}

// newKeyedTestProjector builds a projector signing as the fixture's
// NostrConfig.PrivateKey through an injected LocalKeyer.
func newKeyedTestProjector(cfg config.NostrConfig, source ProjectionSource, publisher ProjectionPublisher, history ProjectionHistory, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	if cfg.PrivateKey != "" {
		pubkey, _ := publicKeyHexFromPrivateKeyHex(cfg.PrivateKey)
		opts = append([]ProjectorOption{WithProjectorSigner(mustLocalKeyer(cfg.PrivateKey), pubkey), WithConfidentialStateHashKeys(testStateHashKeyring(testStateHashKeyFill))}, opts...)
	}
	return NewProjector(cfg, source, publisher, history, logger, opts...)
}

// newKeyedTestPublisher builds a publisher signing as the fixture's
// NostrConfig.PrivateKey through an injected LocalKeyer.
func newKeyedTestPublisher(cfg config.NostrConfig, pool *RelayPool, eventRepo repository.NostrEventRepository, logger *zap.Logger, opts ...PublisherOption) *Publisher {
	if cfg.PrivateKey != "" {
		opts = append([]PublisherOption{WithPublisherSigner(mustLocalKeyer(cfg.PrivateKey))}, opts...)
	}
	return NewPublisher(cfg, pool, eventRepo, logger, opts...)
}
