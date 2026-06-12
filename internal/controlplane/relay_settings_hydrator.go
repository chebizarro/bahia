package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

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

type RelaySettingsHydratorConfig struct {
	Pool              *nostradapter.RelayPool
	ServicePubkey     string
	Logger            *zap.Logger
	Now               func() time.Time
	OnSnapshotApplied RelaySettingsSnapshotHandler
}

// RelaySettingsHydrator backfills and tails the canonical relay-settings state
// read model so restart/live operator sessions converge from service-signed
// kind 30900 state instead of process-local defaults.
type RelaySettingsHydrator struct {
	pool          *nostradapter.RelayPool
	servicePubkey string
	logger        *zap.Logger
	now           func() time.Time
	onSnapshot    RelaySettingsSnapshotHandler

	mu             sync.Mutex
	seenEventIDs   map[string]struct{}
	latestCreated  int64
	latestEventID  string
	latestSnapshot RelayPolicyState
	hasSnapshot    bool
	caughtUp       atomic.Bool
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
	return &RelaySettingsHydrator{
		pool:          cfg.Pool,
		servicePubkey: strings.ToLower(strings.TrimSpace(cfg.ServicePubkey)),
		logger:        logger.Named("relay-settings-hydrator"),
		now:           now,
		onSnapshot:    cfg.OnSnapshotApplied,
		seenEventIDs:  make(map[string]struct{}),
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
		h.logger.Warn("relay settings hydration subscription ended, reconnecting with backoff", zap.Error(err), zap.Duration("delay", delay), zap.Int("attempt", backoff.Attempt()))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

func (h *RelaySettingsHydrator) subscribe(ctx context.Context) error {
	h.caughtUp.Store(false)
	merged, err := relaySettingsSubscribeAllWithEOSE(h.pool, ctx, []gonostr.Filter{h.filter()})
	if err != nil {
		return err
	}
	defer merged.Close()

	h.logger.Info("subscribed to canonical relay settings state", zap.Strings("relays", h.pool.URLs()), zap.String("service_pubkey", h.servicePubkey))
	authAttempted := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				h.logger.Debug("relay sent relay-settings EOSE", zap.String("relay", eose.RelayURL), zap.String("subscription_id", eose.SubscriptionID))
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if h.handleRelayClosed(ctx, closed, authAttempted) {
					return errRelaySettingsResubscribeNow
				}
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			h.caughtUp.Store(true)
			h.logger.Info("relay settings EOSE received")
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				h.markCaughtUpIfEOSEClosed(merged)
				return nil
			}
			if ev == nil {
				continue
			}
			h.handleEvent(ctx, ev)
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

func (h *RelaySettingsHydrator) markCaughtUpIfEOSEClosed(merged *nostradapter.MergedSubscription) {
	if h == nil || merged == nil || merged.EndOfStoredEvents == nil || h.caughtUp.Load() {
		return
	}
	select {
	case <-merged.EndOfStoredEvents:
		h.caughtUp.Store(true)
		h.logger.Info("relay settings EOSE received")
	default:
	}
}

func (h *RelaySettingsHydrator) handleRelayClosed(ctx context.Context, closed nostradapter.RelayClosed, authAttempted map[string]struct{}) bool {
	h.logger.Warn("relay closed relay-settings subscription", zap.String("relay", closed.RelayURL), zap.String("subscription_id", closed.SubscriptionID), zap.String("reason", closed.Reason))
	if !nostradapter.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || h.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := h.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		h.pool.RecordRelayError(closed.RelayURL, "auth-unavailable: "+closed.Reason+": "+err.Error())
		h.logger.Warn("relay settings subscription auth failed", zap.String("relay", closed.RelayURL), zap.String("reason", closed.Reason), zap.Error(err))
		return false
	}
	return true
}

func (h *RelaySettingsHydrator) handleEvent(ctx context.Context, ev *gonostr.Event) bool {
	_ = ctx
	if h == nil || ev == nil {
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
	if !h.shouldApply(ev) {
		return false
	}
	h.storeSnapshot(*state)
	if h.onSnapshot != nil {
		if err := h.onSnapshot(ctx, cloneRelayPolicyState(*state)); err != nil {
			h.logger.Warn("relay settings snapshot callback failed", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
		}
	}
	h.logger.Info("hydrated relay settings policy snapshot from canonical state", zap.String("event_id", ev.ID.Hex()), zap.Int("browser_relays", len(state.BrowserRelays)), zap.Int("contextvm_relays", len(state.ContextVMRelays)), zap.Int("service_relays", len(state.ServiceRelays)))
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

func (h *RelaySettingsHydrator) storeSnapshot(state RelayPolicyState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latestSnapshot = cloneRelayPolicyState(state)
	h.hasSnapshot = true
}

func (h *RelaySettingsHydrator) shouldApply(ev *gonostr.Event) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ev.ID != (gonostr.ID{}) {
		eventID := ev.ID.Hex()
		if _, ok := h.seenEventIDs[eventID]; ok {
			return false
		}
		h.seenEventIDs[eventID] = struct{}{}
	}
	created := ev.CreatedAt.Time().Unix()
	if created < h.latestCreated {
		return false
	}
	eventID := ev.ID.Hex()
	if created == h.latestCreated && h.latestEventID != "" && eventID >= h.latestEventID {
		return false
	}
	h.latestCreated = created
	h.latestEventID = eventID
	return true
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

func cloneRelayPolicyState(state RelayPolicyState) RelayPolicyState {
	state.BrowserRelays = cloneStrings(state.BrowserRelays)
	state.ContextVMRelays = cloneStrings(state.ContextVMRelays)
	state.ServiceRelays = cloneStrings(state.ServiceRelays)
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

func eventIDForLog(ev *gonostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.ID.Hex()
}
