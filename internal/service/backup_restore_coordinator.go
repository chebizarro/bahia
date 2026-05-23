package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const defaultBackupRestoreRecoveryPollInterval = 30 * time.Second

// BackupRestoreResponder publishes Nostr-native restore status/result events.
type BackupRestoreResponder interface {
	PublishBackupRestoreStatus(ctx context.Context, restore *domain.BackupRestoreRun, step, message string) error
	PublishBackupRestoreResult(ctx context.Context, restore *domain.BackupRestoreRun, message string) error
}

// BackupRestoreQueueRepository provides durable queue and lease-recovery primitives for restore runs.
type BackupRestoreQueueRepository interface {
	ClaimNextQueuedBackupRestore(ctx context.Context) (*domain.BackupRestoreRun, error)
	RequeueStaleBackupRestores(ctx context.Context, olderThan time.Duration) (int, error)
}

// BackupRestoreCoordinatorConfig controls durable restore recovery.
type BackupRestoreCoordinatorConfig struct {
	RecoveryPollInterval time.Duration
	StaleRunTimeout      time.Duration
	HealthCheckBeforeRun bool
}

// BackupRestoreCoordinator executes approved restore runs from durable queued state.
type BackupRestoreCoordinator struct {
	registry        *BackupRegistryService
	queue           BackupRestoreQueueRepository
	backendResolver BackupBackendResolver
	responder       BackupRestoreResponder
	logger          *zap.Logger
	config          BackupRestoreCoordinatorConfig

	restoreGroup singleflight.Group
	locksMu      sync.Mutex
	restoreLocks map[uuid.UUID]*sync.Mutex
}

type BackupRestoreCoordinatorOption func(*BackupRestoreCoordinator)

func WithBackupRestoreResponder(responder BackupRestoreResponder) BackupRestoreCoordinatorOption {
	return func(c *BackupRestoreCoordinator) { c.responder = responder }
}

func WithBackupRestoreCoordinatorConfig(cfg BackupRestoreCoordinatorConfig) BackupRestoreCoordinatorOption {
	return func(c *BackupRestoreCoordinator) {
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

func WithBackupRestoreHealthCheck(enabled bool) BackupRestoreCoordinatorOption {
	return func(c *BackupRestoreCoordinator) { c.config.HealthCheckBeforeRun = enabled }
}

func NewBackupRestoreCoordinator(registry *BackupRegistryService, backendResolver BackupBackendResolver, logger *zap.Logger, opts ...BackupRestoreCoordinatorOption) *BackupRestoreCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &BackupRestoreCoordinator{
		registry:        registry,
		backendResolver: backendResolver,
		logger:          logger.Named("backup-restore-coordinator"),
		config: BackupRestoreCoordinatorConfig{
			RecoveryPollInterval: defaultBackupRestoreRecoveryPollInterval,
			StaleRunTimeout:      15 * time.Minute,
			HealthCheckBeforeRun: true,
		},
		restoreLocks: make(map[uuid.UUID]*sync.Mutex),
	}
	if registry != nil && registry.repo != nil {
		if queue, ok := registry.repo.(BackupRestoreQueueRepository); ok {
			c.queue = queue
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *BackupRestoreCoordinator) Name() string { return "backup-restore-recovery" }

// Run performs durable worker recovery for stored restore work. It does not poll for Nostr messages.
func (c *BackupRestoreCoordinator) Run(ctx context.Context) error {
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

func (c *BackupRestoreCoordinator) runRecoveryOnce(ctx context.Context) {
	if err := c.ProcessOnce(ctx); err != nil {
		c.logger.Warn("backup restore recovery scan failed", zap.Error(err))
	}
}

// ProcessOnce performs one stale-lease recovery and queued approved-restore claim/process cycle.
func (c *BackupRestoreCoordinator) ProcessOnce(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	if c.config.StaleRunTimeout > 0 {
		if n, err := c.queue.RequeueStaleBackupRestores(ctx, c.config.StaleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale backup restores", zap.Int("count", n))
		}
	}
	restore, err := c.queue.ClaimNextQueuedBackupRestore(ctx)
	if err != nil || restore == nil {
		return err
	}
	return c.ProcessBackupRestore(ctx, restore.ID)
}

func (c *BackupRestoreCoordinator) ProcessBackupRestore(ctx context.Context, restoreID uuid.UUID) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("backup registry is not configured")
	}
	_, err, _ := c.restoreGroup.Do(restoreID.String(), func() (any, error) {
		return nil, c.withRestoreLock(restoreID, func() error { return c.processBackupRestoreLocked(ctx, restoreID) })
	})
	return err
}

func (c *BackupRestoreCoordinator) processBackupRestoreLocked(ctx context.Context, restoreID uuid.UUID) error {
	restore, err := c.registry.GetBackupRestore(ctx, restoreID)
	if err != nil {
		return err
	}
	if restore == nil {
		return fmt.Errorf("backup restore %s not found", restoreID)
	}
	if isTerminalRunStatus(restore.Status) {
		return nil
	}
	if restore.ApprovalStatus != domain.BackupApprovalApproved && restore.ApprovalStatus != domain.BackupApprovalNotRequired {
		c.publishStatus(ctx, restore, "pending_approval", "backup restore is waiting for approval")
		return nil
	}
	sourceRun, recipe, repo, policy, err := c.loadRestoreInputs(ctx, restore)
	if err != nil {
		return c.completeFailed(ctx, restore, "load_inputs", err)
	}
	backend, restoreBackend, err := c.resolveRestoreBackend(restore.Backend)
	if err != nil {
		return c.completeFailed(ctx, restore, "backend_resolve", err)
	}
	if err := c.markRunning(ctx, restore); err != nil {
		return err
	}
	if c.config.HealthCheckBeforeRun {
		if err := c.withRestoreHeartbeat(ctx, restore.ID, func() error { return backend.Health(ctx, repo) }); err != nil {
			if contextCancellationErr(ctx, err) {
				return err
			}
			return c.completeFailed(ctx, restore, "backend_health", err)
		}
	}
	c.publishStatus(ctx, restore, "restoring", "restoring backup snapshot")
	restoreSetMetadata(restore, map[string]any{"current_step": "restoring"})
	if err := c.registry.CreateOrUpdateBackupRestore(ctx, restore); err != nil {
		return err
	}
	var result *BackupRestoreResult
	if err := c.withRestoreHeartbeat(ctx, restore.ID, func() error {
		var restoreErr error
		result, restoreErr = restoreBackend.Restore(ctx, BackupRestoreRequest{Run: restore, SourceRun: sourceRun, Recipe: recipe, Repository: repo, Policy: policy})
		return restoreErr
	}); err != nil {
		if contextCancellationErr(ctx, err) {
			return err
		}
		return c.completeFailed(ctx, restore, "restoring", err)
	}
	if result == nil {
		return c.completeFailed(ctx, restore, "restoring", fmt.Errorf("%w: backup backend returned no restore result", ErrBackupBackendExecution))
	}
	completed, err := c.registry.CompleteBackupRestore(ctx, restore.ID, result, nil)
	if err != nil {
		return err
	}
	if completed.Status == domain.RunStatusFailed {
		c.publishResult(ctx, completed, completed.Error)
		return fmt.Errorf("%w: %s", ErrBackupBackendExecution, completed.Error)
	}
	c.publishResult(ctx, completed, "backup restore completed")
	return nil
}

func (c *BackupRestoreCoordinator) loadRestoreInputs(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRun, *domain.BackupRecipe, *domain.BackupRepository, *domain.BackupPolicy, error) {
	sourceRun, err := c.registry.GetBackupRun(ctx, restore.BackupRunID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if sourceRun == nil {
		return nil, nil, nil, nil, fmt.Errorf("backup run %s not found", restore.BackupRunID)
	}
	if !domain.BackupRunRestoreEligible(sourceRun) {
		return nil, nil, nil, nil, fmt.Errorf("%w: backup run %s is not restore-eligible", domain.ErrInvalidValue, sourceRun.ID)
	}
	recipe, err := c.registry.GetRecipe(ctx, restore.RecipeID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if recipe == nil {
		return nil, nil, nil, nil, fmt.Errorf("backup recipe %s not found", restore.RecipeID)
	}
	repo, err := c.registry.GetRepository(ctx, restore.RepositoryID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if repo == nil {
		return nil, nil, nil, nil, fmt.Errorf("backup repository %s not found", restore.RepositoryID)
	}
	var policy *domain.BackupPolicy
	if restore.PolicyID != nil {
		policy, err = c.registry.GetPolicy(ctx, *restore.PolicyID)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if policy == nil {
			return nil, nil, nil, nil, fmt.Errorf("backup policy %s not found", *restore.PolicyID)
		}
	}
	return sourceRun, recipe, repo, policy, nil
}

func (c *BackupRestoreCoordinator) resolveRestoreBackend(kind domain.BackupBackendKind) (BackupBackend, BackupRestoreBackend, error) {
	if c.backendResolver == nil {
		return nil, nil, fmt.Errorf("%w: backup backend resolver is not configured", ErrBackupBackendConfiguration)
	}
	backend, ok := c.backendResolver.Resolve(kind)
	if !ok || backend == nil {
		return nil, nil, fmt.Errorf("%w: backup backend %q is not registered", ErrBackupBackendUnsupported, kind)
	}
	restoreBackend, ok := backend.(BackupRestoreBackend)
	if !ok {
		return nil, nil, fmt.Errorf("%w: backup backend %q does not support restore execution", ErrBackupBackendUnsupported, kind)
	}
	return backend, restoreBackend, nil
}

func (c *BackupRestoreCoordinator) markRunning(ctx context.Context, restore *domain.BackupRestoreRun) error {
	if restore.Status == domain.RunStatusRunning {
		return nil
	}
	now := time.Now().UTC()
	restore.Status = domain.RunStatusRunning
	if restore.StartedAt == nil {
		restore.StartedAt = &now
	}
	restore.FinishedAt = nil
	restore.Error = ""
	restoreSetMetadata(restore, map[string]any{"current_step": "running"})
	return c.registry.CreateOrUpdateBackupRestore(ctx, restore)
}

func (c *BackupRestoreCoordinator) completeFailed(ctx context.Context, restore *domain.BackupRestoreRun, step string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("backup restore failed")
	}
	restoreSetMetadata(restore, map[string]any{"failed_step": step})
	restore.FailureCategory = backupFailureCategoryForStep(step, cause)
	if err := c.registry.CreateOrUpdateBackupRestore(ctx, restore); err != nil {
		return err
	}
	completed, err := c.registry.CompleteBackupRestore(ctx, restore.ID, nil, cause)
	if err != nil {
		return err
	}
	c.publishResult(ctx, completed, cause.Error())
	return cause
}

func (c *BackupRestoreCoordinator) withRestoreHeartbeat(ctx context.Context, restoreID uuid.UUID, fn func() error) error {
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
				c.touchBackupRestore(heartbeatCtx, restoreID)
			}
		}
	}()
	err := fn()
	cancel()
	<-done
	return err
}

func (c *BackupRestoreCoordinator) touchBackupRestore(ctx context.Context, restoreID uuid.UUID) {
	restore, err := c.registry.GetBackupRestore(ctx, restoreID)
	if err != nil || restore == nil || restore.Status != domain.RunStatusRunning {
		return
	}
	if err := c.registry.CreateOrUpdateBackupRestore(ctx, restore); err != nil {
		c.logger.Warn("failed to heartbeat backup restore", zap.String("restore_id", restoreID.String()), zap.Error(err))
	}
}

func (c *BackupRestoreCoordinator) withRestoreLock(restoreID uuid.UUID, fn func() error) error {
	c.locksMu.Lock()
	lock := c.restoreLocks[restoreID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.restoreLocks[restoreID] = lock
	}
	c.locksMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func (c *BackupRestoreCoordinator) publishStatus(ctx context.Context, restore *domain.BackupRestoreRun, step, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRestoreStatus(ctx, restore, step, message); err != nil {
		c.logger.Warn("publish backup restore status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *BackupRestoreCoordinator) publishResult(ctx context.Context, restore *domain.BackupRestoreRun, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRestoreResult(ctx, restore, message); err != nil {
		c.logger.Warn("publish backup restore result failed", zap.Error(err))
	}
}

func restoreSetMetadata(restore *domain.BackupRestoreRun, values map[string]any) {
	if restore.Metadata == nil {
		restore.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			restore.Metadata[k] = v
		}
	}
}
