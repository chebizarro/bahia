package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// RelayPolicyProjection is the durable server-side projection of one validated
// canonical relay-policy event. It is not relay discovery cache state and it is
// not a browser emergency override.
type RelayPolicyProjection struct {
	AuthorPubkey     string
	EventID          string
	EventCreatedAt   time.Time
	EventAcceptedAt  time.Time
	Schema           string
	CanonicalPayload json.RawMessage
	PayloadHash      string
	SourceRelay      string
	LastSyncAt       time.Time
	// RelayConfirmedAt is nil after restore. Only promotion of a valid
	// same-or-newer canonical relay event may mark the projection live again.
	RelayConfirmedAt *time.Time
}

// RelayPolicyProjectionRepository persists the last-known-good validated relay
// policy. Absence and synchronization failures have no operation that can
// delete or replace the accepted head.
type RelayPolicyProjectionRepository interface {
	Get(ctx context.Context, authorPubkey string) (*RelayPolicyProjection, error)
	Promote(ctx context.Context, projection RelayPolicyProjection) (bool, error)
	MarkSynced(ctx context.Context, authorPubkey string, syncedAt time.Time) error
}

// RelayPolicyProjectionShouldReplace is the executable form of the PostgreSQL
// ON CONFLICT ordering predicate used by Promote and RestoreCached.
func RelayPolicyProjectionShouldReplace(current, candidate RelayPolicyProjection) bool {
	if current.EventCreatedAt.Before(candidate.EventCreatedAt) {
		return true
	}
	if !current.EventCreatedAt.Equal(candidate.EventCreatedAt) {
		return false
	}
	if current.EventID > candidate.EventID {
		return true
	}
	return current.EventID == candidate.EventID &&
		current.Schema == candidate.Schema &&
		bytes.Equal(current.CanonicalPayload, candidate.CanonicalPayload) &&
		current.PayloadHash == candidate.PayloadHash &&
		current.RelayConfirmedAt == nil
}
