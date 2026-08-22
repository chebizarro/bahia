package hiveci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const artifactRegistrationAuditSchema = "bahia.audit.artifact-registration.v1"

type AuditEventStore interface {
	Record(context.Context, *repository.NostrEventRecord) (bool, error)
}

type RegistrationAudit struct {
	signer nostr.Signer
	events AuditEventStore
	now    func() time.Time
}

func NewRegistrationAudit(signer nostr.Signer, events AuditEventStore) *RegistrationAudit {
	return &RegistrationAudit{signer: signer, events: events, now: func() time.Time { return time.Now().UTC() }}
}

func (a *RegistrationAudit) AuditReleaseRejection(ctx context.Context, source *nostr.Event, decisionErr error) error {
	if source == nil {
		return fmt.Errorf("source release event is required")
	}
	return a.record(ctx, "rejected", source.ID.Hex(), "", "", "", "", decisionErr)
}

func (a *RegistrationAudit) AuditReleaseRegistration(ctx context.Context, release domain.HiveCIAcceptedRelease, artifact *domain.Artifact, decision string, decisionErr error) error {
	artifactID := ""
	if artifact != nil {
		artifactID = artifact.ID.String()
	}
	return a.record(ctx, decision, release.ResultEventID, release.Result.ReleaseIdentity,
		release.Result.Manifest.Repository, release.Result.Manifest.Digest, artifactID, decisionErr)
}

func (a *RegistrationAudit) record(ctx context.Context, decision, resultEventID, releaseIdentity, imageRepo, imageDigest, artifactID string, decisionErr error) error {
	if a == nil || a.signer == nil || a.events == nil {
		return fmt.Errorf("artifact registration audit dependencies are not configured")
	}
	now := a.now().UTC()
	reason := ""
	if decisionErr != nil {
		reason = strings.TrimSpace(decisionErr.Error())
	}
	body := map[string]any{
		"schema":           artifactRegistrationAuditSchema,
		"type":             "artifact.registration.decision",
		"decision":         decision,
		"result_event_id":  resultEventID,
		"release_identity": releaseIdentity,
		"image_repository": imageRepo,
		"image_digest":     imageDigest,
		"artifact_id":      artifactID,
		"reason":           reason,
		"recorded_at":      now.Format(time.RFC3339Nano),
	}
	content, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode artifact registration audit: %w", err)
	}
	tags := nostr.Tags{
		{"domain", "artifact"}, {"entity", "registration"}, {"type", "artifact.registration.decision"},
		{"schema", artifactRegistrationAuditSchema}, {"decision", decision},
	}
	for _, tag := range []nostr.Tag{
		{"e", resultEventID}, {"release", releaseIdentity}, {"image_repo", imageRepo},
		{"image_digest", imageDigest}, {"artifact", artifactID},
	} {
		if len(tag) == 2 && tag[1] != "" {
			tags = append(tags, tag)
		}
	}
	event := &nostr.Event{
		Kind: nostr.Kind(cascadia.CAS_AUDIT), CreatedAt: nostr.Timestamp(now.Unix()),
		Tags: append(nostr.Tags{{"-"}}, tags...), Content: string(content),
	}
	if err := a.signer.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("sign artifact registration audit: %w", err)
	}
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		return fmt.Errorf("encode artifact registration audit tags: %w", err)
	}
	_, err = a.events.Record(ctx, &repository.NostrEventRecord{
		ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
		Content: event.Content, Tags: tagsJSON, Sig: hex.EncodeToString(event.Sig[:]),
		CreatedAt: event.CreatedAt.Time(), ReceivedAt: now,
		EntityType: "artifact_registration", PublishState: repository.NostrPublishStatePending,
	})
	if err != nil {
		return fmt.Errorf("persist artifact registration audit outbox event: %w", err)
	}
	return nil
}
