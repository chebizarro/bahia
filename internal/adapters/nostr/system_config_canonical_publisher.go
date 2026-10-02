package nostr

import (
	"context"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// SystemConfigCanonicalPublisher publishes system-configuration-derived records
// (DM relay lists, system discovery announcement, relay sets, NIP-65 preferences)
// through the shared signing and outbox pipeline. It replaces the unconditional
// startup and periodic snapshot publish of these records: publishing is now
// triggered once at startup (through warm-start comparison, which skips
// unchanged records) and event-driven when config changes.
//
// Phase 3 X1: these records were previously published by RepublishSnapshot's
// publishConfiguredDMRelayListsFromSystemConfig and publishSystemDiscovery.
type SystemConfigCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewSystemConfigCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewSystemConfigCanonicalPublisher(projector *Projector, logger *zap.Logger) *SystemConfigCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SystemConfigCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("system-config-canonical"),
	}
}

// PublishSystemConfig publishes all system-configuration-derived records: DM
// relay lists, system discovery announcement, relay sets, and NIP-65 relay
// preferences. It goes through the projector's fingerprint-dedupe pipeline,
// so unchanged records are not re-signed.
func (p *SystemConfigCanonicalPublisher) PublishSystemConfig(ctx context.Context, cfg *config.Config, mcpTransport bool) error {
	if p.projector == nil || !p.projector.Enabled() || cfg == nil {
		return nil
	}
	if err := p.projector.publishConfiguredDMRelayLists(ctx, cfg.Nostr.EnabledDMRelayLists()); err != nil {
		p.logger.Warn("publish DM relay-list projection failed", zap.Error(err))
		return err
	}
	if err := p.projector.publishSystemDiscovery(ctx); err != nil {
		p.logger.Warn("publish system discovery projection failed", zap.Error(err))
		return err
	}
	return nil
}

// PublishObservedDeploymentsAnnouncement publishes the system discovery
// announcement with refreshed observed-deployment data. Called from bus event
// handlers when runtime state changes.
func (p *SystemConfigCanonicalPublisher) PublishObservedDeploymentsAnnouncement(ctx context.Context, cfg *config.Config) error {
	if p.projector == nil || !p.projector.Enabled() || cfg == nil {
		return nil
	}
	if len(cfg.Nostr.BrowserRelayPolicyRelays()) == 0 {
		return nil
	}
	return p.projector.publishSystemDiscoveryAnnouncement(ctx, cfg)
}
