package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

// NostrDMSender delivers notifications as NIP-44 encrypted direct messages.
type NostrDMSender struct {
	relayPool *nostrAdapter.RelayPool
	publish   func(context.Context, nostr.Event) (int, error)
	signer    nostr.Keyer
	logger    *zap.Logger
}

// NewNostrDMSender creates a new Nostr DM sender that encrypts and signs as
// the injected service Keyer.
func NewNostrDMSender(relayPool *nostrAdapter.RelayPool, signer nostr.Keyer, logger *zap.Logger) *NostrDMSender {
	var publish func(context.Context, nostr.Event) (int, error)
	if relayPool != nil {
		publish = relayPool.Publish
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NostrDMSender{
		relayPool: relayPool,
		publish:   publish,
		signer:    signer,
		logger:    logger,
	}
}

// Send delivers a notification as an encrypted Nostr DM (Kind 4 with NIP-44).
// Config keys:
//   - "pubkey" (required): recipient's Nostr public key (hex)
func (s *NostrDMSender) Send(ctx context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	if s == nil || s.publish == nil {
		return fmt.Errorf("nostr DM relay publisher is not configured")
	}
	if s.signer == nil {
		return fmt.Errorf("nostr DM signer is not configured")
	}
	if ch == nil {
		return fmt.Errorf("nostr DM channel is required")
	}
	recipientPubkey, ok := ch.Config["pubkey"].(string)
	if !ok || recipientPubkey == "" {
		return fmt.Errorf("nostr_dm channel %q missing pubkey config", ch.Name)
	}

	// Build the DM content.
	content := fmt.Sprintf("🔔 Bahia Notification: %s\n", eventType)
	if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
		content += string(data)
	}

	recipient, err := nostrutil.PubKeyFromHex(recipientPubkey)
	if err != nil {
		return fmt.Errorf("decode recipient pubkey: %w", err)
	}
	encrypted, err := s.signer.Encrypt(ctx, content, recipient)
	if err != nil {
		return fmt.Errorf("encrypting DM: %w", err)
	}

	// Create Kind 4 encrypted DM event.
	ev := nostr.Event{
		Kind:      4, // Encrypted Direct Message
		Content:   encrypted,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"p", recipientPubkey},
		},
	}

	if err := s.signer.SignEvent(ctx, &ev); err != nil {
		return fmt.Errorf("signing DM event: %w", err)
	}

	// Publish to relays via the pool.
	published, err := s.publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publishing DM: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publishing DM: no relay accepted the event")
	}

	s.logger.Info("nostr DM notification sent",
		zap.String("recipient", recipientPubkey[:min(8, len(recipientPubkey))]+"..."),
		zap.String("event", eventType),
		zap.Int("relays", published),
	)

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
