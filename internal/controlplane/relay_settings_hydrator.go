package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const defaultRelaySettingsPostEOSEDrain = 300 * time.Millisecond

var (
	errRelaySettingsResubscribeNow    = errors.New("relay settings subscription should resubscribe immediately")
	relaySettingsSubscribeAllWithEOSE = func(pool *nostradapter.RelayPool, ctx context.Context, filters []gonostr.Filter) (*nostradapter.MergedSubscription, error) {
		if pool == nil {
			return nil, fmt.Errorf("relay settings hydrator relay pool is required")
		}
		return pool.SubscribeAllWithEOSE(ctx, filters)
	}
)

type RelaySettingsSnapshotHandler func(context.Context, RelayPolicyState) error

type relaySettingsDrainTimerFactory func(time.Duration) (<-chan time.Time, func())

type RelaySettingsHydratorConfig struct {
	Pool              *nostradapter.RelayPool
	ServicePubkey     string
	ProjectionStore   repository.RelayPolicyProjectionRepository
	Logger            *zap.Logger
	Now               func() time.Time
	PostEOSEDrain     time.Duration
	NewDrainTimer     relaySettingsDrainTimerFactory
	OnSnapshotApplied RelaySettingsSnapshotHandler
}

// RelaySettingsHydrator validates canonical relay events and promotes only valid
// heads into the durable PostgreSQL server projection. Relay discovery caches
// and browser emergency overrides are intentionally outside this component.
type RelaySettingsHydrator struct {
	pool          *nostradapter.RelayPool
	servicePubkey string
	store         repository.RelayPolicyProjectionRepository
	logger        *zap.Logger
	now           func() time.Time
	postEOSEDrain time.Duration
	newDrainTimer relaySettingsDrainTimerFactory
	onSnapshot    RelaySettingsSnapshotHandler

	mu               sync.Mutex
	seenEventIDs     map[string]struct{}
	latestCreated    int64
	latestEventID    string
	latestSnapshot   RelayPolicyState
	latestProjection repository.RelayPolicyProjection
	hasSnapshot      bool
	relayEOSE        map[string]struct{}
	projectionLoaded atomic.Bool
	caughtUp         atomic.Bool
}

func NewRelaySettingsHydrator(cfg RelaySettingsHydratorConfig) *RelaySettingsHydrator {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	drain := cfg.PostEOSEDrain
	if drain <= 0 {
		drain = defaultRelaySettingsPostEOSEDrain
	}
	newDrainTimer := cfg.NewDrainTimer
	if newDrainTimer == nil {
		newDrainTimer = func(duration time.Duration) (<-chan time.Time, func()) {
			timer := time.NewTimer(duration)
			return timer.C, func() {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		}
	}
	return &RelaySettingsHydrator{
		pool:          cfg.Pool,
		servicePubkey: strings.ToLower(strings.TrimSpace(cfg.ServicePubkey)),
		store:         cfg.ProjectionStore,
		logger:        logger.Named("relay-settings-hydrator"),
		now:           now,
		postEOSEDrain: drain,
		newDrainTimer: newDrainTimer,
		onSnapshot:    cfg.OnSnapshotApplied,
		seenEventIDs:  make(map[string]struct{}),
		relayEOSE:     make(map[string]struct{}),
	}
}

func (h *RelaySettingsHydrator) Name() string { return "relay-settings-hydrator" }

func (h *RelaySettingsHydrator) IsCaughtUp() bool {
	if h == nil {
		return false
	}
	return h.caughtUp.Load()
}

func (h *RelaySettingsHydrator) Run(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("relay settings hydrator is nil")
	}
	if err := h.LoadProjection(ctx); err != nil {
		return err
	}
	if h.pool == nil {
		return fmt.Errorf("relay settings hydrator relay pool is required")
	}

	backoff := nostradapter.DefaultBackoff()
	for {
		err := h.subscribe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRelaySettingsResubscribeNow) {
			continue
		}
		delay := backoff.Next()
		h.logger.Warn("relay settings hydration subscription ended, retaining durable projection and reconnecting",
			zap.Error(err),
			zap.Duration("delay", delay),
			zap.Int("attempt", backoff.Attempt()),
		)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// LoadProjection synchronously loads and validates the durable head before
// policy-dependent background transports are activated. It is idempotent.
func (h *RelaySettingsHydrator) LoadProjection(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("relay settings hydrator is nil")
	}
	if h.projectionLoaded.Load() {
		return nil
	}
	if h.store == nil {
		return fmt.Errorf("relay settings hydrator projection store is required")
	}
	if h.servicePubkey == "" {
		return fmt.Errorf("relay settings hydrator trusted service pubkey is required")
	}
	projection, err := h.store.Get(ctx, h.servicePubkey)
	if err != nil {
		return fmt.Errorf("loading durable relay settings projection: %w", err)
	}
	if projection == nil {
		h.projectionLoaded.Store(true)
		return nil
	}
	state, err := relayPolicyStateFromProjection(projection, h.servicePubkey)
	if err != nil {
		return fmt.Errorf("validating durable relay settings projection: %w", err)
	}
	h.storeSnapshot(*state, *projection)
	if h.onSnapshot != nil {
		if err := h.onSnapshot(ctx, cloneRelayPolicyState(*state)); err != nil {
			return fmt.Errorf("applying durable relay settings projection: %w", err)
		}
	}
	h.projectionLoaded.Store(true)
	h.logger.Info("loaded durable relay settings projection",
		zap.String("event_id", projection.EventID),
		zap.String("payload_hash", projection.PayloadHash),
	)
	return nil
}

func (h *RelaySettingsHydrator) subscribe(ctx context.Context) error {
	h.caughtUp.Store(false)
	h.mu.Lock()
	h.relayEOSE = make(map[string]struct{})
	h.mu.Unlock()

	merged, err := relaySettingsSubscribeAllWithEOSE(h.pool, ctx, []gonostr.Filter{h.filter()})
	if err != nil {
		return err
	}
	defer merged.Close()

	h.logger.Info("subscribed to canonical relay settings state",
		zap.Strings("relays", safeRelayURLsForLog(merged.RelayURLs())),
		zap.String("service_pubkey", h.servicePubkey),
	)
	authAttempted := make(map[string]struct{})
	eventsCh := merged.Events
	eoseCh := merged.RelayEOSE
	allEOSECh := merged.EndOfStoredEvents
	closedCh := merged.Closed
	var drainCh <-chan time.Time
	stopDrain := func() {}
	defer func() { stopDrain() }()

	startDrain := func() {
		if drainCh != nil || h.caughtUp.Load() {
			return
		}
		drainCh, stopDrain = h.newDrainTimer(h.postEOSEDrain)
	}

	for {
		if eventsCh == nil && eoseCh == nil && allEOSECh == nil && closedCh == nil && drainCh == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case eose, ok := <-eoseCh:
			if !ok {
				eoseCh = nil
				continue
			}
			relayURL := safeRelayURL(eose.RelayURL)
			h.mu.Lock()
			h.relayEOSE[relayURL] = struct{}{}
			h.mu.Unlock()
			h.logger.Debug("relay sent relay-settings EOSE",
				zap.String("relay", relayURL),
				zap.String("subscription_id", eose.SubscriptionID),
			)
		case closed, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			if h.handleRelayClosed(ctx, closed, authAttempted) {
				return errRelaySettingsResubscribeNow
			}
		case _, ok := <-allEOSECh:
			if !ok {
				allEOSECh = nil
				startDrain()
			}
		case <-drainCh:
			drainCh = nil
			stopDrain = func() {}
			syncedAt := h.now().UTC()
			if err := h.store.MarkSynced(ctx, h.servicePubkey, syncedAt); err != nil {
				h.logger.Warn("relay settings sync checkpoint failed; retaining durable projection", zap.Error(err))
			} else {
				h.markProjectionSynced(syncedAt)
			}
			h.caughtUp.Store(true)
			h.logger.Info("relay settings historical catch-up completed after bounded EOSE drain",
				zap.Int("relay_eose_count", h.relayEOSECount()),
			)
		case ev, ok := <-eventsCh:
			if !ok {
				eventsCh = nil
				if allEOSECh != nil {
					select {
					case <-allEOSECh:
						allEOSECh = nil
						startDrain()
					default:
					}
				}
				continue
			}
			if ev == nil {
				continue
			}
			h.handleEventFromRelay(ctx, ev, merged.EventSource(ev.ID.Hex()))
		}
	}
}

func (h *RelaySettingsHydrator) filter() gonostr.Filter {
	filter := gonostr.Filter{
		Kinds: []gonostr.Kind{kinds.CASControlState},
		Tags: gonostr.TagMap{
			"d":      []string{RelaySettingsDTag},
			"domain": []string{RelaySettingsDomain},
			"schema": []string{RelaySettingsSchema},
		},
		Limit: 10,
	}
	if h.servicePubkey != "" {
		if pubkey, err := gonostr.PubKeyFromHex(h.servicePubkey); err == nil {
			filter.Authors = []gonostr.PubKey{pubkey}
		}
	}
	return filter
}

func (h *RelaySettingsHydrator) handleRelayClosed(ctx context.Context, closed nostradapter.RelayClosed, authAttempted map[string]struct{}) bool {
	relayURL := safeRelayURL(closed.RelayURL)
	h.logger.Warn("relay closed relay-settings subscription",
		zap.String("relay", relayURL),
		zap.String("subscription_id", closed.SubscriptionID),
		zap.Bool("auth_required", nostradapter.IsAuthRequiredReason(closed.Reason)),
	)
	if !nostradapter.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || h.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := h.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		h.pool.RecordRelayError(closed.RelayURL, "auth-unavailable")
		h.logger.Warn("relay settings subscription auth failed",
			zap.String("relay", relayURL),
			zap.String("failure_class", "auth_unavailable"),
		)
		return false
	}
	return true
}

func (h *RelaySettingsHydrator) handleEvent(ctx context.Context, ev *gonostr.Event) bool {
	return h.handleEventFromRelay(ctx, ev, "")
}

func (h *RelaySettingsHydrator) handleEventFromRelay(ctx context.Context, ev *gonostr.Event, sourceRelay string) bool {
	if h == nil || ev == nil || h.store == nil {
		return false
	}
	if err := nostradapter.ValidateInboundEvent(ev, h.now(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		h.logger.Warn("dropping invalid relay settings state event", zap.String("event_id", eventIDForLog(ev)), zap.Error(err))
		return false
	}
	state, err := relayPolicyStateFromCanonicalEvent(ev, h.servicePubkey)
	if err != nil {
		h.logger.Warn("dropping relay settings state event", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
		return false
	}
	canonicalPayload, payloadHash, err := canonicalRelayPolicyPayload(*state)
	if err != nil {
		h.logger.Warn("canonicalizing relay settings state event", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
		return false
	}

	eventID := ev.ID.Hex()
	h.mu.Lock()
	_, seen := h.seenEventIDs[eventID]
	h.mu.Unlock()
	if seen {
		return false
	}

	acceptedAt := h.now().UTC()
	projection := repository.RelayPolicyProjection{
		AuthorPubkey:     strings.ToLower(ev.PubKey.Hex()),
		EventID:          eventID,
		EventCreatedAt:   ev.CreatedAt.Time().UTC(),
		EventAcceptedAt:  acceptedAt,
		Schema:           RelaySettingsSchema,
		CanonicalPayload: canonicalPayload,
		PayloadHash:      payloadHash,
		SourceRelay:      safeRelayURL(sourceRelay),
		LastSyncAt:       acceptedAt,
	}
	promoted, err := h.store.Promote(ctx, projection)
	if err != nil {
		h.logger.Warn("durable relay settings projection promotion failed; retaining last-known-good",
			zap.String("event_id", eventID),
			zap.Error(err),
		)
		return false
	}
	h.mu.Lock()
	h.seenEventIDs[eventID] = struct{}{}
	h.mu.Unlock()
	if !promoted {
		return false
	}

	h.storeSnapshot(*state, projection)
	if h.onSnapshot != nil {
		if err := h.onSnapshot(ctx, cloneRelayPolicyState(*state)); err != nil {
			h.logger.Warn("relay settings snapshot callback failed", zap.String("event_id", eventID), zap.Error(err))
		}
	}
	h.logger.Info("promoted validated relay settings policy projection",
		zap.String("event_id", eventID),
		zap.String("payload_hash", payloadHash),
		zap.String("source_relay", projection.SourceRelay),
		zap.Int("browser_relays", len(state.BrowserRelays)),
		zap.Int("contextvm_relays", len(state.ContextVMRelays)),
		zap.Int("service_relays", len(state.ServiceRelays)),
	)
	return true
}

func (h *RelaySettingsHydrator) Snapshot() (RelayPolicyState, bool) {
	if h == nil {
		return RelayPolicyState{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasSnapshot {
		return RelayPolicyState{}, false
	}
	return cloneRelayPolicyState(h.latestSnapshot), true
}

func (h *RelaySettingsHydrator) Projection() (repository.RelayPolicyProjection, bool) {
	if h == nil {
		return repository.RelayPolicyProjection{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasSnapshot {
		return repository.RelayPolicyProjection{}, false
	}
	projection := h.latestProjection
	projection.CanonicalPayload = append([]byte(nil), projection.CanonicalPayload...)
	return projection, true
}

func (h *RelaySettingsHydrator) storeSnapshot(state RelayPolicyState, projection repository.RelayPolicyProjection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latestSnapshot = cloneRelayPolicyState(state)
	h.latestProjection = projection
	h.latestProjection.CanonicalPayload = append([]byte(nil), projection.CanonicalPayload...)
	h.latestCreated = projection.EventCreatedAt.Unix()
	h.latestEventID = projection.EventID
	h.hasSnapshot = true
}

func (h *RelaySettingsHydrator) markProjectionSynced(syncedAt time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hasSnapshot && syncedAt.After(h.latestProjection.LastSyncAt) {
		h.latestProjection.LastSyncAt = syncedAt
	}
}

func (h *RelaySettingsHydrator) relayEOSECount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.relayEOSE)
}

func relayPolicyStateFromCanonicalEvent(ev *gonostr.Event, servicePubkey string) (*RelayPolicyState, error) {
	if ev == nil {
		return nil, fmt.Errorf("event is required")
	}
	if ev.Kind != kinds.CASControlState {
		return nil, fmt.Errorf("unexpected event kind %d", ev.Kind)
	}
	if expected := strings.ToLower(strings.TrimSpace(servicePubkey)); expected != "" && strings.ToLower(ev.PubKey.Hex()) != expected {
		return nil, fmt.Errorf("event pubkey %q does not match trusted service pubkey", ev.PubKey.Hex())
	}
	if tagValueNostr(ev.Tags, "d") != RelaySettingsDTag {
		return nil, fmt.Errorf("missing relay settings d tag %q", RelaySettingsDTag)
	}
	if tagValueNostr(ev.Tags, "domain") != RelaySettingsDomain {
		return nil, fmt.Errorf("missing relay settings domain tag %q", RelaySettingsDomain)
	}
	if tagValueNostr(ev.Tags, "schema") != RelaySettingsSchema {
		return nil, fmt.Errorf("missing relay settings schema tag %q", RelaySettingsSchema)
	}
	var state RelayPolicyState
	if err := json.Unmarshal([]byte(ev.Content), &state); err != nil {
		return nil, fmt.Errorf("decode relay settings state content: %w", err)
	}
	if state.Schema != RelaySettingsSchema {
		return nil, fmt.Errorf("relay settings content schema %q does not match %q", state.Schema, RelaySettingsSchema)
	}
	if err := normalizeAndValidateRelayPolicyForSettings(&state, false); err != nil {
		return nil, err
	}
	return &state, nil
}

func relayPolicyStateFromProjection(projection *repository.RelayPolicyProjection, servicePubkey string) (*RelayPolicyState, error) {
	if projection == nil {
		return nil, fmt.Errorf("projection is required")
	}
	if strings.ToLower(projection.AuthorPubkey) != strings.ToLower(strings.TrimSpace(servicePubkey)) {
		return nil, fmt.Errorf("projection author does not match trusted service pubkey")
	}
	if projection.Schema != RelaySettingsSchema {
		return nil, fmt.Errorf("projection schema %q does not match %q", projection.Schema, RelaySettingsSchema)
	}
	var state RelayPolicyState
	if err := json.Unmarshal(projection.CanonicalPayload, &state); err != nil {
		return nil, fmt.Errorf("decode projected relay settings payload: %w", err)
	}
	if state.Schema != RelaySettingsSchema {
		return nil, fmt.Errorf("projected payload schema %q does not match %q", state.Schema, RelaySettingsSchema)
	}
	if err := normalizeAndValidateRelayPolicyForSettings(&state, false); err != nil {
		return nil, err
	}
	canonicalPayload, payloadHash, err := canonicalRelayPolicyPayload(state)
	if err != nil {
		return nil, err
	}
	if payloadHash != projection.PayloadHash {
		return nil, fmt.Errorf("projected relay settings payload hash mismatch")
	}
	if string(canonicalPayload) != string(projection.CanonicalPayload) {
		return nil, fmt.Errorf("projected relay settings payload is not canonical")
	}
	return &state, nil
}

func canonicalRelayPolicyPayload(state RelayPolicyState) (json.RawMessage, string, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, "", fmt.Errorf("marshal canonical relay settings payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	return json.RawMessage(payload), hex.EncodeToString(sum[:]), nil
}

func cloneRelayPolicyState(state RelayPolicyState) RelayPolicyState {
	state.BrowserRelays = cloneStrings(state.BrowserRelays)
	state.ContextVMRelays = cloneStrings(state.ContextVMRelays)
	state.ServiceRelays = cloneStrings(state.ServiceRelays)
	state.NIP34Relays = cloneStrings(state.NIP34Relays)
	state.TrustedRelayMonitorPubkeys = cloneStrings(state.TrustedRelayMonitorPubkeys)
	state.DMRelayLists = append([]RelayPolicyDMRelayList(nil), state.DMRelayLists...)
	for i := range state.DMRelayLists {
		state.DMRelayLists[i].Relays = cloneStrings(state.DMRelayLists[i].Relays)
	}
	state.RelayAdministration.Targets = append([]RelayPolicyAdminTarget(nil), state.RelayAdministration.Targets...)
	for i := range state.RelayAdministration.Targets {
		state.RelayAdministration.Targets[i].AdministratorPubkeys = cloneStrings(state.RelayAdministration.Targets[i].AdministratorPubkeys)
	}
	return state
}

func safeRelayURLsForLog(relays []string) []string {
	safe := make([]string, 0, len(relays))
	for _, relay := range relays {
		if sanitized := safeRelayURL(relay); sanitized != "" {
			safe = append(safe, sanitized)
		}
	}
	return safe
}

func safeRelayURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func eventIDForLog(ev *gonostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.ID.Hex()
}
