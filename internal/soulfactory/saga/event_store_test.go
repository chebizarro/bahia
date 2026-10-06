package saga

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const sagaTestKey = "a777777777777777777777777777777777777777777777777777777777777777"

// sagaStorePublisher signs a record with the service key and keeps it in the
// local event store, as the daemon's publisher does before relay delivery.
type sagaStorePublisher struct {
	store  *localstore.Store
	secret gonostr.SecretKey

	mu     sync.Mutex
	events []gonostr.Event
}

func (p *sagaStorePublisher) PublishSignedEvent(_ context.Context, event *gonostr.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := event.Sign(p.secret); err != nil {
		return err
	}
	if _, err := p.store.SaveEvent(*event); err != nil {
		return err
	}
	p.events = append(p.events, *event)
	return nil
}

func (p *sagaStorePublisher) published() []gonostr.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]gonostr.Event(nil), p.events...)
}

// sagaHost is one daemon host: a local event store, the publisher over it and
// an optional checkpoint cache directory.
type sagaHost struct {
	store     *localstore.Store
	publisher *sagaStorePublisher
	secret    gonostr.SecretKey
	clock     *sagaClock
}

type sagaClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *sagaClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func newSagaHost(t *testing.T) *sagaHost {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	secret, err := gonostr.SecretKeyFromHex(sagaTestKey)
	if err != nil {
		t.Fatal(err)
	}
	return &sagaHost{store: store, publisher: &sagaStorePublisher{store: store, secret: secret}, secret: secret, clock: &sagaClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}}
}

func (h *sagaHost) eventStore(t *testing.T, cacheDir string) *EventStore {
	t.Helper()
	store, err := NewEventStore(EventStoreConfig{Reader: h.store, Publisher: h.publisher, ServicePubkey: h.secret.Public().Hex(), CacheDir: cacheDir, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// replicate copies every saga-run record of the host into other, as relay
// catch-up fills a fresh host's local store.
func (h *sagaHost) replicate(t *testing.T, other *sagaHost) {
	t.Helper()
	for ev := range h.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Tags: gonostr.TagMap{"t": {kinds.CPStateTopicSoulFactorySagaRun}}}) {
		if _, err := other.store.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func fixtureEngineWith(t *testing.T, store Store, mutate func(map[Stage]*memoryDriver)) (*Engine, map[Stage]*memoryDriver) {
	t.Helper()
	drivers := map[Stage]*memoryDriver{}
	interfaces := make([]StageDriver, 0, len(forwardStages))
	for _, stage := range forwardStages {
		driver := &memoryDriver{stage: stage}
		drivers[stage] = driver
		interfaces = append(interfaces, driver)
	}
	if mutate != nil {
		mutate(drivers)
	}
	engine, err := NewEngine(store, interfaces)
	if err != nil {
		t.Fatal(err)
	}
	return engine, drivers
}

func applyCounts(drivers map[Stage]*memoryDriver) map[Stage]int {
	out := map[Stage]int{}
	for stage, driver := range drivers {
		driver.mu.Lock()
		out[stage] = driver.applyCount
		driver.mu.Unlock()
	}
	return out
}

func TestEventStoreFreshHostResumesNonTerminalRunFromCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	first := newSagaHost(t)
	retryable := &SafeError{Code: "response_lost", Retryable: true}
	engine, drivers := fixtureEngineWith(t, first.eventStore(t, filepath.Join(t.TempDir(), "sagas")), func(ds map[Stage]*memoryDriver) {
		ds[StageSignerEnrolled].applyErr = retryable
		ds[StageSignerEnrolled].applyLeavesAbsent = true
	})
	if _, err := engine.Start(ctx, "request-fresh", "run-fresh", "agent-fresh", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Reconcile(ctx, "request-fresh", false)
	if err == nil || report.Stage != StageFailedRecoverable {
		t.Fatalf("expected a recoverable failure, got stage %v err %v", report, err)
	}
	before := applyCounts(drivers)

	// The daemon moves to a fresh host with relay state only: no checkpoint
	// file, no in-process copy. The engine resumes the run from its
	// canonical record and the drivers' inspected reality.
	second := newSagaHost(t)
	first.replicate(t, second)
	freshCache := filepath.Join(t.TempDir(), "sagas")
	resumed, sharedDrivers := fixtureEngineWith(t, second.eventStore(t, freshCache), func(ds map[Stage]*memoryDriver) {
		for stage, driver := range drivers {
			driver.mu.Lock()
			ds[stage].resource, ds[stage].extraResources = driver.resource, append([]Resource(nil), driver.extraResources...)
			driver.mu.Unlock()
		}
	})
	loaded, err := resumed.store.Load(ctx, "request-fresh")
	if err != nil {
		t.Fatalf("fresh host could not load the run from its record: %v", err)
	}
	if loaded.Stage != StageFailedRecoverable || loaded.ResumeStage != StageSignerEnrolled || len(loaded.Resources) != 4 {
		t.Fatalf("fresh host loaded run = stage %s resume %s resources %d", loaded.Stage, loaded.ResumeStage, len(loaded.Resources))
	}
	report, err = resumed.Reconcile(ctx, "request-fresh", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stage != StageRunning {
		t.Fatalf("stage = %s", report.Stage)
	}
	after := applyCounts(sharedDrivers)
	for i, stage := range forwardStages {
		wantBefore, wantAfter := 0, 1
		if i < 4 {
			// Completed before the move: never re-applied on the fresh host.
			wantBefore, wantAfter = 1, 0
		} else if stage == StageSignerEnrolled {
			// Failed before the move without a side effect; retried once.
			wantBefore = 1
		}
		if before[stage] != wantBefore || after[stage] != wantAfter {
			t.Fatalf("%s applied %d times before and %d after the move (want %d/%d)", stage, before[stage], after[stage], wantBefore, wantAfter)
		}
	}
	// The resumed run is canonical on the fresh host: a third restart there
	// finds the terminal record and replays nothing.
	third, thirdDrivers := fixtureEngineWith(t, second.eventStore(t, ""), func(ds map[Stage]*memoryDriver) {
		for stage, driver := range sharedDrivers {
			driver.mu.Lock()
			ds[stage].resource, ds[stage].extraResources = driver.resource, append([]Resource(nil), driver.extraResources...)
			driver.mu.Unlock()
		}
	})
	if _, err := third.Reconcile(ctx, "request-fresh", false); err != nil {
		t.Fatal(err)
	}
	for stage, count := range applyCounts(thirdDrivers) {
		if count != 0 {
			t.Fatalf("%s re-applied after terminal record: %d", stage, count)
		}
	}
}

func TestEventStoreRecordWinsOverStaleCacheFile(t *testing.T) {
	ctx := context.Background()
	host := newSagaHost(t)
	cache := filepath.Join(t.TempDir(), "sagas")
	store := host.eventStore(t, cache)
	run, err := NewRun("request-stale", "run-stale", "agent-stale", "sha256:spec", host.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	// A stale file: the version the cache held before two newer checkpoints
	// were published (the daemon's disk was restored from an older snapshot).
	stale := run.clone()
	for _, to := range []Stage{StageIdentityReserved, StageServiceRegistered} {
		next := run.clone()
		next.Stage = to
		next.Transitions = append(next.Transitions, Transition{From: run.Stage, To: to, At: host.clock.Now()})
		next.Version = run.Version + 1
		if err := store.Save(ctx, next, run.Version); err != nil {
			t.Fatal(err)
		}
		run = next
	}
	if err := writeAtomic(store.cachePath("request-stale"), stale); err != nil {
		t.Fatal(err)
	}
	restarted := host.eventStore(t, cache)
	loaded, err := restarted.Load(ctx, "request-stale")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 3 || loaded.Stage != StageServiceRegistered {
		t.Fatalf("stale file won: version %d stage %s", loaded.Version, loaded.Stage)
	}
	// Saving against the stale file's version is the conflict the engine's
	// optimistic versioning promises.
	rewrite := stale.clone()
	rewrite.Version = 2
	if err := restarted.Save(ctx, rewrite, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save err = %v", err)
	}
	// Without any file the record alone is the run.
	bare := host.eventStore(t, "")
	if loaded, err = bare.Load(ctx, "request-stale"); err != nil || loaded.Version != 3 {
		t.Fatalf("record-only load = %v, %v", loaded, err)
	}
	// A file ahead of the record is a journal written after the record it
	// follows: it wins, as the newest version.
	ahead := run.clone()
	ahead.Version = 4
	ahead.Stage = StageReleaseSelected
	if err := writeAtomic(store.cachePath("request-stale"), ahead); err != nil {
		t.Fatal(err)
	}
	if loaded, err = host.eventStore(t, cache).Load(ctx, "request-stale"); err != nil || loaded.Version != 4 {
		t.Fatalf("newer file load = %v, %v", loaded, err)
	}
}

func TestEventStoreRecordIsBoundedAcrossRetries(t *testing.T) {
	ctx := context.Background()
	host := newSagaHost(t)
	retryable := &SafeError{Code: "response_lost", Retryable: true}
	engine, _ := fixtureEngineWith(t, host.eventStore(t, ""), func(ds map[Stage]*memoryDriver) {
		ds[StageRuntimeAllocated].applyErr = retryable
		ds[StageRuntimeAllocated].applyLeavesAbsent = true
	})
	if _, err := engine.Start(ctx, "request-bounded", "run-bounded", "agent-bounded", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	sizeAfter := func(retries int) int {
		t.Helper()
		for i := 0; i < retries; i++ {
			if _, err := engine.Retry(ctx, "request-bounded", false); err == nil {
				t.Fatal("expected the retryable failure to persist")
			}
		}
		published := host.publisher.published()
		return len(published[len(published)-1].Content)
	}
	first := sizeAfter(recordHistoryLimit)
	later := sizeAfter(3 * recordHistoryLimit)
	if later > first+64 {
		t.Fatalf("record grew with retries: %d -> %d bytes", first, later)
	}
	published := host.publisher.published()
	var record Record
	if err := json.Unmarshal([]byte(published[len(published)-1].Content), &record); err != nil {
		t.Fatal(err)
	}
	if len(record.RecentTransitions) != recordHistoryLimit || len(record.RecentFailures) != recordHistoryLimit {
		t.Fatalf("recent history = %d transitions %d failures", len(record.RecentTransitions), len(record.RecentFailures))
	}
	if record.TransitionCount <= recordHistoryLimit || record.FailureCount != 4*recordHistoryLimit {
		t.Fatalf("totals = %d transitions %d failures", record.TransitionCount, record.FailureCount)
	}
	if record.Stage != StageFailedRecoverable || record.ResumeStage != StageRuntimeAllocated || record.Failure == nil || len(record.Resources) != 3 {
		t.Fatalf("record lost resume state: %+v", record)
	}
	// Every run has exactly one live coordinate.
	coords := map[string]int{}
	for _, ev := range published {
		coords[tagValue(ev.Tags, "d")]++
	}
	if len(coords) != 1 {
		t.Fatalf("coordinates = %v", coords)
	}
	stored := 0
	for range host.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Tags: gonostr.TagMap{"t": {kinds.CPStateTopicSoulFactorySagaRun}}}) {
		stored++
	}
	if stored != 1 {
		t.Fatalf("local store holds %d saga records for one run", stored)
	}
}

func TestEventStoreRecordCarriesNoSecrets(t *testing.T) {
	ctx := context.Background()
	host := newSagaHost(t)
	const bunker = "bunker://deadbeef?relay=wss://signer.example&secret=hunter2"
	engine, _ := fixtureEngineWith(t, host.eventStore(t, ""), func(ds map[Stage]*memoryDriver) {
		ds[StageSignerEnrolled].applyErr = &SafeError{Code: "policy_denied", Message: "nsec1qqqq leaked " + bunker, Retryable: true, Cause: errors.New("token=abc " + bunker)}
		ds[StageSignerEnrolled].applyLeavesAbsent = true
	})
	if _, err := engine.Start(ctx, "request-secret", "run-secret", "agent-secret", "sha256:spec"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Reconcile(ctx, "request-secret", false); err == nil {
		t.Fatal("expected failure")
	}
	for _, ev := range host.publisher.published() {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(raw))
		for _, marker := range []string{"nsec1", "bunker://", "secret=", "token=", "hunter2", "leaked"} {
			if strings.Contains(lower, marker) {
				t.Fatalf("record carries %q: %s", marker, ev.Content)
			}
		}
		for _, tag := range ev.Tags {
			if tag[0] == "d" && !strings.HasPrefix(tag[1], "soul-factory:saga-run:") {
				t.Fatalf("d tag = %q", tag[1])
			}
		}
	}
	loaded, err := engine.store.Load(ctx, "request-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range loaded.Resources {
		if !strings.HasPrefix(resource.ExternalID, "ref:sha256:") {
			t.Fatalf("resource lineage is not a one-way reference: %s", resource.ExternalID)
		}
	}
}

func TestEventStoreDeleteTombstonesAndListSkipsIt(t *testing.T) {
	ctx := context.Background()
	host := newSagaHost(t)
	cache := filepath.Join(t.TempDir(), "sagas")
	store := host.eventStore(t, cache)
	for _, id := range []string{"request-a", "request-b"} {
		run, err := NewRun(id, "run-"+id, "agent", "sha256:spec", host.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := store.Create(ctx, run); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate create err = %v", err)
		}
	}
	if err := store.Delete(ctx, "request-a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "request-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted load err = %v", err)
	}
	// A restarted process and a fresh host both see one live run.
	for _, s := range []*EventStore{host.eventStore(t, cache), host.eventStore(t, "")} {
		runs, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].RequestID != "request-b" {
			t.Fatalf("list = %+v", runs)
		}
	}
	// A file left behind at the tombstoned version cannot resurrect the run.
	ghost, _ := NewRun("request-a", "run-request-a", "agent", "sha256:spec", host.clock.Now())
	if err := writeAtomic(store.cachePath("request-a"), ghost); err != nil {
		t.Fatal(err)
	}
	if _, err := host.eventStore(t, cache).Load(ctx, "request-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost file resurrected the run: %v", err)
	}
	// The coordinate can be used again by a new run.
	again, _ := NewRun("request-a", "run-request-a-2", "agent", "sha256:spec", host.clock.Now())
	if err := store.Create(ctx, again); err != nil {
		t.Fatal(err)
	}
	if loaded, err := host.eventStore(t, "").Load(ctx, "request-a"); err != nil || loaded.RunID != "run-request-a-2" {
		t.Fatalf("recreated load = %v, %v", loaded, err)
	}
}

func TestEventStoreUpgradedHostResumesFromFileAndPublishesRecord(t *testing.T) {
	ctx := context.Background()
	host := newSagaHost(t)
	dir := filepath.Join(t.TempDir(), "sagas")
	files, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewRun("request-upgrade", "run-upgrade", "agent", "sha256:spec", host.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	store := host.eventStore(t, dir)
	loaded, err := store.Load(ctx, "request-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	next := loaded.clone()
	next.Stage = StageIdentityReserved
	next.Transitions = append(next.Transitions, Transition{From: loaded.Stage, To: next.Stage, At: host.clock.Now()})
	next.Version = loaded.Version + 1
	if err := store.Save(ctx, next, loaded.Version); err != nil {
		t.Fatal(err)
	}
	if got := host.publisher.published(); len(got) != 1 || tagValue(got[0].Tags, kinds.CASControlStateTagLegacyKind) != kinds.CPStateFamilySoulFactorySagaRun.TagValue() {
		t.Fatalf("published = %d", len(got))
	}
	if loaded, err = host.eventStore(t, "").Load(ctx, "request-upgrade"); err != nil || loaded.Version != 2 {
		t.Fatalf("record-only load after upgrade = %v, %v", loaded, err)
	}
}
