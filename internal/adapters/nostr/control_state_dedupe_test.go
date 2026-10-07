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
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	// publishStateForTest calls through the same dedupe pipeline as the
	// mutation-site publishers.
	for i := 0; i < 3; i++ {
		if err := projector.publishStateForTest(ctx, &state); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("service state events after 3 unchanged publishes = %d, want 1", got)
	}
	m := projector.ProjectionMetrics()["service/state"]
	if m.Accepted != 1 || m.Deduped < 2 {
		t.Fatalf("service/state metrics = %+v, want accepted=1 deduped>=2", m)
	}

	// A REAL change publishes exactly once more.
	state.DriftStatus = domain.DriftStatusDrifted
	if err := projector.publishStateForTest(ctx, &state); err != nil {
		t.Fatalf("publish after change: %v", err)
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
	now := time.Now().UTC()
	state := dedupeTestState(serviceID, envID, now)
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	// publishStateForTest calls through the same dedupe pipeline as the
	// mutation-site publishers.
	if err := projector.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Minute)
	newObs := uuid.New()
	state.UpdatedAt = later
	state.LastReconciledAt = &later
	state.CurrentObservationID = &newObs
	if err := projector.publishStateForTest(ctx, &state); err != nil {
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
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
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

// TestSystemConfigStartupPublishDoesNotRepeat covers the startup publish path:
// a second call with nothing changed emits no discovery or DM relay-list
// events (replaces the old RepublishSnapshot dedupe test).
func TestSystemConfigStartupPublishDoesNotRepeat(t *testing.T) {
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
	projector := newTestProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))

	projector.publishSystemConfigOnStartup(ctx)
	sink.mu.Lock()
	first := len(sink.events)
	sink.mu.Unlock()
	if first == 0 {
		t.Fatal("first startup publish published nothing")
	}
	projector.publishSystemConfigOnStartup(ctx)
	sink.mu.Lock()
	second := len(sink.events)
	sink.mu.Unlock()
	if second != first {
		t.Fatalf("unchanged startup re-signed %d events (had %d, now %d)", second-first, first, second)
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
	projector := newTestProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	res := events.ResourceData{ServiceID: serviceID.String(), EnvironmentID: envID.String()}

	for i := 0; i < 2; i++ {
		if err := projector.publishStateTombstoneForTest(ctx, res); err != nil {
			t.Fatalf("tombstone %d: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindServiceState, true); got != 2 {
		t.Fatalf("tombstones published = %d, want 2 (never suppressed)", got)
	}
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	if err := projector.publishStateForTest(ctx, &state); err != nil {
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
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	clock := time.Unix(1_800_000_000, 0).UTC()
	s := projector.projection()
	s.now = func() time.Time { return clock }
	s.jitterSource = nil

	// Learn the wire kind for service state from a successful publish.
	state := dedupeTestState(serviceID, envID, clock)
	if err := projector.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	wire := eventKindInt(&sink.events[0])
	sink.mu.Unlock()

	// Reject the next publish (a changed state) -> backoff opens.
	sink.errorsByKind = map[int]error{wire: errors.New("rate-limited")}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := projector.publishStateForTest(ctx, &state); err == nil {
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
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	const burst = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = projector.publishStateForTest(ctx, &state)
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

	first := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), &captureProjectionPublisher{}, repo, zap.NewNop())
	if err := first.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}

	// Restart: new instance, same retained store, empty in-memory cache.
	sink := &captureProjectionPublisher{}
	restarted := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	if err := restarted.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("restart re-signed %d unchanged events, want 0", got)
	}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := restarted.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("real change after restart = %d events, want 1", got)
	}
}

type transientHydrationRepo struct {
	*memoryNostrEventRepo
	mu       sync.Mutex
	loadErr  error
	failures int
	loads    int
}

func (r *transientHydrationRepo) ListByKind(ctx context.Context, kind, limit int) ([]repository.NostrEventRecord, error) {
	r.mu.Lock()
	r.loads++
	if r.failures > 0 {
		r.failures--
		r.mu.Unlock()
		return nil, r.loadErr
	}
	r.mu.Unlock()
	return r.memoryNostrEventRepo.ListByKind(ctx, kind, limit)
}

func (r *transientHydrationRepo) loadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads
}

// A failed retained-state read must neither sign an unchanged event nor make
// the kind permanently ready. The next event after backoff retries hydration.
func TestProjectionHydrationFailureFailsClosedAndRecovers(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	retained := newMemoryNostrEventRepo()
	first := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), &captureProjectionPublisher{}, retained, zap.NewNop())
	if err := first.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}

	readErr := errors.New("retained-state read unavailable")
	repo := &transientHydrationRepo{memoryNostrEventRepo: retained, loadErr: readErr, failures: 1}
	sink := &captureProjectionPublisher{}
	restarted := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	clock := time.Unix(1_800_000_000, 0).UTC()
	restarted.projection().now = func() time.Time { return clock }

	if err := restarted.publishStateForTest(ctx, &state); !errors.Is(err, readErr) {
		t.Fatalf("first publish error = %v, want retained-state read error", err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("failed hydration published %d events, want 0", got)
	}
	if err := restarted.publishStateForTest(ctx, &state); !errors.Is(err, ErrProjectorHydrationBackoff) {
		t.Fatalf("publish before hydration retry = %v, want backoff", err)
	}
	if got := repo.loadCount(); got != 1 {
		t.Fatalf("retained-state loads during backoff = %d, want 1", got)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("cold cache published %d events during backoff, want 0", got)
	}

	clock = clock.Add(projectionHydrationBackoffMin)
	if err := restarted.publishStateForTest(ctx, &state); err != nil {
		t.Fatalf("publish after successful retry: %v", err)
	}
	if got := repo.loadCount(); got != 2 {
		t.Fatalf("retained-state loads after retry = %d, want 2", got)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("unchanged state after restart published %d events, want 0", got)
	}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := restarted.publishStateForTest(ctx, &state); err != nil {
		t.Fatalf("real change after hydration: %v", err)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 1 {
		t.Fatalf("real change after hydration published %d events, want 1", got)
	}
	if got := repo.loadCount(); got != 2 {
		t.Fatalf("already hydrated kind reloaded %d times, want 2", got)
	}
}

// A retained-state read failure must not hold back a tombstone: tombstones
// are never deduped, so they do not depend on the hydrated cache.
func TestProjectionHydrationFailureDoesNotSuppressTombstones(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	readErr := errors.New("retained-state read unavailable")
	repo := &transientHydrationRepo{memoryNostrEventRepo: newMemoryNostrEventRepo(), loadErr: readErr, failures: 2}
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	clock := time.Unix(1_800_000_000, 0).UTC()
	projector.projection().now = func() time.Time { return clock }
	res := events.ResourceData{ServiceID: serviceID.String(), EnvironmentID: envID.String()}

	// First tombstone hits the failing load; the second lands inside the
	// hydration backoff window. Both must publish.
	for i := 0; i < 2; i++ {
		if err := projector.publishStateTombstoneForTest(ctx, res); err != nil {
			t.Fatalf("tombstone %d during hydration failure: %v", i, err)
		}
	}
	if got := countLegacy(sink, KindServiceState, true); got != 2 {
		t.Fatalf("tombstones published during hydration failure = %d, want 2", got)
	}
	state := dedupeTestState(serviceID, envID, clock)
	if err := projector.publishStateForTest(ctx, &state); !errors.Is(err, ErrProjectorHydrationBackoff) {
		t.Fatalf("non-tombstone during hydration backoff = %v, want hydration backoff", err)
	}
}

type blockingHydrationRepo struct {
	*memoryNostrEventRepo
	release chan struct{}
	entered chan struct{}
	mu      sync.Mutex
	loads   int
}

func (r *blockingHydrationRepo) ListByKind(ctx context.Context, kind, limit int) ([]repository.NostrEventRecord, error) {
	r.mu.Lock()
	r.loads++
	first := r.loads == 1
	r.mu.Unlock()
	if first {
		close(r.entered)
		<-r.release
	}
	return r.memoryNostrEventRepo.ListByKind(ctx, kind, limit)
}

// Concurrent publishes on a cold wire kind share one retained-state load and
// none of them observes the kind as ready before the load has been applied,
// so an unchanged coordinate is never re-signed during the race.
func TestProjectionConcurrentHydrationIsSerialized(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())
	retained := newMemoryNostrEventRepo()
	first := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), &captureProjectionPublisher{}, retained, zap.NewNop())
	if err := first.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}

	repo := &blockingHydrationRepo{memoryNostrEventRepo: retained, release: make(chan struct{}), entered: make(chan struct{})}
	sink := &captureProjectionPublisher{}
	restarted := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())

	const workers = 16
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := state
			errs <- restarted.publishStateForTest(ctx, &st)
		}()
	}
	<-repo.entered
	close(repo.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish: %v", err)
		}
	}
	repo.mu.Lock()
	loads := repo.loads
	repo.mu.Unlock()
	if loads != 1 {
		t.Fatalf("retained-state loads = %d, want 1", loads)
	}
	if got := countLegacy(sink, KindServiceState, false); got != 0 {
		t.Fatalf("unchanged state published %d times during concurrent hydration, want 0", got)
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

// TestBackoffRetryTimerFlushesPendingRecords proves that when a publish is
// suppressed by the shared backoff window, the record is saved and flushed
// when the backoff timer fires (event-driven retry).
func TestBackoffRetryTimerFlushesPendingRecords(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	clock := time.Unix(1_800_000_000, 0).UTC()
	s := projector.projection()
	s.now = func() time.Time { return clock }
	s.jitterSource = nil

	// First publish succeeds to prime the cache.
	state := dedupeTestState(serviceID, envID, clock)
	if err := projector.publishStateForTest(ctx, &state); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	wire := eventKindInt(&sink.events[0])
	sink.mu.Unlock()

	// Reject the next publish to open backoff.
	sink.errorsByKind = map[int]error{wire: errors.New("rate-limited")}
	state.DriftStatus = domain.DriftStatusDrifted
	if err := projector.publishStateForTest(ctx, &state); err == nil {
		t.Fatal("expected rejection error")
	}

	// Now relay is healthy but backoff is open. A publish of new state
	// should be suppressed and saved as pending.
	sink.errorsByKind = nil
	state.DriftStatus = domain.DriftStatusDeploying
	err := projector.publishStateForTest(ctx, &state)
	if !errors.Is(err, ErrProjectorBackoff) {
		t.Fatalf("error = %v, want ErrProjectorBackoff", err)
	}

	// Verify pending retry was saved.
	s.mu.Lock()
	pendingCount := len(s.pendingRetries)
	s.mu.Unlock()
	if pendingCount == 0 {
		t.Fatal("expected pending retry to be saved during backoff")
	}

	// Advance past the backoff window and flush manually (simulates timer).
	clock = clock.Add(projectionBackoffMax + time.Second)
	projector.flushPendingRetries()

	// The pending record should now be published.
	s.mu.Lock()
	afterPending := len(s.pendingRetries)
	s.mu.Unlock()
	if afterPending != 0 {
		t.Fatalf("pending retries after flush = %d, want 0", afterPending)
	}

	// Count: initial + retry = 2 (the rejected publish is not captured by the sink).
	sink.mu.Lock()
	total := len(sink.events)
	sink.mu.Unlock()
	if total != 2 {
		t.Fatalf("total events = %d, want 2 (initial + retry)", total)
	}
}
