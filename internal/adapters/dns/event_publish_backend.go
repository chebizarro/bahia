package dns

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ZoneSyncPublisher publishes zone sync events that DNS agents subscribe to.
type ZoneSyncPublisher interface {
	PublishZoneSync(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}

// EventPublishDNSBackend implements DNSBackend by publishing zone sync events
// instead of pushing zones via ContextVM RPC (C-34). DNS agents subscribe to
// these events from their local store rather than listening for ContextVM calls.
type EventPublishDNSBackend struct {
	publisher ZoneSyncPublisher
	logger    *zap.Logger
}

var _ Backend = (*EventPublishDNSBackend)(nil)

// NewEventPublishDNSBackend creates a backend that publishes zone sync events.
func NewEventPublishDNSBackend(publisher ZoneSyncPublisher, logger *zap.Logger) *EventPublishDNSBackend {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EventPublishDNSBackend{publisher: publisher, logger: logger.Named("dns-event-backend")}
}

func (b *EventPublishDNSBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeDnsmasqAgent
}

func (b *EventPublishDNSBackend) Health(ctx context.Context) error {
	// Agent health is read from NIP-38 status events; the backend does not
	// need to RPC for health. Return nil — the reconciler uses a separate
	// health poller for agents that publish status events.
	return nil
}

func (b *EventPublishDNSBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	// The reconciler's source of truth is its own projection; it does not
	// need to ask the agent for its current records. Return nil so the
	// reconciler treats the zone as empty-on-backend (will sync desired state).
	return nil, nil
}

func (b *EventPublishDNSBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := b.publisher.PublishZoneSync(ctx, zone, records); err != nil {
		return fmt.Errorf("publish zone sync for %q: %w", zone.Name, err)
	}
	b.logger.Info("published zone sync event", zap.String("zone", zone.Name), zap.Int("records", len(records)))
	return nil
}
