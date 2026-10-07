package reconcile

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
)

// DNSCanonicalPublisher publishes authoritative DNS state records through the
// shared builder and outbox. Implementations live in the nostr adapter layer;
// the reconciler calls them after a material change so canonical records are
// published once per mutation, not on a timer (B-17, Phase 3 D1).
type DNSCanonicalPublisher interface {
	// PublishEndpoints publishes DNS endpoint state for all current endpoints
	// and tombstones for any endpoints that have been removed since the last
	// call. Returns (published, tombstoned, error).
	PublishEndpoints(ctx context.Context, endpoints []domain.DNSEndpoint) (int, int, error)

	// PublishZone publishes a DNS zone configuration record.
	PublishZone(ctx context.Context, zone domain.DNSZone) error

	// PublishZoneTombstone marks a zone as deleted.
	PublishZoneTombstone(ctx context.Context, zoneName string) error

	// PublishBackend publishes a DNS backend state record.
	PublishBackend(ctx context.Context, backend domain.DNSBackendState) error

	// PublishPolicy publishes a DNS policy state record.
	PublishPolicy(ctx context.Context, policy domain.DNSPolicy) error

	// PublishZoneSync publishes the full set of zone records for a zone so
	// that DNS agents can subscribe instead of receiving ContextVM pushes
	// (C-34, Phase 3 D1).
	PublishZoneSync(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}
