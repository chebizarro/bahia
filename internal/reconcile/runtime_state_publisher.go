package reconcile

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// RuntimeStatePublisher publishes the canonical cp-state record of a
// service/environment runtime state pair (or its tombstone) to relays.
// The reconciler calls it after each material state change so the relay
// holds the authoritative state without projector re-projection.
//
// Implementations are fingerprint-deduped: publishing unchanged state is a
// no-op (returns nil without signing). Tombstones are never deduped.
//
// internal/adapters/nostr.RelayFirstStatePublisher satisfies this interface.
type RuntimeStatePublisher interface {
	// PublishState publishes the runtime state record for the given
	// service/environment state. observation is the latest observation linked
	// to the state; nil when none exists.
	PublishState(ctx context.Context, state *domain.EnvironmentServiceState, observation *domain.RuntimeObservation) error

	// PublishStateTombstone publishes a deletion marker for the
	// service/environment state coordinate so relay readers see the removal.
	PublishStateTombstone(ctx context.Context, serviceID, envID uuid.UUID) error
}
