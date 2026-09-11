package repository

import (
	"context"
	"errors"
	"time"
)

var ErrContextVMResponseFingerprintConflict = errors.New("ContextVM response fingerprint conflicts with stored request")

// ContextVMResponseRecord is a terminal JSON-RPC response retained for
// idempotent replay. RequestFingerprint is empty only for legacy rows written
// before request binding was introduced. Response contains the complete
// marshaled response without its Nostr encryption envelope.
type ContextVMResponseRecord struct {
	RequesterPubkey    string
	Method             string
	ProgressToken      string
	RequestFingerprint string
	Response           []byte
	CreatedAt          time.Time
}

// ContextVMResponseStore persists terminal ContextVM responses across process
// restarts. Implementations must scope records by requester, method, and token,
// and Put must return ErrContextVMResponseFingerprintConflict rather than
// overwrite a non-legacy row with a different fingerprint.
type ContextVMResponseStore interface {
	Put(ctx context.Context, record ContextVMResponseRecord) error
	Get(ctx context.Context, requesterPubkey, method, progressToken string, createdAfter time.Time) (*ContextVMResponseRecord, error)
	DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
