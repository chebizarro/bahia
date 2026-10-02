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

// immediateReadiness satisfies ReadinessWaiter with an already-closed channel.
type immediateReadiness struct{ ch chan struct{} }

func newImmediateReadiness() *immediateReadiness {
	ch := make(chan struct{})
	close(ch)
	return &immediateReadiness{ch: ch}
}
func (r *immediateReadiness) Ready() <-chan struct{} { return r.ch }

// neverReadiness satisfies ReadinessWaiter with a channel that never closes.
type neverReadiness struct{ ch chan struct{} }

func newNeverReadiness() *neverReadiness {
	return &neverReadiness{ch: make(chan struct{})}
}
func (r *neverReadiness) Ready() <-chan struct{} { return r.ch }

// gatedReadiness becomes ready when MarkReady is called, closing the channel.
type gatedReadiness struct{ ch chan struct{} }

func newGatedReadiness() *gatedReadiness {
	return &gatedReadiness{ch: make(chan struct{})}
}
func (g *gatedReadiness) Ready() <-chan struct{} { return g.ch }
func (g *gatedReadiness) MarkReady()             { close(g.ch) }

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

// --- acceptance tests -------------------------------------------------------

// TestDaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState exercises
// the full warm-start flow: Phase 1 publishes services via RepublishSnapshot,
// Phase 2 "restarts" with the same history and intent domains, and should
// publish zero service events because the cache matches history.
//
// Note: after F3, environments are no longer published via RepublishSnapshot.
func TestDaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Phase 1: initial run publishes canonical service state to the relay.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger,
		WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	initialSvc := countByDomain(sink1.events, "service")
	if initialSvc == 0 {
		t.Fatalf("initial run published no service events: svc=%d", initialSvc)
	}

	// Phase 2: "restart" — new projector, same history (repo), fresh sink.
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for the migrated service domain.
	svcCount := countByDomain(sink2.events, "service")
	if svcCount != 0 {
		t.Errorf("expected 0 service publishes after restart, got %d", svcCount)
	}
}

func TestWarmStartColdStorePublishesZero(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Cold store: empty repo, no history of prior publishes.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for migrated domains despite Postgres having data.
	svcCount := countByDomain(sink.events, "service")
	if svcCount != 0 {
		t.Errorf("cold store: expected 0 service publishes, got %d", svcCount)
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
	staleSvcID := svcIDs[2]
	staleDTag := staleSvcID.String()
	markRecordFailed(t, repo, "domain", "service", staleDTag)

	// Phase 2: "restart" — the stale record should be re-published.
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newImmediateReadiness()),
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

// TestWarmStartUnmigratedServiceStillGetsLegacySnapshot verifies that when
// only environment (already removed by F3) is in intent_domains but service
// is NOT, services are still published via the legacy RepublishSnapshot path.
// This matters while F2 hasn't landed yet — services still rely on the
// Postgres→relay re-projection.
func TestWarmStartUnmigratedServiceStillGetsLegacySnapshot(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Service is NOT in intent_domains → RepublishSnapshot still publishes it.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"environment"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Service domain is NOT migrated → still published via RepublishSnapshot.
	svcCount := countByDomain(sink.events, "service")
	if svcCount == 0 {
		t.Errorf("unmigrated domain: expected service publishes from legacy snapshot, got 0")
	}
}

func TestWarmStartPeriodicRepairSkipsMigratedDomains(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	// Use a very short repair interval to trigger the periodic ticker.
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(50*time.Millisecond))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	// Wait for at least one periodic tick.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes for migrated service domain from both startup
	// and periodic repair.
	svcCount := countByDomain(sink.events, "service")
	if svcCount != 0 {
		t.Errorf("periodic repair: expected 0 service publishes, got %d", svcCount)
	}
}

// --- readiness ordering tests (no sleeps, no polling) -----------------------

// TestWarmStartReadyBeforeCtxDoneRunsWarmStart verifies that when readiness
// fires before ctx is cancelled, warm-start executes (the happy path).
func TestWarmStartReadyBeforeCtxDoneRunsWarmStart(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Phase 1: publish so history has records.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger, WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Mark one record as stale.
	markRecordFailed(t, repo, "domain", "service", svcIDs[1].String())

	// Phase 2: gated readiness fires before ctx cancellation.
	gate := newGatedReadiness()
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(gate),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()

	// Fire readiness — warm-start proceeds.
	gate.MarkReady()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// The stale record should have been re-published.
	svcCount := countByDomain(sink2.events, "service")
	if svcCount != 1 {
		t.Errorf("ready-before-ctx: expected 1 stale re-publish, got %d", svcCount)
	}
}

// TestWarmStartCtxDoneBeforeReadySkipsWarmStart verifies that when ctx is
// cancelled before readiness, warm-start is skipped entirely — no events
// published against an incomplete store.
func TestWarmStartCtxDoneBeforeReadySkipsWarmStart(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	source := warmStartTestSource(svcIDs, nil)

	// Phase 1: publish so history has records.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger, WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Mark one record as stale.
	markRecordFailed(t, repo, "domain", "service", svcIDs[1].String())

	// Phase 2: readiness never fires, ctx cancelled immediately.
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newNeverReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	// Cancel immediately — readiness will never fire.
	cancel()
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()
	<-done

	// No warm-start should have run. The projector also shouldn't have
	// run RepublishSnapshot since ctx is already done.
	svcCount := countByDomain(sink2.events, "service")
	if svcCount != 0 {
		t.Errorf("ctx-before-ready: expected 0 publishes, got %d", svcCount)
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
