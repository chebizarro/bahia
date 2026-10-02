package nostr

import (
	"context"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/google/uuid"
)

// publishConfidentialControlState publishes a control-state record whose content
// is NIP-44 encrypted to the service's own pubkey. The outer envelope tags
// (d, domain, schema, legacy_kind, deleted, topic) remain plaintext so relay
// coordination and tombstone replacement work as usual. Only the payload in the
// event's Content field is encrypted.
//
// This is used by sensitive domains (secret, notification) where canonical state
// must never appear in plaintext on any relay event (design §1.7).
//
// The encryption is self-addressed: the daemon encrypts to its own pubkey using
// a NIP-44 conversation key derived from (servicePubKey, servicePrivateKey).
// Only the daemon (holder of the private key) can decrypt the content.
func (p *Projector) publishConfidentialControlState(ctx context.Context, legacyKind int, id string, deleted bool, tags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	if p == nil || !p.Enabled() {
		return nil
	}

	// Encrypt the content with NIP-44 self-encryption.
	encrypted, err := p.selfEncryptNIP44(content)
	if err != nil {
		return fmt.Errorf("encrypt confidential state: %w", err)
	}

	// Publish with the encrypted content; outer envelope tags stay plaintext.
	return p.publishControlState(ctx, legacyKind, id, deleted, tags, encrypted, entityType, entityID)
}

// selfEncryptNIP44 encrypts plaintext to the service's own pubkey using NIP-44.
// The conversation key is derived from (servicePubKey, servicePrivateKey),
// ensuring only the daemon can decrypt it.
func (p *Projector) selfEncryptNIP44(plaintext string) (string, error) {
	if p.privateKey == "" {
		return "", fmt.Errorf("no private key configured for NIP-44 self-encryption")
	}

	secret, err := gonostr.SecretKeyFromHex(p.privateKey)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	servicePubKey := secret.Public()

	conversationKey, err := nip44.GenerateConversationKey(servicePubKey, secret)
	if err != nil {
		return "", fmt.Errorf("generate conversation key: %w", err)
	}

	ciphertext, err := nip44.Encrypt(plaintext, conversationKey)
	if err != nil {
		return "", fmt.Errorf("NIP-44 encrypt: %w", err)
	}

	return ciphertext, nil
}
