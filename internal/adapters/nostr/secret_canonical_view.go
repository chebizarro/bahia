package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// CanonicalSecretView reads secret and service state from the daemon's own
// retained canonical records in the local event store; it never consults
// PostgreSQL. It resolves the org a secret record is encrypted for and the
// current canonical secret record the secret intent handler reconciles
// against.
type CanonicalSecretView struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
}

// NewCanonicalSecretView returns a view over the projector's retained
// history, decrypting secret records with the confidential encryptor.
func NewCanonicalSecretView(projector *Projector, encryptor ConfidentialStateEncryptor) *CanonicalSecretView {
	return &CanonicalSecretView{projector: projector, encryptor: encryptor}
}

func (v *CanonicalSecretView) latest(ctx context.Context, legacyKind int, id string) (*repository.NostrEventRecord, error) {
	if v == nil || v.projector == nil {
		return nil, fmt.Errorf("canonical secret view is unavailable")
	}
	history, ok := v.projector.history.(ProjectionCoordinateHistory)
	if !ok {
		return nil, fmt.Errorf("canonical secret view needs the local event store")
	}
	record, err := history.LatestByCoordinate(ctx, KindCASControlState, canonicalStateDTag(legacyKind, id))
	if err != nil || record == nil {
		return nil, err
	}
	tags := recordTags(*record)
	if got := tagValue(tags, "legacy_kind"); got != strconv.Itoa(legacyKind) {
		return nil, fmt.Errorf("canonical coordinate %s holds family %q, not %d", id, got, legacyKind)
	}
	if isTombstoneTags(tags) {
		return nil, nil
	}
	return record, nil
}

// ServiceOrgID is the org of the service's current canonical record.
func (v *CanonicalSecretView) ServiceOrgID(ctx context.Context, serviceID uuid.UUID) (string, error) {
	record, err := v.latest(ctx, KindServiceRegistry, serviceID.String())
	if err != nil {
		return "", fmt.Errorf("resolve service %s: %w", serviceID, err)
	}
	if record == nil {
		return "", fmt.Errorf("service %s has no canonical record", serviceID)
	}
	var content struct {
		OrgID string `json:"org_id"`
	}
	if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
		return "", fmt.Errorf("decode service %s: %w", serviceID, err)
	}
	orgID, err := uuid.Parse(content.OrgID)
	if err != nil || orgID == uuid.Nil {
		return "", fmt.Errorf("service %s has no organization", serviceID)
	}
	return orgID.String(), nil
}

// SecretOrgID is the org of the service the secret's current canonical
// record names.
func (v *CanonicalSecretView) SecretOrgID(ctx context.Context, secretID uuid.UUID) (string, error) {
	ref, err := v.CurrentSecretRef(ctx, secretID)
	if err != nil {
		return "", err
	}
	if ref == nil {
		return "", fmt.Errorf("secret %s has no canonical record", secretID)
	}
	return v.ServiceOrgID(ctx, ref.ServiceID)
}

// CurrentSecretRef is the secret's current canonical record, or nil when it
// has none or was deleted.
func (v *CanonicalSecretView) CurrentSecretRef(ctx context.Context, secretID uuid.UUID) (*domain.SecretRef, error) {
	dTag := SecretDTag(secretID)
	record, err := v.latest(ctx, KindSecretRegistry, dTag)
	if err != nil || record == nil {
		return nil, err
	}
	if v.encryptor == nil {
		return nil, fmt.Errorf("canonical secret view has no confidential encryptor")
	}
	plaintext, err := v.encryptor.DecryptConfidential(ctx, record.Content, KindSecretRegistry, dTag, cpStateFamilies[KindSecretRegistry].topic)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret %s: %w", secretID, err)
	}
	var ref domain.SecretRef
	if err := json.Unmarshal(plaintext, &ref); err != nil {
		return nil, fmt.Errorf("decode secret %s: %w", secretID, err)
	}
	if ref.ID != secretID {
		return nil, fmt.Errorf("secret %s coordinate mismatch", secretID)
	}
	return &ref, nil
}
