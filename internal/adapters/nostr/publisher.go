// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Canonical Cascadia observable event kinds.
const (
	KindCASAudit        = kinds.CASAudit
	KindNIP38Status     = kinds.NIP38Status
	KindCASControlState = kinds.CASControlState
)

// Nostr event kinds for Bahia outbound audit events.
const (
	KindBuildRegistered           = kinds.BuildRegistered
	KindArtifactRegistered        = kinds.ArtifactRegistered
	KindDeploymentCreated         = kinds.DeploymentCreated
	KindDeploymentComplete        = kinds.DeploymentComplete
	KindDriftDetected             = kinds.DriftDetected
	KindObservation               = kinds.Observation
	KindServiceRegistryAudit      = kinds.ServiceRegistryAudit
	KindEnvironmentRegistryAudit  = kinds.EnvironmentRegistryAudit
	KindStateChangedAudit         = kinds.StateChangedAudit
	KindRuntimeActionAudit        = kinds.RuntimeActionAudit
	KindReconcileAudit            = kinds.ReconcileAudit
	KindAdoptionAudit             = kinds.AdoptionAudit
	KindDeploymentApprovalAudit   = kinds.DeploymentApprovalAudit
	KindDeploymentRunAudit        = kinds.DeploymentRunAudit
	KindLLMRouteRegistryAudit     = kinds.LLMRouteRegistryAudit
	KindLLMReleaseRegisteredAudit = kinds.LLMReleaseRegisteredAudit
	KindLLMDeploymentAudit        = kinds.LLMDeploymentAudit
	KindLLMRunAudit               = kinds.LLMRunAudit
	KindLLMRouteStateAudit        = kinds.LLMRouteStateAudit
	KindLLMGatewayAudit           = kinds.LLMGatewayAudit

	KindDNSZoneSyncedAudit           = kinds.DNSZoneSyncedAudit
	KindDNSRecordChangedAudit        = kinds.DNSRecordChangedAudit
	KindDNSDriftDetectedAudit        = kinds.DNSDriftDetectedAudit
	KindDNSEndpointRegisteredAudit   = kinds.DNSEndpointRegisteredAudit
	KindDNSEndpointDeregisteredAudit = kinds.DNSEndpointDeregisteredAudit
)

// Canonical replaceable read-model kinds are aliases to internal/kinds.
const (
	KindServiceState              = kinds.ServiceState
	KindServiceRegistry           = kinds.ServiceRegistry
	KindEnvironmentRegistry       = kinds.EnvironmentRegistry
	KindLLMRouteRegistry          = kinds.LLMRouteRegistry
	KindLLMRouteState             = kinds.LLMRouteState
	KindArtifactRegistry          = kinds.ArtifactRegistry
	KindDeploymentIntentRegistry  = kinds.DeploymentIntentRegistry
	KindDeploymentRunRegistry     = kinds.DeploymentRunRegistry
	KindBuildRegistry             = kinds.BuildRegistry
	KindPolicyRegistry            = kinds.PolicyRegistry
	KindPackageRepositoryRegistry = kinds.PackageRepositoryRegistry
	KindPackageArtifactRegistry   = kinds.PackageArtifactRegistry
	KindPackagePromotionRegistry  = kinds.PackagePromotionRegistry
	KindSystemDiscovery           = kinds.SystemDiscovery

	KindDNSZoneState     = kinds.DNSZoneState
	KindDNSEndpointState = kinds.DNSEndpointState
	KindDNSPolicyState   = kinds.DNSPolicyState
	KindDNSBackendState  = kinds.DNSBackendState

	KindMLModelRegistry             = kinds.MLModelRegistry
	KindMLModelVersionRegistry      = kinds.MLModelVersionRegistry
	KindMLDatasetRegistry           = kinds.MLDatasetRegistry
	KindMLRecipeRegistry            = kinds.MLRecipeRegistry
	KindMLRecipeRunState            = kinds.MLRecipeRunState
	KindMLInferenceEndpointRegistry = kinds.MLInferenceEndpointRegistry
	KindMLInferenceEndpointState    = kinds.MLInferenceEndpointState
	KindMLEvaluationExperimentState = kinds.MLEvaluationExperimentState
	KindMLArtifactProvenanceGraph   = kinds.MLArtifactProvenanceGraph
	KindMLRuntimeCapabilityProfile  = kinds.MLRuntimeCapabilityProfile

	KindWorkerState              = kinds.WorkerState
	KindWorkerAssignmentState    = kinds.WorkerAssignmentState
	KindWorkerDrainStatus        = kinds.WorkerDrainStatus
	KindWorkerEligibilityPreview = kinds.WorkerEligibilityPreview
)

// Continuity fabric event kinds are aliases to internal/kinds.
const (
	KindContinuityProfile      = kinds.ContinuityProfile
	KindFailoverPolicy         = kinds.FailoverPolicy
	KindStandbyNodeDefinition  = kinds.StandbyNodeDefinition
	KindReplicationPolicy      = kinds.ReplicationPolicy
	KindRecoveryWorkflow       = kinds.RecoveryWorkflow
	KindHeartbeatObservation   = kinds.HeartbeatObservation
	KindContinuityStatus       = kinds.ContinuityStatus
	KindDegradedModeActivation = kinds.DegradedModeActivation
	KindRecoveryProgress       = kinds.RecoveryProgress
	KindFailoverRequest        = kinds.FailoverRequest
	KindRecoveryRequest        = kinds.RecoveryRequest
)

// Operator assistant event kinds are aliases to internal/kinds.
const (
	KindAssistantSession       = kinds.AssistantSession
	KindAssistantPromptRequest = kinds.AssistantPromptRequest
	KindAssistantApproval      = kinds.AssistantApproval
	KindAssistantStatus        = kinds.AssistantStatus
	KindAssistantResult        = kinds.AssistantResult
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
		poolOpts := []RelayPoolOption(nil)
		if cfg.PrivateKey != "" {
			poolOpts = append(poolOpts, WithPrivateKey(cfg.PrivateKey))
		}
		pool = NewRelayPool(cfg.Relays, logger, poolOpts...)
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
		Kind:      canonicalKind(kind),
		Content:   string(content),
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"t", label},
			{"d", e.EntityID},
		},
	}

	if err := signEventWithPrivateKeyHex(&ev, p.privateKey); err != nil {
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
			ID:         ev.ID.Hex(),
			Kind:       int(ev.Kind),
			PubKey:     ev.PubKey.Hex(),
			Content:    ev.Content,
			Tags:       tagsJSON,
			Sig:        eventSignatureHex(&ev),
			CreatedAt:  ev.CreatedAt.Time(),
			EntityType: label,
		}
		if _, recordErr := p.eventRepo.Record(ctx, rec); recordErr != nil {
			p.logger.Warn("failed to record nostr event to audit table",
				zap.String("event_id", ev.ID.Hex()),
				zap.Error(recordErr),
			)
		}
	}

	if published > 0 {
		p.logger.Debug("nostr event published",
			zap.String("event_type", label),
			zap.String("event_id", ev.ID.Hex()),
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
		Kinds: filterKindsFromInts(kinds),
		Since: since,
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
	if err := signEventWithPrivateKeyHex(ev, p.privateKey); err != nil {
		return nil, err
	}
	return p.pool.PublishWithResults(ctx, *ev)
}

// Close shuts down the relay pool.
func (p *Publisher) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}
