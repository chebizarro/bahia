// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	pool         *RelayPool
	privateKey   string
	enabled      bool
	logger       *zap.Logger
	eventRepo    repository.NostrEventRepository
	outboxRepo   repository.NostrEventOutboxRepository
	publishFn    func(context.Context, nostr.Event) ([]PublishResult, error)
	publishMu    sync.Mutex
	newBackoff   func() *Backoff
	idleInterval time.Duration
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

	publisher := &Publisher{
		pool:         pool,
		privateKey:   cfg.PrivateKey,
		enabled:      cfg.PublishEnabled && cfg.PrivateKey != "",
		logger:       logger,
		eventRepo:    eventRepo,
		publishFn:    pool.PublishWithResults,
		newBackoff:   DefaultBackoff,
		idleInterval: time.Second,
	}
	publisher.outboxRepo, _ = eventRepo.(repository.NostrEventOutboxRepository)
	return publisher
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

	rec := nostrEventRecordFromEvent(ev, label)
	if p.eventRepo != nil {
		if p.outboxRepo != nil {
			rec.PublishState = repository.NostrPublishStatePending
		}
		if _, recordErr := p.eventRepo.Record(ctx, rec); recordErr != nil {
			p.logger.Warn("failed to persist nostr event before publish",
				zap.String("event_id", ev.ID.Hex()),
				zap.Error(recordErr),
			)
			return
		}
	}

	attempt := p.publishOutboxEvent(ctx, ev)
	if attempt.err != nil {
		p.logger.Warn("failed to publish nostr event; retained for redelivery",
			zap.String("event_type", label),
			zap.String("event_id", ev.ID.Hex()),
			zap.Bool("rate_limited", attempt.rateLimited),
			zap.Error(attempt.err),
		)
		return
	}

	p.logger.Debug("nostr event published",
		zap.String("event_type", label),
		zap.String("event_id", ev.ID.Hex()),
		zap.Int("relays", attempt.published),
	)
}

type publishAttempt struct {
	published   int
	rateLimited bool
	err         error
}

func nostrEventRecordFromEvent(ev nostr.Event, entityType string) *repository.NostrEventRecord {
	tagsJSON, _ := json.Marshal(ev.Tags)
	return &repository.NostrEventRecord{
		ID:         ev.ID.Hex(),
		Kind:       int(ev.Kind),
		PubKey:     ev.PubKey.Hex(),
		Content:    ev.Content,
		Tags:       tagsJSON,
		Sig:        eventSignatureHex(&ev),
		CreatedAt:  ev.CreatedAt.Time(),
		ReceivedAt: time.Now().UTC(),
		EntityType: entityType,
	}
}

func (p *Publisher) publishOutboxEvent(ctx context.Context, ev nostr.Event) publishAttempt {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()

	if p.publishFn == nil {
		return p.recordPublishFailure(ctx, ev.ID.Hex(), nil, fmt.Errorf("relay publisher is not configured"))
	}
	results, publishErr := p.publishFn(ctx, ev)
	published := countSuccessfulPublishResults(results)
	if published > 0 {
		if p.outboxRepo != nil {
			if err := p.outboxRepo.MarkPublished(ctx, ev.ID.Hex(), time.Now().UTC()); err != nil {
				return publishAttempt{published: published, err: err}
			}
		}
		return publishAttempt{published: published}
	}
	return p.recordPublishFailure(ctx, ev.ID.Hex(), results, publishErr)
}

func (p *Publisher) recordPublishFailure(ctx context.Context, eventID string, results []PublishResult, publishErr error) publishAttempt {
	rateLimited := false
	details := make([]string, 0, len(results)+1)
	if publishErr != nil {
		details = append(details, publishErr.Error())
	}
	for _, result := range results {
		rateLimited = rateLimited || result.IsRateLimited()
		switch {
		case result.Error != nil:
			details = append(details, fmt.Sprintf("%s: %v", result.RelayURL, result.Error))
		case result.Reason != "":
			details = append(details, fmt.Sprintf("%s: %s", result.RelayURL, result.Reason))
		}
	}
	if len(details) == 0 {
		details = append(details, "no relay accepted the event")
	}
	failure := strings.Join(details, "; ")
	if p.outboxRepo != nil {
		if err := p.outboxRepo.RecordPublishFailure(ctx, eventID, failure); err != nil {
			failure += "; persist publish failure: " + err.Error()
		}
	}
	return publishAttempt{rateLimited: rateLimited, err: fmt.Errorf("%s", failure)}
}

func eventFromNostrRecord(rec repository.NostrEventRecord) (nostr.Event, error) {
	var ev nostr.Event
	if err := decodeEventHex(ev.ID[:], rec.ID, "id"); err != nil {
		return nostr.Event{}, err
	}
	if err := decodeEventHex(ev.PubKey[:], rec.PubKey, "pubkey"); err != nil {
		return nostr.Event{}, err
	}
	if err := decodeEventHex(ev.Sig[:], rec.Sig, "signature"); err != nil {
		return nostr.Event{}, err
	}
	if err := json.Unmarshal(rec.Tags, &ev.Tags); err != nil {
		return nostr.Event{}, fmt.Errorf("decode tags: %w", err)
	}
	ev.Kind = canonicalKind(rec.Kind)
	ev.Content = rec.Content
	ev.CreatedAt = nostr.Timestamp(rec.CreatedAt.Unix())
	return ev, nil
}

func decodeEventHex(dst []byte, value, field string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	if len(decoded) != len(dst) {
		return fmt.Errorf("decode %s: got %d bytes, want %d", field, len(decoded), len(dst))
	}
	copy(dst, decoded)
	return nil
}

// Name implements app.BackgroundRunner.
func (p *Publisher) Name() string { return "nostr-publish-outbox" }

// Run redelivers pending outbound events until the application context is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	if !p.enabled || p.outboxRepo == nil {
		<-ctx.Done()
		return nil
	}

	backoff := p.newBackoff()
	if backoff == nil {
		backoff = DefaultBackoff()
	}
	for {
		pending, failed, rateLimited, err := p.retryUnpublished(ctx)
		if ctx.Err() != nil {
			return nil
		}

		delay := p.idleInterval
		if err != nil || failed {
			delay = backoff.Next()
			p.logger.Warn("nostr outbox redelivery delayed",
				zap.Duration("delay", delay),
				zap.Bool("rate_limited", rateLimited),
				zap.Error(err),
			)
		} else {
			backoff.Reset()
			if pending > 0 {
				continue
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (p *Publisher) retryUnpublished(ctx context.Context) (pending int, failed bool, rateLimited bool, err error) {
	records, err := p.outboxRepo.ListUnpublished(ctx, 100)
	if err != nil {
		return 0, false, false, err
	}
	for _, rec := range records {
		ev, decodeErr := eventFromNostrRecord(rec)
		if decodeErr != nil {
			attempt := p.recordPublishFailure(ctx, rec.ID, nil, decodeErr)
			failed = true
			rateLimited = rateLimited || attempt.rateLimited
			continue
		}
		attempt := p.publishOutboxEvent(ctx, ev)
		if attempt.err != nil {
			failed = true
			rateLimited = rateLimited || attempt.rateLimited
			err = attempt.err
		}
	}
	return len(records), failed, rateLimited, err
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
