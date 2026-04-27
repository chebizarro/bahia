package app

import (
	"context"
	"sync"

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
