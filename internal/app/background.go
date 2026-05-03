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

// BackgroundManager tracks and coordinates background runners.
type BackgroundManager struct {
	mu      sync.Mutex
	runners []BackgroundRunner
	wg      sync.WaitGroup
	logger  *zap.Logger
}

// NewBackgroundManager creates a new manager.
func NewBackgroundManager(logger *zap.Logger) *BackgroundManager {
	return &BackgroundManager{logger: logger}
}

// Register adds a runner. Must be called before Start.
func (m *BackgroundManager) Register(r BackgroundRunner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runners = append(m.runners, r)
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
		go func(runner BackgroundRunner) {
			defer m.wg.Done()
			m.logger.Info("background runner starting", zap.String("name", runner.Name()))

			if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
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

// runToolProvisioningWorker polls and processes pending tool provisioning intents.
func (a *App) runToolProvisioningWorker(ctx context.Context) {
	if a == nil || a.toolCoordinator == nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.toolCoordinator.ProcessPendingIntents(ctx); err != nil {
				a.Logger.Error("tool provisioning worker error", zap.Error(err))
			}
		}
	}
}
