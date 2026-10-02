package nostr

import (
	"context"
	"encoding/json"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// SecretCanonicalPublisher publishes authoritative secret state records through
// the shared builder and outbox. Secret values are NEVER included in the
// published event — only the SecretRef metadata (name, version, scope).
//
// Follows the BackupCanonicalPublisher pattern: holds a *Projector reference
// and calls publishControlState for each entity, going through
// controlStateEnvelope → the shared signing/outbox pipeline.
// The cpStateFamilies table is the single envelope source.
type SecretCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewSecretCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewSecretCanonicalPublisher(projector *Projector, logger *zap.Logger) *SecretCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecretCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("secret-canonical"),
	}
}

// PublishSecretRef publishes a canonical secret registry record containing
// only the SecretRef metadata (id, name, service_id, environment_id, version,
// encryption_method). The encrypted value is NEVER included.
func (p *SecretCanonicalPublisher) PublishSecretRef(ctx context.Context, ref domain.SecretRef) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	dTag := SecretDTag(ref.ID)
	tags, content := SecretRegistryRecord(&ref, false)
	return p.projector.publishConfidentialControlState(ctx, KindSecretRegistry, dTag, false, tags, content, "secret.projection", &ref.ID)
}

// PublishSecretDeleted publishes a tombstone for a deleted secret.
func (p *SecretCanonicalPublisher) PublishSecretDeleted(ctx context.Context, secretID uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	dTag := SecretDTag(secretID)
	contentJSON, _ := json.Marshal(map[string]any{"id": secretID.String(), "deleted": true})
	content := string(contentJSON)
	return p.projector.publishConfidentialControlState(ctx, KindSecretRegistry, dTag, true, nil, content, "secret.projection", &secretID)
}

// SecretDTag returns the d-tag for a secret entity.
func SecretDTag(id uuid.UUID) string {
	return id.String()
}

// SecretRegistryRecord builds the tags and content for a secret registry
// canonical state record. The content contains ONLY ref metadata — never
// the encrypted value.
func SecretRegistryRecord(ref *domain.SecretRef, deleted bool) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"service_id", ref.ServiceID.String()},
		{"name", ref.Name},
	}
	if ref.EnvironmentID != nil {
		tags = append(tags, gonostr.Tag{"environment_id", ref.EnvironmentID.String()})
	}

	payload := map[string]any{
		"id":                ref.ID.String(),
		"service_id":        ref.ServiceID.String(),
		"name":              ref.Name,
		"encryption_method": string(ref.EncryptionMethod),
		"version":           ref.Version,
		"created_by":        ref.CreatedBy,
		"deleted":           deleted,
	}
	if ref.EnvironmentID != nil {
		payload["environment_id"] = ref.EnvironmentID.String()
	}
	if !ref.CreatedAt.IsZero() {
		payload["created_at"] = ref.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !ref.UpdatedAt.IsZero() {
		payload["updated_at"] = ref.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	content, _ := json.Marshal(payload)
	return tags, string(content)
}
