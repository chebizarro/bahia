package app

import (
	"context"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// BackgroundRunner is a long-lived goroutine managed by the application.
// Implementations must respect the context for shutdown.
type BackgroundRunner interface {
	// Name returns a human-readable identifier for logging.
	Name() string
	// Run starts the runner and blocks until ctx is cancelled or a fatal error occurs.
	Run(ctx context.Context) error
}

type RunnerStatus struct {
	Name      string
	Running   bool
	Required  bool
	StartedAt time.Time
	StoppedAt time.Time
	LastError error
}

// BackgroundManager tracks and coordinates background runners.
type OCIUploadCleaner interface {
	CleanupExpiredUploads(ctx context.Context, now time.Time) (int, error)
}

type OSVVulnerabilityCachePruner interface {
	PruneExpiredOSVVulnerabilityCache(ctx context.Context, now time.Time) (int64, error)
}

type OCIUploadCleanupRunner struct {
	cleaner  OCIUploadCleaner
	interval time.Duration
	logger   *zap.Logger
}

func NewOCIUploadCleanupRunner(cleaner OCIUploadCleaner, interval time.Duration, logger *zap.Logger) *OCIUploadCleanupRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &OCIUploadCleanupRunner{cleaner: cleaner, interval: interval, logger: logger}
}

func (r *OCIUploadCleanupRunner) Name() string { return "oci-upload-cleanup" }

func (r *OCIUploadCleanupRunner) Run(ctx context.Context) error {
	if r.cleaner == nil {
		return nil
	}
	//nostr:allow-poll housekeeping: expires abandoned OCI upload sessions; no event signals their expiry
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			count, err := r.cleaner.CleanupExpiredUploads(ctx, time.Now())
			if err != nil {
				r.logger.Warn("oci upload cleanup failed", zap.Error(err))
				continue
			}
			if count > 0 {
				r.logger.Info("cleaned expired oci uploads", zap.Int("count", count))
			}
		}
	}
}

const defaultOSVVulnerabilityCacheCleanupInterval = time.Hour

type OSVVulnerabilityCacheCleanupRunner struct {
	pruner   OSVVulnerabilityCachePruner
	interval time.Duration
	logger   *zap.Logger
}

func NewOSVVulnerabilityCacheCleanupRunner(pruner OSVVulnerabilityCachePruner, interval time.Duration, logger *zap.Logger) *OSVVulnerabilityCacheCleanupRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultOSVVulnerabilityCacheCleanupInterval
	}
	return &OSVVulnerabilityCacheCleanupRunner{pruner: pruner, interval: interval, logger: logger}
}

func (r *OSVVulnerabilityCacheCleanupRunner) Name() string {
	return "osv-vulnerability-cache-cleanup"
}

func (r *OSVVulnerabilityCacheCleanupRunner) Run(ctx context.Context) error {
	if r.pruner == nil {
		return nil
	}
	//nostr:allow-poll housekeeping: prunes the expired OSV vulnerability cache; no event signals expiry
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			count, err := r.pruner.PruneExpiredOSVVulnerabilityCache(ctx, time.Now())
			if err != nil {
				r.logger.Warn("osv vulnerability cache cleanup failed", zap.Error(err))
				continue
			}
			if count > 0 {
				r.logger.Info("pruned expired osv vulnerability cache", zap.Int64("count", count))
			}
		}
	}
}

const (
	defaultContextVMResponseRetention = 24 * time.Hour
	defaultRetentionInterval          = time.Hour
)

type ContextVMResponsePruner interface {
	DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// RetentionTask is one bounded-retention pass the RetentionRunner drives:
// Retire removes what has outlived its retention as of now and reports how
// much it removed. Tasks are idempotent and tolerate any cadence.
type RetentionTask interface {
	Name() string
	Retire(ctx context.Context, now time.Time) (int64, error)
}

// ContextVMResponseRetention retires ContextVM responses older than
// Retention.
type ContextVMResponseRetention struct {
	Pruner    ContextVMResponsePruner
	Retention time.Duration
}

func (ContextVMResponseRetention) Name() string { return "contextvm-responses" }

func (t ContextVMResponseRetention) Retire(ctx context.Context, now time.Time) (int64, error) {
	retention := t.Retention
	if retention <= 0 {
		retention = defaultContextVMResponseRetention
	}
	return t.Pruner.DeleteCreatedBefore(ctx, now.Add(-retention))
}

// RetentionRunner is the daemon's one housekeeping wakeup for records whose
// expiry no event signals: on each interval it runs every RetentionTask in
// turn. New retention work joins it as a task instead of adding a ticker.
type RetentionRunner struct {
	tasks    []RetentionTask
	interval time.Duration
	logger   *zap.Logger
}

func NewRetentionRunner(interval time.Duration, logger *zap.Logger, tasks ...RetentionTask) *RetentionRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultRetentionInterval
	}
	return &RetentionRunner{tasks: append([]RetentionTask(nil), tasks...), interval: interval, logger: logger}
}

func (r *RetentionRunner) Name() string { return "retention" }

func (r *RetentionRunner) Run(ctx context.Context) error {
	if len(r.tasks) == 0 {
		return nil
	}
	//nostr:allow-poll housekeeping: retires records past their retention (ContextVM responses, OpenClaw saga runs); no event signals their expiry
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.retire(ctx, time.Now().UTC())
		}
	}
}

// retire runs every task once. A failing task is logged and does not hold
// back the others; it is retried on the next wakeup.
func (r *RetentionRunner) retire(ctx context.Context, now time.Time) {
	for _, task := range r.tasks {
		if ctx.Err() != nil {
			return
		}
		count, err := task.Retire(ctx, now)
		if err != nil {
			r.logger.Warn("retention pass failed", zap.String("task", task.Name()), zap.Int64("retired", count), zap.Error(err))
			continue
		}
		if count > 0 {
			r.logger.Info("retention pass retired records", zap.String("task", task.Name()), zap.Int64("retired", count))
		}
	}
}

type backgroundRunnerRegistration struct {
	runner   BackgroundRunner
	required bool
}

// RunnerOption configures background runner health metadata.
type RunnerOption func(*RunnerStatus)

// RunnerRequired configures whether a runner is required for readiness.
func RunnerRequired(required bool) RunnerOption {
	return func(status *RunnerStatus) {
		status.Required = required
	}
}

// BackgroundManager tracks and coordinates background runners.
type BackgroundManager struct {
	mu       sync.Mutex
	runners  []backgroundRunnerRegistration
	statuses map[string]RunnerStatus
	wg       sync.WaitGroup
	logger   *zap.Logger
}

// NewBackgroundManager creates a new manager.
func NewBackgroundManager(logger *zap.Logger) *BackgroundManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackgroundManager{logger: logger, statuses: make(map[string]RunnerStatus)}
}

// Register adds a runner. Must be called before Start.
func (m *BackgroundManager) Register(r BackgroundRunner) {
	m.RegisterWithOptions(r)
}

// RegisterWithOptions adds a runner with health metadata. Must be called before Start.
func (m *BackgroundManager) RegisterWithOptions(r BackgroundRunner, opts ...RunnerOption) {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := RunnerStatus{Name: r.Name(), Required: true}
	for _, opt := range opts {
		opt(&status)
	}

	m.runners = append(m.runners, backgroundRunnerRegistration{runner: r, required: status.Required})
	m.statuses[status.Name] = status
	m.logger.Info("background runner registered", zap.String("name", r.Name()))
}

// Start launches all registered runners in separate goroutines.
// Each runner receives the given context; when it is cancelled the runners
// should shut themselves down.
func (m *BackgroundManager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.runners {
		m.wg.Add(1)
		go func(reg backgroundRunnerRegistration) {
			runner := reg.runner
			defer m.wg.Done()
			m.logger.Info("background runner starting", zap.String("name", runner.Name()))
			m.markRunnerStarted(runner.Name())

			err := runner.Run(ctx)
			m.markRunnerStopped(runner.Name(), err, ctx.Err() != nil)
			if err != nil && ctx.Err() == nil {
				// Only log as error if the context wasn't cancelled.
				m.logger.Error("background runner exited with error",
					zap.String("name", runner.Name()),
					zap.Error(err),
				)
			} else {
				m.logger.Info("background runner stopped", zap.String("name", runner.Name()))
			}
		}(r)
	}
}

func (m *BackgroundManager) markRunnerStarted(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.statuses[name]
	status.Running = true
	status.StartedAt = time.Now()
	status.StoppedAt = time.Time{}
	status.LastError = nil
	m.statuses[name] = status
}

func (m *BackgroundManager) markRunnerStopped(name string, err error, contextCancelled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.statuses[name]
	status.Running = false
	status.StoppedAt = time.Now()
	if err != nil && !contextCancelled {
		status.LastError = err
	}
	m.statuses[name] = status
}

// RunnerStatuses returns a snapshot of registered runner statuses.
func (m *BackgroundManager) RunnerStatuses() []RunnerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make([]RunnerStatus, 0, len(m.runners))
	for _, reg := range m.runners {
		statuses = append(statuses, m.statuses[reg.runner.Name()])
	}
	return statuses
}

// Wait blocks until all runners have finished.
func (m *BackgroundManager) Wait() {
	m.wg.Wait()
}

// Count returns the number of registered runners.
func (m *BackgroundManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runners)
}

// BackupScheduleProcessor processes due backup schedules for periodic dispatch.
type BackupScheduleProcessor interface {
	ProcessDueSchedules(ctx context.Context) (*service.BackupScheduleProcessResult, error)
	// NextDueTime returns the earliest next-due schedule time across all
	// definitions, or nil if no schedules are enabled.
	NextDueTime(ctx context.Context) (*time.Time, error)
}

const defaultBackupSchedulerInterval = 5 * time.Minute

type BackupSchedulerRunner struct {
	scheduler BackupScheduleProcessor
	interval  time.Duration
	triggerCh chan struct{}
	logger    *zap.Logger
}

func NewBackupSchedulerRunner(scheduler BackupScheduleProcessor, interval time.Duration, logger *zap.Logger) *BackupSchedulerRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultBackupSchedulerInterval
	}
	return &BackupSchedulerRunner{scheduler: scheduler, interval: interval, triggerCh: make(chan struct{}, 1), logger: logger}
}

func (r *BackupSchedulerRunner) Name() string { return "backup-scheduler" }

// Run performs schedule evaluation using a next-due timer: it fires at the
// earliest next-due schedule time, falling back to the configured interval as
// a ceiling; no polling ticker runs.
func (r *BackupSchedulerRunner) Run(ctx context.Context) error {
	if r.scheduler == nil {
		return nil
	}
	result, err := r.scheduler.ProcessDueSchedules(ctx)
	if err != nil {
		r.logger.Error("backup scheduler initial run failed", zap.Error(err))
	} else {
		r.logger.Info("backup scheduler initial run complete",
			zap.Int("checked", result.Checked),
			zap.Int("dispatched", result.Dispatched),
			zap.Int("skipped", result.Skipped),
			zap.Int("missed_runs", result.MissedRuns),
			zap.Int("errors", len(result.Errors)),
		)
	}
	timer := time.NewTimer(r.nextDueInterval(ctx))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.triggerCh:
			// Definition or schedule changed — recompute immediately.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(r.nextDueInterval(ctx))
		case <-timer.C:
			result, err := r.scheduler.ProcessDueSchedules(ctx)
			if err != nil {
				r.logger.Error("backup scheduler periodic run failed", zap.Error(err))
			} else if result.Dispatched > 0 || len(result.Errors) > 0 {
				r.logger.Info("backup scheduler periodic run complete",
					zap.Int("checked", result.Checked),
					zap.Int("dispatched", result.Dispatched),
					zap.Int("skipped", result.Skipped),
					zap.Int("missed_runs", result.MissedRuns),
					zap.Int("errors", len(result.Errors)),
				)
			}
			timer.Reset(r.nextDueInterval(ctx))
		}
	}
}

// nextDueInterval computes how long to wait before the next schedule evaluation.
// Uses the earliest next-due time from the scheduler; falls back to the
// configured interval when no schedules are due.
// Trigger wakes the scheduler runner to recompute the next-due timer. Called
// when a backup definition or schedule changes so the runner picks up the
// new schedule immediately.
func (r *BackupSchedulerRunner) Trigger() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

func (r *BackupSchedulerRunner) nextDueInterval(ctx context.Context) time.Duration {
	next, err := r.scheduler.NextDueTime(ctx)
	if err != nil || next == nil {
		return r.interval
	}
	d := time.Until(*next)
	if d <= 0 {
		return time.Second // already due, process immediately
	}
	if d > r.interval {
		return r.interval // ceiling
	}
	return d
}
