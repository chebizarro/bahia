package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type memoryRelayPolicyProjectionStore struct {
	mu         sync.Mutex
	projection *repository.RelayPolicyProjection
	getErr     error
	promoteErr error
	syncErr    error
}

func (s *memoryRelayPolicyProjectionStore) Get(_ context.Context, author string) (*repository.RelayPolicyProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.projection == nil || s.projection.AuthorPubkey != author {
		return nil, nil
	}
	return cloneTestProjection(s.projection), nil
}

func (s *memoryRelayPolicyProjectionStore) Promote(_ context.Context, candidate repository.RelayPolicyProjection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promoteErr != nil {
		return false, s.promoteErr
	}
	confirmedAt := candidate.EventAcceptedAt.UTC()
	if s.projection != nil {
		current := s.projection
		if !repository.RelayPolicyProjectionShouldReplace(*current, candidate) {
			return false, nil
		}
		if current.EventID == candidate.EventID {
			promoted := cloneTestProjection(current)
			promoted.SourceRelay = candidate.SourceRelay
			promoted.LastSyncAt = candidate.LastSyncAt
			promoted.RelayConfirmedAt = &confirmedAt
			s.projection = promoted
			return true, nil
		}
	}
	candidate.RelayConfirmedAt = &confirmedAt
	s.projection = cloneTestProjection(&candidate)
	return true, nil
}

func (s *memoryRelayPolicyProjectionStore) MarkSynced(_ context.Context, author string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncErr != nil {
		return s.syncErr
	}
	if s.projection != nil && s.projection.AuthorPubkey == author && at.After(s.projection.LastSyncAt) {
		s.projection.LastSyncAt = at
	}
	return nil
}

func cloneTestProjection(in *repository.RelayPolicyProjection) *repository.RelayPolicyProjection {
	if in == nil {
		return nil
	}
	out := *in
	out.CanonicalPayload = append([]byte(nil), in.CanonicalPayload...)
	return &out
}

func TestRelaySettingsHydratorFilterIsScopedToCanonicalState(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	h := newTestRelaySettingsHydrator(t, servicePubkey, &memoryRelayPolicyProjectionStore{})

	filter := h.filter()
	if len(filter.Kinds) != 1 || filter.Kinds[0] != nostr.Kind(kinds.CASControlState) {
		t.Fatalf("unexpected filter kinds: %#v", filter.Kinds)
	}
	if len(filter.Authors) != 1 || filter.Authors[0].Hex() != servicePubkey {
		t.Fatalf("unexpected filter authors: %#v", filter.Authors)
	}
	for tag, want := range map[string]string{
		"d": RelaySettingsDTag, "domain": RelaySettingsDomain, "schema": RelaySettingsSchema,
	} {
		if got := filter.Tags[tag]; len(got) != 1 || got[0] != want {
			t.Fatalf("unexpected %s tag filter: %#v", tag, got)
		}
	}
}

func TestRelaySettingsHydratorPersistsLatestValidStateWithoutMutatingConfig(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	cfg := &config.Config{Nostr: config.NostrConfig{BrowserRelays: []string{"wss://old.example"}}}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)

	newer := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0).UTC(), RelayPolicyState{
		Schema:          RelaySettingsSchema,
		BrowserRelays:   []string{"wss://browser.example", "wss://browser.example"},
		ContextVMRelays: []string{"wss://contextvm.example"},
		ServiceRelays:   []string{"wss://service.example"},
	})
	older := signedRelaySettingsStateEvent(t, time.Unix(900, 0).UTC(), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://older.example"},
	})

	if !h.handleEventFromRelay(context.Background(), newer, "wss://secondary.example/private?ignored=value") {
		t.Fatal("expected newer relay settings event to promote")
	}
	if got := cfg.Nostr.BrowserRelays; len(got) != 1 || got[0] != "wss://old.example" {
		t.Fatalf("hydrator mutated runtime config: %#v", got)
	}
	snapshot, ok := h.Snapshot()
	if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://browser.example" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	projection, ok := h.Projection()
	if !ok || projection.EventID != newer.ID.Hex() || projection.PayloadHash == "" {
		t.Fatalf("missing projection provenance: %#v", projection)
	}
	if projection.SourceRelay != "wss://secondary.example/private" {
		t.Fatalf("source relay was not sanitized: %q", projection.SourceRelay)
	}
	if h.handleEvent(context.Background(), older) {
		t.Fatal("older event replaced valid head")
	}
	if h.handleEvent(context.Background(), newer) {
		t.Fatal("duplicate event replaced valid head")
	}
}

func TestRelaySettingsHydratorLoadsDurableProjectionWhenRelaysUnavailable(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://durable.example"}}
	store := &memoryRelayPolicyProjectionStore{projection: testProjection(t, servicePubkey, "event-head", time.Unix(1_000, 0), state)}
	var applied RelayPolicyState
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		ServicePubkey:   servicePubkey,
		ProjectionStore: store,
		Logger:          zap.NewNop(),
		Now:             func() time.Time { return time.Unix(2_000, 0).UTC() },
		OnSnapshotApplied: func(_ context.Context, state RelayPolicyState) error {
			applied = state
			return nil
		},
	})

	if err := h.LoadProjection(context.Background()); err != nil {
		t.Fatalf("load durable projection: %v", err)
	}
	snapshot, ok := h.Snapshot()
	if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://durable.example" {
		t.Fatalf("restart lost durable policy: %#v", snapshot)
	}
	if strings.Join(applied.BrowserRelays, ",") != "wss://durable.example" {
		t.Fatalf("durable projection was not applied to runtime topology: %#v", applied)
	}

	original := relaySettingsSubscribeAllWithEOSE
	relaySettingsSubscribeAllWithEOSE = func(*nostradapter.RelayPool, context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error) {
		return nil, errors.New("canonical relay unavailable")
	}
	t.Cleanup(func() { relaySettingsSubscribeAllWithEOSE = original })
	h.pool = nostradapter.NewRelayPool([]string{"wss://unavailable.example"}, zap.NewNop())
	if err := h.subscribe(context.Background()); err == nil {
		t.Fatal("expected relay outage")
	}
	after, ok := h.Snapshot()
	if !ok || strings.Join(after.BrowserRelays, ",") != "wss://durable.example" {
		t.Fatalf("outage invalidated durable head: %#v", after)
	}
}

func TestRelaySettingsHydratorResubscribesAfterAllRelayOutageAndRecovers(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	recovered := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		Pool:            nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
		ServicePubkey:   servicePubkey,
		ProjectionStore: store,
		Logger:          zap.NewNop(),
		Now:             func() time.Time { return time.Unix(2_000, 0).UTC() },
		OnSnapshotApplied: func(context.Context, RelayPolicyState) error {
			select {
			case recovered <- struct{}{}:
			default:
			}
			return nil
		},
	})
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://recovered.example"},
	})

	var callsMu sync.Mutex
	calls := 0
	setRelaySettingsSubscription(t, func(context.Context) *nostradapter.MergedSubscription {
		callsMu.Lock()
		calls++
		call := calls
		callsMu.Unlock()
		events := make(chan *nostr.Event, 1)
		relayEOSE := make(chan nostradapter.RelayEOSE)
		closed := make(chan nostradapter.RelayClosed)
		allEOSE := make(chan struct{})
		if call == 1 {
			close(events)
			close(relayEOSE)
			close(closed)
			return &nostradapter.MergedSubscription{Events: events, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
		}
		events <- event
		close(events)
		close(relayEOSE)
		close(closed)
		close(allEOSE)
		return &nostradapter.MergedSubscription{Events: events, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
	})

	runErr := make(chan error, 1)
	go func() { runErr <- h.Run(ctx) }()
	select {
	case <-recovered:
		cancel()
	case <-time.After(4 * time.Second):
		t.Fatal("hydrator did not resubscribe and recover after relay outage")
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	callsMu.Lock()
	gotCalls := calls
	callsMu.Unlock()
	if gotCalls < 2 {
		t.Fatalf("subscription attempts = %d, want at least 2", gotCalls)
	}
	projection, err := store.Get(context.Background(), servicePubkey)
	if err != nil || projection == nil || projection.RelayConfirmedAt == nil {
		t.Fatalf("recovered projection was not relay-confirmed: projection=%#v err=%v", projection, err)
	}
}

func TestRelaySettingsHydratorRestoredCachedEventCanBeRelayConfirmedWithoutRestart(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://restored.example"},
	})
	if !h.handleEventFromRelay(context.Background(), event, "wss://relay.example") {
		t.Fatal("initial relay observation did not promote")
	}

	store.mu.Lock()
	store.projection.RelayConfirmedAt = nil
	store.mu.Unlock()

	if !h.handleEventFromRelay(context.Background(), event, "wss://relay.example") {
		t.Fatal("restored cached event was blocked by in-process dedupe")
	}
	projection, err := store.Get(context.Background(), servicePubkey)
	if err != nil || projection == nil || projection.RelayConfirmedAt == nil {
		t.Fatalf("restored projection was not relay-confirmed: projection=%#v err=%v", projection, err)
	}
}

func TestRelaySettingsHydratorAcceptsExplicitSignedEmptyPolicy(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{Schema: RelaySettingsSchema})

	if !h.handleEvent(context.Background(), event) {
		t.Fatal("explicit signed empty policy must be valid")
	}
	snapshot, ok := h.Snapshot()
	if !ok {
		t.Fatal("missing explicit empty snapshot")
	}
	if len(snapshot.BrowserRelays)+len(snapshot.ContextVMRelays)+len(snapshot.ServiceRelays)+len(snapshot.NIP34Relays) != 0 {
		t.Fatalf("signed empty policy was not preserved: %#v", snapshot)
	}
}

func TestRelaySettingsHydratorZeroEventEOSERetainsLastKnownGood(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://last-known-good.example"}}
	store := &memoryRelayPolicyProjectionStore{projection: testProjection(t, servicePubkey, "event-head", time.Unix(1_000, 0), state)}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	if err := h.LoadProjection(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}

	drainStarted := make(chan struct{})
	drain := make(chan time.Time, 1)
	h.newDrainTimer = func(time.Duration) (<-chan time.Time, func()) {
		close(drainStarted)
		return drain, func() {}
	}
	eventCh := make(chan *nostr.Event)
	allEOSE := make(chan struct{})
	relayEOSE := make(chan nostradapter.RelayEOSE, 1)
	closed := make(chan nostradapter.RelayClosed)
	setRelaySettingsSubscription(t, func(context.Context) *nostradapter.MergedSubscription {
		return &nostradapter.MergedSubscription{Events: eventCh, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
	})

	errCh := make(chan error, 1)
	go func() { errCh <- h.subscribe(context.Background()) }()
	relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://primary.example", SubscriptionID: "policy"}
	close(relayEOSE)
	close(allEOSE)
	<-drainStarted
	drain <- time.Unix(2_001, 0)
	close(eventCh)
	close(closed)
	if err := <-errCh; err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	snapshot, ok := h.Snapshot()
	if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://last-known-good.example" {
		t.Fatalf("zero-event EOSE erased last-known-good: %#v", snapshot)
	}
}

func TestRelaySettingsHydratorDrainsEventAfterEOSEFromSecondaryRelay(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://secondary-policy.example"},
	})

	drainStarted := make(chan struct{})
	drain := make(chan time.Time, 1)
	h.newDrainTimer = func(time.Duration) (<-chan time.Time, func()) {
		close(drainStarted)
		return drain, func() {}
	}
	eventCh := make(chan *nostr.Event)
	allEOSE := make(chan struct{})
	relayEOSE := make(chan nostradapter.RelayEOSE, 2)
	closed := make(chan nostradapter.RelayClosed)
	setRelaySettingsSubscription(t, func(context.Context) *nostradapter.MergedSubscription {
		return &nostradapter.MergedSubscription{Events: eventCh, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
	})

	errCh := make(chan error, 1)
	go func() { errCh <- h.subscribe(context.Background()) }()
	relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://primary.example", SubscriptionID: "policy-primary"}
	relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://secondary.example", SubscriptionID: "policy-secondary"}
	close(relayEOSE)
	close(allEOSE)
	<-drainStarted
	eventCh <- event
	drain <- time.Unix(2_001, 0)
	close(eventCh)
	close(closed)
	if err := <-errCh; err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !h.IsCaughtUp() {
		t.Fatal("bounded drain did not mark catch-up")
	}
	snapshot, ok := h.Snapshot()
	if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://secondary-policy.example" {
		t.Fatalf("post-EOSE event was lost: %#v", snapshot)
	}
	if h.relayEOSECount() != 2 {
		t.Fatalf("per-relay EOSE count = %d, want 2", h.relayEOSECount())
	}
}

func TestRelaySettingsHydratorInvalidCandidatesNeverInvalidateValidHead(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	valid := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://valid.example"},
	})
	tests := []struct {
		name  string
		event func(*testing.T) *nostr.Event
	}{
		{name: "older", event: func(t *testing.T) *nostr.Event {
			return signedRelaySettingsStateEvent(t, time.Unix(999, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://older.example"}})
		}},
		{name: "malformed payload", event: func(t *testing.T) *nostr.Event {
			return signedRelaySettingsRawEvent(t, testServiceKey, time.Unix(1_001, 0), RelaySettingsSchema, "{")
		}},
		{name: "invalid signature", event: func(t *testing.T) *nostr.Event {
			ev := signedRelaySettingsStateEvent(t, time.Unix(1_001, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://invalid.example"}})
			ev.Sig[0] ^= 0xff
			return ev
		}},
		{name: "wrong author", event: func(t *testing.T) *nostr.Event {
			return signedRelaySettingsStateEventWithKey(t, testRequesterKey, time.Unix(1_001, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://wrong-author.example"}})
		}},
		{name: "wrong schema", event: func(t *testing.T) *nostr.Event {
			return signedRelaySettingsRawEvent(t, testServiceKey, time.Unix(1_001, 0), "wrong-schema", `{"schema":"wrong-schema"}`)
		}},
		{name: "future timestamp", event: func(t *testing.T) *nostr.Event {
			return signedRelaySettingsStateEvent(t, time.Unix(4_000, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://future.example"}})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryRelayPolicyProjectionStore{}
			h := newTestRelaySettingsHydrator(t, servicePubkey, store)
			if !h.handleEvent(context.Background(), valid) {
				t.Fatal("seed valid head")
			}
			if h.handleEvent(context.Background(), tc.event(t)) {
				t.Fatal("invalid candidate promoted")
			}
			snapshot, ok := h.Snapshot()
			if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://valid.example" {
				t.Fatalf("invalid candidate erased head: %#v", snapshot)
			}
		})
	}
}

func TestRelayPolicyCanonicalTagsAreRequired(t *testing.T) {
	base := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://valid.example"},
	})
	tests := []struct {
		name  string
		index int
		value string
	}{
		{name: "wrong d", index: 0, value: "wrong-d"},
		{name: "wrong domain", index: 1, value: "wrong-domain"},
		{name: "wrong schema tag", index: 2, value: "wrong-schema"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := *base
			ev.Tags = append(nostr.Tags(nil), base.Tags...)
			ev.Tags[tc.index] = append(nostr.Tag(nil), base.Tags[tc.index]...)
			ev.Tags[tc.index][1] = tc.value
			if _, err := relayPolicyStateFromCanonicalEvent(&ev, base.PubKey.Hex()); err == nil {
				t.Fatal("invalid canonical tag accepted")
			}
		})
	}
}

func TestRelaySettingsHydratorEqualTimestampUsesLowestEventID(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	at := time.Unix(1_000, 0)
	first := signedRelaySettingsStateEvent(t, at, RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://one.example"}})
	second := signedRelaySettingsStateEvent(t, at, RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://two.example"}})
	low, high := first, second
	if low.ID.Hex() > high.ID.Hex() {
		low, high = high, low
	}
	store := &memoryRelayPolicyProjectionStore{}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	if !h.handleEvent(context.Background(), high) {
		t.Fatal("initial equal-time candidate did not promote")
	}
	if !h.handleEvent(context.Background(), low) {
		t.Fatal("lower event ID must win equal timestamp tie")
	}
	projection, ok := h.Projection()
	if !ok || projection.EventID != low.ID.Hex() {
		t.Fatalf("equal-time winner = %q, want %q", projection.EventID, low.ID.Hex())
	}
}

func TestRelaySettingsHydratorPromotionFailureAuthAndTimeoutRetainHead(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://valid.example"}}
	store := &memoryRelayPolicyProjectionStore{projection: testProjection(t, servicePubkey, "event-head", time.Unix(1_000, 0), state)}
	h := newTestRelaySettingsHydrator(t, servicePubkey, store)
	if err := h.LoadProjection(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}

	store.promoteErr = errors.New("postgres timeout")
	newer := signedRelaySettingsStateEvent(t, time.Unix(1_001, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://new.example"}})
	if h.handleEvent(context.Background(), newer) {
		t.Fatal("failed persistence must not promote")
	}
	if h.handleRelayClosed(context.Background(), nostradapter.RelayClosed{RelayURL: "wss://auth.example", Reason: "auth-required"}, map[string]struct{}{}) {
		t.Fatal("unavailable auth must not claim resubscribe success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eventCh := make(chan *nostr.Event)
	allEOSE := make(chan struct{})
	relayEOSE := make(chan nostradapter.RelayEOSE)
	closed := make(chan nostradapter.RelayClosed)
	setRelaySettingsSubscription(t, func(context.Context) *nostradapter.MergedSubscription {
		return &nostradapter.MergedSubscription{Events: eventCh, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
	})
	if err := h.subscribe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("timeout/cancel error = %v", err)
	}
	snapshot, ok := h.Snapshot()
	if !ok || strings.Join(snapshot.BrowserRelays, ",") != "wss://valid.example" {
		t.Fatalf("failure path invalidated head: %#v", snapshot)
	}
}

func TestRelaySettingsHydratorLoadFailsBeforeActivationWhenTopologyApplyFails(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://durable.example"}}
	store := &memoryRelayPolicyProjectionStore{projection: testProjection(t, servicePubkey, "event-head", time.Unix(1_000, 0), state)}
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		ServicePubkey:   servicePubkey,
		ProjectionStore: store,
		Logger:          zap.NewNop(),
		OnSnapshotApplied: func(context.Context, RelayPolicyState) error {
			return errors.New("topology apply failed")
		},
	})
	if err := h.LoadProjection(context.Background()); err == nil || !strings.Contains(err.Error(), "topology apply failed") {
		t.Fatalf("LoadProjection error = %v", err)
	}
	if h.projectionLoaded.Load() {
		t.Fatal("failed topology application marked projection loaded")
	}
}

func TestRelaySettingsHydratorRejectsCorruptStoredProjection(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://valid.example"}}
	projection := testProjection(t, servicePubkey, "event-head", time.Unix(1_000, 0), state)
	projection.PayloadHash = strings.Repeat("0", 64)
	h := newTestRelaySettingsHydrator(t, servicePubkey, &memoryRelayPolicyProjectionStore{projection: projection})
	if err := h.LoadProjection(context.Background()); err == nil {
		t.Fatal("corrupt stored projection must fail closed")
	}
	if _, ok := h.Snapshot(); ok {
		t.Fatal("corrupt stored projection became runtime state")
	}
}

func newTestRelaySettingsHydrator(t *testing.T, servicePubkey string, store repository.RelayPolicyProjectionRepository) *RelaySettingsHydrator {
	t.Helper()
	return NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		Pool:            nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
		ServicePubkey:   servicePubkey,
		ProjectionStore: store,
		Logger:          zap.NewNop(),
		Now:             func() time.Time { return time.Unix(2_000, 0).UTC() },
	})
}

func setRelaySettingsSubscription(t *testing.T, factory func(context.Context) *nostradapter.MergedSubscription) {
	t.Helper()
	original := relaySettingsSubscribeAllWithEOSE
	relaySettingsSubscribeAllWithEOSE = func(_ *nostradapter.RelayPool, ctx context.Context, filters []nostr.Filter) (*nostradapter.MergedSubscription, error) {
		if len(filters) != 1 || filters[0].Tags["d"][0] != RelaySettingsDTag {
			t.Fatalf("unexpected subscription filters: %#v", filters)
		}
		return factory(ctx), nil
	}
	t.Cleanup(func() { relaySettingsSubscribeAllWithEOSE = original })
}

func testProjection(t *testing.T, author, eventID string, createdAt time.Time, state RelayPolicyState) *repository.RelayPolicyProjection {
	t.Helper()
	if err := normalizeAndValidateRelayPolicyForSettings(&state, false); err != nil {
		t.Fatalf("normalize projection state: %v", err)
	}
	payload, hash, err := canonicalRelayPolicyPayload(state)
	if err != nil {
		t.Fatalf("canonical payload: %v", err)
	}
	if len(eventID) != 64 {
		eventID = strings.Repeat("a", 64)
	}
	return &repository.RelayPolicyProjection{
		AuthorPubkey: author, EventID: eventID, EventCreatedAt: createdAt.UTC(),
		EventAcceptedAt: createdAt.Add(time.Second).UTC(), Schema: RelaySettingsSchema,
		CanonicalPayload: payload, PayloadHash: hash, SourceRelay: "wss://relay.example",
		LastSyncAt: createdAt.Add(2 * time.Second).UTC(),
	}
}

func signedRelaySettingsStateEvent(t *testing.T, createdAt time.Time, state RelayPolicyState) *nostr.Event {
	t.Helper()
	return signedRelaySettingsStateEventWithKey(t, testServiceKey, createdAt, state)
}

func signedRelaySettingsStateEventWithKey(t *testing.T, privateKey string, createdAt time.Time, state RelayPolicyState) *nostr.Event {
	t.Helper()
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return signedRelaySettingsRawEvent(t, privateKey, createdAt, RelaySettingsSchema, string(content))
}

func signedRelaySettingsRawEvent(t *testing.T, privateKey string, createdAt time.Time, schemaTag, content string) *nostr.Event {
	t.Helper()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	event := &nostr.Event{
		Kind: kinds.CASControlState, CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:    nostr.Tags{{"d", RelaySettingsDTag}, {"domain", RelaySettingsDomain}, {"schema", schemaTag}},
		Content: content,
	}
	if err := SignGoNostrEvent(context.Background(), signer, event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}
