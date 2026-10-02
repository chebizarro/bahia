package nostr

// This test exercises the exact production assembly path that app.go uses:
//
//     projectorOpts = append(projectorOpts,
//         WithReadinessTracker(intentReadiness),
//         WithIntentDomains(cfg.Nostr.IntentDomains),
//     )
//     NewProjector(cfg, source, publisher, history, logger, projectorOpts...)
//
// It uses a ReadinessWaiter that mirrors controlplane.ReadinessTracker's
// channel-based Ready() contract (register filter, mark ready → channel
// closes). The test cannot import controlplane directly due to the circular
// import (controlplane → adapters/nostr), but ReadinessTracker satisfies
// ReadinessWaiter, so the contract is identical.
//
// Assertions:
//   - Zero projector publishes for migrated domains when relay holds current state
//   - Exactly one re-publish for a deliberately stale (abandoned) record

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"go.uber.org/zap"
)

// trackerReadiness mirrors controlplane.ReadinessTracker's channel-based
// behavior: register a filter key, mark it ready, channel closes. This is
// the same contract that app.go wires via WithReadinessTracker(intentReadiness).
type trackerReadiness struct {
	ch      chan struct{}
	filters map[string]bool
}

func newTrackerReadiness(filterKeys ...string) *trackerReadiness {
	t := &trackerReadiness{
		ch:      make(chan struct{}),
		filters: make(map[string]bool, len(filterKeys)),
	}
	for _, k := range filterKeys {
		t.filters[k] = false
	}
	return t
}

func (t *trackerReadiness) Ready() <-chan struct{} { return t.ch }

func (t *trackerReadiness) MarkFilterReady(key string) {
	t.filters[key] = true
	for _, ready := range t.filters {
		if !ready {
			return
		}
	}
	select {
	case <-t.ch:
	default:
		close(t.ch)
	}
}

func TestProductionAssemblyWarmStartZeroPublishAndStaleRepublish(t *testing.T) {
	ctx := t.Context()
	logger := zap.NewNop()
	cfg := warmStartTestCfg()

	svcIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	source := warmStartTestSource(svcIDs, nil)
	repo := repositorytest.NewInMemoryNostrEventRepository()

	// Phase 1: initial publish seeds the daemon's local history.
	sink1 := &captureProjectionPublisher{}
	p1 := newTestProjector(cfg, source, sink1, repo, logger,
		WithProjectorRepairInterval(-1))
	if err := p1.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if n := countByDomain(sink1.events, "service"); n != 3 {
		t.Fatalf("expected 3 initial service publishes, got %d", n)
	}

	// Phase 2: "restart" — same history, fresh projector.
	// Mirror the production wiring: register the filter, then mark ready
	// after the projector starts (simulating subscriber EOSE).
	readiness := newTrackerReadiness("intent-30900")

	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(readiness),
		WithProjectorRepairInterval(-1))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p2.Run(runCtx) }()

	// Simulate subscriber catch-up (EOSE).
	readiness.MarkFilterReady("intent-30900")
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes — relay holds current state.
	if n := countByDomain(sink2.events, "service"); n != 0 {
		t.Errorf("zero-publish restart: expected 0 service publishes, got %d", n)
	}

	// Phase 3: introduce a stale record and restart.
	markRecordFailed(t, repo, "domain", "service", svcIDs[2].String())

	readiness3 := newTrackerReadiness("intent-30900")
	sink3 := &captureProjectionPublisher{}
	p3 := newTestProjector(cfg, source, sink3, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(readiness3),
		WithProjectorRepairInterval(-1))

	runCtx3, cancel3 := context.WithCancel(ctx)
	done3 := make(chan error, 1)
	go func() { done3 <- p3.Run(runCtx3) }()

	readiness3.MarkFilterReady("intent-30900")
	time.Sleep(100 * time.Millisecond)
	cancel3()
	<-done3

	// Assert: exactly 1 re-publish for the stale record.
	if n := countByDomain(sink3.events, "service"); n != 1 {
		t.Errorf("stale re-publish: expected 1, got %d", n)
	}
}
