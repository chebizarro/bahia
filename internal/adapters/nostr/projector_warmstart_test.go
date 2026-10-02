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
	"github.com/openagentsinc/bahia/internal/repository"
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

// seedControlStateRecords signs and writes kind-30900 events directly into the
// history repo for the given domain family and entity IDs. This mirrors the
// production path: intent handlers publish via PublishBeforeCommit which signs
// and records events in the local store.
func seedControlStateRecords(t *testing.T, repo *repositorytest.InMemoryNostrEventRepository, legacyKind int, ids []uuid.UUID) {
	t.Helper()
	for _, id := range ids {
		wireKind, tags := controlStateEnvelope(legacyKind, id.String(), false)
		content := `{"id":"` + id.String() + `","name":"entity-` + id.String()[:8] + `"}`
		ev := gonostr.Event{
			Kind:      gonostr.Kind(wireKind),
			CreatedAt: gonostr.Now(),
			Tags:      tags,
			Content:   content,
		}
		if err := signEventWithPrivateKeyHex(&ev, projectorTestPrivateKey); err != nil {
			t.Fatalf("sign event: %v", err)
		}
		tagsJSON, _ := json.Marshal(ev.Tags)
		if _, err := repo.Record(context.Background(), &repository.NostrEventRecord{
			ID: eventIDHex(&ev), Kind: eventKindInt(&ev), PubKey: eventPubKeyHex(&ev),
			Content: ev.Content, Tags: tagsJSON, Sig: eventSignatureHex(&ev),
			CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(),
			EntityType: "seed", EntityID: &id,
			PublishState: repository.NostrPublishStatePublished,
		}); err != nil {
			t.Fatalf("record seed event: %v", err)
		}
	}
}

// --- acceptance tests -------------------------------------------------------

// TestDaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState seeds the
// history repo with kind-30900 service records (the production publish path),
// then "restarts" with warm-start enabled and asserts zero publishes.
func TestDaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	repo := repositorytest.NewInMemoryNostrEventRepository()

	// Seed: write signed kind-30900 service records into history.
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)

	// "Restart" with warm-start enabled.
	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes — relay holds current state.
	if n := countByDomain(sink.events, "service"); n != 0 {
		t.Errorf("expected 0 service publishes after restart, got %d", n)
	}
}

func TestWarmStartColdStorePublishesZero(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	// Cold store: empty repo, no history.
	repo := repositorytest.NewInMemoryNostrEventRepository()
	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if n := countByDomain(sink.events, "service"); n != 0 {
		t.Errorf("cold store: expected 0 service publishes, got %d", n)
	}
}

func TestWarmStartStaleRecordPublishesExactlyOne(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	repo := repositorytest.NewInMemoryNostrEventRepository()

	// Seed three service records.
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)

	// Mark one as failed (simulating abandoned outbox publish).
	staleDTag := svcIDs[2].String()
	markRecordFailed(t, repo, "domain", "service", staleDTag)

	// "Restart" — the stale record should be re-published.
	source := newFakeProjectionSource()
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

	// Assert: exactly 1 publish for the stale record.
	if n := countByDomain(sink.events, "service"); n != 1 {
		t.Errorf("expected exactly 1 service publish (stale record), got %d", n)
	}
	// Verify it's the stale record.
	if countByDomain(sink.events, "service") == 1 {
		for _, ev := range sink.events {
			if hasDomainTag(ev, "service") {
				if d := tagValue(ev.Tags, "d"); d != staleDTag {
					t.Errorf("republished d-tag = %q, want %q", d, staleDTag)
				}
			}
		}
	}
}

// TestWarmStartUnmigratedLLMRouteStillGetsLegacySnapshot verifies that a domain
// family with a legacy RepublishSnapshot leg (llm) is still published by
// the legacy path when it is NOT listed in intent_domains. Services,
// environments, and policies are migrated; LLM routes are not yet.
func TestWarmStartUnmigratedLLMRouteStillGetsLegacySnapshot(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	routeID := uuid.New()
	source := newFakeProjectionSource()
	source.llmRoutes[routeID] = domain.LLMRoute{
		ID:   routeID,
		Name: "test-llm-route",
	}

	repo := repositorytest.NewInMemoryNostrEventRepository()
	sink := &captureProjectionPublisher{}
	// Service, environment, and policy are migrated; LLM is NOT.
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithLLMProjectionSource(source),
		WithIntentDomains([]string{"service", "environment", "policy"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// LLM is NOT migrated → still published via RepublishSnapshot.
	llmCount := countByDomain(sink.events, "llm")
	if llmCount == 0 {
		t.Errorf("unmigrated domain: expected LLM route publishes from legacy snapshot, got 0")
	}
}

func TestWarmStartPeriodicRepairSkipsMigratedDomains(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New()}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)

	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(newImmediateReadiness()),
		WithProjectorRepairInterval(50*time.Millisecond))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if n := countByDomain(sink.events, "service"); n != 0 {
		t.Errorf("periodic repair: expected 0 service publishes, got %d", n)
	}
}

// --- readiness ordering tests -----------------------------------------------

func TestWarmStartReadyBeforeCtxDoneRunsWarmStart(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)

	// Mark one record stale.
	markRecordFailed(t, repo, "domain", "service", svcIDs[1].String())

	gate := newGatedReadiness()
	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(gate),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()

	gate.MarkReady()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if n := countByDomain(sink.events, "service"); n != 1 {
		t.Errorf("ready-before-ctx: expected 1 stale re-publish, got %d", n)
	}
}

func TestWarmStartCtxDoneBeforeReadySkipsWarmStart(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New()}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)
	markRecordFailed(t, repo, "domain", "service", svcIDs[1].String())

	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service"}),
		WithReadinessTracker(newNeverReadiness()),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	cancel() // Cancel immediately.
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	<-done

	if n := countByDomain(sink.events, "service"); n != 0 {
		t.Errorf("ctx-before-ready: expected 0 publishes, got %d", n)
	}
}

// --- helpers for test data --------------------------------------------------

func markRecordFailed(t *testing.T, repo *repositorytest.InMemoryNostrEventRepository, tagKey, tagValue string, dTag string) {
	t.Helper()
	records, err := repo.FindByTag(t.Context(), tagKey, tagValue, nil, 1000)
	if err != nil {
		t.Fatalf("find records by %s=%s: %v", tagKey, tagValue, err)
	}
	for _, record := range records {
		var tags gonostr.Tags
		_ = json.Unmarshal(record.Tags, &tags)
		if eventTagValue(tags, "d") == dTag {
			if err := repo.RecordPublishFailure(t.Context(), record.ID, "test-abandon"); err != nil {
				t.Fatalf("record publish failure: %v", err)
			}
			if err := repo.AbandonPublish(t.Context(), record.ID, "test-abandon"); err != nil {
				t.Fatalf("abandon publish: %v", err)
			}
			return
		}
	}
	t.Fatalf("no record found with %s=%s d=%s", tagKey, tagValue, dTag)
}

// eventTagValue extracts a tag value from a gonostr.Tags slice.
// Named differently from tagValue to avoid shadowing the production function.
func eventTagValue(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func hasDomainTag(ev gonostr.Event, domain string) bool {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "domain" && tag[1] == domain {
			return true
		}
	}
	return false
}

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
