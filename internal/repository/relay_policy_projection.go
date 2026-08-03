package repository

import (
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
