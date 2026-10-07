package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// SecretOrgResolver resolves the org ID for secrets. Used by the secret
// publisher to route encryption through the correct per-org content key.
type SecretOrgResolver interface {
	ServiceOrgID(ctx context.Context, serviceID uuid.UUID) (string, error)
	SecretOrgID(ctx context.Context, secretID uuid.UUID) (string, error)
}

// SecretCanonicalPublisher publishes authoritative secret state records through
// the shared builder and outbox. Secret values are NEVER included in the
// published event — only the SecretRef metadata (name, version, scope).
//
// uses the unified confidential encryption path with per-org
// content key. The metadata is encrypted under the OCK so org members can see
// which secrets exist; actual secret values remain service-only.
type SecretCanonicalPublisher struct {
	projector   *Projector
	encryptor   ConfidentialStateEncryptor
	orgResolver SecretOrgResolver
	logger      *zap.Logger
}

// NewSecretCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewSecretCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, orgResolver SecretOrgResolver, logger *zap.Logger) *SecretCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SecretCanonicalPublisher{
		projector:   projector,
		encryptor:   encryptor,
		orgResolver: orgResolver,
		logger:      logger.Named("secret-canonical"),
	}
}

// PublishSecretRef publishes a canonical secret registry record containing
// only the SecretRef metadata (id, name, service_id, environment_id, version,
// encryption_method). The encrypted value is NEVER included.
func (p *SecretCanonicalPublisher) PublishSecretRef(ctx context.Context, ref domain.SecretRef) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	orgID, err := p.resolveOrgID(ctx, ref.ServiceID)
	if err != nil {
		return fmt.Errorf("resolve org for secret %s: %w", ref.ID, err)
	}

	dTag := SecretDTag(ref.ID)
	tags, content := SecretRegistryRecord(&ref, false)

	return p.publishConfidential(ctx, KindSecretRegistry, dTag, false, tags, content, "secret.projection", &ref.ID, orgID)
}

// PublishSecretDeleted publishes a tombstone for a deleted secret.
func (p *SecretCanonicalPublisher) PublishSecretDeleted(ctx context.Context, secretID uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	orgID, err := p.orgResolver.SecretOrgID(ctx, secretID)
	if err != nil {
		return fmt.Errorf("resolve org for secret %s: %w", secretID, err)
	}

	dTag := SecretDTag(secretID)
	contentJSON, _ := json.Marshal(map[string]any{"id": secretID.String(), "deleted": true})
	content := string(contentJSON)

	return p.publishConfidential(ctx, KindSecretRegistry, dTag, true, nil, content, "secret.projection", &secretID, orgID)
}

func (p *SecretCanonicalPublisher) publishConfidential(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content, entityType string, entityID *uuid.UUID, orgID string) error {
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish")
	}

	encrypted, err := p.encryptor.EncryptConfidential(ctx, orgID, []byte(content), legacyKind, dTag, topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt secret state: %w", err)
	}

	return p.projector.publishControlState(ctx, legacyKind, dTag, deleted, extraTags, encrypted, entityType, entityID)
}

func (p *SecretCanonicalPublisher) resolveOrgID(ctx context.Context, serviceID uuid.UUID) (string, error) {
	if p.orgResolver == nil {
		return "", fmt.Errorf("secret org resolver not configured")
	}
	return p.orgResolver.ServiceOrgID(ctx, serviceID)
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

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}
