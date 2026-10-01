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
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Canonical Cascadia observable event kinds.
const (
	KindCASAudit        = kinds.CASAudit
	KindNIP38Status     = kinds.NIP38Status
	KindCASControlState = kinds.CASControlState
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

	KindWorkerState              = int(kinds.CPStateFamilyWorkerState)
	KindWorkerAssignmentState    = int(kinds.CPStateFamilyWorkerAssignment)
	KindWorkerDrainStatus        = int(kinds.CPStateFamilyWorkerDrain)
	KindWorkerEligibilityPreview = int(kinds.CPStateFamilyWorkerEligibility)
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
//     ErrPublishIncomplete while the event stays queued for retry, or
//     ErrPublishAbandoned once the quorum is unreachable and the entry is
//     failed. OnDelivered and OnDeliveryAbandoned report the same outcomes when
//     the runner reaches them later.
//   - Delivery completion: acceptance is tracked per relay, and relays that
//     have not accepted keep being retried with backoff up to a bounded attempt
//     budget. The outbox entry settles (published or failed) only once every
//     write relay has accepted or reached a terminal state.
//
// Duplicate OK counts as acceptance; blocked:, invalid: and pow: rejections
// are terminal for the relay that sent them.
//
// The outbox is the daemon's local outbox (WithLocalOutbox, bahia-irsry.10.4):
// the signed event is durable there before the first relay attempt, and every
// counted round commits each relay's state, so a restart resumes exactly where
// delivery stopped without resending to relays that already accepted. No
// PostgreSQL write gates a publish. When PostgreSQL is configured (eventRepo),
// each event is also archived to nostr_events with its outcome mirrored, best
// effort, for the PostgreSQL-backed readers.
//
// PostgreSQL outbox rows are drained in place by the same runner: rows that
// producers write inside a PostgreSQL transaction with the domain change they
// audit (a local outbox cannot join that transaction), and rows left pending
// by a daemon that predates the local outbox. Their per-relay acceptance is
// kept in memory only, so after a restart those rows are resent to every
// write relay and relays that already accepted answer OK "duplicate:". The
// same applies to a row published before this publisher's Run is active.
//
// Every outbox entry a Publisher writes carries its publish target (see
// WithPublishTarget), and its Run only drains entries and rows for that
// target, so each event is retried to the relays of the pool it was written
// for. Run one registered Publisher per target.
type Publisher struct {
	pool       *RelayPool
	privateKey string
	enabled    bool
	logger     *zap.Logger
	// eventRepo is the optional PostgreSQL nostr_events table. Without a local
	// outbox it is the outbox itself; with one it is a best-effort archive.
	eventRepo repository.NostrEventRepository
	// outboxRepo is eventRepo's publish-state extension: the PostgreSQL rows
	// this publisher drains (see the type comment).
	outboxRepo repository.NostrEventOutboxRepository
	// localOutbox, when set, owns the delivery of every event this publisher
	// is asked to publish.
	localOutbox *localstore.Outbox
	// ownEvents is the daemon's local event store. Each published event is
	// kept there as the daemon's latest output, and removed again if its
	// delivery is abandoned (the Projector hydrates its dedupe from it, B-3).
	ownEvents *localstore.Store
	// archive mirrors locally delivered events into eventRepo.
	archive      *postgresArchive
	publishFn    func(ctx context.Context, ev nostr.Event, relayURLs []string) ([]PublishResult, error)
	relayURLs    func() []string
	newBackoff   func() *Backoff
	idleInterval time.Duration
	now          func() time.Time
	quorum       int
	maxAttempts  int
	pageSize     int
	// target is the publish target recorded on this publisher's outbox rows
	// and the only target its runner discovers.
	target string

	// deliveriesMu guards the deliveries map and each delivery's nextAt.
	deliveriesMu sync.Mutex
	deliveries   map[string]*outboxDelivery
	// running is set while Run is active; only then are partially delivered
	// events kept in memory for retry by this publisher.
	running atomic.Bool
	// wake nudges Run to recompute its next retry time.
	wake chan struct{}
	// outboxCursor and localCursor are the runner's keyset positions in the
	// PostgreSQL and local pending outboxes; lastPrune is when it last pruned
	// settled local entries. Only the Run goroutine touches them.
	outboxCursor *repository.NostrOutboxCursor
	localCursor  *localstore.OutboxCursor
	lastPrune    time.Time
	// handlersMu guards the delivery outcome handlers (see OnDelivered and
	// OnDeliveryAbandoned).
	handlersMu        sync.RWMutex
	abandonedHandlers []func(nostr.Event)
	deliveredHandlers []func(nostr.Event)
}

// Settled local outbox entries are kept this long so a producer that stores
// an event id after the outbox settled it still reads the outcome (see
// DeliveryOutcome), and failed ones for the BahiaNostrOutboxFailed alert.
const (
	publishedOutboxRetention = 24 * time.Hour
	failedOutboxRetention    = 7 * 24 * time.Hour
	outboxPruneInterval      = time.Hour
)

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithLocalOutbox delivers every event this publisher is asked to publish
// from the local outbox, and keeps the daemon's own outputs in its local
// event store (both may be shared by several publishers). The PostgreSQL
// repository given to NewPublisher, if any, becomes a best-effort archive,
// and its pending rows are still drained (see the Publisher comment).
func WithLocalOutbox(outbox *localstore.Outbox, ownEvents *localstore.Store) PublisherOption {
	return func(p *Publisher) {
		p.localOutbox = outbox
		p.ownEvents = ownEvents
	}
}

// WithPublishTarget binds the publisher to a named publish target
// (repository.NostrPublishTarget*), which must identify the relay pool the
// publisher was built with. Its outbox rows record the target and its Run only
// redelivers rows for that target, so a pool that is not the daemon interop
// pool gets its own retry loop instead of leaking rows to another pool's
// runner. Exactly one registered publisher may own each target. A publisher
// bound to a non-default target redelivers even when nostr.publish_enabled is
// off: that flag gates the daemon's audit-event bridge, while rows for a
// dedicated target exist only because a caller asked for them to be delivered.
func WithPublishTarget(target string) PublisherOption {
	return func(p *Publisher) { p.target = target }
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
	if publisher.localOutbox != nil {
		publisher.archive = newPostgresArchive(eventRepo, logger)
	}
	return publisher
}

// Pool returns the underlying relay pool for sharing with other components.
func (p *Publisher) Pool() *RelayPool {
	return p.pool
}

// OnDeliveryAbandoned registers fn to be called for every event this
// publisher abandons: the outbox entry moved to failed because the publish
// quorum can no longer be reached. Every registered handler is called, in
// registration order, synchronously and while the delivery is still locked,
// whether the abandonment happened in the caller's first round (which also
// returns ErrPublishAbandoned) or later in Run. A handler receives every
// abandoned event of this publisher and must ignore events it does not own;
// it must be quick and must not publish.
//
// The Projector uses it to drop the dedupe entry for a coordinate whose latest
// event never reached the quorum, so the next repair re-signs it; producers
// that keep publish state of their own (Security publications, SBOM
// manifests) use it to record a terminal failure for an event they were told
// was queued. A nil fn is ignored.
func (p *Publisher) OnDeliveryAbandoned(fn func(nostr.Event)) {
	if fn == nil {
		return
	}
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.abandonedHandlers = append(p.abandonedHandlers, fn)
}

// OnDelivered registers fn to be called when the publish quorum has accepted
// an event of this publisher and that is durably recorded, whether in the
// caller's first round (which also returns nil) or later in Run, and even
// while other relays are still being retried. It is the counterpart of
// OnDeliveryAbandoned with the same calling rules: handlers run synchronously
// in registration order, receive every delivered event of this publisher,
// must ignore events they do not own, must be quick and must not publish.
//
// Delivery is reported at least once: an event whose quorum was reached
// before a restart is reported again when the runner resumes it, so handlers
// must be idempotent. Producers that recorded an event as queued (Security
// publications, SBOM manifests) use it to move it to published. A nil fn is
// ignored.
func (p *Publisher) OnDelivered(fn func(nostr.Event)) {
	if fn == nil {
		return
	}
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.deliveredHandlers = append(p.deliveredHandlers, fn)
}

func (p *Publisher) notifyAbandoned(ev nostr.Event) {
	p.handlersMu.RLock()
	handlers := p.abandonedHandlers
	p.handlersMu.RUnlock()
	for _, fn := range handlers {
		fn(ev)
	}
}

func (p *Publisher) notifyDelivered(ev nostr.Event) {
	p.handlersMu.RLock()
	handlers := p.deliveredHandlers
	p.handlersMu.RUnlock()
	for _, fn := range handlers {
		fn(ev)
	}
}

type publishAttempt struct {
	results     []PublishResult
	accepted    int
	rateLimited bool
	// settled reports that delivery finished in this round: every relay
	// accepted or reached a terminal state, so nothing is left to retry.
	settled bool
	err     error
}

func nostrEventRecordFromEvent(ev nostr.Event, entityType string, entityID *uuid.UUID) *repository.NostrEventRecord {
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
		EntityID:   entityID,
	}
}

// markOutbound makes rec a pending PostgreSQL outbox row for this publisher's
// target when PostgreSQL is the outbox.
func (p *Publisher) markOutbound(rec *repository.NostrEventRecord) {
	if p.outboxRepo == nil {
		return
	}
	rec.PublishState = repository.NostrPublishStatePending
	rec.PublishTarget = p.target
}

// publishOutboxEvent runs a delivery round for ev. It returns nil error once
// the publish quorum has accepted the event. Relays that have not accepted are
// retried by Run (in memory, and from the durable outbox when one is
// configured).
func (p *Publisher) publishOutboxEvent(ctx context.Context, ev nostr.Event) publishAttempt {
	d, _ := p.trackDelivery(ev, 0)
	d.mu.Lock()
	report := p.deliverRound(ctx, d)
	settled := d.settled
	d.mu.Unlock()
	if settled || !p.running.Load() {
		// Without an active runner this publisher cannot retry from memory;
		// the pending outbox row remains for this target's runner.
		p.forgetDelivery(d)
	} else {
		p.nudge()
	}
	return publishAttempt{results: report.results, accepted: report.accepted, rateLimited: report.rateLimited, settled: settled, err: report.err}
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

// Name implements app.BackgroundRunner. Each publish target has its own
// runner name.
func (p *Publisher) Name() string {
	if p.target == repository.NostrPublishTargetDefault {
		return "nostr-publish-outbox"
	}
	return "nostr-publish-outbox:" + p.target
}

// Target returns the publish target this publisher records and redelivers.
func (p *Publisher) Target() string { return p.target }

// redeliveryEnabled reports whether Run retries this publisher's rows. The
// default target keeps the nostr.publish_enabled gate; see WithPublishTarget.
func (p *Publisher) redeliveryEnabled() bool {
	return p.enabled || p.target != repository.NostrPublishTargetDefault
}

// Run retries relays that have not accepted tracked events and discovers
// pending outbox rows for this publisher's target until the application
// context is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	if !p.redeliveryEnabled() {
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
		p.pruneLocalOutbox()
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
//
// Error contract (shared by PublishPresignedEvent and PublishProjection): nil
// means the publish quorum accepted; an error wrapping ErrPublishIncomplete
// means the row is pending and relays that have not accepted are still being
// retried; an error wrapping ErrPublishAbandoned means the first round already
// made the quorum unreachable (permanent relay rejections) and the row is
// failed; any other error means the event was never queued.
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
	attempt, err := p.enqueueAndDeliver(ctx, *ev, signedEventAuditLabel(*ev), nil)
	if err != nil {
		return nil, err
	}
	return attempt.results, attempt.err
}

// PublishPresignedEvent delivers an event that was signed elsewhere (for
// example by an operator's remote signer) through this publisher's outbox and
// relay pool, with the same durability, per-relay retry and quorum semantics
// as PublishSignedEventWithResults. entityType labels the outbox row. The
// event is not re-signed; an event whose id or signature does not verify is
// refused before anything is recorded.
func (p *Publisher) PublishPresignedEvent(ctx context.Context, ev nostr.Event, entityType string) ([]PublishResult, error) {
	if p == nil {
		return nil, fmt.Errorf("nostr publisher not configured")
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return nil, fmt.Errorf("presigned nostr event %s has an invalid id or signature", ev.ID.Hex())
	}
	if strings.TrimSpace(entityType) == "" {
		entityType = signedEventAuditLabel(ev)
	}
	attempt, err := p.enqueueAndDeliver(ctx, ev, entityType, nil)
	if err != nil {
		return nil, err
	}
	return attempt.results, attempt.err
}

// PublishProjection delivers a read-model event the Projector signed with the
// daemon key through this publisher's outbox, recording the projected entity
// on the row. It implements ProjectionPublisher with the error contract of
// PublishSignedEventWithResults: any error other than ErrPublishIncomplete
// means the event is not being retried, including ErrPublishAbandoned.
func (p *Publisher) PublishProjection(ctx context.Context, ev nostr.Event, entityType string, entityID *uuid.UUID) error {
	if p == nil {
		return fmt.Errorf("nostr publisher not configured")
	}
	attempt, err := p.enqueueAndDeliver(ctx, ev, entityType, entityID)
	if err != nil {
		return err
	}
	return attempt.err
}

// enqueueAndDeliver makes a signed event durable in this publisher's outbox
// (before the first relay attempt) and runs the first delivery round.
// Admission is idempotent by event id: re-enqueueing an event that is already
// held adds nothing. The returned error covers only admission; the delivery
// outcome is in the attempt.
//
// The in-memory delivery is registered before the entry becomes durable, so a
// concurrent runner discovery pass that lists the new entry sees it as tracked
// and skips it, instead of starting a second delivery with empty per-relay
// state that would resend to relays the first round has already covered.
func (p *Publisher) enqueueAndDeliver(ctx context.Context, ev nostr.Event, entityType string, entityID *uuid.UUID) (publishAttempt, error) {
	d, created := p.trackDelivery(ev, 0)
	if err := p.admit(ctx, ev, entityType, entityID, nil); err != nil {
		if created {
			p.forgetDelivery(d)
		}
		return publishAttempt{}, err
	}
	return p.publishOutboxEvent(ctx, ev), nil
}

// Enqueue makes an already-signed event durable in this publisher's local
// outbox without delivering it inline; the runner delivers it. It is for
// producers that record an event as "pending delivery" and carry on, such as
// the PostgreSQL-less event repository (see LocalEventRepository). The event
// must verify.
func (p *Publisher) Enqueue(ctx context.Context, ev nostr.Event, entityType string, entityID *uuid.UUID) error {
	if p == nil || p.localOutbox == nil {
		return fmt.Errorf("nostr publisher has no local outbox")
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return fmt.Errorf("nostr event %s has an invalid id or signature", ev.ID.Hex())
	}
	if err := p.admit(ctx, ev, entityType, entityID, nil); err != nil {
		return err
	}
	p.nudge()
	return nil
}

// admit makes ev durable in the outbox that delivers it: the local outbox
// when configured (then also the daemon's own event store and the PostgreSQL
// archive, both best effort), else the PostgreSQL outbox, else the
// PostgreSQL audit table. prior, when non-nil, is a delivery round that ran
// before admission (see PublishBeforeCommit): the local outbox entry starts
// from its per-relay state, round count and delivered flag, so no relay that
// accepted is contacted again. A PostgreSQL row keeps per-relay state in
// memory only (see the Publisher comment).
func (p *Publisher) admit(ctx context.Context, ev nostr.Event, entityType string, entityID *uuid.UUID, prior *outboxDelivery) error {
	switch {
	case p.localOutbox != nil:
		entry := localstore.OutboxEntry{Event: ev, Target: p.target, EntityType: entityType, EnqueuedAt: p.now()}
		if entityID != nil {
			entry.EntityID = entityID.String()
		}
		if prior != nil {
			entry.Rounds = prior.rounds
			entry.Delivered = prior.delivered
			entry.Relays = prior.relayDeliveries()
		}
		if _, err := p.localOutbox.Enqueue(entry); err != nil {
			return fmt.Errorf("persist signed nostr event before publish: %w", err)
		}
		p.keepOwnEvent(ev)
		p.archive.write(ctx, "outbound event", ev.ID.Hex(), func(ctx context.Context, repo repository.NostrEventRepository) error {
			rec := nostrEventRecordFromEvent(ev, entityType, entityID)
			rec.PublishState = repository.NostrPublishStatePending
			rec.PublishTarget = repository.LocalOutboxArchiveTarget(p.target)
			_, err := repo.Record(ctx, rec)
			return err
		})
	case p.eventRepo != nil:
		rec := nostrEventRecordFromEvent(ev, entityType, entityID)
		p.markOutbound(rec)
		if _, err := p.eventRepo.Record(ctx, rec); err != nil {
			return fmt.Errorf("persist signed nostr event before publish: %w", err)
		}
	}
	return nil
}

// keepOwnEvent stores ev in the daemon's local event store as its latest
// output. The store is a cache, so a failure is only logged.
func (p *Publisher) keepOwnEvent(ev nostr.Event) {
	if p.ownEvents == nil {
		return
	}
	if _, err := p.ownEvents.SaveEvent(ev); err != nil {
		p.logger.Warn("failed to keep published event in the local event store", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}

// forgetOwnEvent removes an abandoned event from the local event store: it
// never reached the quorum, so it must not count as the daemon's output.
func (p *Publisher) forgetOwnEvent(ev nostr.Event) {
	if p.ownEvents == nil {
		return
	}
	if err := p.ownEvents.DeleteEvent(ev.ID); err != nil {
		p.logger.Warn("failed to drop abandoned event from the local event store", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}

// DeliveryOutcome reports what this publisher's outbox knows about the event
// with id (hex). A producer that records an event id after publishing calls it
// once the id is stored, so an outcome the outbox reached in between (before
// OnDelivered or OnDeliveryAbandoned could find the producer's row) is not
// lost (bahia-irsry.40). Settled local entries stay readable for a day.
func (p *Publisher) DeliveryOutcome(ctx context.Context, id string) (nostrutil.DeliveryOutcome, error) {
	if p == nil {
		return nostrutil.DeliveryUnknown, nil
	}
	parsed, err := nostr.IDFromHex(id)
	if err != nil {
		return nostrutil.DeliveryUnknown, fmt.Errorf("delivery outcome: %w", err)
	}
	if p.localOutbox != nil {
		entry, found, err := p.localOutbox.Get(parsed)
		if err != nil {
			return nostrutil.DeliveryUnknown, err
		}
		if found {
			switch {
			case entry.State == localstore.OutboxFailed:
				return nostrutil.DeliveryAbandoned, nil
			case entry.State == localstore.OutboxPublished || entry.Delivered:
				return nostrutil.DeliveryDelivered, nil
			default:
				return nostrutil.DeliveryPending, nil
			}
		}
	}
	if p.outboxRepo != nil {
		rec, err := p.outboxRepo.GetByID(ctx, id)
		if err != nil {
			return nostrutil.DeliveryUnknown, err
		}
		if rec != nil && !repository.IsLocalOutboxArchiveTarget(rec.PublishTarget) {
			switch rec.PublishState {
			case repository.NostrPublishStateFailed:
				return nostrutil.DeliveryAbandoned, nil
			case repository.NostrPublishStatePublished:
				return nostrutil.DeliveryDelivered, nil
			case repository.NostrPublishStatePending:
				if p.trackedDelivered(id) {
					return nostrutil.DeliveryDelivered, nil
				}
				return nostrutil.DeliveryPending, nil
			}
		}
	}
	if p.trackedDelivered(id) {
		return nostrutil.DeliveryDelivered, nil
	}
	return nostrutil.DeliveryUnknown, nil
}

// pruneLocalOutbox drops long-settled local outbox entries, at most once per
// outboxPruneInterval. Only Run calls it.
func (p *Publisher) pruneLocalOutbox() {
	if p.localOutbox == nil {
		return
	}
	now := p.now()
	if !p.lastPrune.IsZero() && now.Sub(p.lastPrune) < outboxPruneInterval {
		return
	}
	p.lastPrune = now
	removed, err := p.localOutbox.Prune(now.Add(-publishedOutboxRetention), now.Add(-failedOutboxRetention))
	if err != nil {
		p.logger.Warn("prune settled local outbox entries failed", zap.Error(err))
		return
	}
	if removed > 0 {
		p.logger.Debug("pruned settled local outbox entries", zap.Int("removed", removed))
	}
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
