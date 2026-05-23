package app

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
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
	Tier      int
	StartedAt time.Time
	StoppedAt time.Time
	LastError error
}

// BackgroundManager tracks and coordinates background runners.
type OCIUploadCleaner interface {
	CleanupExpiredUploads(ctx context.Context, now time.Time) (int, error)
}

type HiveCIRetryRepository interface {
	ListPendingResults(ctx context.Context) ([]domain.HiveCIWorkflowResult, error)
	IncrementResultRetry(ctx context.Context, eventID string, at time.Time) (int, error)
	MarkResultFailed(ctx context.Context, eventID, reason string) error
}

type HiveCIBridgeProcessor interface {
	ProcessResult(ctx context.Context, resultEventID string) error
}

type HiveCIRetryRunner struct {
	repo       HiveCIRetryRepository
	processor  HiveCIBridgeProcessor
	interval   time.Duration
	maxRetries int
	logger     *zap.Logger
}

func NewHiveCIRetryRunner(repo HiveCIRetryRepository, processor HiveCIBridgeProcessor, interval time.Duration, maxRetries int, logger *zap.Logger) *HiveCIRetryRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxRetries <= 0 {
		maxRetries = 10
	}
	return &HiveCIRetryRunner{repo: repo, processor: processor, interval: interval, maxRetries: maxRetries, logger: logger}
}

func (r *HiveCIRetryRunner) Name() string { return "hiveci-retry" }

func (r *HiveCIRetryRunner) Run(ctx context.Context) error {
	if r.repo == nil || r.processor == nil {
		return nil
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *HiveCIRetryRunner) runOnce(ctx context.Context) {
	pending, err := r.repo.ListPendingResults(ctx)
	if err != nil {
		r.logger.Warn("hiveci retry list pending failed", zap.Error(err))
		return
	}
	now := time.Now()
	for _, result := range pending {
		if result.RetryCount >= r.maxRetries {
			_ = r.repo.MarkResultFailed(ctx, result.ResultEventID, "max retries exceeded")
			continue
		}
		if !r.shouldRetryNow(result, now) {
			continue
		}

		attempt, err := r.repo.IncrementResultRetry(ctx, result.ResultEventID, now)
		if err != nil {
			r.logger.Warn("hiveci retry increment failed", zap.String("result_event_id", result.ResultEventID), zap.Error(err))
			continue
		}
		if attempt > r.maxRetries {
			_ = r.repo.MarkResultFailed(ctx, result.ResultEventID, "max retries exceeded")
			continue
		}

		if err := r.processor.ProcessResult(ctx, result.ResultEventID); err != nil {
			r.logger.Warn("hiveci retry process failed", zap.String("result_event_id", result.ResultEventID), zap.Int("retry_count", attempt), zap.Error(err))
			if attempt >= r.maxRetries {
				_ = r.repo.MarkResultFailed(ctx, result.ResultEventID, "max retries exceeded")
			}
		}
	}
}

func (r *HiveCIRetryRunner) shouldRetryNow(result domain.HiveCIWorkflowResult, now time.Time) bool {
	if result.LastRetryAt == nil {
		return true
	}
	backoff := float64(r.interval) * math.Pow(2, float64(result.RetryCount))
	next := result.LastRetryAt.Add(time.Duration(backoff))
	return !now.Before(next)
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

type backgroundRunnerRegistration struct {
	runner   BackgroundRunner
	required bool
	tier     int
}

// RunnerOption configures background runner health metadata.
type RunnerOption func(*RunnerStatus)

// RunnerRequired configures whether a runner is required for readiness.
func RunnerRequired(required bool) RunnerOption {
	return func(status *RunnerStatus) {
		status.Required = required
	}
}

// RunnerTier configures the subsystem tier a runner belongs to.
func RunnerTier(t Tier) RunnerOption {
	return func(status *RunnerStatus) {
		status.Tier = int(t)
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

	status := RunnerStatus{Name: r.Name(), Required: true, Tier: int(Tier0)}
	for _, opt := range opts {
		opt(&status)
	}

	m.runners = append(m.runners, backgroundRunnerRegistration{runner: r, required: status.Required, tier: status.Tier})
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
