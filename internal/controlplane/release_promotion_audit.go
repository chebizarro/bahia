package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/repository"
)

const releasePromotionAuditSchema = "bahia.audit.release-promotion.v1"

type promotionAuditStore interface {
	Record(context.Context, *repository.NostrEventRecord) (bool, error)
}

type SignedReleasePromotionAudit struct {
	signer nostr.Signer
	events promotionAuditStore
	now    func() time.Time
}

func NewSignedReleasePromotionAudit(signer nostr.Signer, events promotionAuditStore) *SignedReleasePromotionAudit {
	return &SignedReleasePromotionAudit{signer: signer, events: events, now: func() time.Time { return time.Now().UTC() }}
}

func (a *SignedReleasePromotionAudit) AuditPromotionDecision(ctx context.Context, decision ReleasePromotionDecision, status string, decisionErr error) error {
	if a == nil || a.signer == nil || a.events == nil {
		return fmt.Errorf("release promotion audit dependencies are not configured")
	}
	now := a.now().UTC()
	reason := ""
	if decisionErr != nil {
		reason = decisionErr.Error()
	}
	content, err := json.Marshal(map[string]any{
		"schema":                   releasePromotionAuditSchema,
		"type":                     "artifact.promotion.decision",
		"decision":                 status,
		"release_identity":         decision.ReleaseIdentity,
		"artifact_digest":          decision.ArtifactDigest,
		"previous_artifact_digest": decision.PreviousArtifactDigest,
		"idempotency_key":          decision.IdempotencyKey,
		"fingerprint":              decision.Fingerprint,
		"requester":                decision.Requester,
		"request_event_id":         decision.RequestEventID,
		"replay":                   decision.Replay,
		"reason":                   reason,
		"recorded_at":              now.Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("encode release promotion audit: %w", err)
	}
	tags := nostr.Tags{
		{"-"}, {"domain", "artifact"}, {"entity", "promotion"},
		{"type", "artifact.promotion.decision"}, {"schema", releasePromotionAuditSchema},
		{"decision", status},
	}
	for _, tag := range []nostr.Tag{
		{"e", decision.RequestEventID}, {"release", decision.ReleaseIdentity},
		{"image_digest", decision.ArtifactDigest}, {"previous_image_digest", decision.PreviousArtifactDigest},
		{"idempotency", decision.IdempotencyKey}, {"fingerprint", decision.Fingerprint},
		{"p", decision.Requester},
	} {
		if tag[1] != "" {
			tags = append(tags, tag)
		}
	}
	event := &nostr.Event{
		Kind: nostr.Kind(cascadia.CAS_AUDIT), CreatedAt: nostr.Timestamp(now.Unix()),
		Tags: tags, Content: string(content),
	}
	if err := a.signer.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("sign release promotion audit: %w", err)
	}
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		return fmt.Errorf("encode release promotion audit tags: %w", err)
	}
	_, err = a.events.Record(ctx, &repository.NostrEventRecord{
		ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
		Content: event.Content, Tags: tagsJSON, Sig: hex.EncodeToString(event.Sig[:]),
		CreatedAt: event.CreatedAt.Time(), ReceivedAt: now,
		EntityType: "release_promotion", PublishState: repository.NostrPublishStatePending,
	})
	if err != nil {
		return fmt.Errorf("persist release promotion audit outbox event: %w", err)
	}
	return nil
}

var _ ReleasePromotionAuditor = (*SignedReleasePromotionAudit)(nil)
