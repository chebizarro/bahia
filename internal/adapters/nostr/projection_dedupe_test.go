package nostr

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// countLegacy counts sink events belonging to a legacy projection family
// (matched by the legacy_kind tag), optionally only tombstones.
func countLegacy(sink *captureProjectionPublisher, legacyKind int, tombstonesOnly bool) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	want := strconv.Itoa(legacyKind)
	n := 0
	for _, ev := range sink.events {
		if tagValue(ev.Tags, "legacy_kind") != want {
			continue
		}
		if tombstonesOnly && !isTombstoneTags(ev.Tags) {
			continue
		}
		n++
	}
	return n
}

func countWireKind(sink *captureProjectionPublisher, wireKind int) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	n := 0
	for i := range sink.events {
		if eventKindInt(&sink.events[i]) == wireKind {
			n++
		}
	}
	return n
}

func dedupeTestState(serviceID, envID uuid.UUID, now time.Time) domain.EnvironmentServiceState {
	artifactID := uuid.New()
	obsID := uuid.New()
	return domain.EnvironmentServiceState{
		ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID,
		CurrentObservationID: &obsID, DriftStatus: domain.DriftStatusInSync,
		LastReconciledAt: &now, UpdatedAt: now, DesiredHash: "sha256:desired",
	}
}

// TestProjectionUnchangedServiceStateEmitsNoNewEvent is the core projector-side
// P0 regression: republishing an unchanged read model must not re-sign it.
func TestProjectionUnchangedServiceStateEmitsNoNewEvent(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	source := newFakeProjectionSource()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	source.states[stateKeyForTest(serviceID, envID)] = state
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())

	for i := 0; i < 3; i++ {
		if err := projector.RepublishSnapshot(ctx); err != nil {
			t.Fatalf("republish %d: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("service state events after 3 unchanged republishes = %d, want 1", got)
	}
	m := projector.ProjectionMetrics()["service/state"]
	if m.Accepted != 1 || m.Deduped < 2 {
		t.Fatalf("service/state metrics = %+v, want accepted=1 deduped>=2", m)
	}

	// A REAL change republishes exactly once more.
	state.DriftStatus = domain.DriftStatusDrifted
	source.states[stateKeyForTest(serviceID, envID)] = state
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish after change: %v", err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 2 {
		t.Fatalf("service state events after real change = %d, want 2", got)
	}
}

// TestProjectionIgnoresVolatileBookkeepingFields proves that bumping only the
// volatile bookkeeping the reconciler touches on every no-op pass does not
// produce a new event.
func TestProjectionIgnoresVolatileBookkeepingFields(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	source := newFakeProjectionSource()
	now := time.Now().UTC()
	state := dedupeTestState(serviceID, envID, now)
	source.states[stateKeyForTest(serviceID, envID)] = state
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Minute)
	newObs := uuid.New()
	state.UpdatedAt = later
	state.LastReconciledAt = &later
	state.CurrentObservationID = &newObs
	source.states[stateKeyForTest(serviceID, envID)] = state
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("volatile-only change produced %d service state events, want 1", got)
	}
}

// TestProjectionUnchangedDNSEndpointEmitsNoNewEvent generalizes the original
// DNS containment: only MaterializedAt differs, so no new event.
func TestProjectionUnchangedDNSEndpointEmitsNoNewEvent(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	endpoint := domain.DNSEndpoint{Coordinate: "svc:api", FQDN: "api.example", MaterializedAt: time.Now().UTC()}
	for i := 0; i < 3; i++ {
		endpoint.MaterializedAt = endpoint.MaterializedAt.Add(time.Minute)
		if err := projector.publishReplaceableJSON(ctx, KindDNSEndpointState, endpoint.Coordinate, gonostr.Tags{}, endpoint, "dns_endpoint.projection", nil); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindDNSEndpointState, false); got != 1 {
		t.Fatalf("DNS endpoint events = %d, want 1", got)
	}
	endpoint.FQDN = "api2.example"
	if err := projector.publishReplaceableJSON(ctx, KindDNSEndpointState, endpoint.Coordinate, gonostr.Tags{}, endpoint, "dns_endpoint.projection", nil); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindDNSEndpointState, false); got != 2 {
		t.Fatalf("DNS endpoint events after real change = %d, want 2", got)
	}
}

// TestProjectionStartupRepairAndSystemDiscoveryDoNotRepeat covers the periodic
// snapshot repair path: a second full repair with nothing changed emits no
// discovery or read-model events.
func TestProjectionStartupRepairAndSystemDiscoveryDoNotRepeat(t *testing.T) {
	ctx := context.Background()
	withProjectorVersionVars(t, "0.1.0", "abcdef1234567890", "")
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"wss://browser.example"}
	cfg.Nostr.ContextVMRelays = []string{"wss://contextvm.example"}
	cfg.Nostr.ServiceRelays = []string{"wss://service.example"}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("first repair: %v", err)
	}
	sink.mu.Lock()
	first := len(sink.events)
	sink.mu.Unlock()
	if first == 0 {
		t.Fatal("first repair published nothing")
	}
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("second repair: %v", err)
	}
	sink.mu.Lock()
	second := len(sink.events)
	sink.mu.Unlock()
	if second != first {
		t.Fatalf("unchanged repair re-signed %d events (had %d, now %d)", second-first, first, second)
	}
}

// TestProjectionTombstonesNeverSuppressedAndRecreateRepublishes proves a
// tombstone always publishes and that a later re-creation of the coordinate
// is not mistaken for the cached tombstone.
func TestProjectionTombstonesNeverSuppressedAndRecreateRepublishes(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	sink := &captureProjectionPublisher{}
	source := newFakeProjectionSource()
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	res := events.ResourceData{ServiceID: serviceID.String(), EnvironmentID: envID.String()}

	for i := 0; i < 2; i++ {
		if err := projector.publishStateTombstone(ctx, res); err != nil {
			t.Fatalf("tombstone %d: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindServiceState, true); got != 2 {
		t.Fatalf("tombstones published = %d, want 2 (never suppressed)", got)
	}
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	if err := projector.publishState(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false) - countLegacy(sink, KindServiceState, true); got != 1 {
		t.Fatalf("re-created state events = %d, want 1", got)
	}
}

// TestProjectionRejectionOpensSharedBackoffThenRecovers proves a synchronous
// relay rejection opens one bounded backoff shared across families: while it
// is open no relay attempt is made (fail fast, counted), and after it expires
// publishing resumes.
func TestProjectionRejectionOpensSharedBackoffThenRecovers(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	clock := time.Unix(1_800_000_000, 0).UTC()
	s := projector.projection()
	s.now = func() time.Time { return clock }
	s.jitterSource = nil

	// Learn the wire kind for service state from a successful publish.
	state := dedupeTestState(serviceID, envID, clock)
	if err := projector.publishState(ctx, &state); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	wire := eventKindInt(&sink.events[0])
	sink.mu.Unlock()

	// Reject the next publish (a changed state) -> backoff opens.
	sink.errorsByKind = map[int]error{wire: errors.New("rate-limited")}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := projector.publishState(ctx, &state); err == nil {
		t.Fatal("expected rejection error")
	}
	if m := projector.ProjectionMetrics()["service/state"]; m.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", m.Rejected)
	}

	// Relay is healthy again, but the window is open: a DIFFERENT family is
	// also held back without touching the relay.
	sink.errorsByKind = nil
	sink.mu.Lock()
	before := len(sink.events)
	sink.mu.Unlock()
	other := domain.DNSEndpoint{Coordinate: "svc:other", FQDN: "o.example"}
	err := projector.publishReplaceableJSON(ctx, KindDNSEndpointState, other.Coordinate, gonostr.Tags{}, other, "dns_endpoint.projection", nil)
	if !errors.Is(err, ErrProjectorBackoff) {
		t.Fatalf("error = %v, want ErrProjectorBackoff", err)
	}
	sink.mu.Lock()
	after := len(sink.events)
	sink.mu.Unlock()
	if after != before {
		t.Fatal("publish during backoff must not reach the relay")
	}
	if m := projector.ProjectionMetrics()["dns/endpoint"]; m.Backoff != 1 {
		t.Fatalf("dns/endpoint backoff = %d, want 1", m.Backoff)
	}

	// Advance past the window: publishing resumes and the window resets.
	clock = clock.Add(projectionBackoffMax + time.Second)
	if err := projector.publishReplaceableJSON(ctx, KindDNSEndpointState, other.Coordinate, gonostr.Tags{}, other, "dns_endpoint.projection", nil); err != nil {
		t.Fatalf("publish after backoff: %v", err)
	}
	if projector.projectionBackoffActive() {
		t.Fatal("backoff must reset after a successful publish")
	}
}

// TestProjectionBurstCoalescesToSinglePublish proves concurrent triggers for
// the same coordinate collapse into exactly one signed event.
func TestProjectionBurstCoalescesToSinglePublish(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	const burst = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = projector.publishState(ctx, &state)
		}()
	}
	close(start)
	wg.Wait()
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("burst produced %d events, want 1", got)
	}
	m := projector.ProjectionMetrics()["service/state"]
	if m.Accepted != 1 || m.Deduped+m.Coalesced != burst-1 {
		t.Fatalf("metrics = %+v, want accepted=1 and deduped+coalesced=%d", m, burst-1)
	}
}

// TestProjectionDedupeHydratesAcrossRestart proves the cache is warmed from
// retained records: a fresh projector over the same store does not re-sign an
// unchanged coordinate, but does publish a real change.
func TestProjectionDedupeHydratesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	repo := &memoryNostrEventRepo{records: map[string]repository.NostrEventRecord{}}
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	first := NewProjector(projectorTestConfig(), newFakeProjectionSource(), &captureProjectionPublisher{}, repo, zap.NewNop())
	if err := first.publishState(ctx, &state); err != nil {
		t.Fatal(err)
	}

	// Restart: new instance, same retained store, empty in-memory cache.
	sink := &captureProjectionPublisher{}
	restarted := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	if err := restarted.publishState(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("restart re-signed %d unchanged events, want 0", got)
	}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := restarted.publishState(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("real change after restart = %d events, want 1", got)
	}
}

// TestProjectionAuditLogIsNeverDeduped proves the append-only audit path is
// excluded from dedupe: identical audits are distinct records.
func TestProjectionAuditLogIsNeverDeduped(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	ev := events.Event{
		Type: events.EventEnvironmentServiceStateChanged, EntityID: "svc:env",
		Data: events.ResourceData{ServiceID: uuid.NewString(), EnvironmentID: uuid.NewString()},
	}
	for i := 0; i < 2; i++ {
		if err := projector.publishAudit(ctx, ev); err != nil {
			t.Fatalf("audit %d: %v", i, err)
		}
	}
	if got := countWireKind(sink, KindCASAudit); got != 2 {
		t.Fatalf("audit events = %d, want 2 (never deduped)", got)
	}
	if m := projector.ProjectionMetrics()[projectionFamilyAudit]; m.Accepted != 2 || m.Deduped != 0 {
		t.Fatalf("audit metrics = %+v, want accepted=2 deduped=0", m)
	}
}

// TestProjectionFingerprintIsStableAndStripsVolatileKeys unit-tests the hash.
func TestProjectionFingerprintIsStableAndStripsVolatileKeys(t *testing.T) {
	tags := gonostr.Tags{{"d", "x"}, {"legacy_kind", "1"}}
	a := projectionFingerprint(1, tags, `{"z":1,"a":2,"updated_at":"t1","observed_at":"o1"}`)
	b := projectionFingerprint(1, gonostr.Tags{{"legacy_kind", "1"}, {"d", "x"}}, `{"a":2,"z":1,"updated_at":"t2","current_observation_id":"n"}`)
	if a != b {
		t.Fatal("fingerprint must ignore tag order, key order, and volatile keys")
	}
	c := projectionFingerprint(1, tags, `{"z":1,"a":3}`)
	if a == c {
		t.Fatal("fingerprint must change on a real content change")
	}
}
