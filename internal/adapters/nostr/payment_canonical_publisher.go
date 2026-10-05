package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// PaymentCanonicalPublisher publishes authoritative payment records through
// the shared cp-state signing/outbox pipeline. Content is OCK-encrypted under
// the "fleet" scope: payment records contain financial data (amounts, mint
// URLs, token hashes) that must not appear as plaintext on any relay.
//
// Each mutation (RecordPayment, MarkPaymentSent, RecordChange) publishes
// exactly one 30900 record before the service touches its SQL index (audit
// B-31). The d-tag is "payment:<id>" so each payment has a unique relay
// coordinate and a status transition replaces it. Warm-start covers the
// "payment" domain automatically via CPStateDomains().
//
// The publisher is also the service's read view: ListPaymentRecords decodes
// the daemon's own retained records from the local event store, so payment
// history needs no SQL repository.
//
// bahia-irsry.60: confidential cp-state for payments.
type PaymentCanonicalPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	logger    *zap.Logger
}

// NewPaymentCanonicalPublisher creates a publisher backed by the given
// projector. encryptor is required; publishes fail closed without it.
func NewPaymentCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *PaymentCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PaymentCanonicalPublisher{
		projector: projector,
		encryptor: encryptor,
		logger:    logger.Named("payment-canonical"),
	}
}

// PublishPaymentRecord publishes a single payment record as a confidential
// 30900 cp-state record. Called from PaymentService mutation sites.
func (p *PaymentCanonicalPublisher) PublishPaymentRecord(ctx context.Context, rec *domain.PaymentRecord) error {
	if rec == nil {
		return nil
	}
	if p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("payment canonical projector is unavailable")
	}
	dTag := PaymentDTag(rec.ID)
	tags, content := PaymentRecordContent(rec)
	return p.publishConfidential(ctx, KindPaymentRecord, dTag, false, tags, content, "payment.projection", &rec.ID)
}

func (p *PaymentCanonicalPublisher) publishConfidential(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content, entityType string, entityID *uuid.UUID) error {
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of payment record")
	}

	// Payments are fleet-scoped (no per-org dimension in the payment model).
	// Use "fleet" as the OCK org scope, consistent with fleet-scoped
	// notification channels.
	encrypted, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, []byte(content), legacyKind, dTag, topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt payment state: %w", err)
	}

	return p.projector.publishCanonicalFirst(ctx, legacyKind, dTag, deleted, extraTags, content, encrypted, entityType, entityID)
}

// PaymentDTag returns the d-tag for a payment record: "payment:<id>".
func PaymentDTag(id uuid.UUID) string {
	return "payment:" + id.String()
}

// PaymentRecordContent builds the tags and JSON content for a payment record
// canonical state record.
func PaymentRecordContent(rec *domain.PaymentRecord) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"worker_pubkey", rec.WorkerPubkey},
		{"direction", string(rec.Direction)},
		{"status", string(rec.Status)},
	}
	if rec.DeploymentRunID != uuid.Nil {
		tags = append(tags, gonostr.Tag{"run_id", rec.DeploymentRunID.String()})
	}

	payload := map[string]any{
		"id":                rec.ID.String(),
		"deployment_run_id": rec.DeploymentRunID.String(),
		"worker_pubkey":     rec.WorkerPubkey,
		"mint_url":          rec.MintURL,
		"amount_sats":       rec.AmountSats,
		"direction":         string(rec.Direction),
		"status":            string(rec.Status),
	}
	// Token hash is included in the encrypted content — never in tags.
	if rec.TokenHash != "" {
		payload["token_hash"] = rec.TokenHash
	}
	if rec.ErrorMessage != "" {
		payload["error_message"] = rec.ErrorMessage
	}
	if rec.Metadata != nil {
		payload["metadata"] = rec.Metadata
	}
	if !rec.CreatedAt.IsZero() {
		payload["created_at"] = rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !rec.UpdatedAt.IsZero() {
		payload["updated_at"] = rec.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}

// ListPaymentRecords decodes the daemon's own retained payment records from
// the local event store, oldest first. The store keeps only the winning event
// of each replaceable coordinate, so every payment appears once, in its latest
// state. A record that cannot be decrypted or decoded fails the read: a
// partial history would understate what was paid.
func (p *PaymentCanonicalPublisher) ListPaymentRecords(ctx context.Context) ([]domain.PaymentRecord, error) {
	if p == nil || p.projector == nil || p.projector.history == nil || p.encryptor == nil {
		return nil, fmt.Errorf("payment canonical local view is unavailable")
	}
	family := cpStateFamilies[KindPaymentRecord]
	records, err := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, canonicalViewLimit)
	if err != nil {
		return nil, err
	}
	if len(records) >= canonicalViewLimit {
		return nil, fmt.Errorf("payment canonical view reached history limit")
	}
	out := make([]domain.PaymentRecord, 0, len(records))
	for _, record := range records {
		tags := recordTags(record)
		if tagValue(tags, "legacy_kind") != strconv.Itoa(KindPaymentRecord) || isTombstoneTags(tags) {
			continue
		}
		dTag := tagValue(tags, "d")
		if dTag == "" {
			return nil, fmt.Errorf("payment record %s lacks d tag", record.ID)
		}
		plaintext, err := p.encryptor.DecryptConfidential(ctx, record.Content, KindPaymentRecord, dTag, family.topic)
		if err != nil {
			return nil, fmt.Errorf("decrypt payment %s: %w", record.ID, err)
		}
		var rec domain.PaymentRecord
		if err := json.Unmarshal(plaintext, &rec); err != nil {
			return nil, fmt.Errorf("decode payment %s: %w", record.ID, err)
		}
		if PaymentDTag(rec.ID) != dTag {
			return nil, fmt.Errorf("payment %s coordinate mismatch", record.ID)
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
