package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// DurableNIP98ReplayStore always claims in the preserved local ledger. When
// configured, the shared store additionally fences other API replicas. Local
// claims survive a later startup without SQL; independent writable ledgers
// without a shared store are not a supported multi-replica auth topology.
type DurableNIP98ReplayStore struct {
	local               *localstore.Outbox
	shared              NIP98ReplayStore
	initializationError error
}

func NewDurableNIP98ReplayStore(local *localstore.Outbox, shared NIP98ReplayStore) *DurableNIP98ReplayStore {
	store := &DurableNIP98ReplayStore{local: local, shared: shared}
	if local != nil && local.MovedAside() != "" {
		// Persist refusal across another restart; the in-memory MovedAside flag
		// alone would disappear after opening the newly created ledger again.
		store.initializationError = local.PutControlRecord("http-auth-replay-v1", "requires-recovery", []byte("corrupt-ledger"))
	}
	return store
}

func (s *DurableNIP98ReplayStore) Claim(ctx context.Context, eventID string, expiresAt time.Time) (bool, error) {
	if s == nil || s.local == nil {
		return false, fmt.Errorf("durable HTTP authentication replay store unavailable")
	}
	if s.initializationError != nil {
		return false, fmt.Errorf("HTTP authentication replay quarantine could not be persisted")
	}
	quarantine, err := s.local.GetControlRecord("http-auth-replay-v1", "requires-recovery")
	if err != nil {
		return false, fmt.Errorf("read HTTP authentication replay recovery state: %w", err)
	}
	if s.local.MovedAside() != "" || len(quarantine) > 0 {
		return false, fmt.Errorf("HTTP authentication replay ledger requires recovery after corruption")
	}
	claimed, err := s.local.ClaimHTTPAuth(ctx, eventID, expiresAt)
	if err != nil || !claimed {
		return false, err
	}
	if s.shared != nil {
		return s.shared.Claim(ctx, eventID, expiresAt)
	}
	return true, nil
}
