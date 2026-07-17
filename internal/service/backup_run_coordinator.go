package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const defaultBackupRunRecoveryPollInterval = 30 * time.Second

var (
	ErrBackupBackendConfiguration = errors.New("backup backend configuration error")
	ErrBackupBackendUnsupported   = errors.New("backup backend unsupported operation")
	ErrBackupBackendExecution     = errors.New("backup backend execution error")
)

// BackupSnapshotRequest is the coordinator-to-backend contract for creating one snapshot.
type BackupSnapshotRequest struct {
	Run        *domain.BackupRun        `json:"-"`
	Recipe     *domain.BackupRecipe     `json:"-"`
	Repository *domain.BackupRepository `json:"-"`
	Policy     *domain.BackupPolicy     `json:"-"`
}

// BackupSnapshotResult records backend-provided snapshot evidence.
type BackupSnapshotResult struct {
	SnapshotID string         `json:"snapshot_id"`
	Evidence   map[string]any `json:"evidence,omitempty"`
}

// BackupVerifyRequest is the coordinator-to-backend contract for verifying one snapshot.
type BackupVerifyRequest struct {
	Run                *domain.BackupRun             `json:"-"`
	Recipe             *domain.BackupRecipe          `json:"-"`
	Repository         *domain.BackupRepository      `json:"-"`
	Policy             *domain.BackupPolicy          `json:"-"`
	SnapshotID         string                        `json:"snapshot_id"`
	Mode               domain.BackupVerificationMode `json:"mode"`
	VerifyFilesPercent float64                       `json:"verify_files_percent"`
}

// BackupVerifyResult records backend-provided verification evidence.
type BackupVerifyResult struct {
	Verified bool                            `json:"verified"`
	Status   domain.BackupVerificationStatus `json:"status"`
	Evidence map[string]any                  `json:"evidence,omitempty"`
	Error    string                          `json:"error,omitempty"`
}

// BackupRunResponder publishes Nostr-native backup status/result events.
type BackupRunResponder interface {
	PublishBackupRunStatus(ctx context.Context, run *domain.BackupRun, step, message string) error
	PublishBackupRunResult(ctx context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord, message string) error
}

// BackupRunCoordinatorConfig controls durable backup queue recovery.
type BackupRunCoordinatorConfig struct {
	RecoveryPollInterval time.Duration
	StaleRunTimeout      time.Duration
	VerifyFilesPercent   float64
	HealthCheckBeforeRun bool
}

// BackupRunCoordinator executes fixed-step backup runs from durable queued state.
type BackupRunCoordinator struct {
	registry        *BackupRegistryService
	queue           BackupRunQueueRepository
	backendResolver BackupBackendResolver
	responder       BackupRunResponder
	logger          *zap.Logger
	config          BackupRunCoordinatorConfig

	runGroup singleflight.Group
	locksMu  sync.Mutex
	runLocks map[uuid.UUID]*sync.Mutex
}

// BackupRunQueueRepository provides durable queue and lease-recovery primitives for backup runs.
type BackupRunQueueRepository interface {
	ClaimNextQueuedBackupRun(ctx context.Context) (*domain.BackupRun, error)
	RequeueStaleBackupRuns(ctx context.Context, olderThan time.Duration) (int, error)
}

type BackupRunCoordinatorOption func(*BackupRunCoordinator)

func WithBackupRunResponder(responder BackupRunResponder) BackupRunCoordinatorOption {
	return func(c *BackupRunCoordinator) { c.responder = responder }
}

func WithBackupRunCoordinatorConfig(cfg BackupRunCoordinatorConfig) BackupRunCoordinatorOption {
	return func(c *BackupRunCoordinator) {
		if cfg.RecoveryPollInterval > 0 {
			c.config.RecoveryPollInterval = cfg.RecoveryPollInterval
		}
		if cfg.StaleRunTimeout > 0 {
			c.config.StaleRunTimeout = cfg.StaleRunTimeout
		}
		if cfg.VerifyFilesPercent > 0 {
			c.config.VerifyFilesPercent = cfg.VerifyFilesPercent
		}
		if cfg.HealthCheckBeforeRun {
			c.config.HealthCheckBeforeRun = true
		}
	}
}

func WithBackupRunHealthCheck(enabled bool) BackupRunCoordinatorOption {
	return func(c *BackupRunCoordinator) { c.config.HealthCheckBeforeRun = enabled }
}

func NewBackupRunCoordinator(registry *BackupRegistryService, backendResolver BackupBackendResolver, logger *zap.Logger, opts ...BackupRunCoordinatorOption) *BackupRunCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &BackupRunCoordinator{
		registry:        registry,
		backendResolver: backendResolver,
		logger:          logger.Named("backup-run-coordinator"),
		config: BackupRunCoordinatorConfig{
			RecoveryPollInterval: defaultBackupRunRecoveryPollInterval,
			StaleRunTimeout:      15 * time.Minute,
			VerifyFilesPercent:   100,
			HealthCheckBeforeRun: true,
		},
		runLocks: make(map[uuid.UUID]*sync.Mutex),
	}
	if registry != nil && registry.repo != nil {
		if queue, ok := registry.repo.(BackupRunQueueRepository); ok {
			c.queue = queue
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *BackupRunCoordinator) validateDependencies() error {
	if c == nil {
		return fmt.Errorf("BackupRunCoordinator is nil")
	}
	if c.registry == nil {
		return fmt.Errorf("BackupRunCoordinator registry is not configured")
	}
	if c.queue == nil {
		return fmt.Errorf("BackupRunCoordinator queue is not configured")
	}
	if c.backendResolver == nil {
		return fmt.Errorf("BackupRunCoordinator backend resolver is not configured")
	}
	return nil
}

func (c *BackupRunCoordinator) Name() string { return "backup-run-recovery" }

// Run performs durable worker recovery for stored backup work. It does not poll for Nostr messages.
func (c *BackupRunCoordinator) Run(ctx context.Context) error {
	if err := c.validateDependencies(); err != nil {
		return err
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

func (c *BackupRunCoordinator) runRecoveryOnce(ctx context.Context) {
	if err := c.ProcessOnce(ctx); err != nil {
		c.logger.Warn("backup run recovery scan failed", zap.Error(err))
	}
}

// ProcessOnce performs one stale-lease recovery and queued-run claim/process cycle.
func (c *BackupRunCoordinator) ProcessOnce(ctx context.Context) error {
	if err := c.validateDependencies(); err != nil {
		return err
	}
	if c.config.StaleRunTimeout > 0 {
		if n, err := c.queue.RequeueStaleBackupRuns(ctx, c.config.StaleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale backup runs", zap.Int("count", n))
		}
	}
	run, err := c.queue.ClaimNextQueuedBackupRun(ctx)
	if err != nil || run == nil {
		return err
	}
	return c.ProcessBackupRun(ctx, run.ID)
}

// ProcessRun is an alias for callers that use generic run terminology.
func (c *BackupRunCoordinator) ProcessRun(ctx context.Context, runID uuid.UUID) error {
	return c.ProcessBackupRun(ctx, runID)
}

// ProcessBackupRun executes a queued or running backup run using fixed durable steps.
func (c *BackupRunCoordinator) ProcessBackupRun(ctx context.Context, runID uuid.UUID) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("backup registry is not configured")
	}
	_, err, _ := c.runGroup.Do(runID.String(), func() (any, error) {
		return nil, c.withRunLock(runID, func() error { return c.processBackupRunLocked(ctx, runID) })
	})
	return err
}

func (c *BackupRunCoordinator) processBackupRunLocked(ctx context.Context, runID uuid.UUID) error {
	run, err := c.registry.GetBackupRun(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("backup run %s not found", runID)
	}
	if isTerminalRunStatus(run.Status) {
		return nil
	}
	recipe, repo, policy, err := c.loadRunInputs(ctx, run)
	if err != nil {
		return c.completeFailed(ctx, run, nil, "load_inputs", err)
	}
	backend, snapshotBackend, err := c.resolveSnapshotBackend(run.Backend)
	if err != nil {
		return c.completeFailed(ctx, run, nil, "backend_resolve", err)
	}
	if err := c.markRunning(ctx, run); err != nil {
		return err
	}
	if c.config.HealthCheckBeforeRun {
		if err := c.withRunHeartbeat(ctx, run.ID, func() error { return backend.Health(ctx, repo) }); err != nil {
			if contextCancellationErr(ctx, err) {
				return err
			}
			return c.completeFailed(ctx, run, nil, "backend_health", err)
		}
	}
	if !run.SnapshotCreated || strings.TrimSpace(run.SnapshotID) == "" {
		c.publishStatus(ctx, run, "snapshotting", "creating backup snapshot")
		var snapshot *BackupSnapshotResult
		if err := c.withRunHeartbeat(ctx, run.ID, func() error {
			var snapshotErr error
			snapshot, snapshotErr = snapshotBackend.CreateSnapshot(ctx, BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo, Policy: policy})
			return snapshotErr
		}); err != nil {
			if contextCancellationErr(ctx, err) {
				return err
			}
			return c.completeFailed(ctx, run, nil, "snapshotting", err)
		}
		if snapshot == nil || strings.TrimSpace(snapshot.SnapshotID) == "" {
			return c.completeFailed(ctx, run, nil, "snapshotting", fmt.Errorf("%w: backup backend returned no snapshot id", ErrBackupBackendExecution))
		}
		run.SnapshotCreated = true
		run.SnapshotID = strings.TrimSpace(snapshot.SnapshotID)
		backupSetRunMetadata(run, map[string]any{"snapshot_evidence": snapshot.Evidence, "current_step": "snapshot_created"})
		if err := c.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
			return err
		}
	}
	mode, required := c.verificationPlan(recipe, policy)
	run.VerificationMode = mode
	backupSetRunMetadata(run, map[string]any{backupMetadataEffectiveVerificationMode: string(mode), backupMetadataPolicyRequiresVerification: policy != nil && policy.RequireVerification})
	if err := c.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
		return err
	}
	if !required {
		completed, err := c.registry.CompleteBackupRun(ctx, run.ID, run.SnapshotID, nil, nil)
		if err != nil {
			return err
		}
		c.publishResult(ctx, completed, nil, "backup run completed without required verification")
		return nil
	}
	verification, err := c.runVerification(ctx, snapshotBackend, run, recipe, repo, policy, mode)
	if err != nil {
		if contextCancellationErr(ctx, err) {
			return err
		}
		completed, completeErr := c.registry.CompleteBackupRun(ctx, run.ID, run.SnapshotID, verification, err)
		if completeErr != nil {
			return completeErr
		}
		c.publishResult(ctx, completed, verification, "backup run failed verification")
		return err
	}
	completed, err := c.registry.CompleteBackupRun(ctx, run.ID, run.SnapshotID, verification, nil)
	if err != nil {
		return err
	}
	c.publishResult(ctx, completed, verification, "backup run completed and verified")
	return nil
}

func (c *BackupRunCoordinator) loadRunInputs(ctx context.Context, run *domain.BackupRun) (*domain.BackupRecipe, *domain.BackupRepository, *domain.BackupPolicy, error) {
	recipe, err := c.registry.GetRecipe(ctx, run.RecipeID)
	if err != nil {
		return nil, nil, nil, err
	}
	if recipe == nil {
		return nil, nil, nil, fmt.Errorf("backup recipe %s not found", run.RecipeID)
	}
	repo, err := c.registry.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return nil, nil, nil, err
	}
	if repo == nil {
		return nil, nil, nil, fmt.Errorf("backup repository %s not found", run.RepositoryID)
	}
	var policy *domain.BackupPolicy
	if run.PolicyID != nil {
		policy, err = c.registry.GetPolicy(ctx, *run.PolicyID)
		if err != nil {
			return nil, nil, nil, err
		}
		if policy == nil {
			return nil, nil, nil, fmt.Errorf("backup policy %s not found", *run.PolicyID)
		}
	}
	return recipe, repo, policy, nil
}

func (c *BackupRunCoordinator) markRunning(ctx context.Context, run *domain.BackupRun) error {
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
	backupSetRunMetadata(run, map[string]any{"current_step": "running"})
	return c.registry.CreateOrUpdateBackupRun(ctx, run)
}

func (c *BackupRunCoordinator) verificationPlan(recipe *domain.BackupRecipe, policy *domain.BackupPolicy) (domain.BackupVerificationMode, bool) {
	mode := domain.BackupVerificationNone
	if recipe != nil && recipe.VerificationMode != "" {
		mode = recipe.VerificationMode
	}
	if policy != nil && policy.RequireVerification {
		mode = policy.VerificationMode
	}
	return mode, mode != domain.BackupVerificationNone
}

func (c *BackupRunCoordinator) resolveSnapshotBackend(kind domain.BackupBackendKind) (BackupBackend, BackupSnapshotBackend, error) {
	if c.backendResolver == nil {
		return nil, nil, fmt.Errorf("%w: backup backend resolver is not configured", ErrBackupBackendConfiguration)
	}
	backend, ok := c.backendResolver.Resolve(kind)
	if !ok || backend == nil {
		return nil, nil, fmt.Errorf("%w: backup backend %q is not registered", ErrBackupBackendUnsupported, kind)
	}
	snapshotBackend, ok := backend.(BackupSnapshotBackend)
	if !ok {
		return nil, nil, fmt.Errorf("%w: backup backend %q does not support snapshot execution", ErrBackupBackendUnsupported, kind)
	}
	return backend, snapshotBackend, nil
}

func (c *BackupRunCoordinator) runVerification(ctx context.Context, backend BackupSnapshotBackend, run *domain.BackupRun, recipe *domain.BackupRecipe, repo *domain.BackupRepository, policy *domain.BackupPolicy, mode domain.BackupVerificationMode) (*domain.BackupVerificationRecord, error) {
	c.publishStatus(ctx, run, "verifying", "verifying backup snapshot")
	backupSetRunMetadata(run, map[string]any{"current_step": "verifying"})
	if err := c.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
		return nil, err
	}
	var result *BackupVerifyResult
	err := c.withRunHeartbeat(ctx, run.ID, func() error {
		var verifyErr error
		result, verifyErr = backend.VerifySnapshot(ctx, BackupVerifyRequest{
			Run:                run,
			Recipe:             recipe,
			Repository:         repo,
			Policy:             policy,
			SnapshotID:         run.SnapshotID,
			Mode:               mode,
			VerifyFilesPercent: c.config.VerifyFilesPercent,
		})
		return verifyErr
	})
	if err != nil {
		if contextCancellationErr(ctx, err) {
			return nil, err
		}
		status := domain.BackupVerificationFailed
		if errors.Is(err, ErrBackupBackendUnsupported) {
			status = domain.BackupVerificationUnsupported
		}
		record := backupVerificationRecord(run.ID, mode, status, false, nil, err.Error())
		_ = c.registry.RecordBackupVerification(ctx, record)
		return record, err
	}
	if result == nil {
		err = fmt.Errorf("%w: backup backend returned no verification result", ErrBackupBackendExecution)
		record := backupVerificationRecord(run.ID, mode, domain.BackupVerificationFailed, false, nil, err.Error())
		_ = c.registry.RecordBackupVerification(ctx, record)
		return record, err
	}
	status := result.Status
	if status == "" {
		if result.Verified {
			status = domain.BackupVerificationSucceeded
		} else {
			status = domain.BackupVerificationFailed
		}
	}
	record := backupVerificationRecord(run.ID, mode, status, result.Verified, result.Evidence, result.Error)
	if err := c.registry.RecordBackupVerification(ctx, record); err != nil {
		return nil, err
	}
	if !result.Verified || status != domain.BackupVerificationSucceeded {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = "backup verification did not succeed"
		}
		return record, fmt.Errorf("%w: %s", ErrBackupBackendExecution, message)
	}
	return record, nil
}

func (c *BackupRunCoordinator) completeFailed(ctx context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord, step string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("backup run failed")
	}
	backupSetRunMetadata(run, map[string]any{"failed_step": step})
	run.FailureCategory = backupFailureCategoryForStep(step, cause)
	if err := c.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
		return err
	}
	completed, err := c.registry.CompleteBackupRun(ctx, run.ID, run.SnapshotID, verification, cause)
	if err != nil {
		return err
	}
	c.publishResult(ctx, completed, verification, cause.Error())
	return cause
}

func backupVerificationRecord(runID uuid.UUID, mode domain.BackupVerificationMode, status domain.BackupVerificationStatus, verified bool, evidence map[string]any, errMsg string) *domain.BackupVerificationRecord {
	return &domain.BackupVerificationRecord{
		ID:              uuid.New(),
		BackupRunID:     runID,
		Mode:            mode,
		Status:          status,
		Verified:        verified,
		Evidence:        cloneMap(evidence),
		EvidenceDetails: cloneMap(evidence),
		Error:           strings.TrimSpace(errMsg),
	}
}

func (c *BackupRunCoordinator) withRunHeartbeat(ctx context.Context, runID uuid.UUID, fn func() error) error {
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
				c.touchBackupRun(heartbeatCtx, runID)
			}
		}
	}()
	err := fn()
	cancel()
	<-done
	return err
}

func (c *BackupRunCoordinator) touchBackupRun(ctx context.Context, runID uuid.UUID) {
	run, err := c.registry.GetBackupRun(ctx, runID)
	if err != nil || run == nil || run.Status != domain.RunStatusRunning {
		return
	}
	if err := c.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
		c.logger.Warn("failed to heartbeat backup run", zap.String("run_id", runID.String()), zap.Error(err))
	}
}

func (c *BackupRunCoordinator) withRunLock(runID uuid.UUID, fn func() error) error {
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

func (c *BackupRunCoordinator) publishStatus(ctx context.Context, run *domain.BackupRun, step, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRunStatus(ctx, run, step, message); err != nil {
		c.logger.Warn("publish backup run status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *BackupRunCoordinator) publishResult(ctx context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishBackupRunResult(ctx, run, verification, message); err != nil {
		c.logger.Warn("publish backup run result failed", zap.Error(err))
	}
}

func contextCancellationErr(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

func backupSetRunMetadata(run *domain.BackupRun, values map[string]any) {
	if run.Metadata == nil {
		run.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			run.Metadata[k] = v
		}
	}
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
