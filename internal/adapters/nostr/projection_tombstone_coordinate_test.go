package nostr

import (
	"context"
	"fmt"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// These tests pin B-18 / B-19 (bahia-irsry.1, .2): a deletion is only real if
// the relay's newest event on the live record's addressable coordinate
// (kind, pubkey, d) is the tombstone. They assert against a relay fake with
// NIP-01 addressable replacement semantics rather than against the local
// event log, and they compare wire kinds exactly (the capture sink's byKind
// also matches legacy_kind, which is how the wrong-kind tombstones slipped by).

type relayCoordinate struct {
	kind   int
	pubkey string
	d      string
}

func coordinateOf(ev gonostr.Event) relayCoordinate {
	return relayCoordinate{kind: int(ev.Kind), pubkey: ev.PubKey.Hex(), d: eventDTag(ev)}
}

// replaceableRelay keeps, per addressable coordinate, the event a NIP-01 relay
// would serve: the newest created_at, ties broken by the lowest event id.
type replaceableRelay struct {
	mu     sync.Mutex
	all    []gonostr.Event
	latest map[relayCoordinate]gonostr.Event
}

func newReplaceableRelay() *replaceableRelay {
	return &replaceableRelay{latest: map[relayCoordinate]gonostr.Event{}}
}

func (r *replaceableRelay) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	if !ev.VerifySignature() {
		return 0, errors.New("invalid: bad signature")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, ev)
	if ev.Kind >= 30000 && ev.Kind < 40000 {
		c := coordinateOf(ev)
		current, ok := r.latest[c]
		if !ok || ev.CreatedAt > current.CreatedAt || (ev.CreatedAt == current.CreatedAt && ev.ID.Hex() < current.ID.Hex()) {
			r.latest[c] = ev
		}
	}
	return 1, nil
}

func (r *replaceableRelay) query(c relayCoordinate) (gonostr.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.latest[c]
	return ev, ok
}

// liveByLegacyKind returns the relay's current events for one projected family.
func (r *replaceableRelay) liveByLegacyKind(legacyKind int) []gonostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []gonostr.Event
	for _, ev := range r.latest {
		if tagValue(ev.Tags, "legacy_kind") == strconv.Itoa(legacyKind) {
			out = append(out, ev)
		}
	}
	return out
}

func (r *replaceableRelay) countKind(kind int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.all {
		if int(ev.Kind) == kind {
			n++
		}
	}
	return n
}

func (r *replaceableRelay) countOn(c relayCoordinate) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.all {
		if coordinateOf(ev) == c {
			n++
		}
	}
	return n
}

// assertRelayTombstoned proves the relay now serves a tombstone on live's
// exact coordinate, carrying the same projection envelope as the live record.
func assertRelayTombstoned(t *testing.T, relay *replaceableRelay, live gonostr.Event) gonostr.Event {
	t.Helper()
	c := coordinateOf(live)
	got, ok := relay.query(c)
	if !ok {
		t.Fatalf("relay has no event on %+v", c)
	}
	if got.ID == live.ID || !isTombstoneTags(got.Tags) {
		t.Fatalf("relay still serves live event on %+v (deleted tag %q)", c, tagValue(got.Tags, "deleted"))
	}
	if coordinateOf(got) != c {
		t.Fatalf("tombstone coordinate %+v != live coordinate %+v", coordinateOf(got), c)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(got.Content), &content); err != nil || content["deleted"] != true {
		t.Fatalf("tombstone content deleted != true: %s", got.Content)
	}
	for _, key := range []string{"domain", "schema", "legacy_kind"} {
		if tagValue(got.Tags, key) != tagValue(live.Tags, key) {
			t.Fatalf("tombstone %s tag %q != live %q", key, tagValue(got.Tags, key), tagValue(live.Tags, key))
		}
	}
	if got.CreatedAt <= live.CreatedAt {
		t.Fatalf("tombstone created_at %d must be after live created_at %d", got.CreatedAt, live.CreatedAt)
	}
	return got
}

func onlyLive(t *testing.T, relay *replaceableRelay, legacyKind int) gonostr.Event {
	t.Helper()
	live := relay.liveByLegacyKind(legacyKind)
	if len(live) != 1 {
		t.Fatalf("expected one live record for legacy kind %d, got %d", legacyKind, len(live))
	}
	if int(live[0].Kind) != KindCASControlState || isTombstoneTags(live[0].Tags) {
		t.Fatalf("live record for legacy kind %d is kind %d deleted=%q", legacyKind, live[0].Kind, tagValue(live[0].Tags, "deleted"))
	}
	return live[0]
}

func assertNoLegacyDNSKinds(t *testing.T, relay *replaceableRelay) {
	t.Helper()
	for _, kind := range dnsStateLegacyKinds {
		if n := relay.countKind(kind); n != 0 {
			t.Fatalf("published %d event(s) on legacy wire kind %d; DNS state lives on %d", n, kind, KindCASControlState)
		}
	}
}

type dnsTestSources struct {
	endpoints *fakeDNSProjectionSource
	zones     *fakeDNSZoneProjectionSource
	backends  *fakeDNSBackendProjectionSource
	policies  *fakeDNSPolicyProjectionSource
}

func (s dnsTestSources) options() []ProjectorOption {
	return []ProjectorOption{WithDNSProjectionSource(s.endpoints), WithDNSZoneProjectionSource(s.zones), WithDNSBackendProjectionSource(s.backends), WithDNSPolicyProjectionSource(s.policies)}
}

func testDNSEndpoint(name string) domain.DNSEndpoint {
	port := 8443
	return domain.DNSEndpoint{
		Family: domain.DNSEndpointFamilyService, Name: name, Environment: "prod", Zone: "prod.cascadia",
		FQDN: name + ".prod.cascadia", Protocol: "https", Address: "10.0.1.44", Port: &port,
		Runtime: string(domain.RuntimeTypeDocker), Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync, Source: "test",
	}
}

func fullDNSTestSources(policyID uuid.UUID) dnsTestSources {
	now := time.Now().UTC()
	return dnsTestSources{
		endpoints: &fakeDNSProjectionSource{endpoints: []domain.DNSEndpoint{testDNSEndpoint("api")}},
		zones:     &fakeDNSZoneProjectionSource{zones: []domain.DNSZone{{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityInternal, BackendRef: "fs-primary", TTL: 60}}},
		backends:  &fakeDNSBackendProjectionSource{backends: []domain.DNSBackendState{{Ref: "fs-primary", Type: domain.DNSBackendTypeFilesystem, Health: domain.HealthStatusHealthy, ZoneRefs: []string{"prod.cascadia"}, UpdatedAt: now}}},
		policies: &fakeDNSPolicyProjectionSource{policies: []domain.DNSPolicy{{
			ID: policyID, Name: "latency-aware", Enabled: true, CreatedAt: now, UpdatedAt: now,
			Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityInternal}}},
		}}},
	}
}

// TestDNSTombstonesLandOnLiveCoordinate covers the in-process removal path for
// every DNS family: the tombstone's (kind, pubkey, d) equals the live record's
// and the relay serves the tombstone afterwards.
func TestDNSTombstonesLandOnLiveCoordinate(t *testing.T) {
	ctx := context.Background()
	sources := fullDNSTestSources(uuid.New())
	relay := newReplaceableRelay()
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, newMemoryNostrEventRepo(), zap.NewNop(), sources.options()...)

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	live := map[int]gonostr.Event{}
	for _, kind := range dnsStateLegacyKinds {
		live[kind] = onlyLive(t, relay, kind)
	}

	sources.endpoints.endpoints = nil
	sources.zones.zones = nil
	sources.backends.backends = nil
	sources.policies.policies = nil
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after removal: %v", err)
	}
	for _, kind := range dnsStateLegacyKinds {
		assertRelayTombstoned(t, relay, live[kind])
	}
	assertNoLegacyDNSKinds(t, relay)
}

// TestDNSSnapshotRepairTombstonesRowsRemovedWhileDown is the restart path: a
// fresh projector (empty memory, same retained event store) must tombstone the
// DNS rows that vanished while the daemon was down, on their live coordinate,
// and must not re-sign the rows that are unchanged.
func TestDNSSnapshotRepairTombstonesRowsRemovedWhileDown(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	relay := newReplaceableRelay()
	sources := fullDNSTestSources(uuid.New())
	sources.endpoints.endpoints = append(sources.endpoints.endpoints, testDNSEndpoint("web"))

	before := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, repo, zap.NewNop(), sources.options()...)
	if err := before.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot before restart: %v", err)
	}
	var removedEndpoint, keptEndpoint gonostr.Event
	for _, ev := range relay.liveByLegacyKind(KindDNSEndpointState) {
		switch eventDTag(ev) {
		case "endpoint:service:api:prod":
			removedEndpoint = ev
		case "endpoint:service:web:prod":
			keptEndpoint = ev
		}
	}
	if removedEndpoint.ID == (gonostr.ID{}) || keptEndpoint.ID == (gonostr.ID{}) {
		t.Fatalf("expected both endpoints live before restart")
	}
	removed := []gonostr.Event{removedEndpoint}
	for _, kind := range []int{KindDNSZoneState, KindDNSBackendState, KindDNSPolicyState} {
		removed = append(removed, onlyLive(t, relay, kind))
	}

	// Daemon is down; api endpoint, zone, backend and policy are deleted.
	down := dnsTestSources{
		endpoints: &fakeDNSProjectionSource{endpoints: []domain.DNSEndpoint{testDNSEndpoint("web")}},
		zones:     &fakeDNSZoneProjectionSource{},
		backends:  &fakeDNSBackendProjectionSource{},
		policies:  &fakeDNSPolicyProjectionSource{},
	}
	after := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, repo, zap.NewNop(), down.options()...)
	if err := after.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after restart: %v", err)
	}

	for _, live := range removed {
		assertRelayTombstoned(t, relay, live)
	}
	if got, _ := relay.query(coordinateOf(keptEndpoint)); got.ID != keptEndpoint.ID {
		t.Fatalf("unchanged endpoint was replaced after restart (deleted=%q)", tagValue(got.Tags, "deleted"))
	}
	if n := relay.countOn(coordinateOf(keptEndpoint)); n != 1 {
		t.Fatalf("unchanged endpoint signed %d times, want 1 (no re-sign after restart)", n)
	}
	assertNoLegacyDNSKinds(t, relay)

	// Repair is idempotent: a third process has nothing left to tombstone.
	again := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, repo, zap.NewNop(), down.options()...)
	if err := again.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot on second restart: %v", err)
	}
	for _, live := range removed {
		if n := relay.countOn(coordinateOf(live)); n != 2 {
			t.Fatalf("coordinate %s has %d events after second restart, want live+tombstone", eventDTag(live), n)
		}
	}
}

// TestDNSSnapshotRepairSupersedesLegacyKindTombstone covers deployments that
// already ran the buggy build: the only tombstone for a removed endpoint went
// out on the legacy wire kind, so the relay still serves the live 30900 record.
// Restart repair must treat that coordinate as live and tombstone it properly.
func TestDNSSnapshotRepairSupersedesLegacyKindTombstone(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	relay := newReplaceableRelay()
	source := &fakeDNSProjectionSource{endpoints: []domain.DNSEndpoint{testDNSEndpoint("api")}}
	before := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, repo, zap.NewNop(), WithDNSProjectionSource(source))
	if err := before.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	live := onlyLive(t, relay, KindDNSEndpointState)

	legacy := gonostr.Event{
		Kind:      gonostr.Kind(KindDNSEndpointState),
		CreatedAt: live.CreatedAt + 5,
		Tags:      gonostr.Tags{{"d", eventDTag(live)}, {"deleted", "true"}, {"t", "dns-endpoint"}},
		Content:   `{"deleted":true,"coordinate":"endpoint:service:api:prod"}`,
	}
	if err := signEventWithPrivateKeyHex(&legacy, projectorTestPrivateKey); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Publish(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	tagsJSON, _ := json.Marshal(legacy.Tags)
	if _, err := repo.Record(ctx, &repository.NostrEventRecord{ID: legacy.ID.Hex(), Kind: KindDNSEndpointState, PubKey: legacy.PubKey.Hex(), Content: legacy.Content, Tags: tagsJSON, CreatedAt: legacy.CreatedAt.Time()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := relay.query(coordinateOf(live)); got.ID != live.ID {
		t.Fatal("precondition: a legacy-kind tombstone must not touch the live coordinate")
	}

	after := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, repo, zap.NewNop(), WithDNSProjectionSource(&fakeDNSProjectionSource{}))
	if err := after.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after restart: %v", err)
	}
	assertRelayTombstoned(t, relay, live)
}

// TestServiceStateTombstoneSharesLiveCoordinate pins B-19: the bus-driven
// service-state tombstone uses d=service:<sid>:environment:<eid>, the live d.
// The tombstone follows the live publish within the same second, so this also
// proves created_at is bumped past the live event instead of tying with it.
func TestServiceStateTombstoneSharesLiveCoordinate(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	relay := newReplaceableRelay()
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, newMemoryNostrEventRepo(), zap.NewNop())

	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	if err := projector.publishStateForTest(ctx, &state); err != nil {
		t.Fatalf("publish state: %v", err)
	}
	live := onlyLive(t, relay, KindServiceState)
	if want := "service:" + serviceID.String() + ":environment:" + envID.String(); eventDTag(live) != want {
		t.Fatalf("live d = %q, want %q", eventDTag(live), want)
	}

	// Phase 3 S1: state tombstones are now published by the reconciler's
	// StateTombstoneHandler; use the test helper for projector-level tests.
	if err := projector.publishStateTombstoneForTest(ctx, events.ResourceData{ServiceID: serviceID.String(), EnvironmentID: envID.String(), Deleted: true}); err != nil {
		t.Fatalf("publish tombstone: %v", err)
	}
	tombstone := assertRelayTombstoned(t, relay, live)
	if tagValue(tombstone.Tags, "service") != serviceID.String() || tagValue(tombstone.Tags, "environment") != envID.String() {
		t.Fatalf("tombstone lost service/environment scope tags: %v", tombstone.Tags)
	}
}

func TestLLMRouteStateTombstoneSharesLiveCoordinate(t *testing.T) {
	ctx := context.Background()
	routeID, envID := uuid.New(), uuid.New()
	relay := newReplaceableRelay()
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, newMemoryNostrEventRepo(), zap.NewNop())

	// Phase 3 L1: LLM publish methods moved out of projector; use publishControlState directly.
	dTag := fmt.Sprintf("%s:%s", routeID, envID)
	if err := projector.publishControlState(ctx, KindLLMRouteState, dTag, false, nil, `{"route_id":"`+routeID.String()+`"}`, "llm_route_state", nil); err != nil {
		t.Fatalf("publish LLM route state: %v", err)
	}
	live := onlyLive(t, relay, KindLLMRouteState)
	if err := projector.publishControlState(ctx, KindLLMRouteState, dTag, true, nil, `{"deleted":true}`, "llm_route_state", nil); err != nil {
		t.Fatalf("publish LLM route state tombstone: %v", err)
	}
	assertRelayTombstoned(t, relay, live)
}

// Every projected family's live record and tombstone come from one builder.
func TestControlStateEnvelopeLiveAndTombstoneShareCoordinate(t *testing.T) {
	for _, legacyKind := range append([]int{KindServiceState, KindLLMRouteState}, dnsStateLegacyKinds...) {
		liveKind, liveTags := controlStateEnvelope(legacyKind, "id-1", false)
		deadKind, deadTags := controlStateEnvelope(legacyKind, "id-1", true)
		if liveKind != KindCASControlState || deadKind != liveKind {
			t.Fatalf("legacy kind %d: wire kinds live=%d tombstone=%d, want %d", legacyKind, liveKind, deadKind, KindCASControlState)
		}
		if tagValue(liveTags, "d") != tagValue(deadTags, "d") || tagValue(deadTags, "deleted") != "true" || tagValue(liveTags, "deleted") != "false" {
			t.Fatalf("legacy kind %d: live/tombstone envelopes diverge: %v vs %v", legacyKind, liveTags, deadTags)
		}
	}
}
