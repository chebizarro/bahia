package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// Legacy NIP-44 self-encryption (Phase 3 N1). Retained as a read-only path
// during migration from the old per-private-key scheme to the per-org content
// key (OCK) scheme. New publishes use ConfidentialStateEncryptor.
//
// Remove after all historical records have been re-published under the OCK
// scheme (warm-start migration).

// selfDecryptNIP44Legacy decrypts content that was NIP-44 self-encrypted
// to the service's own pubkey using the raw private key. Legacy read-only path.
func (p *Projector) selfDecryptNIP44Legacy(content string) (string, error) {
	if p.privateKey == "" {
		return "", fmt.Errorf("no private key configured for legacy NIP-44 self-decryption")
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

	plaintext, err := nip44.Decrypt(content, conversationKey)
	if err != nil {
		return "", fmt.Errorf("NIP-44 decrypt: %w", err)
	}

	return plaintext, nil
}

// PublishKeyEnvelope publishes a key-envelope record through the shared
// cp-state signing/outbox pipeline. Implements controlplane.OCKEnvelopePublisher
// via Go structural typing — no import of controlplane needed.
func (p *Projector) PublishKeyEnvelope(ctx context.Context, dTag string, content string) error {
	if p == nil || !p.Enabled() {
		return fmt.Errorf("projector not enabled for key-envelope publish")
	}
	return p.publishControlState(ctx, KindOrgKeyEnvelope, dTag, false, nil, content, "org_key_envelope.projection", nil)
}

// ProjectorOCKEnvelopeHistory adapts the Projector's history to the
// controlplane.OCKEnvelopeHistory interface via Go structural typing.
// Returns []domain.KeyEnvelopeRecord — no import of controlplane needed.
type ProjectorOCKEnvelopeHistory struct {
	history ProjectionHistory
}

// NewProjectorOCKEnvelopeHistory creates an adapter from ProjectionHistory to
// the OCKEnvelopeHistory interface.
func NewProjectorOCKEnvelopeHistory(history ProjectionHistory) *ProjectorOCKEnvelopeHistory {
	return &ProjectorOCKEnvelopeHistory{history: history}
}

// FindKeyEnvelopes retrieves key-envelope records from history for OCK recovery.
func (h *ProjectorOCKEnvelopeHistory) FindKeyEnvelopes(ctx context.Context, orgID string) ([]domain.KeyEnvelopeRecord, error) {
	if h.history == nil {
		return nil, fmt.Errorf("no projection history for key-envelope recovery")
	}
	records, err := h.history.FindByTag(ctx, "t", "org-key-envelope", nil, 10000)
	if err != nil {
		return nil, fmt.Errorf("query key-envelope history: %w", err)
	}
	var out []domain.KeyEnvelopeRecord
	for _, rec := range records {
		dTag := extractDTagFromRecord(rec)
		out = append(out, domain.KeyEnvelopeRecord{
			DTag:    dTag,
			Content: rec.Content,
		})
	}
	return out, nil
}

// extractDTagFromRecord extracts the d-tag value from a NostrEventRecord's
// Tags JSON.
func extractDTagFromRecord(rec repository.NostrEventRecord) string {
	var tags [][]string
	if err := json.Unmarshal(rec.Tags, &tags); err != nil {
		return ""
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "d" {
			return tag[1]
		}
	}
	return ""
}
