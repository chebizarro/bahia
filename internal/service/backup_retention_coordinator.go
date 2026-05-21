package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const defaultBackupRetentionRecoveryPollInterval = 30 * time.Second

// BackupRetentionResponder publishes retention progress and terminal outcomes.
type BackupRetentionResponder interface {
	PublishBackupRetentionStatus(ctx context.Context, run *domain.BackupRetentionRun, step, message string) error
	PublishBackupRetentionResult(ctx context.Context, run *domain.BackupRetentionRun, message string) error
}

// BackupRetentionCoordinatorConfig controls durable retention queue recovery.
type BackupRetentionCoordinatorConfig struct {
	RecoveryPollInterval time.Duration
	StaleRunTimeout      time.Duration
	HealthCheckBeforeRun bool
}

// BackupRetentionCoordinator executes durable backend-native retention enforcement runs.
type BackupRetentionCoordinator struct {
	registry        *BackupRegistryService
	queue           BackupRetentionQueueRepository
	backendResolver BackupBackendResolver
	responder       BackupRetentionResponder
	logger          *zap.Logger
	config          BackupRetentionCoordinatorConfig

	runGroup singleflight.Group
	locksMu  sync.Mutex
	runLocks map[uuid.UUID]*sync.Mutex
}

// BackupRetentionQueueRepository provides durable queue and lease-recovery primitives for retention runs.
type BackupRetentionQueueRepository interface {
	ClaimNextQueuedBackupRetentionRun(ctx context.Context) (*domain.BackupRetentionRun, error)
	RequeueStaleBackupRetentionRuns(ctx context.Context, olderThan time.Duration) (int, error)
}

type BackupRetentionCoordinatorOption func(*BackupRetentionCoordinator)

func WithBackupRetentionResponder(responder BackupRetentionResponder) BackupRetentionCoordinatorOption {
	return func(c *BackupRetentionCoordinator) { c.responder = responder }
}

func WithBackupRetentionCoordinatorConfig(cfg BackupRetentionCoordinatorConfig) BackupRetentionCoordinatorOption {
	return func(c *BackupRetentionCoordinator) {
		if cfg.RecoveryPollInterval > 0 {
			c.config.RecoveryPollInterval = cfg.RecoveryPollInterval
		}
		if cfg.StaleRunTimeout > 0 {
			c.config.StaleRunTimeout = cfg.StaleRunTimeout
		}
		if cfg.HealthCheckBeforeRun {
			c.config.HealthCheckBeforeRun = true
		}
	}
}

func WithBackupRetentionHealthCheck(enabled bool) BackupRetentionCoordinatorOption {
	return func(c *BackupRetentionCoordinator) { c.config.HealthCheckBeforeRun = enabled }
}

func NewBackupRetentionCoordinator(registry *BackupRegistryService, backendResolver BackupBackendResolver, logger *zap.Logger, opts ...BackupRetentionCoordinatorOption) *BackupRetentionCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &BackupRetentionCoordinator{
		registry:        registry,
		backendResolver: backendResolver,
		logger:          logger.Named("backup-retention-coordinator"),
		config: BackupRetentionCoordinatorConfig{
			RecoveryPollInterval: defaultBackupRetentionRecoveryPollInterval,
			StaleRunTimeout:      15 * time.Minute,
			HealthCheckBeforeRun: true,
		},
		runLocks: make(map[uuid.UUID]*sync.Mutex),
	}
	if registry != nil && registry.repo != nil {
		if queue, ok := registry.repo.(BackupRetentionQueueRepository); ok {
			c.queue = queue
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *BackupRetentionCoordinator) Name() string { return "backup-retention-recovery" }

// Run performs durable retention worker recovery. It does not poll for Nostr messages.
func (c *BackupRetentionCoordinator) Run(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	c.runRecoveryOnce(ctx)
	ticker := time.NewTicker(c.config.RecoveryPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.runRecoveryOnce(ctx)
		}
	}
}

func (c *BackupRetentionCoordinator) runRecoveryOnce(ctx context.Context) {
	if err := c.ProcessOnce(ctx); err != nil {
		c.logger.Warn("backup retention recovery scan failed", zap.Error(err))
	}
}

// ProcessOnce performs one stale-lease recovery and queued retention claim/process cycle.
func (c *BackupRetentionCoordinator) ProcessOnce(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	if c.config.StaleRunTimeout > 0 {
		if n, err := c.queue.RequeueStaleBackupRetentionRuns(ctx, c.config.StaleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale backup retention runs", zap.Int("count", n))
		}
	}
	run, err := c.queue.ClaimNextQueuedBackupRetentionRun(ctx)
	if err != nil || run == nil {
		return err
	}
	return c.ProcessBackupRetentionRun(ctx, run.ID)
}

// ProcessRun is an alias for callers that use generic run terminology.
func (c *BackupRetentionCoordinator) ProcessRun(ctx context.Context, runID uuid.UUID) error {
	return c.ProcessBackupRetentionRun(ctx, runID)
}

// ProcessBackupRetentionRun executes one queued or running backend-native retention run.
func (c *BackupRetentionCoordinator) ProcessBackupRetentionRun(ctx context.Context, runID uuid.UUID) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("backup registry is not configured")
	}
	_, err, _ := c.runGroup.Do(runID.String(), func() (any, error) {
		return nil, c.withRunLock(runID, func() error { return c.processBackupRetentionRunLocked(ctx, runID) })
	})
	return err
}

func (c *BackupRetentionCoordinator) processBackupRetentionRunLocked(ctx context.Context, runID uuid.UUID) error {
	run, err := c.registry.GetBackupRetentionRun(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("backup retention run %s not found", runID)
	}
	if isTerminalRunStatus(run.Status) {
		return nil
	}
	repo, policy, contract, err := c.loadRunInputs(ctx, run)
	if err != nil {
		return c.completeFailed(ctx, run, "load_inputs", err)
	}
	backend, retentionBackend, err := c.resolveRetentionBackend(run.Backend)
	if err != nil {
		return c.completeFailed(ctx, run, "backend_resolve", err)
	}
	if err := c.markRunning(ctx, run, contract); err != nil {
		return err
	}
	if c.config.HealthCheckBeforeRun {
		if err := c.withRunHeartbeat(ctx, run.ID, func() error { return backend.Health(ctx, repo) }); err != nil {
			if contextCancellationErr(ctx, err) {
				return err
			}
			return c.completeFailed(ctx, run, "backend_health", err)
		}
	}
	c.publishStatus(ctx, run, "enforcing_retention", "enforcing backend-native backup retention")
	backupSetRetentionMetadata(run, map[string]any{"current_step": "enforcing_retention"})
	if err := c.registry.CreateOrUpdateBackupRetentionRun(ctx, run); err != nil {
		return err
	}
	var result *BackupRetentionResult
	if err := c.withRunHeartbeat(ctx, run.ID, func() error {
		var retentionErr error
		result, retentionErr = retentionBackend.EnforceRetention(ctx, BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})
		return retentionErr
	}); err != nil {
		if contextCancellationErr(ctx, err) {
			return err
		}
		return c.completeFailed(ctx, run, "enforcing_retention", err)
	}
	if result == nil {
		return c.completeFailed(ctx, run, "enforcing_retention", fmt.Errorf("%w: backup backend returned no retention result", ErrBackupBackendExecution))
	}
	if message := strings.TrimSpace(result.Error); message != "" {
		run.Evidence = cloneMap(result.Evidence)
		return c.completeFailed(ctx, run, "enforcing_retention", fmt.Errorf("%w: %s", ErrBackupBackendExecution, message))
	}
	completed, err := c.registry.CompleteBackupRetentionRun(ctx, run.ID, result.Evidence, nil)
	if err != nil {
		return err
	}
	c.publishResult(ctx, completed, "backup retention completed")
	return nil
}

func (c *BackupRetentionCoordinator) loadRunInputs(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRepository, *domain.BackupPolicy, BackupPolicyRuntimeContract, error) {
	repo, err := c.registry.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return nil, nil, BackupPolicyRuntimeContract{}, err
	}
	if repo == nil {
		return nil, nil, BackupPolicyRuntimeContract{}, fmt.Errorf("backup repository %s not found", run.RepositoryID)
	}
	if run.Backend != repo.Backend {
		return nil, nil, BackupPolicyRuntimeContract{}, fmt.Errorf("%w: backup retention run backend %q does not match repository backend %q", domain.ErrInvalidValue, run.Backend, repo.Backend)
	}
	if run.PolicyID == nil {
		return nil, nil, BackupPolicyRuntimeContract{}, fmt.Errorf("%w: backup retention policy_id must be set for backend-native retention", ErrBackupBackendConfiguration)
	}
	policy, err := c.registry.GetPolicy(ctx, *run.PolicyID)
	if err != nil {
		return nil, nil, BackupPolicyRuntimeContract{}, err
	}
	if policy == nil {
		return nil, nil, BackupPolicyRuntimeContract{}, fmt.Errorf("backup policy %s not found", *run.PolicyID)
	}
	contract, err := ParseBackupPolicyRuntimeContract(policy)
	if err != nil {
		return nil, nil, BackupPolicyRuntimeContract{}, err
	}
	if contract.RetentionMode != BackupRetentionModeBackendNative {
		return nil, nil, BackupPolicyRuntimeContract{}, fmt.Errorf("%w: backup policy metadata.%s must be %q for retention enforcement", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionMode, BackupRetentionModeBackendNative)
	}
	return repo, policy, contract, nil
}

func (c *BackupRetentionCoordinator) resolveRetentionBackend(kind domain.BackupBackendKind) (BackupBackend, BackupRetentionBackend, error) {
	if c.backendResolver == nil {
		return nil, nil, fmt.Errorf("%w: backup backend resolver is not configured", ErrBackupBackendConfiguration)
	}
	backend, ok := c.backendResolver.Resolve(kind)
	if !ok || backend == nil {
		return nil, nil, fmt.Errorf("%w: backup backend %q is not registered", ErrBackupBackendUnsupported, kind)
	}
	retentionBackend, ok := backend.(BackupRetentionBackend)
	if !ok {
		return nil, nil, fmt.Errorf("%w: backup backend %q does not support retention enforcement", ErrBackupBackendUnsupported, kind)
	}
	return backend, retentionBackend, nil
}

func (c *BackupRetentionCoordinator) markRunning(ctx context.Context, run *domain.BackupRetentionRun, contract BackupPolicyRuntimeContract) error {
	if run.Status == domain.RunStatusRunning {
		return nil
	}
	now := time.Now().UTC()
	run.Status = domain.RunStatusRunning
	if run.StartedAt == nil {
		run.StartedAt = &now
	}
	run.FinishedAt = nil
	run.Error = ""
	backupSetRetentionMetadata(run, map[string]any{
		"current_step":              "running",
		"retention_mode":            string(contract.RetentionMode),
		"retention_selector":        contract.RetentionSelector,
		"retention_dry_run_default": contract.RetentionDryRunDefault,
	})
	return c.registry.CreateOrUpdateBackupRetentionRun(ctx, run)
}

func (c *BackupRetentionCoordinator) completeFailed(ctx context.Context, run *domain.BackupRetentionRun, step string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("backup retention failed")
	}
	backupSetRetentionMetadata(run, map[string]any{"failed_step": step})
	completed, err := c.registry.CompleteBackupRetentionRun(ctx, run.ID, run.Evidence, cause)
	if err != nil {
		return err
	}
	c.publishResult(ctx, completed, cause.Error())
	return cause
}

func (c *BackupRetentionCoordinator) withRunHeartbeat(ctx context.Context, runID uuid.UUID, fn func() error) error {
	if c == nil || c.registry == nil || c.config.StaleRunTimeout <= 0 {
		return fn()
	}
	interval := c.config.StaleRunTimeout / 3
	if interval <= 0 {
		interval = time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				c.touchBackupRetentionRun(heartbeatCtx, runID)
			}
		}
	}()
	err := fn()
	cancel()
	<-done
	return err
}

func (c *BackupRetentionCoordinator) touchBackupRetentionRun(ctx context.Context, runID uuid.UUID) {
	run, err := c.registry.GetBackupRetentionRun(ctx, runID)
	if err != nil || run == nil || run.Status != domain.RunStatusRunning {
		return
	}
	if err := c.registry.CreateOrUpdateBackupRetentionRun(ctx, run); err != nil {
		c.logger.Warn("failed to heartbeat backup retention run", zap.String("run_id", runID.String()), zap.Error(err))
	}
}

func (c *BackupRetentionCoordinator) withRunLock(runID uuid.UUID, fn func() error) error {
	c.locksMu.Lock()
	lock := c.runLocks[runID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.runLocks[runID] = lock
	}
	c.locksMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func (c *BackupRetentionCoordinator) publishStatus(ctx context.Context, run *domain.BackupRetentionRun, step, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRetentionStatus(ctx, run, step, message); err != nil {
		c.logger.Warn("publish backup retention status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *BackupRetentionCoordinator) publishResult(ctx context.Context, run *domain.BackupRetentionRun, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRetentionResult(ctx, run, message); err != nil {
		c.logger.Warn("publish backup retention result failed", zap.Error(err))
	}
}
