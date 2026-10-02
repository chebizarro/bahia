package controlplane

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
	"go.uber.org/zap"
)

// IntentGiftWrapIngress handles NIP-59 gift-wrapped intents for sensitive
// domains (org, secret, notification). Per design §1.7, sensitive-domain
// intents arrive as kind 1059 with #p=service-pubkey; the inner event is an
// operator-signed 30900 t=bahia-intent.
//
// Plaintext 30900 intents for sensitive domains are REJECTED with a bounded
// status so nobody publishes them unencrypted (§1.7 enforcement).
//
// The ingress is shared by O1 (org) and N1 (secret, notification) slices.
type IntentGiftWrapIngress struct {
	signer           casnostr.Signer
	processor        *IntentProcessor
	sensitiveDomains map[string]bool
	logger           *zap.Logger
}

// IntentGiftWrapIngressConfig configures the ingress.
type IntentGiftWrapIngressConfig struct {
	// Signer is the daemon's NIP-44 keypair for unwrapping 1059 events.
	Signer casnostr.Signer
	// Processor is the shared intent processor pipeline.
	Processor *IntentProcessor
	// SensitiveDomains is the set of domains that require gift-wrapped delivery.
	// Plaintext 30900 intents for these domains are rejected.
	SensitiveDomains []string
	Logger           *zap.Logger
}

// NewIntentGiftWrapIngress creates a gift-wrap ingress for sensitive domains.
func NewIntentGiftWrapIngress(cfg IntentGiftWrapIngressConfig) *IntentGiftWrapIngress {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	domains := make(map[string]bool, len(cfg.SensitiveDomains))
	for _, d := range cfg.SensitiveDomains {
		domains[d] = true
	}
	return &IntentGiftWrapIngress{
		signer:           cfg.Signer,
		processor:        cfg.Processor,
		sensitiveDomains: domains,
		logger:           logger.Named("intent-giftwrap-ingress"),
	}
}

// isSensitiveDomain returns true if the domain requires gift-wrapped delivery.
func (ig *IntentGiftWrapIngress) isSensitiveDomain(domain string) bool {
	return ig.sensitiveDomains[domain]
}

// ProcessGiftWrappedIntent unwraps a kind 1059 event, verifies the inner
// signature, extracts the inner 30900 intent, and hands it to the processor.
func (ig *IntentGiftWrapIngress) ProcessGiftWrappedIntent(ctx context.Context, outer *nostr.Event) error {
	if outer == nil {
		return fmt.Errorf("nil outer event")
	}
	if int(outer.Kind) != 1059 {
		return fmt.Errorf("expected kind 1059 gift wrap, got %d", outer.Kind)
	}

	// Unwrap using the existing NIP-59 / NIP-44 logic (same as ContextVM).
	inner, _, err := cascontextvm.UnwrapAny(ctx, ig.signer, outer)
	if err != nil {
		ig.logger.Debug("failed to unwrap gift-wrapped intent",
			zap.String("outer_id", outer.ID.Hex()),
			zap.Error(err),
		)
		return nil // silent drop for unwrap failures
	}
	if inner == nil {
		return nil
	}

	// Verify inner event signature.
	if !inner.VerifySignature() {
		ig.logger.Debug("gift-wrapped intent inner signature invalid",
			zap.String("outer_id", outer.ID.Hex()),
			zap.String("inner_id", inner.ID.Hex()),
		)
		return nil // silent drop for invalid signatures
	}

	// The inner event must be a 30900 intent.
	if int(inner.Kind) != 30900 {
		ig.logger.Debug("gift-wrapped inner event is not a 30900 intent",
			zap.String("outer_id", outer.ID.Hex()),
			zap.Int("inner_kind", int(inner.Kind)),
		)
		return nil
	}

	// Parse and hand off to the processor.
	intent, err := ParseIntent(inner)
	if err != nil {
		ig.logger.Debug("failed to parse gift-wrapped inner intent",
			zap.String("outer_id", outer.ID.Hex()),
			zap.Error(err),
		)
		return nil
	}
	intent.Actor = inner.PubKey.Hex()

	// Verify domain is sensitive.
	if !ig.sensitiveDomains[intent.Domain] {
		ig.logger.Debug("gift-wrapped intent for non-sensitive domain, processing normally",
			zap.String("domain", intent.Domain),
		)
	}

	return ig.processor.process(ctx, intent)
}

// RejectPlaintextSensitiveIntent checks if a plaintext 30900 intent is for a
// sensitive domain and rejects it if so. Returns true if the intent was
// rejected (callers should stop processing), false if the domain is not
// sensitive and the intent can proceed normally.
func (ig *IntentGiftWrapIngress) RejectPlaintextSensitiveIntent(ctx context.Context, intent *Intent) bool {
	if intent == nil || !ig.sensitiveDomains[intent.Domain] {
		return false
	}
	ig.logger.Warn("rejecting plaintext intent for sensitive domain",
		zap.String("domain", intent.Domain),
		zap.String("intent_id", intent.IntentID),
		zap.String("actor", intent.Actor),
	)
	if ig.processor.status != nil {
		ig.processor.status.PublishRejection(ctx, intent,
			fmt.Sprintf("domain %q requires gift-wrapped delivery (kind 1059); plaintext 30900 intents are rejected", intent.Domain))
	}
	return true
}

// ProcessUnwrappedIntent handles an inner 30900 intent that was already
// unwrapped from a kind 1059 gift-wrap by the EncryptedRequestTransport.
// The transport has already verified the outer routing and unwrapped the
// NIP-59 seal; this method verifies the inner signature, parses the intent,
// and hands it to the processor pipeline.
//
// This is the production entry point: the transport's event handler calls this
// when an unwrapped inner event is kind 30900 with t=bahia-intent, instead of
// dispatching it as a ContextVM message.
func (ig *IntentGiftWrapIngress) ProcessUnwrappedIntent(ctx context.Context, inner *nostr.Event) error {
	if inner == nil {
		return fmt.Errorf("nil inner event")
	}

	// The inner event must be a 30900 intent.
	if int(inner.Kind) != 30900 {
		ig.logger.Debug("unwrapped inner event is not a 30900 intent",
			zap.String("inner_id", inner.ID.Hex()),
			zap.Int("inner_kind", int(inner.Kind)),
		)
		return nil
	}

	// Verify inner event signature.
	if !inner.VerifySignature() {
		ig.logger.Debug("unwrapped intent inner signature invalid",
			zap.String("inner_id", inner.ID.Hex()),
		)
		return nil
	}

	// Parse and hand off to the processor.
	intent, err := ParseIntent(inner)
	if err != nil {
		ig.logger.Debug("failed to parse unwrapped inner intent",
			zap.String("inner_id", inner.ID.Hex()),
			zap.Error(err),
		)
		return nil
	}
	intent.Actor = inner.PubKey.Hex()

	if !ig.sensitiveDomains[intent.Domain] {
		ig.logger.Debug("unwrapped intent for non-sensitive domain, processing normally",
			zap.String("domain", intent.Domain),
		)
	}

	return ig.processor.process(ctx, intent)
}

// IsIntentEvent checks whether a Nostr event is a kind 30900 event tagged with
// t=bahia-intent. Used by the EncryptedRequestTransport to route unwrapped
// gift-wrapped events.
func IsIntentEvent(ev *nostr.Event) bool {
	if ev == nil || int(ev.Kind) != 30900 {
		return false
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "bahia-intent" {
			return true
		}
	}
	return false
}
