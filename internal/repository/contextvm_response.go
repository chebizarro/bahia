package repository

import (
	"context"
	"time"
)

// ContextVMResponseRecord is a terminal JSON-RPC response retained for
// idempotent replay. Response contains the complete marshaled response without
// its Nostr encryption envelope.
type ContextVMResponseRecord struct {
	RequesterPubkey string
	Method          string
	ProgressToken   string
	Response        []byte
	CreatedAt       time.Time
}

// ContextVMResponseStore persists terminal ContextVM responses across process
// restarts. Implementations must scope records by requester, method, and token.
type ContextVMResponseStore interface {
	Put(ctx context.Context, record ContextVMResponseRecord) error
	Get(ctx context.Context, requesterPubkey, method, progressToken string, createdAfter time.Time) (*ContextVMResponseRecord, error)
	DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
