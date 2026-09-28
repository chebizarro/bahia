package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// fakeScanClock is a manual clock: timers fire only when the test advances
// time, and each armed timer is announced on armed so tests synchronize on the
// runner actually waiting instead of sleeping.
type fakeScanClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeScanTimer
	armed  chan time.Duration
}

type fakeScanTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeScanClock(start time.Time) *fakeScanClock {
	return &fakeScanClock{now: start, armed: make(chan time.Duration, 16)}
}

func (c *fakeScanClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeScanClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeScanTimer{at: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	c.armed <- d
	return ch, func() {}
}

func (c *fakeScanClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			timer.ch <- c.now
			continue
		}
		kept = append(kept, timer)
	}
	c.timers = kept
}

type scanCall struct {
	req         AdoptionScanRequest
	hasDeadline bool
}

// fakeTargetScanner answers per target name. A nil result means a complete
// scan with no containers.
type fakeTargetScanner struct {
	mu      sync.Mutex
	calls   []scanCall
	results map[string]func(ctx context.Context) ([]AdoptionPreview, error)
}

func (s *fakeTargetScanner) Scan(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error) {
	_, hasDeadline := ctx.Deadline()
	s.mu.Lock()
	s.calls = append(s.calls, scanCall{req: req, hasDeadline: hasDeadline})
	result := s.results[req.Targets[0].Name]
	s.mu.Unlock()
	if result != nil {
		return result(ctx)
	}
	return []AdoptionPreview{{Target: req.Targets[0]}}, nil
}

func (s *fakeTargetScanner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func noJitter(time.Duration) time.Duration { return 0 }

func backgroundScanTestConfig(clock BackgroundScanClock, names ...string) AdoptionBackgroundScanConfig {
	cfg := AdoptionBackgroundScanConfig{
		Interval:     5 * time.Minute,
		Timeout:      time.Minute,
		Concurrency:  2,
		MaxBackoff:   time.Hour,
		Clock:        clock,
		RandomJitter: noJitter,
	}
	for _, name := range names {
		cfg.Targets = append(cfg.Targets, AdoptionTarget{Name: name, EndpointRef: name + "-docker", EnvironmentName: "production"})
	}
	return cfg
}

func targetStatus(t *testing.T, runner *AdoptionBackgroundScanRunner, name string) BackgroundScanTargetStatus {
	t.Helper()
	for _, status := range runner.Status().Targets {
		if status.Target == name {
			return status
		}
	}
	t.Fatalf("no status for target %s", name)
	return BackgroundScanTargetStatus{}
}

func TestBackgroundScanRunnerScansEachConfiguredTargetPeriodically(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	clock := newFakeScanClock(start)
	scanner := &fakeTargetScanner{}
	runner, err := NewAdoptionBackgroundScanRunner(scanner, backgroundScanTestConfig(clock, "edge-01", "edge-02"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// With zero jitter both targets are due immediately; the runner then
	// arms a timer for the next interval.
	if wait := <-clock.armed; wait != 5*time.Minute {
		t.Fatalf("first wait = %s, want the scan interval", wait)
	}
	if got := scanner.callCount(); got != 2 {
		t.Fatalf("initial cycle scans = %d, want 2", got)
	}
	clock.Advance(5 * time.Minute)
	<-clock.armed
	if got := scanner.callCount(); got != 4 {
		t.Fatalf("after one interval scans = %d, want 4", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	scanner.mu.Lock()
	defer scanner.mu.Unlock()
	for _, call := range scanner.calls {
		if len(call.req.Targets) != 1 || call.req.Origin != AdoptionScanOriginBackground || !call.hasDeadline {
			t.Fatalf("scan call = %+v, want one background target with a per-target deadline", call)
		}
	}
	if scope := runner.Scope(); len(scope) != 2 || scope[0] != (RuntimeTargetScanScope{Environment: "production", Target: "edge-01"}) {
		t.Fatalf("scope = %+v, want the configured targets", scope)
	}
	if status := runner.Status(); status.Cycles != 2 || status.Failing != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestBackgroundScanRunnerBacksOffFailingTargetsAndRecovers(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	clock := newFakeScanClock(start)
	var failing atomic.Bool
	failing.Store(true)
	scanner := &fakeTargetScanner{results: map[string]func(context.Context) ([]AdoptionPreview, error){
		"edge-01": func(context.Context) ([]AdoptionPreview, error) {
			if failing.Load() {
				return nil, errors.New("dial tcp 10.0.0.5:2376: connection refused")
			}
			return []AdoptionPreview{{Target: AdoptionTarget{Name: "edge-01"}}}, nil
		},
		"edge-02": func(context.Context) ([]AdoptionPreview, error) {
			return []AdoptionPreview{{Target: AdoptionTarget{Name: "edge-02"}, Error: "connection refused"}}, nil
		},
	}}
	cfg := backgroundScanTestConfig(clock, "edge-01", "edge-02")
	cfg.MaxBackoff = 30 * time.Minute
	runner, err := NewAdoptionBackgroundScanRunner(scanner, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	wantNext := []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, backoff := range wantNext {
		runner.RunCycle(ctx)
		now := clock.Now()
		for _, name := range []string{"edge-01", "edge-02"} {
			status := targetStatus(t, runner, name)
			if status.ConsecutiveFailures != i+1 || !status.NextDueAt.Equal(now.Add(backoff)) {
				t.Fatalf("%s failure %d: failures=%d next=%s, want next=%s", name, i+1, status.ConsecutiveFailures, status.NextDueAt.Sub(now), backoff)
			}
		}
		// Nothing is due before the backoff elapses.
		clock.Advance(backoff - time.Second)
		before := scanner.callCount()
		runner.RunCycle(ctx)
		if scanner.callCount() != before {
			t.Fatalf("target scanned during backoff window")
		}
		clock.Advance(time.Second)
	}
	if status := targetStatus(t, runner, "edge-01"); status.Outcome != BackgroundScanOutcomeError {
		t.Fatalf("edge-01 outcome = %s", status.Outcome)
	}
	if status := targetStatus(t, runner, "edge-02"); status.Outcome != BackgroundScanOutcomeUnavailable {
		t.Fatalf("unreachable target outcome = %s, want unavailable", status.Outcome)
	}
	if runner.Status().Failing != 2 {
		t.Fatalf("failing = %d, want 2", runner.Status().Failing)
	}

	failing.Store(false)
	runner.RunCycle(ctx)
	status := targetStatus(t, runner, "edge-01")
	if status.ConsecutiveFailures != 0 || status.Outcome != BackgroundScanOutcomeOK || !status.NextDueAt.Equal(clock.Now().Add(5*time.Minute)) {
		t.Fatalf("recovered status = %+v", status)
	}
}

func TestBackgroundScanRunnerTimesOutHungTargetWithoutBlockingOthers(t *testing.T) {
	clock := newFakeScanClock(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	scanner := &fakeTargetScanner{results: map[string]func(context.Context) ([]AdoptionPreview, error){
		"hung": func(ctx context.Context) ([]AdoptionPreview, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		// Overran its deadline but still returned results: classified by
		// the deadline, never as a success.
		"late": func(ctx context.Context) ([]AdoptionPreview, error) {
			<-ctx.Done()
			return []AdoptionPreview{{}}, nil
		},
	}}
	cfg := backgroundScanTestConfig(clock, "hung", "late", "healthy")
	cfg.Concurrency = 3
	cfg.Timeout = time.Millisecond
	runner, err := NewAdoptionBackgroundScanRunner(scanner, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	runner.RunCycle(context.Background())
	hung := targetStatus(t, runner, "hung")
	if hung.Outcome != BackgroundScanOutcomeTimeout || hung.ConsecutiveFailures != 1 || !hung.NextDueAt.Equal(clock.Now().Add(10*time.Minute)) {
		t.Fatalf("hung target status = %+v, want timeout with backoff", hung)
	}
	if late := targetStatus(t, runner, "late"); late.Outcome != BackgroundScanOutcomeTimeout || late.ConsecutiveFailures != 1 {
		t.Fatalf("late target status = %+v, want timeout", late)
	}
	if healthy := targetStatus(t, runner, "healthy"); healthy.Outcome != BackgroundScanOutcomeOK {
		t.Fatalf("healthy target status = %+v", healthy)
	}
}

func TestBackgroundScanRunnerNeverOverlapsCyclesAndBoundsConcurrency(t *testing.T) {
	clock := newFakeScanClock(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	entered := make(chan string, 8)
	release := make(chan struct{})
	var inFlight, maxInFlight atomic.Int32
	block := func(ctx context.Context) ([]AdoptionPreview, error) {
		n := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if n <= prev || maxInFlight.CompareAndSwap(prev, n) {
				break
			}
		}
		entered <- "scan"
		<-release
		inFlight.Add(-1)
		return []AdoptionPreview{{}}, nil
	}
	names := []string{"a", "b", "c", "d"}
	scanner := &fakeTargetScanner{results: map[string]func(context.Context) ([]AdoptionPreview, error){}}
	for _, name := range names {
		scanner.results[name] = block
	}
	runner, err := NewAdoptionBackgroundScanRunner(scanner, backgroundScanTestConfig(clock, names...), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	cycleDone := make(chan bool, 1)
	go func() { cycleDone <- runner.RunCycle(context.Background()) }()
	<-entered
	<-entered

	// A second trigger while the first cycle is in flight is skipped, not
	// queued, and scans nothing.
	if ran := runner.RunCycle(context.Background()); ran {
		t.Fatal("overlapping cycle ran")
	}
	if skipped := runner.Status().OverlapSkipped; skipped != 1 {
		t.Fatalf("overlap skipped = %d, want 1", skipped)
	}

	for range names {
		release <- struct{}{}
	}
	if ran := <-cycleDone; !ran {
		t.Fatal("first cycle did not run")
	}
	if got := maxInFlight.Load(); got > 2 {
		t.Fatalf("max in-flight scans = %d, want <= concurrency 2", got)
	}
	if got := scanner.callCount(); got != 4 {
		t.Fatalf("scans = %d, want each target exactly once", got)
	}
}

func TestBackgroundScanRunnerRejectsRawDockerHostTargets(t *testing.T) {
	cfg := backgroundScanTestConfig(nil)
	cfg.Targets = []AdoptionTarget{{Name: "breakglass", DockerHost: "tcp://127.0.0.1:2375"}}
	if _, err := NewAdoptionBackgroundScanRunner(&fakeTargetScanner{}, cfg, zap.NewNop()); err == nil {
		t.Fatal("expected raw docker_host background target to be rejected")
	}
}

// TestBackgroundScanIsReadOnlyAndPublishesOnlyAggregates drives the real
// adoption scan through the runner against a Docker API double that refuses
// anything but GET, and checks nothing is written to Bahia state.
func TestBackgroundScanIsReadOnlyAndPublishesOnlyAggregates(t *testing.T) {
	var methodsMu sync.Mutex
	var nonGet []string
	docker := newAdoptionDockerServer(t)
	defer docker.Close()
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodsMu.Lock()
			nonGet = append(nonGet, r.Method+" "+r.URL.Path)
			methodsMu.Unlock()
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		docker.Config.Handler.ServeHTTP(w, r)
	}))
	defer guard.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	publisher := &capturePublisher{}
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, publisher, zap.NewNop(),
		WithAdoptionRuntimeConfig(config.RuntimeConfig{Endpoints: map[string]config.RuntimeEndpointConfig{"edge-01-docker": {DockerHost: guard.URL}}}, false),
	)
	targets, err := adoption.ResolveScanTargets([]AdoptionTarget{{Name: "edge-01", EndpointRef: "edge-01-docker", EnvironmentName: "production"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := backgroundScanTestConfig(newFakeScanClock(time.Now().UTC()))
	cfg.Targets = targets
	runner, err := NewAdoptionBackgroundScanRunner(adoption, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	runner.RunCycle(context.Background())

	if status := targetStatus(t, runner, "edge-01"); status.Outcome != BackgroundScanOutcomeOK {
		t.Fatalf("scan outcome = %+v", status)
	}
	if len(nonGet) != 0 {
		t.Fatalf("background scan issued non-GET Docker API calls: %v", nonGet)
	}
	if services, _ := svcRepo.List(context.Background()); len(services) != 0 {
		t.Fatalf("background scan created services: %+v", services)
	}
	if len(publisher.events) != 1 || publisher.events[0].Type != events.EventAdoptionScanCompleted {
		t.Fatalf("published events = %+v, want exactly one scan completion", publisher.events)
	}
	completed, ok := publisher.events[0].Data.(AdoptionScanCompleted)
	if !ok || completed.Origin != AdoptionScanOriginBackground || len(completed.Targets) != 1 {
		t.Fatalf("scan completion = %#v", publisher.events[0].Data)
	}
	if summary := completed.Targets[0]; !summary.Available || summary.Total != 1 || summary.Unmanaged != 1 || summary.Target != "edge-01" || summary.Environment != "production" {
		t.Fatalf("scan summary = %+v", summary)
	}
}

type deadlineRecordingPublisher struct {
	mu          sync.Mutex
	events      []events.Event
	hadDeadline []bool
}

func (p *deadlineRecordingPublisher) Publish(ctx context.Context, e events.Event) {
	_, deadline := ctx.Deadline()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	p.hadDeadline = append(p.hadDeadline, deadline)
}

func (p *deadlineRecordingPublisher) Subscribe(events.EventType, events.Handler) {}

// TestAdoptionScanPublishesCompletionWithoutTheScanDeadline guards against the
// in-process bus copying the per-target scan deadline onto the asynchronous
// projector handler.
func TestAdoptionScanPublishesCompletionWithoutTheScanDeadline(t *testing.T) {
	docker := newAdoptionDockerServer(t)
	defer docker.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	publisher := &deadlineRecordingPublisher{}
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, publisher, zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if _, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: docker.URL}}, Origin: AdoptionScanOriginBackground}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 || publisher.hadDeadline[0] {
		t.Fatalf("completion published %d times, deadline=%v; want once without the scan deadline", len(publisher.events), publisher.hadDeadline)
	}
}

// cancellingServiceRepo cancels the scan context on the first service
// lookup, which runs after Docker discovery has completed.
type cancellingServiceRepo struct {
	*mockServiceRepo
	cancel context.CancelFunc
}

func (r cancellingServiceRepo) GetByName(ctx context.Context, name string) (*domain.Service, error) {
	r.cancel()
	return r.mockServiceRepo.GetByName(ctx, name)
}

// TestAdoptionScanCancelledAfterDiscoveryPublishesNothing: once the scan
// context is done, service lookups fail and adopted workloads would be
// miscounted as unmanaged, so the scan must fail rather than publish.
func TestAdoptionScanCancelledAfterDiscoveryPublishesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	docker := newAdoptionDockerServer(t)
	defer docker.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	publisher := &capturePublisher{}
	adoption := NewAdoptionService(registry, cancellingServiceRepo{mockServiceRepo: svcRepo, cancel: cancel}, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, publisher, zap.NewNop())
	_, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: docker.URL}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan error = %v, want context.Canceled", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("cancelled scan published %d events", len(publisher.events))
	}
}
