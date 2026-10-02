package nostr

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

// --- helpers ----------------------------------------------------------------

// immediateReadiness satisfies ReadinessWaiter and reports ready immediately.
type immediateReadiness struct{}

func (immediateReadiness) IsReady() bool { return true }

// neverReadiness satisfies ReadinessWaiter and never reports ready.
type neverReadiness struct{}

func (neverReadiness) IsReady() bool { return false }

// delayedReadiness becomes ready after MarkReady is called.
type delayedReadiness struct{ ready chan struct{} }

func newDelayedReadiness() *delayedReadiness { return &delayedReadiness{ready: make(chan struct{})} }
func (d *delayedReadiness) IsReady() bool {
	select {
	case <-d.ready:
		return true
	default:
		return false
	}
}
func (d *delayedReadiness) MarkReady() { close(d.ready) }

// countByDomain counts events whose tags include domain=<one of domains>.
func countByDomain(events []gonostr.Event, domains ...string) int {
	domainSet := make(map[string]bool, len(domains))
	for _, d := range domains {
		domainSet[d] = true
	}
	count := 0
	for _, ev := range events {
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "domain" && domainSet[tag[1]] {
				count++
				break
			}
		}
	}
	return count
}

// warmStartTestCfg returns a NostrConfig suitable for warmstart tests.
func warmStartTestCfg() config.NostrConfig {
	return config.NostrConfig{
		PrivateKey:     projectorTestPrivateKey,
		PublishEnabled: true,
	}
}

// warmStartTestSource builds a fakeProjectionSource with deterministic service
// and environment entities.
func warmStartTestSource(svcIDs []uuid.UUID, envIDs []uuid.UUID) *fakeProjectionSource {
	src := newFakeProjectionSource()
	for _, id := range svcIDs {
		src.services[id] = domain.Service{
			ID: id, Name: "svc-" + id.String()[:8],
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	for _, id := range envIDs {
		src.envs[id] = domain.Environment{
			ID: id, Name: "env-" + id.String()[:8],
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	return src
}

// --- acceptance test: DaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState ---

func TestDaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	envIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, envIDs)

	// Phase 1: initial run publishes canonical state to the relay.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger,
		WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	initialSvc := countByDomain(sink1.events, "service")
	initialEnv := countByDomain(sink1.events, "environment")
	if initialSvc == 0 || initialEnv == 0 {
		t.Fatalf("initial run published no service/environment events: svc=%d env=%d",
			initialSvc, initialEnv)
	}

	// Phase 2: "restart" — new projector, same history (repo), fresh sink.
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(immediateReadiness{}),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()
	// Give it a moment to complete startup.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for migrated domains.
	svcCount := countByDomain(sink2.events, "service")
	envCount := countByDomain(sink2.events, "environment")
	if svcCount != 0 {
		t.Errorf("expected 0 service publishes after restart, got %d", svcCount)
	}
	if envCount != 0 {
		t.Errorf("expected 0 environment publishes after restart, got %d", envCount)
	}
}

func TestWarmStartColdStorePublishesZero(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	envIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, envIDs)

	// Cold store: empty repo, no history of prior publishes.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(immediateReadiness{}),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for migrated domains despite Postgres having data.
	svcCount := countByDomain(sink.events, "service")
	envCount := countByDomain(sink.events, "environment")
	if svcCount != 0 {
		t.Errorf("cold store: expected 0 service publishes, got %d", svcCount)
	}
	if envCount != 0 {
		t.Errorf("cold store: expected 0 environment publishes, got %d", envCount)
	}
}

func TestWarmStartStaleRecordPublishesExactlyOne(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Phase 1: publish three services so history has records.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger,
		WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	initialSvc := countByDomain(sink1.events, "service")
	if initialSvc != 3 {
		t.Fatalf("expected 3 initial service publishes, got %d", initialSvc)
	}

	// Mark one record as failed (simulating an abandoned outbox publish).
	// Find the events for service domain and mark the last one as failed.
	staleSvcID := svcIDs[2]
	staleDTag := staleSvcID.String()
	markRecordFailed(t, repo, "domain", "service", staleDTag)

	// Phase 2: "restart" — the stale record should be re-published.
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(immediateReadiness{}),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: exactly 1 publish for the stale record.
	svcCount := countByDomain(sink2.events, "service")
	if svcCount != 1 {
		t.Errorf("expected exactly 1 service publish (stale record), got %d", svcCount)
	}

	// Verify it's the stale record that was republished.
	if svcCount == 1 {
		found := false
		for _, ev := range sink2.events {
			if hasDomainTag(ev, "service") {
				d := tagValue(ev.Tags, "d")
				if d == staleDTag {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the republished service event should be for the stale entity %s", staleDTag)
		}
	}
}

func TestWarmStartUnmigratedDomainsStillGetLegacySnapshot(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	envIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, envIDs)

	// Only "service" is migrated; "environment" is not.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(immediateReadiness{}),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Service domain is migrated → 0 publishes from RepublishSnapshot.
	svcCount := countByDomain(sink.events, "service")
	if svcCount != 0 {
		t.Errorf("migrated domain: expected 0 service publishes, got %d", svcCount)
	}

	// Environment domain is NOT migrated → still published via RepublishSnapshot.
	envCount := countByDomain(sink.events, "environment")
	if envCount == 0 {
		t.Errorf("unmigrated domain: expected environment publishes from legacy snapshot, got 0")
	}
}

func TestWarmStartPeriodicRepairSkipsMigratedDomains(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	envIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, envIDs)

	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	// Use a very short repair interval to trigger the periodic ticker.
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(immediateReadiness{}),
		WithProjectorRepairInterval(50*time.Millisecond))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	// Wait for at least one periodic tick.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for migrated domains from both startup and periodic repair.
	svcCount := countByDomain(sink.events, "service")
	envCount := countByDomain(sink.events, "environment")
	if svcCount != 0 {
		t.Errorf("periodic repair: expected 0 service publishes, got %d", svcCount)
	}
	if envCount != 0 {
		t.Errorf("periodic repair: expected 0 environment publishes, got %d", envCount)
	}
}

// --- helpers for test data --------------------------------------------------

// markRecordFailed finds a record in the repo by domain and d-tag and marks it
// as failed (simulating an abandoned outbox publish). It uses the two-step
// transition: RecordPublishFailure (published→pending) then AbandonPublish
// (pending→failed).
func markRecordFailed(t *testing.T, repo *repositorytest.InMemoryNostrEventRepository, domainTag, domainValue, dTag string) {
	t.Helper()
	records, err := repo.FindByTag(t.Context(), domainTag, domainValue, nil, 1000)
	if err != nil {
		t.Fatalf("find records by domain: %v", err)
	}
	for _, record := range records {
		var tags gonostr.Tags
		_ = json.Unmarshal(record.Tags, &tags)
		if tagValue(tags, "d") == dTag {
			// RecordPublishFailure moves state → pending; AbandonPublish
			// then moves pending → failed (the only transition AbandonPublish
			// accepts).
			if err := repo.RecordPublishFailure(t.Context(), record.ID, "test-abandon"); err != nil {
				t.Fatalf("record publish failure: %v", err)
			}
			if err := repo.AbandonPublish(t.Context(), record.ID, "test-abandon"); err != nil {
				t.Fatalf("abandon publish: %v", err)
			}
			return
		}
	}
	t.Fatalf("no record found with domain=%s d=%s", domainValue, dTag)
}

func hasDomainTag(ev gonostr.Event, domain string) bool {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "domain" && tag[1] == domain {
			return true
		}
	}
	return false
}

// Ensure constants compile. Prevent drift between cpStateFamilies domain names
// and the config intent_domains strings used in guards.
func TestMigratedDomainNamesMatchCPStateFamilies(t *testing.T) {
	want := map[string]int{
		"service":     KindServiceRegistry,
		"environment": KindEnvironmentRegistry,
	}
	for domain, legacyKind := range want {
		family, ok := cpStateFamilies[legacyKind]
		if !ok {
			t.Errorf("cpStateFamilies missing legacy kind %d for domain %q", legacyKind, domain)
			continue
		}
		if family.domain != domain {
			t.Errorf("cpStateFamilies[%d].domain = %q, want %q", legacyKind, family.domain, domain)
		}
	}
}

// Verify that controlStateEnvelope produces tags the warm-start FindByTag can find.
func TestControlStateEnvelopeProducesDomainTag(t *testing.T) {
	for _, tc := range []struct {
		legacyKind int
		domain     string
	}{
		{KindServiceRegistry, "service"},
		{KindEnvironmentRegistry, "environment"},
	} {
		wireKind, tags := controlStateEnvelope(tc.legacyKind, "test-id", false)
		if wireKind != KindCASControlState {
			t.Errorf("controlStateEnvelope(%d) wire kind = %d, want %d", tc.legacyKind, wireKind, KindCASControlState)
		}
		if v := tagValue(tags, "domain"); v != tc.domain {
			t.Errorf("controlStateEnvelope(%d) domain tag = %q, want %q", tc.legacyKind, v, tc.domain)
		}
		if v := tagValue(tags, "legacy_kind"); v != strconv.Itoa(tc.legacyKind) {
			t.Errorf("controlStateEnvelope(%d) legacy_kind tag = %q, want %q", tc.legacyKind, v, strconv.Itoa(tc.legacyKind))
		}
	}
}
