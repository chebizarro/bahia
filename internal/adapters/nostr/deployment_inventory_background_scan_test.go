package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Default background scan budget: 10m heartbeat + 2*(5m interval + 30s
// jitter + 1m timeout).
const defaultBackgroundTargetScanStaleAfter = 23 * time.Minute

func backgroundScanProjectorConfig(backgroundScans bool, endpoints ...string) *config.Config {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Adoption.Enabled = backgroundScans
	cfg.Runtime.Endpoints = map[string]config.RuntimeEndpointConfig{}
	for _, ref := range endpoints {
		cfg.Runtime.Endpoints[ref] = config.RuntimeEndpointConfig{DockerHost: "tcp://" + ref + ":2376"}
	}
	return cfg
}

// newBackgroundScanProjector models a backend with background scanning of the
// given production targets (each on endpoint "<target>-docker").
func newBackgroundScanProjector(repo repository.NostrEventRepository, sink *captureProjectionPublisher, targets ...string) *Projector {
	var endpoints []string
	var scope []service.RuntimeTargetScanScope
	for _, target := range targets {
		endpoints = append(endpoints, target+"-docker")
		scope = append(scope, service.RuntimeTargetScanScope{Environment: "production", Target: target})
	}
	cfg := backgroundScanProjectorConfig(true, endpoints...)
	return NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, repo, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true), WithBackgroundTargetScanScope(scope))
}

type targetCounts struct {
	name               string
	available          bool
	managed, unmanaged int
}

func targetScanCompleted(origin string, at time.Time, targets ...targetCounts) service.AdoptionScanCompleted {
	scan := service.AdoptionScanCompleted{Origin: origin, CompletedAt: at}
	for _, target := range targets {
		summary := service.AdoptionScanTargetSummary{Target: target.name, Environment: "production", EndpointRef: target.name + "-docker", Available: target.available}
		if target.available {
			summary.Managed, summary.Unmanaged, summary.Total = target.managed, target.unmanaged, target.managed+target.unmanaged
		}
		scan.Targets = append(scan.Targets, summary)
	}
	return scan
}

func deliverScan(t *testing.T, projector *Projector, scan service.AdoptionScanCompleted) {
	t.Helper()
	if err := projector.publishRuntimeTargetScans(context.Background(), scan); err != nil {
		t.Fatalf("publish target scans: %v", err)
	}
}

func targetScanEventsFor(sink *captureProjectionPublisher, target string) []gonostr.Event {
	var out []gonostr.Event
	for _, ev := range deploymentInventoryEvents(sink, DeploymentInventoryTargetScanEntity) {
		if eventDTag(ev) == deploymentInventoryTargetScanDTag("production", target) {
			out = append(out, ev)
		}
	}
	return out
}

func decodeTargetScan(t *testing.T, ev gonostr.Event) deploymentInventoryTargetScanPayload {
	t.Helper()
	if !ev.VerifySignature() {
		t.Fatalf("target scan %s has an invalid signature", eventDTag(ev))
	}
	var payload deploymentInventoryTargetScanPayload
	if err := json.Unmarshal([]byte(ev.Content), &payload); err != nil {
		t.Fatalf("decode target scan: %v", err)
	}
	return payload
}

func TestBackgroundTargetScanRepublishesOnlyMaterialChangesAndFreshnessHeartbeats(t *testing.T) {
	sink := &captureProjectionPublisher{}
	projector := newBackgroundScanProjector(nil, sink)
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	bg := service.AdoptionScanOriginBackground
	edge := func(managed, unmanaged int) targetCounts {
		return targetCounts{name: "edge-01", available: true, managed: managed, unmanaged: unmanaged}
	}

	deliverScan(t, projector, targetScanCompleted(bg, t0, edge(1, 2)))
	published := targetScanEventsFor(sink, "edge-01")
	if len(published) != 1 {
		t.Fatalf("initial publishes = %d, want 1", len(published))
	}
	first := decodeTargetScan(t, published[0])
	if first.ScannedAt != "2026-09-28T12:00:00Z" || first.Freshness.StaleAfterSeconds != int64(defaultBackgroundTargetScanStaleAfter/time.Second) || !hasTag(published[0].Tags, "origin", bg) {
		t.Fatalf("first aggregate = %+v tags=%v", first, published[0].Tags)
	}

	// Unchanged counts within the heartbeat window are not republished.
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(5*time.Minute), edge(1, 2)))
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(10*time.Minute-time.Second), edge(1, 2)))
	if got := len(targetScanEventsFor(sink, "edge-01")); got != 1 {
		t.Fatalf("unchanged counts republished: %d events", got)
	}
	var deduped int64
	for _, metrics := range projector.ProjectionMetrics() {
		deduped += metrics.Deduped
	}
	if deduped < 2 {
		t.Fatalf("deduped metric = %d, want the suppressed scans counted", deduped)
	}

	// Heartbeat: once the published scan time is a refresh interval old, the
	// unchanged aggregate is republished with the new scan time.
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(10*time.Minute), edge(1, 2)))
	published = targetScanEventsFor(sink, "edge-01")
	if len(published) != 2 || decodeTargetScan(t, published[1]).ScannedAt != "2026-09-28T12:10:00Z" {
		t.Fatalf("heartbeat not published: %d events", len(published))
	}

	// A count change publishes promptly.
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(12*time.Minute), edge(1, 3)))
	published = targetScanEventsFor(sink, "edge-01")
	if len(published) != 3 || decodeTargetScan(t, published[2]).Counts.Unmanaged != 3 {
		t.Fatalf("count change not published: %d events", len(published))
	}

	// A scan that completed earlier than the published one never overwrites it.
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(11*time.Minute), edge(0, 0)))
	if got := len(targetScanEventsFor(sink, "edge-01")); got != 3 {
		t.Fatalf("out-of-order older scan published: %d events", got)
	}

	// Losing the target is material: the aggregate flips to unavailable.
	deliverScan(t, projector, targetScanCompleted(bg, t0.Add(13*time.Minute), targetCounts{name: "edge-01"}))
	published = targetScanEventsFor(sink, "edge-01")
	if last := decodeTargetScan(t, published[len(published)-1]); len(published) != 4 || last.ScanState != targetScanUnavailable || last.Counts != nil {
		t.Fatalf("unavailable not published: %d events, last=%+v", len(published), last)
	}
}

func TestBackgroundTargetScanSuppressesUnchangedCountsAcrossRestart(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	edge := targetCounts{name: "edge-01", available: true, managed: 1, unmanaged: 2}
	deliverScan(t, newBackgroundScanProjector(repo, &captureProjectionPublisher{}), targetScanCompleted(service.AdoptionScanOriginBackground, t0, edge))

	sink := &captureProjectionPublisher{}
	restarted := newBackgroundScanProjector(repo, sink)
	deliverScan(t, restarted, targetScanCompleted(service.AdoptionScanOriginBackground, t0.Add(5*time.Minute), edge))
	if got := len(targetScanEventsFor(sink, "edge-01")); got != 0 {
		t.Fatalf("restart republished unchanged counts: %d events", got)
	}
}

// ageRetainedEvents shifts every retained record back in time. Events signed
// within one test run share a wall-clock second, and NIP-01 breaks such ties
// by lowest id; ageing earlier phases models the minutes that separate them in
// production so hydration sees the real newest event per coordinate.
func ageRetainedEvents(repo *memoryNostrEventRepo, by time.Duration) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for id, record := range repo.records {
		record.CreatedAt = record.CreatedAt.Add(-by)
		repo.records[id] = record
	}
}

func TestRuntimeTargetScanRetirementTombstonesRemovedTargetsAcrossRestart(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	ctx := context.Background()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	first := newBackgroundScanProjector(repo, &captureProjectionPublisher{}, "edge-01", "edge-02")
	deliverScan(t, first, targetScanCompleted(service.AdoptionScanOriginBackground, t0,
		targetCounts{name: "edge-01", available: true, unmanaged: 1},
		targetCounts{name: "edge-02", available: true, unmanaged: 4}))
	// An operator scans an unconfigured target of a configured endpoint.
	adhoc := targetScanCompleted(service.AdoptionScanOriginOperator, t0, targetCounts{name: "adhoc", available: true, unmanaged: 7})
	adhoc.Targets[0].EndpointRef = "edge-01-docker"
	deliverScan(t, first, adhoc)

	// edge-02 is removed from configuration while Bahia is down; the
	// restarted projector's repair pass retires it.
	ageRetainedEvents(repo, time.Hour)
	sink := &captureProjectionPublisher{}
	restarted := newBackgroundScanProjector(repo, sink, "edge-01")
	retired, err := restarted.retireRuntimeTargetScans(ctx, t0.Add(time.Minute))
	if err != nil || retired != 1 {
		t.Fatalf("retired=%d err=%v, want the removed background target only", retired, err)
	}
	tombstones := targetScanEventsFor(sink, "edge-02")
	if len(tombstones) != 1 || !hasTag(tombstones[0].Tags, "deleted", "true") || !tombstones[0].VerifySignature() {
		t.Fatalf("edge-02 tombstone = %+v", tombstones)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(tombstones[0].Content), &content); err != nil || content["deleted"] != true || content["target"] != "edge-02" || content["counts"] != nil {
		t.Fatalf("tombstone content = %s", tombstones[0].Content)
	}
	if len(targetScanEventsFor(sink, "edge-01")) != 0 || len(targetScanEventsFor(sink, "adhoc")) != 0 {
		t.Fatal("configured target or fresh operator scan was retired")
	}

	// Retirement is idempotent.
	if retired, err := restarted.retireRuntimeTargetScans(ctx, t0.Add(2*time.Minute)); err != nil || retired != 0 {
		t.Fatalf("second retirement retired=%d err=%v", retired, err)
	}

	// The ad-hoc operator aggregate is retired once it is stale, not left
	// behind forever.
	if retired, err := restarted.retireRuntimeTargetScans(ctx, t0.Add(defaultBackgroundTargetScanStaleAfter+time.Second)); err != nil || retired != 1 {
		t.Fatalf("stale operator retirement retired=%d err=%v", retired, err)
	}
	if adhoc := targetScanEventsFor(sink, "adhoc"); len(adhoc) != 1 || !hasTag(adhoc[0].Tags, "deleted", "true") {
		t.Fatalf("adhoc tombstone = %+v", adhoc)
	}

	// A retired target that comes back publishes again.
	deliverScan(t, restarted, targetScanCompleted(service.AdoptionScanOriginBackground, t0.Add(30*time.Minute), targetCounts{name: "edge-02", available: true, unmanaged: 4}))
	if events := targetScanEventsFor(sink, "edge-02"); len(events) != 2 || hasTag(events[1].Tags, "deleted", "true") {
		t.Fatalf("re-added target not republished: %d events", len(events))
	}

	// After another restart the tombstoned coordinate is not tombstoned again.
	ageRetainedEvents(repo, time.Hour)
	again := &captureProjectionPublisher{}
	third := newBackgroundScanProjector(repo, again, "edge-01", "edge-02")
	if retired, err := third.retireRuntimeTargetScans(ctx, t0.Add(31*time.Minute)); err != nil || retired != 0 {
		t.Fatalf("third retirement retired=%d err=%v", retired, err)
	}
}

// TestRuntimeTargetScanRetirementRunsWithoutBackgroundScanning covers the
// modes where no runner exists: coordinates nothing refreshes any more
// (background-origin) and targets whose endpoint left configuration are
// retired, while operator aggregates of configured endpoints keep ageing into
// stale as before.
func TestRuntimeTargetScanRetirementRunsWithoutBackgroundScanning(t *testing.T) {
	repo := newMemoryNostrEventRepo()
	ctx := context.Background()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	first := newBackgroundScanProjector(repo, &captureProjectionPublisher{}, "edge-01")
	deliverScan(t, first, targetScanCompleted(service.AdoptionScanOriginBackground, t0, targetCounts{name: "edge-01", available: true, unmanaged: 1}))
	deliverScan(t, first, targetScanCompleted(service.AdoptionScanOriginOperator, t0,
		targetCounts{name: "kept", available: true, unmanaged: 2},
		targetCounts{name: "removed", available: true, unmanaged: 3}))

	// Restart with background scans disabled and the "removed-docker"
	// endpoint deleted from runtime.endpoints.
	ageRetainedEvents(repo, time.Hour)
	cfg := backgroundScanProjectorConfig(false, "edge-01-docker", "kept-docker")
	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, repo, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("repair pass: %v", err)
	}
	if edge := targetScanEventsFor(sink, "edge-01"); len(edge) != 1 || !hasTag(edge[0].Tags, "deleted", "true") {
		t.Fatalf("unmaintained background coordinate not retired by the repair pass: %+v", edge)
	}
	if removed := targetScanEventsFor(sink, "removed"); len(removed) != 1 || !hasTag(removed[0].Tags, "deleted", "true") {
		t.Fatalf("target of a removed endpoint not retired: %+v", removed)
	}
	if kept := targetScanEventsFor(sink, "kept"); len(kept) != 0 {
		t.Fatalf("operator aggregate of a configured endpoint retired without background scanning: %+v", kept)
	}
	if retired, err := projector.retireRuntimeTargetScans(ctx, t0.Add(48*time.Hour)); err != nil || retired != 0 {
		t.Fatalf("stale operator aggregate retired without background scanning: retired=%d err=%v", retired, err)
	}
}

type failingFindByTagRepo struct {
	*memoryNostrEventRepo
	fail bool
}

func (r *failingFindByTagRepo) FindByTag(ctx context.Context, name, value string, wanted []int, limit int) ([]repository.NostrEventRecord, error) {
	if r.fail {
		return nil, errors.New("database unavailable")
	}
	return r.memoryNostrEventRepo.FindByTag(ctx, name, value, wanted, limit)
}

func TestTargetScanHydrationKeepsLiveStateOnFailureAndToleratesBadScanTimes(t *testing.T) {
	memory := newMemoryNostrEventRepo()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	edge := targetCounts{name: "edge-01", available: true, managed: 1, unmanaged: 2}
	deliverScan(t, newBackgroundScanProjector(memory, &captureProjectionPublisher{}, "edge-01"), targetScanCompleted(service.AdoptionScanOriginBackground, t0, edge))

	// While hydration fails, live gating state accumulates and is not wiped.
	repo := &failingFindByTagRepo{memoryNostrEventRepo: memory, fail: true}
	sink := &captureProjectionPublisher{}
	projector := newBackgroundScanProjector(repo, sink, "edge-01")
	deliverScan(t, projector, targetScanCompleted(service.AdoptionScanOriginBackground, t0.Add(20*time.Minute), edge))
	deliverScan(t, projector, targetScanCompleted(service.AdoptionScanOriginBackground, t0.Add(25*time.Minute), edge))
	if got := len(targetScanEventsFor(sink, "edge-01")); got != 1 {
		t.Fatalf("publishes during hydration failure = %d, want 1 (gated by live state)", got)
	}
	if _, err := projector.retireRuntimeTargetScans(context.Background(), t0.Add(26*time.Minute)); err == nil {
		t.Fatal("retirement must report a hydration failure")
	}
	repo.fail = false
	if _, err := projector.retireRuntimeTargetScans(context.Background(), t0.Add(27*time.Minute)); err != nil {
		t.Fatalf("retirement after recovery: %v", err)
	}
	deliverScan(t, projector, targetScanCompleted(service.AdoptionScanOriginBackground, t0.Add(28*time.Minute), edge))
	if got := len(targetScanEventsFor(sink, "edge-01")); got != 1 {
		t.Fatalf("recovered hydration replaced newer live state: %d publishes", got)
	}

	// A retained aggregate without a valid scanned_at falls back to its
	// created_at instead of the zero time (which would read as ancient).
	bad := newMemoryNostrEventRepo()
	badSink := &captureProjectionPublisher{}
	writer := newBackgroundScanProjector(bad, badSink, "edge-01")
	if err := writer.publishSigned(context.Background(), KindCASControlState, gonostr.Tags{
		{"d", deploymentInventoryTargetScanDTag("production", "adhoc")}, {"domain", DeploymentInventoryDomain}, {"schema", DeploymentInventorySchema},
		{"entity", DeploymentInventoryTargetScanEntity}, {"target", "adhoc"}, {"status", targetScanComplete}, {"deleted", "false"},
	}, `{"schema":"bahia.deployment-inventory.v1","entity":"runtime-target-scan","environment":"production","target":"adhoc","scan_state":"complete","scanned_at":"not-a-time","freshness":{"stale_after_seconds":720},"counts":{"total":1,"managed":0,"unmanaged":1}}`, "deployment_inventory.target_scan", nil); err != nil {
		t.Fatal(err)
	}
	reader := newBackgroundScanProjector(bad, &captureProjectionPublisher{}, "edge-01")
	if retired, err := reader.retireRuntimeTargetScans(context.Background(), time.Now().UTC()); err != nil || retired != 0 {
		t.Fatalf("fresh aggregate with malformed scanned_at retired=%d err=%v, want kept until stale", retired, err)
	}
}

func TestRuntimeTargetScanRetirementIsNoopWhenProjectorDisabled(t *testing.T) {
	cfg := config.Defaults()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true), WithBackgroundTargetScanScope(nil))
	if retired, err := projector.retireRuntimeTargetScans(context.Background(), time.Now()); err != nil || retired != 0 || len(sink.events) != 0 {
		t.Fatalf("disabled projector retired=%d err=%v events=%d", retired, err, len(sink.events))
	}
}

// syncEventBus dispatches events synchronously to subscribers.
type syncEventBus struct {
	capturingEventBus
}

func (b *syncEventBus) Publish(ctx context.Context, e events.Event) { b.deliver(ctx, e) }

// sensitiveScanner stands in for AdoptionService.Scan: it discovers workloads
// with per-instance detail and publishes the same aggregate-only completion.
type sensitiveScanner struct {
	bus   events.Publisher
	clock *manualScanClock
}

func (s *sensitiveScanner) Scan(ctx context.Context, req service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	target := req.Targets[0]
	preview := service.AdoptionPreview{Target: target}
	if target.Name == "edge-02" {
		preview.Error = "dial tcp 10.0.0.6:2376: connection refused"
	} else {
		preview.Containers = []service.AdoptionPreviewContainer{
			{Discovered: runtime.DiscoveredContainer{ContainerID: "aaaa1111", ContainerName: "mystery-db", ImageRepo: "postgres", ImageDigest: digestOf("1"), Environment: map[string]string{"POSTGRES_PASSWORD": "pw-123"}}},
			{Discovered: runtime.DiscoveredContainer{ContainerID: "cccc3333", ContainerName: "bahia", Labels: map[string]string{"bahia.managed": "true"}}},
		}
	}
	previews := []service.AdoptionPreview{preview}
	s.bus.Publish(ctx, events.Event{Type: events.EventAdoptionScanCompleted, EntityID: "adoption", Data: service.AdoptionScanCompleted{
		Origin: req.Origin, CompletedAt: s.clock.Now(), TargetCount: 1, Targets: service.AdoptionScanTargetSummaries(previews),
	}})
	return previews, nil
}

type manualScanClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualScanClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualScanClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *manualScanClock) NewTimer(time.Duration) (<-chan time.Time, func()) {
	return make(chan time.Time), func() {}
}

func TestBackgroundScanRunnerPublishesOnlyRedactedAggregates(t *testing.T) {
	bus := &syncEventBus{}
	sink := &captureProjectionPublisher{}
	projector := newBackgroundScanProjector(nil, sink, "edge-01", "edge-02")
	projector.SetupSubscriptions(bus)
	clock := &manualScanClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	runner, err := service.NewAdoptionBackgroundScanRunner(&sensitiveScanner{bus: bus, clock: clock}, service.AdoptionBackgroundScanConfig{
		Targets: []service.AdoptionTarget{
			{Name: "edge-01", EndpointRef: "edge-01-docker", EnvironmentName: "production"},
			{Name: "edge-02", EndpointRef: "edge-02-docker", EnvironmentName: "production"},
		},
		Interval: 5 * time.Minute, Timeout: time.Minute, Concurrency: 2, MaxBackoff: time.Hour,
		Clock: clock, RandomJitter: func(time.Duration) time.Duration { return 0 },
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runner.RunCycle(ctx)
	clock.Advance(5 * time.Minute)
	runner.RunCycle(ctx)

	edge1 := targetScanEventsFor(sink, "edge-01")
	if len(edge1) != 1 {
		t.Fatalf("edge-01 publishes = %d, want 1 (second scan unchanged)", len(edge1))
	}
	if payload := decodeTargetScan(t, edge1[0]); payload.Counts == nil || *payload.Counts != (deploymentInventoryCounts{Total: 2, Managed: 1, Unmanaged: 1}) {
		t.Fatalf("edge-01 aggregate = %+v", payload)
	}
	if edge2 := targetScanEventsFor(sink, "edge-02"); len(edge2) != 1 || decodeTargetScan(t, edge2[0]).ScanState != targetScanUnavailable {
		t.Fatalf("edge-02 publishes = %d, want one unavailable aggregate", len(edge2))
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) == 0 {
		t.Fatal("no public events captured")
	}
	for _, ev := range sink.events {
		if int(ev.Kind) == KindCASAudit {
			t.Fatalf("background scan produced a public audit: %s", ev.Content)
		}
		encoded, _ := json.Marshal(struct {
			Tags    gonostr.Tags
			Content string
		}{ev.Tags, ev.Content})
		for _, detail := range []string{"aaaa1111", "cccc3333", "mystery-db", "postgres", digestOf("1"), "pw-123", "POSTGRES_PASSWORD", "10.0.0.6", "connection refused"} {
			if strings.Contains(string(encoded), detail) {
				t.Fatalf("public event kind %d leaked per-instance detail %q: %s", ev.Kind, detail, encoded)
			}
		}
	}
}
