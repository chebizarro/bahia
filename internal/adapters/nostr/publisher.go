// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
//
// Two thresholds apply to every outbound event:
//   - Caller success: a publish call succeeds once nostr.publish_quorum write
//     relays (default 1; -1 = all) have accepted. Below the quorum it returns
//     ErrPublishIncomplete, and the event stays queued for retry either way.
//   - Delivery completion: acceptance is tracked per relay, and relays that
//     have not accepted keep being retried with backoff up to a bounded attempt
//     budget. The outbox row is marked published only once every write relay
//     has accepted or reached a terminal state.
//
// Duplicate OK counts as acceptance; blocked:, invalid: and pow: rejections
// are terminal for the relay that sent them.
type Publisher struct {
	pool         *RelayPool
	privateKey   string
	enabled      bool
	logger       *zap.Logger
	eventRepo    repository.NostrEventRepository
	outboxRepo   repository.NostrEventOutboxRepository
	publishFn    func(ctx context.Context, ev nostr.Event, relayURLs []string) ([]PublishResult, error)
	relayURLs    func() []string
	newBackoff   func() *Backoff
	idleInterval time.Duration
	now          func() time.Time
	quorum       int
	maxAttempts  int
	pageSize     int
	// inlineOnly marks a publisher that has no redelivery runner of its own
	// (see WithInlineDeliveryOnly).
	inlineOnly bool

	// deliveriesMu guards the deliveries map and each delivery's nextAt.
	deliveriesMu sync.Mutex
	deliveries   map[string]*outboxDelivery
	// running is set while Run is active; only then are partially delivered
	// events kept in memory for retry by this publisher.
	running atomic.Bool
	// wake nudges Run to recompute its next retry time.
	wake chan struct{}
	// outboxCursor is the runner's keyset position in the pending outbox. It
	// is only touched by the Run goroutine.
	outboxCursor *repository.NostrOutboxCursor
}

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithInlineDeliveryOnly marks a publisher whose Run is not registered and
// whose pool differs from the outbox runner's pool (for example a
// control-plane-only publisher sharing the daemon outbox). It cannot retry, and
// its pending rows must not be adopted by the runner of a different relay set,
// so once an inline round reaches the publish quorum the row is marked
// published and relays that have not accepted are logged, not retried. Rows
// below the quorum stay pending for the outbox runner, as before.
func WithInlineDeliveryOnly() PublisherOption {
	return func(p *Publisher) { p.inlineOnly = true }
}

// NewPublisher creates a new Nostr event publisher.
// It shares a RelayPool for persistent connections. If pool is nil, a new one
// is created from config (for backward compatibility).
// eventRepo is optional; when non-nil, all published events are recorded to the audit table.
func NewPublisher(cfg config.NostrConfig, pool *RelayPool, eventRepo repository.NostrEventRepository, logger *zap.Logger, opts ...PublisherOption) *Publisher {
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
		publishFn:    pool.PublishToRelaysWithResults,
		relayURLs:    pool.URLs,
		newBackoff:   DefaultBackoff,
		idleInterval: time.Second,
		now:          time.Now,
		quorum:       cfg.PublishQuorum,
		maxAttempts:  defaultMaxPublishAttempts,
		pageSize:     defaultOutboxPageSize,
		deliveries:   make(map[string]*outboxDelivery),
		wake:         make(chan struct{}, 1),
	}
	publisher.outboxRepo, _ = eventRepo.(repository.NostrEventOutboxRepository)
	for _, opt := range opts {
		if opt != nil {
			opt(publisher)
		}
	}
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
		zap.Int("relays", attempt.accepted),
	)
}

type publishAttempt struct {
	results     []PublishResult
	accepted    int
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

// publishOutboxEvent runs a delivery round for ev. It returns nil error once
// the publish quorum has accepted the event. Relays that have not accepted are
// retried by Run (in memory, and from the durable outbox when one is
// configured).
func (p *Publisher) publishOutboxEvent(ctx context.Context, ev nostr.Event) publishAttempt {
	d, _ := p.trackDelivery(ev, 0)
	d.mu.Lock()
	report := p.deliverRound(ctx, d)
	if p.inlineOnly && report.delivered && !d.settled {
		p.settleInlineDelivery(ctx, d, report.detail)
	}
	settled := d.settled
	d.mu.Unlock()
	if settled || !p.running.Load() {
		// Without an active runner this publisher cannot retry from memory;
		// a pending outbox row remains for whichever runner owns the outbox.
		p.forgetDelivery(d)
	} else {
		p.nudge()
	}
	return publishAttempt{results: report.results, accepted: report.accepted, rateLimited: report.rateLimited, err: report.err}
}

// settleInlineDelivery finishes a quorum-accepted event for an inline-only
// publisher, which has no runner to retry the remaining relays. The caller
// must hold d.mu.
func (p *Publisher) settleInlineDelivery(ctx context.Context, d *outboxDelivery, detail string) {
	eventID := d.event.ID.Hex()
	if p.outboxRepo != nil {
		if err := p.outboxRepo.MarkPublished(ctx, eventID, p.now().UTC()); err != nil {
			p.logger.Warn("failed to persist nostr publish state", zap.String("event_id", eventID), zap.Error(err))
			return
		}
	}
	d.settled = true
	p.logger.Warn("nostr event accepted by publish quorum; inline-only publisher will not retry remaining relays",
		zap.String("event_id", eventID),
		zap.String("detail", detail),
	)
}

func (p *Publisher) nudge() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
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

// Run retries relays that have not accepted tracked events and discovers
// pending outbox rows until the application context is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	if !p.enabled {
		<-ctx.Done()
		return nil
	}
	p.running.Store(true)
	defer p.running.Store(false)

	discoveryBackoff := p.newBackoff()
	if discoveryBackoff == nil {
		discoveryBackoff = DefaultBackoff()
	}
	for {
		rateLimited := p.redeliverDue(ctx)
		more, err := p.discoverPending(ctx)
		if ctx.Err() != nil {
			return nil
		}

		delay := p.idleInterval
		switch {
		case err != nil:
			delay = discoveryBackoff.Next()
			p.logger.Warn("nostr outbox discovery delayed",
				zap.Duration("delay", delay),
				zap.Bool("rate_limited", rateLimited),
				zap.Error(err),
			)
		case more:
			discoveryBackoff.Reset()
			delay = 0
		default:
			discoveryBackoff.Reset()
		}
		if _, next := p.dueDeliveries(p.now()); !next.IsZero() {
			// An overdue retry (next in the past) runs immediately.
			if untilNext := next.Sub(p.now()); untilNext < delay {
				delay = max(untilNext, 0)
			}
		}
		if delay == 0 {
			continue
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-p.wake:
			timer.Stop()
		case <-timer.C:
		}
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
// returning per-relay publish outcomes from the underlying relay pool. When an
// outbox repository is configured, the signed event is durable before the first
// relay attempt and failed delivery is left pending for the Publisher runner.
func (p *Publisher) PublishSignedEventWithResults(ctx context.Context, ev *nostr.Event) ([]PublishResult, error) {
	if p == nil || ev == nil {
		return nil, nil
	}
	if p.privateKey == "" {
		return nil, fmt.Errorf("nostr publisher private key not configured")
	}
	if err := signEventWithPrivateKeyHex(ev, p.privateKey); err != nil {
		return nil, err
	}

	if p.eventRepo != nil {
		rec := nostrEventRecordFromEvent(*ev, signedEventAuditLabel(*ev))
		if p.outboxRepo != nil {
			rec.PublishState = repository.NostrPublishStatePending
		}
		if _, err := p.eventRepo.Record(ctx, rec); err != nil {
			return nil, fmt.Errorf("persist signed nostr event before publish: %w", err)
		}
	}

	attempt := p.publishOutboxEvent(ctx, *ev)
	return attempt.results, attempt.err
}

func signedEventAuditLabel(ev nostr.Event) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && strings.TrimSpace(tag[1]) != "" {
			return strings.TrimSpace(tag[1])
		}
	}
	return "nostr.signed"
}

// Close shuts down the relay pool.
func (p *Publisher) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}
