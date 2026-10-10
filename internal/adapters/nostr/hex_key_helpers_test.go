package nostr

import (
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
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
	p.confidentialStateHashKey, p.confidentialStateHashErr = confidentialStateHashKeyFor(keyer)
	return p
}

// newKeyedTestProjector builds a projector signing as the fixture's
// NostrConfig.PrivateKey through an injected LocalKeyer.
func newKeyedTestProjector(cfg config.NostrConfig, source ProjectionSource, publisher ProjectionPublisher, history ProjectionHistory, logger *zap.Logger, opts ...ProjectorOption) *Projector {
	if cfg.PrivateKey != "" {
		pubkey, _ := publicKeyHexFromPrivateKeyHex(cfg.PrivateKey)
		opts = append([]ProjectorOption{WithProjectorSigner(mustLocalKeyer(cfg.PrivateKey), pubkey)}, opts...)
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
