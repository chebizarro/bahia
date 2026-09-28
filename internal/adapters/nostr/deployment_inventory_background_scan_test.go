package nostr

import (
	"context"
	"encoding/json"
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
// jitter) + 1m timeout.
const defaultBackgroundTargetScanStaleAfter = 22 * time.Minute

func newBackgroundScanProjector(repo repository.NostrEventRepository, sink *captureProjectionPublisher) *Projector {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Adoption.Enabled = true
	return NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, repo, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))
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
	first := newBackgroundScanProjector(repo, &captureProjectionPublisher{})
	deliverScan(t, first, targetScanCompleted(service.AdoptionScanOriginBackground, t0,
		targetCounts{name: "edge-01", available: true, unmanaged: 1},
		targetCounts{name: "edge-02", available: true, unmanaged: 4}))
	// An operator scans an unconfigured target ad hoc.
	deliverScan(t, first, targetScanCompleted(service.AdoptionScanOriginOperator, t0, targetCounts{name: "adhoc", available: true, unmanaged: 7}))

	// edge-02 is removed from configuration while Bahia is down.
	ageRetainedEvents(repo, time.Hour)
	sink := &captureProjectionPublisher{}
	restarted := newBackgroundScanProjector(repo, sink)
	keep := []service.RuntimeTargetScanScope{{Environment: "production", Target: "edge-01"}}
	retired, err := restarted.RetireRuntimeTargetScans(ctx, keep, t0.Add(time.Minute))
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
	if retired, err := restarted.RetireRuntimeTargetScans(ctx, keep, t0.Add(2*time.Minute)); err != nil || retired != 0 {
		t.Fatalf("second retirement retired=%d err=%v", retired, err)
	}

	// The ad-hoc operator aggregate is retired once it is stale, not left
	// behind forever.
	if retired, err := restarted.RetireRuntimeTargetScans(ctx, keep, t0.Add(defaultBackgroundTargetScanStaleAfter+time.Second)); err != nil || retired != 1 {
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
	third := newBackgroundScanProjector(repo, again)
	if retired, err := third.RetireRuntimeTargetScans(ctx, []service.RuntimeTargetScanScope{{Environment: "production", Target: "edge-01"}, {Environment: "production", Target: "edge-02"}}, t0.Add(31*time.Minute)); err != nil || retired != 0 {
		t.Fatalf("third retirement retired=%d err=%v", retired, err)
	}
}

func TestRuntimeTargetScanRetirementIsNoopWhenProjectorDisabled(t *testing.T) {
	cfg := config.Defaults()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))
	if retired, err := projector.RetireRuntimeTargetScans(context.Background(), nil, time.Now()); err != nil || retired != 0 || len(sink.events) != 0 {
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
	projector := newBackgroundScanProjector(nil, sink)
	projector.SetupSubscriptions(bus)
	clock := &manualScanClock{now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	runner, err := service.NewAdoptionBackgroundScanRunner(&sensitiveScanner{bus: bus, clock: clock}, projector, service.AdoptionBackgroundScanConfig{
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
