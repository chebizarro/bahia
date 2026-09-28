package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Background adoption scan outcomes. They are stable codes safe to expose on
// health endpoints; raw scan errors can carry Docker hosts and stay in logs.
const (
	BackgroundScanOutcomePending     = "pending"
	BackgroundScanOutcomeOK          = "ok"
	BackgroundScanOutcomeUnavailable = "unavailable"
	BackgroundScanOutcomeTimeout     = "timeout"
	BackgroundScanOutcomeError       = "error"
)

// AdoptionTargetScanner is the read-only adoption scan the background runner
// drives. AdoptionService satisfies it; the scan lists and inspects containers
// and images and never imports, mutates, or stops anything.
type AdoptionTargetScanner interface {
	Scan(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error)
}

// RuntimeTargetScanScope names one published runtime-target-scan coordinate
// (the normalized environment and target alias of a scan target).
type RuntimeTargetScanScope struct {
	Environment string
	Target      string
}

// BackgroundScanClock abstracts time so scheduling is testable without sleeps.
type BackgroundScanClock interface {
	Now() time.Time
	// NewTimer returns a channel that receives once d has elapsed, and a stop
	// function that releases the timer.
	NewTimer(d time.Duration) (<-chan time.Time, func())
}

type systemBackgroundScanClock struct{}

func (systemBackgroundScanClock) Now() time.Time { return time.Now().UTC() }

func (systemBackgroundScanClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(d)
	return timer.C, func() { timer.Stop() }
}

// AdoptionBackgroundScanConfig configures the background adoption scan runner.
// Targets must already be normalized (see AdoptionService.ResolveScanTargets).
type AdoptionBackgroundScanConfig struct {
	Targets []AdoptionTarget
	// SkippedTargets names configured endpoint aliases that could not become
	// scan targets (for example aliases colliding after normalization). They
	// are reported as a health warning.
	SkippedTargets []string
	Interval       time.Duration
	Jitter         time.Duration
	Timeout        time.Duration
	Concurrency    int
	MaxBackoff     time.Duration
	// Clock and RandomJitter are test seams; nil uses the system clock and a
	// uniform random jitter in [0, max].
	Clock        BackgroundScanClock
	RandomJitter func(max time.Duration) time.Duration
}

// BackgroundScanTargetStatus is the health view of one scan target.
type BackgroundScanTargetStatus struct {
	Target              string
	Environment         string
	Outcome             string
	ConsecutiveFailures int
	LastScanAt          time.Time
	LastSuccessAt       time.Time
	NextDueAt           time.Time
}

// AdoptionBackgroundScanStatus is the health view of the runner.
type AdoptionBackgroundScanStatus struct {
	Targets        []BackgroundScanTargetStatus
	SkippedTargets []string
	Failing        int
	Cycles         int64
	OverlapSkipped int64
	LastCycleAt    time.Time
}

type backgroundScanTargetState struct {
	target              AdoptionTarget
	outcome             string
	consecutiveFailures int
	lastScanAt          time.Time
	lastSuccessAt       time.Time
	nextDueAt           time.Time
}

// AdoptionBackgroundScanRunner periodically runs the read-only adoption scan
// against each configured runtime endpoint target so that the public,
// redacted runtime-target-scan aggregates stay fresh. Per-instance detail
// never leaves the scan: the scan's completion event carries only per-target
// counts, which the Nostr projector publishes with material-change gating.
//
// Scheduling guarantees: one cycle at a time (overlapping cycles are skipped,
// never queued), at most Concurrency targets in flight, each bounded by
// Timeout, and failing targets back off exponentially up to MaxBackoff.
type AdoptionBackgroundScanRunner struct {
	scanner AdoptionTargetScanner
	cfg     AdoptionBackgroundScanConfig
	clock   BackgroundScanClock
	jitter  func(max time.Duration) time.Duration
	logger  *zap.Logger
	scope   []RuntimeTargetScanScope

	cycleMu sync.Mutex

	mu             sync.Mutex
	states         []*backgroundScanTargetState
	scheduled      bool
	cycles         int64
	overlapSkipped int64
	lastCycleAt    time.Time
}

// NewAdoptionBackgroundScanRunner validates bounds and builds a runner.
func NewAdoptionBackgroundScanRunner(scanner AdoptionTargetScanner, cfg AdoptionBackgroundScanConfig, logger *zap.Logger) (*AdoptionBackgroundScanRunner, error) {
	if scanner == nil {
		return nil, errors.New("background adoption scan requires a scanner")
	}
	if cfg.Interval <= 0 || cfg.Timeout <= 0 || cfg.Concurrency <= 0 || cfg.MaxBackoff < cfg.Interval || cfg.Jitter < 0 {
		return nil, fmt.Errorf("background adoption scan bounds are invalid: interval=%s timeout=%s concurrency=%d max_backoff=%s jitter=%s", cfg.Interval, cfg.Timeout, cfg.Concurrency, cfg.MaxBackoff, cfg.Jitter)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	r := &AdoptionBackgroundScanRunner{scanner: scanner, cfg: cfg, clock: cfg.Clock, jitter: cfg.RandomJitter, logger: logger}
	if r.clock == nil {
		r.clock = systemBackgroundScanClock{}
	}
	if r.jitter == nil {
		r.jitter = uniformJitter
	}
	for _, target := range cfg.Targets {
		if target.EndpointRef == "" {
			return nil, fmt.Errorf("background adoption scan target %q must use a server-managed endpoint_ref", target.Name)
		}
		// Keep only the alias fields: Scan resolves the endpoint transport
		// itself, so the runner never carries a Docker host or credentials.
		target = AdoptionTarget{Name: target.Name, EndpointRef: target.EndpointRef, EnvironmentName: target.EnvironmentName}
		r.states = append(r.states, &backgroundScanTargetState{target: target, outcome: BackgroundScanOutcomePending})
		r.scope = append(r.scope, RuntimeTargetScanScope{Environment: target.EnvironmentName, Target: target.Name})
	}
	return r, nil
}

func uniformJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1))
}

// Scope returns the published runtime-target-scan coordinates this runner
// maintains. The Nostr projector retires coordinates outside it.
func (r *AdoptionBackgroundScanRunner) Scope() []RuntimeTargetScanScope {
	return append([]RuntimeTargetScanScope(nil), r.scope...)
}

// Name implements the application background runner contract.
func (r *AdoptionBackgroundScanRunner) Name() string { return "adoption-background-scan" }

// Run schedules scans until ctx is cancelled. The first scan of each target
// is spread over [0, Jitter] after start so a restart does not burst every
// endpoint at once.
func (r *AdoptionBackgroundScanRunner) Run(ctx context.Context) error {
	r.schedule(r.clock.Now())
	for {
		wait := r.untilNextDue(r.clock.Now())
		if wait > 0 {
			fired, stop := r.clock.NewTimer(wait)
			select {
			case <-ctx.Done():
				stop()
				return nil
			case <-fired:
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		r.RunCycle(ctx)
	}
}

func (r *AdoptionBackgroundScanRunner) schedule(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scheduled {
		return
	}
	r.scheduled = true
	for _, state := range r.states {
		state.nextDueAt = now.Add(r.jitter(r.cfg.Jitter))
	}
}

// untilNextDue returns how long to wait for the earliest due target; with no
// targets the runner idles one interval at a time.
func (r *AdoptionBackgroundScanRunner) untilNextDue(now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return r.cfg.Interval
	}
	earliest := r.states[0].nextDueAt
	for _, state := range r.states[1:] {
		if state.nextDueAt.Before(earliest) {
			earliest = state.nextDueAt
		}
	}
	return earliest.Sub(now)
}

// RunCycle scans every due target once, with bounded concurrency, and
// returns whether the cycle ran. A cycle requested while another is in flight
// is skipped rather than queued, so scans never pile up. A cycle lasts at most
// about Timeout per wave of Concurrency targets; a target that becomes due
// meanwhile waits for the next cycle, which the projector's staleness budget
// accounts for (see targetScanStaleAfter).
func (r *AdoptionBackgroundScanRunner) RunCycle(ctx context.Context) bool {
	if !r.cycleMu.TryLock() {
		r.mu.Lock()
		r.overlapSkipped++
		r.mu.Unlock()
		r.logger.Debug("background adoption scan cycle skipped: previous cycle still running")
		return false
	}
	defer r.cycleMu.Unlock()
	now := r.clock.Now()
	r.schedule(now)

	r.mu.Lock()
	var due []*backgroundScanTargetState
	for _, state := range r.states {
		if !state.nextDueAt.After(now) {
			due = append(due, state)
		}
	}
	r.mu.Unlock()

	sem := make(chan struct{}, r.cfg.Concurrency)
	var wg sync.WaitGroup
dispatch:
	for _, state := range due {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		wg.Add(1)
		go func(state *backgroundScanTargetState) {
			defer wg.Done()
			defer func() { <-sem }()
			r.scanTarget(ctx, state)
		}(state)
	}
	wg.Wait()

	r.mu.Lock()
	r.cycles++
	r.lastCycleAt = r.clock.Now()
	r.mu.Unlock()
	return true
}

func (r *AdoptionBackgroundScanRunner) scanTarget(ctx context.Context, state *backgroundScanTargetState) {
	target := state.target
	scanCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	previews, err := r.scanner.Scan(scanCtx, AdoptionScanRequest{Targets: []AdoptionTarget{target}, Origin: AdoptionScanOriginBackground})
	timedOut := errors.Is(scanCtx.Err(), context.DeadlineExceeded)
	cancel()
	if ctx.Err() != nil {
		// Shutdown is not a target failure.
		return
	}

	outcome := BackgroundScanOutcomeOK
	switch {
	case timedOut:
		// Classified by the deadline, not by err: a scan that overran its
		// timeout is never counted as a success.
		outcome = BackgroundScanOutcomeTimeout
		if err == nil {
			err = context.DeadlineExceeded
		}
	case err != nil:
		outcome = BackgroundScanOutcomeError
	case len(previews) != 1:
		outcome = BackgroundScanOutcomeError
		err = fmt.Errorf("scan returned %d previews for one target", len(previews))
	case previews[0].Error != "":
		outcome = BackgroundScanOutcomeUnavailable
		err = errors.New(previews[0].Error)
	}

	finished := r.clock.Now()
	r.mu.Lock()
	state.outcome = outcome
	state.lastScanAt = finished
	if outcome == BackgroundScanOutcomeOK {
		state.consecutiveFailures = 0
		state.lastSuccessAt = finished
		state.nextDueAt = finished.Add(r.cfg.Interval + r.jitter(r.cfg.Jitter))
	} else {
		state.consecutiveFailures++
		state.nextDueAt = finished.Add(r.backoff(state.consecutiveFailures) + r.jitter(r.cfg.Jitter))
	}
	failures, nextDue := state.consecutiveFailures, state.nextDueAt
	r.mu.Unlock()

	if outcome != BackgroundScanOutcomeOK {
		r.logger.Warn("background adoption scan target failed",
			zap.String("target", target.Name),
			zap.String("endpoint_ref", target.EndpointRef),
			zap.String("outcome", outcome),
			zap.Int("consecutive_failures", failures),
			zap.Time("next_scan_at", nextDue),
			zap.Error(err),
		)
	}
}

// backoff doubles the interval per consecutive failure, capped at MaxBackoff.
func (r *AdoptionBackgroundScanRunner) backoff(failures int) time.Duration {
	delay := r.cfg.Interval
	for i := 0; i < failures && delay < r.cfg.MaxBackoff; i++ {
		delay *= 2
	}
	if delay > r.cfg.MaxBackoff {
		delay = r.cfg.MaxBackoff
	}
	return delay
}

// Status returns a snapshot for health/readiness reporting.
func (r *AdoptionBackgroundScanRunner) Status() AdoptionBackgroundScanStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := AdoptionBackgroundScanStatus{Cycles: r.cycles, OverlapSkipped: r.overlapSkipped, LastCycleAt: r.lastCycleAt, SkippedTargets: append([]string(nil), r.cfg.SkippedTargets...)}
	for _, state := range r.states {
		if state.consecutiveFailures > 0 {
			status.Failing++
		}
		status.Targets = append(status.Targets, BackgroundScanTargetStatus{
			Target:              state.target.Name,
			Environment:         state.target.EnvironmentName,
			Outcome:             state.outcome,
			ConsecutiveFailures: state.consecutiveFailures,
			LastScanAt:          state.lastScanAt,
			LastSuccessAt:       state.lastSuccessAt,
			NextDueAt:           state.nextDueAt,
		})
	}
	sort.Slice(status.Targets, func(i, j int) bool { return status.Targets[i].Target < status.Targets[j].Target })
	return status
}
