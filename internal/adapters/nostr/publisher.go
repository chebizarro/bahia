// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Nostr event kinds for Bahia outbound audit events.
const (
	KindBuildRegistered           = 31000
	KindArtifactRegistered        = 31001
	KindDeploymentCreated         = 31002
	KindDeploymentComplete        = 31003
	KindDriftDetected             = 31004
	KindObservation               = 31005
	KindServiceRegistryAudit      = 31006
	KindEnvironmentRegistryAudit  = 31007
	KindStateChangedAudit         = 31008
	KindRuntimeActionAudit        = 31009
	KindReconcileAudit            = 31010
	KindAdoptionAudit             = 31011
	KindDeploymentApprovalAudit   = 31012
	KindDeploymentRunAudit        = 31013
	KindLLMRouteRegistryAudit     = 31014
	KindLLMReleaseRegisteredAudit = 31015
	KindLLMDeploymentAudit        = 31016
	KindLLMRunAudit               = 31017
	KindLLMRouteStateAudit        = 31018
	KindLLMGatewayAudit           = 31019

	// DNS audit event kinds are reserved for future DNS audit events.
	KindDNSZoneSyncedAudit           = 31020
	KindDNSRecordChangedAudit        = 31021
	KindDNSDriftDetectedAudit        = 31022
	KindDNSEndpointRegisteredAudit   = 31023
	KindDNSEndpointDeregisteredAudit = 31024
)

// Canonical replaceable read-model kinds (3196x). Keep these values aligned
// with internal/controlplane/reactor.go without importing that package (the
// reactor already imports this adapter package for RelayPool).
const (
	KindServiceState              = 31961
	KindServiceRegistry           = 31962
	KindEnvironmentRegistry       = 31963
	KindLLMRouteRegistry          = 31964
	KindLLMRouteState             = 31965
	KindArtifactRegistry          = 31966
	KindDeploymentIntentRegistry  = 31967
	KindDeploymentRunRegistry     = 31968
	KindBuildRegistry             = 31969
	KindPolicyRegistry            = 31970
	KindPackageRepositoryRegistry = 31971
	KindPackageArtifactRegistry   = 31972
	KindPackagePromotionRegistry  = 31973
	KindSystemDiscovery           = 31974

	KindDNSZoneState     = 31975
	KindDNSEndpointState = 31976
	KindDNSPolicyState   = 31977
	KindDNSBackendState  = 31978

	KindMLModelRegistry             = 31980
	KindMLModelVersionRegistry      = 31981
	KindMLDatasetRegistry           = 31982
	KindMLRecipeRegistry            = 31983
	KindMLRecipeRunState            = 31984
	KindMLInferenceEndpointRegistry = 31985
	KindMLInferenceEndpointState    = 31986
	KindMLEvaluationExperimentState = 31987
	KindMLArtifactProvenanceGraph   = 31988
	KindMLRuntimeCapabilityProfile  = 31989
)

// Operator assistant event kinds. Keep these values aligned with
// internal/domain/assistant.go without importing that package.
const (
	KindAssistantSession       = 31990
	KindAssistantPromptRequest = 38420
	KindAssistantApproval      = 38421
	KindAssistantStatus        = 38422
	KindAssistantResult        = 38423
)

// Nostr event kinds for Bahia inbound command events (311xx series).
// DEPRECATED: These kinds are superseded by the control plane reactor's 596x series
// (see internal/controlplane/reactor.go). The 311xx series remains for backward
// compatibility with existing event processors but new implementations should use:
//   - KindDeployRequest (5961) instead of KindCmdIntentCreate (31102)
//   - KindDeploymentApproval (5966) instead of KindCmdIntentApprove/Reject (31103/31104)
//   - KindRollbackRequest (5962) instead of KindCmdRollbackRequest (31105)
//
// See docs/nostr-commands.md for tag structure and content format.
const (
	KindCmdBuildRegister    = 31100 // Deprecated: use reactor API
	KindCmdArtifactRegister = 31101 // Deprecated: use reactor API
	KindCmdIntentCreate     = 31102 // Deprecated: use KindDeployRequest (5961)
	KindCmdIntentApprove    = 31103 // Deprecated: use KindDeploymentApproval (5966)
	KindCmdIntentReject     = 31104 // Deprecated: use KindDeploymentApproval (5966)
	KindCmdRollbackRequest  = 31105 // Deprecated: use KindRollbackRequest (5962)
)

// Publisher bridges internal events to Nostr relay publication.
type Publisher struct {
	pool       *RelayPool
	privateKey string
	enabled    bool
	logger     *zap.Logger
	eventRepo  repository.NostrEventRepository
}

// NewPublisher creates a new Nostr event publisher.
// It shares a RelayPool for persistent connections. If pool is nil, a new one
// is created from config (for backward compatibility).
// eventRepo is optional; when non-nil, all published events are recorded to the audit table.
func NewPublisher(cfg config.NostrConfig, pool *RelayPool, eventRepo repository.NostrEventRepository, logger *zap.Logger) *Publisher {
	if pool == nil {
		pool = NewRelayPool(cfg.Relays, logger)
		pool.Connect(context.Background())
	}

	return &Publisher{
		pool:       pool,
		privateKey: cfg.PrivateKey,
		enabled:    cfg.PublishEnabled && cfg.PrivateKey != "",
		logger:     logger,
		eventRepo:  eventRepo,
	}
}

// Pool returns the underlying relay pool for sharing with other components.
func (p *Publisher) Pool() *RelayPool {
	return p.pool
}

// SetupSubscriptions registers the Nostr publisher as a handler for internal events.
func (p *Publisher) SetupSubscriptions(pub events.Publisher) {
	if !p.enabled {
		p.logger.Info("nostr publishing disabled")
		return
	}

	pub.Subscribe(events.EventBuildRegistered, func(ctx context.Context, e events.Event) {
		p.publishEvent(ctx, KindBuildRegistered, "build.registered", e)
	})
	pub.Subscribe(events.EventArtifactRegistered, func(ctx context.Context, e events.Event) {
		p.publishEvent(ctx, KindArtifactRegistered, "artifact.registered", e)
	})
	pub.Subscribe(events.EventDeploymentIntentCreated, func(ctx context.Context, e events.Event) {
		p.publishEvent(ctx, KindDeploymentCreated, "deployment.created", e)
	})
	pub.Subscribe(events.EventDeploymentRunCompleted, func(ctx context.Context, e events.Event) {
		p.publishEvent(ctx, KindDeploymentComplete, "deployment.completed", e)
	})
	pub.Subscribe(events.EventDriftDetected, func(ctx context.Context, e events.Event) {
		p.publishEvent(ctx, KindDriftDetected, "drift.detected", e)
	})
}

func (p *Publisher) publishEvent(ctx context.Context, kind int, label string, e events.Event) {
	content, err := json.Marshal(e.Data)
	if err != nil {
		p.logger.Error("failed to marshal event data", zap.Error(err))
		return
	}

	ev := nostr.Event{
		Kind:      kind,
		Content:   string(content),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"t", label},
			{"d", e.EntityID},
		},
	}

	if err := ev.Sign(p.privateKey); err != nil {
		p.logger.Error("failed to sign nostr event", zap.Error(err))
		return
	}

	published, err := p.pool.Publish(ctx, ev)
	if err != nil {
		p.logger.Warn("failed to publish nostr event", zap.String("event_type", label), zap.Error(err))
		return
	}

	// Record to audit table.
	if p.eventRepo != nil {
		tagsJSON, _ := json.Marshal(ev.Tags)
		rec := &repository.NostrEventRecord{
			ID:         ev.ID,
			Kind:       ev.Kind,
			PubKey:     ev.PubKey,
			Content:    ev.Content,
			Tags:       tagsJSON,
			Sig:        ev.Sig,
			CreatedAt:  ev.CreatedAt.Time(),
			EntityType: label,
		}
		if _, recordErr := p.eventRepo.Record(ctx, rec); recordErr != nil {
			p.logger.Warn("failed to record nostr event to audit table",
				zap.String("event_id", ev.ID),
				zap.Error(recordErr),
			)
		}
	}

	if published > 0 {
		p.logger.Debug("nostr event published",
			zap.String("event_type", label),
			zap.String("event_id", ev.ID),
			zap.Int("relays", published),
		)
	}
}

// Subscribe listens for incoming Nostr events on all connected relays.
// Deprecated: use Subscriber for production inbound handling; it supports scoped filters,
// EOSE state, persistence, and duplicate-safe handler invocation.
func (p *Publisher) Subscribe(ctx context.Context, kinds []int, handler func(ev *nostr.Event)) error {
	if !p.enabled {
		return nil
	}

	since := nostr.Timestamp(time.Now().Unix())
	if p.eventRepo != nil {
		latest, err := p.eventRepo.LatestCreatedAtForKinds(ctx, kinds)
		if err != nil {
			return err
		}
		if latest != nil {
			since = nostr.Timestamp(latest.Unix() - 1)
		}
	}

	filters := []nostr.Filter{{
		Kinds: kinds,
		Since: &since,
	}}

	merged, err := p.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return err
	}

	go func() {
		eoseCh := merged.EndOfStoredEvents
		for {
			select {
			case <-ctx.Done():
				return
			case <-eoseCh:
				eoseCh = nil
			case ev, ok := <-merged.Events:
				if !ok {
					return
				}
				handler(ev)
			}
		}
	}()

	return nil
}

// PublishWithResults publishes an already-signed event through the underlying relay pool.
func (p *Publisher) PublishWithResults(ctx context.Context, ev nostr.Event) ([]PublishResult, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("nostr publisher relay pool not configured")
	}
	return p.pool.PublishWithResults(ctx, ev)
}

// PublishSignedEvent signs and publishes an arbitrary Nostr event.
func (p *Publisher) PublishSignedEvent(ctx context.Context, ev *nostr.Event) error {
	_, err := p.PublishSignedEventWithResults(ctx, ev)
	return err
}

// PublishSignedEventWithResults signs and publishes an arbitrary Nostr event,
// returning per-relay publish outcomes from the underlying relay pool.
func (p *Publisher) PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]PublishResult, error) {
	if p == nil || p.pool == nil || ev == nil {
		return nil, nil
	}
	if p.privateKey == "" {
		return nil, fmt.Errorf("nostr publisher private key not configured")
	}
	if err := ev.Sign(p.privateKey); err != nil {
		return nil, fmt.Errorf("signing nostr event: %w", err)
	}
	return p.pool.PublishWithResults(ctx, *ev)
}

// Close shuts down the relay pool.
func (p *Publisher) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}
