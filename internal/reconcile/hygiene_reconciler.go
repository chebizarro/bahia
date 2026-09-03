package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

// MaintenanceIntentPublisher is the slice of the maintenance command
// publisher the hygiene reconciler needs (fakeable in tests).
type MaintenanceIntentPublisher interface {
	PublishScan(ctx context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishQuarantine(ctx context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishRelocate(ctx context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishGC(ctx context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishPressure(ctx context.Context, cmd controlplane.MaintenanceCommand) (*controlplane.WorkerCommandReceipt, error)
}

// HygieneCandidate mirrors a maintenance-driver scan candidate.
type HygieneCandidate struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Class     string `json:"class"`
	Reason    string `json:"reason,omitempty"`
	Canonical string `json:"canonical,omitempty"`
	Blocked   string `json:"blocked,omitempty"`
}

// HygieneMountPressure mirrors a maintenance-driver pressure sample.
type HygieneMountPressure struct {
	Path        string  `json:"path"`
	UsedPct     float64 `json:"used_pct"`
	TotalInodes uint64  `json:"total_inodes"`
	FreeInodes  uint64  `json:"free_inodes"`
}

// HygieneObservation is the latest known hygiene state for one worker,
// assembled from correlated ContextVM responses on kind 25910. Scan and
// pressure freshness are independent because their replies arrive separately.
type HygieneObservation struct {
	WorkerPubKey       string
	Candidates         []HygieneCandidate
	Pressure           []HygieneMountPressure
	ObservedAt         time.Time // diagnostic aggregate; never used for source-backed component freshness
	ScanObservedAt     time.Time
	PressureObservedAt time.Time
	ScanTruncated      bool
	TotalCandidates    int
}

// HygieneObservationSource yields the latest hygiene observation per worker.
// Production reads authenticated, request-correlated kind-25910 responses;
// tests may provide deterministic in-memory observations.
type HygieneObservationSource interface {
	Latest(ctx context.Context, workerPubKey string) (*HygieneObservation, error)
}

// HygieneMetrics is the telemetry slice the reconciler emits into.
type HygieneMetrics interface {
	RecordHygieneScan()
	RecordHygieneCandidates(class string, count int)
	RecordHygieneAction(method, status string)
	RecordHygienePressureBreach()
}

// HygieneAction records one intent issued (or deferred) during a pass.
type HygieneAction struct {
	WorkerPubKey string
	Method       string
	Paths        []string
	Tier         int
	Deferred     string // non-empty ⇒ NOT issued (Tier-2 pending approval, or error)
}

// HygieneReconcileResult summarizes one reconcile pass.
type HygieneReconcileResult struct {
	Workers        int
	ScansIssued    int
	Actions        []HygieneAction
	PendingTier2   []HygieneAction
	PressureAlerts []string
}

// HygieneReconciler models cruft as drift from a clean desired state (J4):
// it periodically issues dry-run scans, converges Tier-1 findings
// automatically when the policy allows (quarantine of unblocked
// dup-clone/cruft candidates; gc on pressure breach), and surfaces Tier-2
// work (relocate/purge) as pending instead of acting — the tier gate
// (doctrine, J6) is enforced both here and in the driver's method ACL.
type HygieneReconciler struct {
	policy    domain.HygienePolicy
	workers   []string
	publisher MaintenanceIntentPublisher
	source    HygieneObservationSource
	metrics   HygieneMetrics
	interval  time.Duration
	now       func() time.Time
	logger    *zap.Logger
}

func NewHygieneReconciler(
	policy domain.HygienePolicy,
	workers []string,
	publisher MaintenanceIntentPublisher,
	source HygieneObservationSource,
	metrics HygieneMetrics,
	interval time.Duration,
	logger *zap.Logger,
) (*HygieneReconciler, error) {
	policy = policy.WithDefaults()
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("hygiene policy: %w", err)
	}
	if publisher == nil {
		return nil, fmt.Errorf("hygiene reconciler requires a maintenance publisher")
	}
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	targets := policy.Workers
	if len(targets) == 0 {
		targets = workers
	}
	return &HygieneReconciler{
		policy:    policy,
		workers:   targets,
		publisher: publisher,
		source:    source,
		metrics:   metrics,
		interval:  interval,
		now:       time.Now,
		logger:    logger,
	}, nil
}

func (r *HygieneReconciler) Name() string { return "hygiene-reconciler" }

// Run implements app.BackgroundRunner.
func (r *HygieneReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if _, err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("hygiene reconcile pass failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ReconcileOnce performs one hygiene pass over all target workers.
func (r *HygieneReconciler) ReconcileOnce(ctx context.Context) (HygieneReconcileResult, error) {
	result := HygieneReconcileResult{Workers: len(r.workers)}
	if !r.policy.Enabled {
		return result, nil
	}
	var firstErr error
	for _, worker := range r.workers {
		worker = strings.TrimSpace(worker)
		if worker == "" {
			continue
		}
		if err := r.reconcileWorker(ctx, worker, &result); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return result, firstErr
}

func (r *HygieneReconciler) reconcileWorker(ctx context.Context, worker string, result *HygieneReconcileResult) error {
	passStartedAt := r.now().UTC()
	// Dry-run first, always: every pass starts with a scan + pressure
	// request so decisions are made on fresh, non-mutating observations.
	if _, err := r.publisher.PublishScan(ctx, controlplane.MaintenanceCommand{WorkerPubKey: worker, Reason: "hygiene reconcile: periodic dry-run scan"}); err != nil {
		r.recordAction("maintenance/scan", "failed")
		return fmt.Errorf("issue scan for %s: %w", worker, err)
	}
	result.ScansIssued++
	if r.metrics != nil {
		r.metrics.RecordHygieneScan()
	}
	if _, err := r.publisher.PublishPressure(ctx, controlplane.MaintenanceCommand{WorkerPubKey: worker, Reason: "hygiene reconcile: pressure sample"}); err != nil {
		r.recordAction("maintenance/pressure", "failed")
	}

	if r.source == nil {
		return nil
	}
	obs, err := r.source.Latest(ctx, worker)
	if err != nil {
		return fmt.Errorf("load hygiene observation for %s: %w", worker, err)
	}
	if obs == nil {
		return nil
	}
	// Anchor both independent freshness checks before the scan round-trip so
	// worker latency cannot disqualify the previous pass's responses while this
	// pass is publishing. Keep the one-interval policy established by fp-9pjq.
	freshnessCutoff := passStartedAt.Add(-r.interval)
	scanObservedAt := obs.ScanObservedAt
	if scanObservedAt.IsZero() {
		scanObservedAt = obs.ObservedAt
	}
	if scanObservedAt.IsZero() || !scanObservedAt.Before(freshnessCutoff) {
		if obs.ScanTruncated {
			r.logger.Warn("hygiene scan result truncated; candidate convergence suppressed", zap.String("worker", worker), zap.Int("total_candidates", obs.TotalCandidates))
		} else {
			r.convergeCandidates(ctx, worker, obs.Candidates, result)
		}
	} else {
		r.logger.Debug("hygiene scan observation stale; candidate convergence suppressed", zap.String("worker", worker), zap.Time("observed_at", scanObservedAt), zap.Time("freshness_cutoff", freshnessCutoff))
	}
	pressureObservedAt := obs.PressureObservedAt
	if pressureObservedAt.IsZero() {
		pressureObservedAt = obs.ObservedAt
	}
	if pressureObservedAt.IsZero() || !pressureObservedAt.Before(freshnessCutoff) {
		r.convergePressure(ctx, worker, obs.Pressure, result)
	} else {
		r.logger.Debug("hygiene pressure observation stale; gc convergence suppressed", zap.String("worker", worker), zap.Time("observed_at", pressureObservedAt), zap.Time("freshness_cutoff", freshnessCutoff))
	}
	return nil
}

func (r *HygieneReconciler) convergeCandidates(ctx context.Context, worker string, candidates []HygieneCandidate, result *HygieneReconcileResult) {
	var quarantine []string
	var relocate []string
	byClass := map[string]int{}
	for _, c := range candidates {
		byClass[c.Class]++
		switch {
		case c.Blocked != "":
			// Blocked candidates (dirty trees, orphans) are report-only.
		case c.Class == domain.HygieneClassDupClone && c.Canonical != "":
			// Tier 1: quarantine of a confirmed duplicate is reversible.
			quarantine = append(quarantine, c.Path)
		case c.Class == domain.HygieneClassCruft:
			quarantine = append(quarantine, c.Path)
		case c.Class == domain.HygieneClassMisplacedBackup:
			// Tier 2: relocation touches someone's backup — never automatic.
			relocate = append(relocate, c.Path)
		}
	}
	if r.metrics != nil {
		for class, count := range byClass {
			r.metrics.RecordHygieneCandidates(class, count)
		}
	}
	if len(quarantine) > 0 {
		if r.policy.AutoQuarantine {
			action := HygieneAction{WorkerPubKey: worker, Method: controlplane.ContextVMMethodMaintenanceQuarantine, Paths: quarantine, Tier: 1}
			if _, err := r.publisher.PublishQuarantine(ctx, controlplane.MaintenanceCommand{WorkerPubKey: worker, Paths: quarantine, Reason: "hygiene reconcile: Tier-1 quarantine of confirmed cruft/dup candidates"}); err != nil {
				action.Deferred = "publish failed: " + err.Error()
				r.recordAction(controlplane.ContextVMMethodMaintenanceQuarantine, "failed")
			} else {
				r.recordAction(controlplane.ContextVMMethodMaintenanceQuarantine, "issued")
			}
			result.Actions = append(result.Actions, action)
		} else {
			result.PendingTier2 = append(result.PendingTier2, HygieneAction{WorkerPubKey: worker, Method: controlplane.ContextVMMethodMaintenanceQuarantine, Paths: quarantine, Tier: 1, Deferred: "auto_quarantine disabled by policy"})
		}
	}
	if len(relocate) > 0 {
		// Tier-2: surfaced for Majordomo approval, never auto-issued.
		result.PendingTier2 = append(result.PendingTier2, HygieneAction{WorkerPubKey: worker, Method: controlplane.ContextVMMethodMaintenanceRelocate, Paths: relocate, Tier: 2, Deferred: "tier-2: requires Majordomo approval"})
		r.recordAction(controlplane.ContextVMMethodMaintenanceRelocate, "pending")
	}
}

func (r *HygieneReconciler) convergePressure(ctx context.Context, worker string, mounts []HygieneMountPressure, result *HygieneReconcileResult) {
	breached := false
	for _, m := range mounts {
		inodeUsedPct := 0.0
		if m.TotalInodes > 0 {
			inodeUsedPct = 100 * float64(m.TotalInodes-m.FreeInodes) / float64(m.TotalInodes)
		}
		if m.UsedPct >= r.policy.Thresholds.DiskUsedPct || inodeUsedPct >= r.policy.Thresholds.InodeUsedPct {
			breached = true
			result.PressureAlerts = append(result.PressureAlerts, fmt.Sprintf("%s %s disk=%.1f%% inode=%.1f%%", worker, m.Path, m.UsedPct, inodeUsedPct))
		}
	}
	if !breached {
		return
	}
	if r.metrics != nil {
		r.metrics.RecordHygienePressureBreach()
	}
	if !r.policy.AutoGC {
		result.PendingTier2 = append(result.PendingTier2, HygieneAction{WorkerPubKey: worker, Method: controlplane.ContextVMMethodMaintenanceGC, Tier: 1, Deferred: "auto_gc disabled by policy"})
		return
	}
	// Tier 1: doctrine already permits pruning docker/build caches on the
	// worker's own node.
	action := HygieneAction{WorkerPubKey: worker, Method: controlplane.ContextVMMethodMaintenanceGC, Tier: 1}
	if _, err := r.publisher.PublishGC(ctx, controlplane.MaintenanceCommand{WorkerPubKey: worker, Reason: "hygiene reconcile: pressure threshold breached, Tier-1 gc"}); err != nil {
		action.Deferred = "publish failed: " + err.Error()
		r.recordAction(controlplane.ContextVMMethodMaintenanceGC, "failed")
	} else {
		r.recordAction(controlplane.ContextVMMethodMaintenanceGC, "issued")
	}
	result.Actions = append(result.Actions, action)
}

func (r *HygieneReconciler) recordAction(method, status string) {
	if r.metrics != nil {
		r.metrics.RecordHygieneAction(method, status)
	}
}
