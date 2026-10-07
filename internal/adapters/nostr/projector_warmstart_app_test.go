package nostr

// This test exercises the exact production assembly path that app.go uses:
//
// projectorOpts = append(projectorOpts,
// WithReadinessTracker(intentReadiness),
// WithIntentDomains(cfg.Nostr.IntentDomains),
// )
// NewProjector(cfg, source, publisher, history, logger, projectorOpts...)
//
// It uses a ReadinessWaiter that mirrors controlplane.ReadinessTracker's
// channel-based Ready contract (register filter, mark ready → channel
// closes). The test cannot import controlplane directly due to the circular
// import (controlplane → adapters/nostr), but ReadinessTracker satisfies
// ReadinessWaiter, so the contract is identical.
//
// Records are seeded by writing signed kind-30900 events directly into the
// history repository, mirroring the intent handler's PublishBeforeCommit path.
//
// Assertions:
// - Zero projector publishes for migrated domains when relay holds current state
// - Exactly one re-publish for a deliberately stale (abandoned) record

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
	repo := repositorytest.NewInMemoryNostrEventRepository()

	// Seed: write signed kind-30900 service records directly into history.
	// This mirrors the production path where intent handlers write via
	// PublishBeforeCommit, which signs and records into the local store.
	seedControlStateRecords(t, repo, KindServiceRegistry, svcIDs)

	// "Restart" — same history, fresh projector.
	// Mirror the production wiring: register the filter, then mark ready
	// after the projector starts (simulating subscriber EOSE).
	readiness := newTrackerReadiness("intent-30900")

	source := newFakeProjectionSource()
	sink := &captureProjectionPublisher{}
	p := newTestProjector(cfg, source, sink, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(readiness))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()

	// Simulate subscriber catch-up (EOSE).
	readiness.MarkFilterReady("intent-30900")
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// Assert: zero publishes — relay holds current state.
	if n := countByDomain(sink.events, "service"); n != 0 {
		t.Errorf("zero-publish restart: expected 0 service publishes, got %d", n)
	}

	// introduce a stale record and restart.
	markRecordFailed(t, repo, "domain", "service", svcIDs[2].String())

	readiness2 := newTrackerReadiness("intent-30900")
	sink2 := &captureProjectionPublisher{}
	p2 := newTestProjector(cfg, source, sink2, repo, logger,
		WithIntentDomains([]string{"service", "environment"}),
		WithReadinessTracker(readiness2))

	runCtx2, cancel2 := context.WithCancel(ctx)
	done2 := make(chan error, 1)
	go func() { done2 <- p2.Run(runCtx2) }()

	readiness2.MarkFilterReady("intent-30900")
	time.Sleep(100 * time.Millisecond)
	cancel2()
	<-done2

	// Assert: exactly 1 re-publish for the stale record.
	if n := countByDomain(sink2.events, "service"); n != 1 {
		t.Errorf("stale re-publish: expected 1, got %d", n)
	}
}
