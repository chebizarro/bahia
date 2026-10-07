package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

// KindUserStatus is NIP-38 (kind 30315) for user/agent status publishing.
const KindUserStatus = nostr.Kind(30315)

// HealthPublisher periodically publishes the agent's health status as NIP-38
// kind 30315 events with NIP-40 expiration. The daemon can subscribe to these
// instead of polling via ContextVM Health() RPC.
type HealthPublisher struct {
	agent    *Agent
	signer   nostr.Signer
	publish  func(ctx context.Context, ev nostr.Event) error
	interval time.Duration
	expiry   time.Duration
	logger   *zap.Logger
}

// HealthPublisherConfig configures the health status publisher.
type HealthPublisherConfig struct {
	Agent  *Agent
	Signer nostr.Signer
	// Publish sends a signed event to relays. Typically wraps pool.Publish.
	Publish func(ctx context.Context, ev nostr.Event) error
	// Interval between health publishes (default: 30s).
	Interval time.Duration
	// Expiry is the NIP-40 expiration window (default: 2× interval).
	Expiry time.Duration
	Logger *zap.Logger
}

// NewHealthPublisher creates a health status publisher.
func NewHealthPublisher(cfg HealthPublisherConfig) *HealthPublisher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	expiry := cfg.Expiry
	if expiry <= 0 {
		expiry = 2 * interval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HealthPublisher{
		agent:    cfg.Agent,
		signer:   cfg.Signer,
		publish:  cfg.Publish,
		interval: interval,
		expiry:   expiry,
		logger:   logger.Named("health-publisher"),
	}
}

// Run publishes health status events at the configured interval until the
// context is cancelled.
func (h *HealthPublisher) Run(ctx context.Context) error {
	// Publish immediately on start.
	if err := h.publishOnce(ctx); err != nil {
		h.logger.Warn("initial health publish failed", zap.Error(err))
	}

	ticker := time.NewTicker(h.interval) //nolint:archticker // health heartbeat, not a reconcile poll
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := h.publishOnce(ctx); err != nil {
				h.logger.Warn("health publish failed", zap.Error(err))
			}
		}
	}
}

func (h *HealthPublisher) publishOnce(ctx context.Context) error {
	status := h.agent.Status()
	content, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal health status: %w", err)
	}

	now := time.Now()
	expiresAt := now.Add(h.expiry)

	ev := nostr.Event{
		Kind:      KindUserStatus,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "dns-agent"},
			{"t", "bahia"},
			{"t", "dns-agent-health"},
			// Advertise zone-subscribe capability so the daemon can choose
			// event-based zone sync instead of ContextVM RPC (C-34).
			{"capability", CapabilityZoneSubscribe, CapabilityZoneSubscribeVersion},
			// NIP-40 expiration: event becomes invalid after this time.
			{"expiration", strconv.FormatInt(expiresAt.Unix(), 10)},
		},
		Content: string(content),
	}

	if err := h.signer.SignEvent(ctx, &ev); err != nil {
		return fmt.Errorf("sign health event: %w", err)
	}
	if err := h.publish(ctx, ev); err != nil {
		return fmt.Errorf("publish health event: %w", err)
	}

	h.logger.Debug("published health status",
		zap.Bool("alive", status.Alive),
		zap.Int64("last_serial", status.LastApplySerial))
	return nil
}
