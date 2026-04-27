package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// NostrDMSender delivers notifications as NIP-44 encrypted direct messages.
type NostrDMSender struct {
	relayPool  *nostrAdapter.RelayPool
	privateKey string
	logger     *zap.Logger
}

// NewNostrDMSender creates a new Nostr DM sender.
// privateKey is Bahia's Nostr private key (hex).
func NewNostrDMSender(relayPool *nostrAdapter.RelayPool, privateKey string, logger *zap.Logger) *NostrDMSender {
	return &NostrDMSender{
		relayPool:  relayPool,
		privateKey: privateKey,
		logger:     logger,
	}
}

// Send delivers a notification as an encrypted Nostr DM (Kind 4 with NIP-44).
// Config keys:
//   - "pubkey" (required): recipient's Nostr public key (hex)
func (s *NostrDMSender) Send(ctx context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	recipientPubkey, ok := ch.Config["pubkey"].(string)
	if !ok || recipientPubkey == "" {
		return fmt.Errorf("nostr_dm channel %q missing pubkey config", ch.Name)
	}

	// Build the DM content.
	content := fmt.Sprintf("🔔 Bahia Notification: %s\n", eventType)
	if data, err := json.MarshalIndent(payload, "", "  "); err == nil {
		content += string(data)
	}

	// Generate conversation key for NIP-44 encryption.
	conversationKey, err := nip44.GenerateConversationKey(recipientPubkey, s.privateKey)
	if err != nil {
		return fmt.Errorf("generating conversation key: %w", err)
	}

	encrypted, err := nip44.Encrypt(content, conversationKey)
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

	if err := ev.Sign(s.privateKey); err != nil {
		return fmt.Errorf("signing DM event: %w", err)
	}

	// Publish to relays via the pool.
	published, err := s.relayPool.Publish(ctx, ev)
	if err != nil {
		return fmt.Errorf("publishing DM: %w", err)
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
