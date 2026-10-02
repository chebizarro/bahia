package dns

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// AgentCapabilityChecker reads cached agent capabilities from NIP-38 health events.
type AgentCapabilityChecker interface {
	HasCapability(agentPubkey, capability string) bool
	IsHealthy(agentPubkey string) bool
}

// CapabilityAwareDNSBackend wraps a legacy ContextVM RPC backend with an
// event-publish backend, switching to events for agents that advertise the
// "zone-subscribe" capability. This enables zero-downtime rolling upgrades:
// new daemon + old agent uses RPC, new daemon + new agent uses events.
type CapabilityAwareDNSBackend struct {
	rpcBackend   Backend // DnsmasqAgentBackend (ContextVM RPC)
	eventBackend Backend // EventPublishDNSBackend (event publish)
	checker      AgentCapabilityChecker
	agentPubkey  string
	logger       *zap.Logger
}

var _ Backend = (*CapabilityAwareDNSBackend)(nil)

// NewCapabilityAwareDNSBackend creates a backend that delegates to the event
// backend when the agent advertises zone-subscribe, or falls back to the RPC
// backend otherwise.
func NewCapabilityAwareDNSBackend(
	rpcBackend Backend,
	eventBackend Backend,
	checker AgentCapabilityChecker,
	agentPubkey string,
	logger *zap.Logger,
) *CapabilityAwareDNSBackend {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CapabilityAwareDNSBackend{
		rpcBackend:   rpcBackend,
		eventBackend: eventBackend,
		checker:      checker,
		agentPubkey:  agentPubkey,
		logger:       logger.Named("capability-dns"),
	}
}

func (b *CapabilityAwareDNSBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypeDnsmasqAgent
}

// Health checks agent health. If the agent publishes NIP-38 status events,
// use the cached status. Otherwise fall back to the RPC health check.
func (b *CapabilityAwareDNSBackend) Health(ctx context.Context) error {
	if b.checker.IsHealthy(b.agentPubkey) {
		return nil
	}
	// Agent hasn't published health events (or expired) — fall back to RPC.
	return b.rpcBackend.Health(ctx)
}

func (b *CapabilityAwareDNSBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	if b.useEvents() {
		return b.eventBackend.ListRecords(ctx, zone)
	}
	return b.rpcBackend.ListRecords(ctx, zone)
}

func (b *CapabilityAwareDNSBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if b.useEvents() {
		b.logger.Debug("using event-publish for zone sync (agent has zone-subscribe capability)",
			zap.String("zone", zone.Name),
			zap.String("agent", b.agentPubkey))
		return b.eventBackend.SyncZone(ctx, zone, records)
	}
	b.logger.Debug("using RPC for zone sync (agent does not have zone-subscribe capability)",
		zap.String("zone", zone.Name),
		zap.String("agent", b.agentPubkey))
	return b.rpcBackend.SyncZone(ctx, zone, records)
}

// Close closes the RPC backend's transport (the event backend is stateless).
func (b *CapabilityAwareDNSBackend) Close() error {
	if closer, ok := b.rpcBackend.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (b *CapabilityAwareDNSBackend) useEvents() bool {
	if b.checker == nil {
		return false
	}
	return b.checker.HasCapability(b.agentPubkey, "zone-subscribe")
}

// RPCBackend returns the underlying RPC backend, used when explicitly needing
// the ContextVM transport (e.g. ListZoneState for initial zone state).
func (b *CapabilityAwareDNSBackend) RPCBackend() Backend {
	return b.rpcBackend
}

// ListZoneState delegates to the RPC backend if it implements DNSZoneStateObserver,
// since the event backend does not support this (the reconciler is the source of truth).
func (b *CapabilityAwareDNSBackend) ListZoneState(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, bool, error) {
	if b.useEvents() {
		// Event-based agents: the reconciler is the source of truth for records.
		// Return nil records with authoritative=false so the reconciler uses its
		// own projection and syncs the desired state.
		return nil, false, nil
	}
	if observer, ok := b.rpcBackend.(interface {
		ListZoneState(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, bool, error)
	}); ok {
		return observer.ListZoneState(ctx, zone)
	}
	records, err := b.rpcBackend.ListRecords(ctx, zone)
	return records, false, err
}

// RecordAgentPubkey exposes the agent's pubkey for subscription filter setup.
func (b *CapabilityAwareDNSBackend) RecordAgentPubkey() string {
	return b.agentPubkey
}

// CapabilityBackendError wraps errors with context about which path was used.
type CapabilityBackendError struct {
	Path string // "rpc" or "event"
	Err  error
}

func (e *CapabilityBackendError) Error() string {
	return fmt.Sprintf("dns backend (%s): %v", e.Path, e.Err)
}

func (e *CapabilityBackendError) Unwrap() error {
	return e.Err
}
