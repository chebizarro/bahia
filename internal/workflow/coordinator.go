// Package workflow coordinates deployment workflows between intents, Loom jobs, and state.
package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Coordinator manages the lifecycle of deployment workflows.
type Coordinator struct {
	registry      *service.RegistryService
	loom          *loom.Client
	workerPolicy  *service.WorkerPolicyService
	workerCatalog *service.WorkerCatalogService
	publisher     events.Publisher
	logger        *zap.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCoordinator creates a new workflow Coordinator.
func NewCoordinator(
	registry *service.RegistryService,
	loomClient *loom.Client,
	publisher events.Publisher,
	logger *zap.Logger,
	opts ...CoordinatorOption,
) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Coordinator{
		registry:  registry,
		loom:      loomClient,
		publisher: publisher,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CoordinatorOption configures optional Coordinator dependencies.
type CoordinatorOption func(*Coordinator)

// WithWorkerPolicy enables environment-specific worker selection.
func WithWorkerPolicy(wp *service.WorkerPolicyService) CoordinatorOption {
	return func(c *Coordinator) { c.workerPolicy = wp }
}

// WithWorkerCatalog enables job stats tracking.
func WithWorkerCatalog(wc *service.WorkerCatalogService) CoordinatorOption {
	return func(c *Coordinator) { c.workerCatalog = wc }
}

// Shutdown cancels all in-flight poll goroutines and waits for them to finish.
// It should be called during graceful application shutdown.
func (c *Coordinator) Shutdown(timeout time.Duration) {
	c.logger.Info("coordinator shutting down, cancelling in-flight polls")
	c.cancel()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("coordinator shutdown complete, all polls finished")
	case <-time.After(timeout):
		c.logger.Warn("coordinator shutdown timed out, some polls may still be running",
			zap.Duration("timeout", timeout),
		)
	}
}

// ExecuteDeployment takes an approved deployment intent and submits it as a Loom job.
func (c *Coordinator) ExecuteDeployment(ctx context.Context, intentID uuid.UUID) error {
	intent, err := c.registry.GetDeploymentIntent(ctx, intentID)
	if err != nil {
		return fmt.Errorf("getting intent: %w", err)
	}
	if intent == nil {
		return fmt.Errorf("intent %s not found", intentID)
	}

	if intent.Status != domain.IntentStatusApproved {
		return fmt.Errorf("intent %s is not in approved state (current: %s)", intentID, intent.Status)
	}

	// Get artifact details for the Loom job.
	artifact, err := c.registry.GetArtifact(ctx, intent.ArtifactID)
	if err != nil || artifact == nil {
		return fmt.Errorf("getting artifact for intent: %w", err)
	}

	svc, err := c.registry.GetService(ctx, intent.ServiceID)
	if err != nil || svc == nil {
		return fmt.Errorf("getting service for intent: %w", err)
	}

	env, err := c.registry.GetEnvironment(ctx, intent.EnvironmentID)
	if err != nil || env == nil {
		return fmt.Errorf("getting environment for intent: %w", err)
	}

	// Select a worker using the environment's worker policy (if configured).
	var workerPubkey string
	if c.workerPolicy != nil {
		selected, err := c.workerPolicy.SelectWorker(ctx, env)
		if err != nil {
			c.logger.Warn("worker policy selection failed, falling back to Loom auto-select",
				zap.String("environment", env.Name),
				zap.Error(err),
			)
		} else {
			workerPubkey = selected.Worker.PubKey
			c.logger.Info("worker selected by policy",
				zap.String("pubkey", workerPubkey),
				zap.String("strategy", selected.Reason),
				zap.Float64("score", selected.Score),
			)
		}
	}

	// Submit the deploy job to Loom.
	jobReq := loom.JobRequest{
		ID:           uuid.New().String(),
		Type:         "deploy",
		Image:        artifact.ImageRepo + ":" + artifact.ImageTag,
		Digest:       artifact.ImageDigest,
		Environment:  env.Name,
		Service:      svc.Name,
		WorkerPubkey: workerPubkey,
	}

	jobEventID, err := c.loom.SubmitJob(ctx, jobReq)
	if err != nil {
		return fmt.Errorf("submitting loom job: %w", err)
	}

	// Create a deployment run record.
	now := time.Now().UTC()
	run := &domain.DeploymentRun{
		DeploymentIntentID: intentID,
		LoomJobID:          jobEventID,
		WorkerPubkey:       workerPubkey,
		Status:             domain.RunStatusQueued,
		StartedAt:          &now,
	}

	if err := c.registry.CreateDeploymentRun(ctx, run); err != nil {
		return fmt.Errorf("creating deployment run: %w", err)
	}

	c.logger.Info("deployment job submitted",
		zap.String("intent_id", intentID.String()),
		zap.String("run_id", run.ID.String()),
		zap.String("loom_job_id", jobEventID),
		zap.String("service", svc.Name),
		zap.String("environment", env.Name),
	)

	// Start polling for job completion in a tracked goroutine.
	// Uses the coordinator's lifecycle context so polls are cancelled on shutdown.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.pollForCompletion(c.ctx, run.ID, jobEventID, workerPubkey, now)
	}()

	return nil
}

func (c *Coordinator) pollForCompletion(ctx context.Context, runID uuid.UUID, jobEventID string, workerPubkey string, startTime time.Time) {
	status, err := c.loom.PollJobStatus(ctx, jobEventID)
	if err != nil {
		// Distinguish between cancellation (shutdown) and actual polling errors.
		if ctx.Err() != nil {
			c.logger.Info("poll cancelled during shutdown, marking run as timeout",
			zap.String("run_id", runID.String()),
			)
		} else {
			c.logger.Error("failed to poll job status",
				zap.String("run_id", runID.String()),
				zap.Error(err),
			)
		}

		// Record job failure in stats
		c.recordJobStats(workerPubkey, startTime, false)

		// Use a detached context for the completion call since the poll context may be cancelled.
		// The run record must be updated even during shutdown to avoid leaving it in queued/running state.
		completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer completeCancel()

		if completeErr := c.registry.CompleteDeploymentRun(completeCtx, runID, domain.RunStatusTimeout, nil); completeErr != nil {
			c.logger.Error("failed to mark timed-out run as complete",
				zap.String("run_id", runID.String()),
				zap.Error(completeErr),
			)
		}
		return
	}

	runStatus := mapLoomStatus(status.Status)

	// Record job completion in stats
	success := runStatus == domain.RunStatusSucceeded
	c.recordJobStats(workerPubkey, startTime, success)

	if err := c.registry.CompleteDeploymentRun(ctx, runID, runStatus, status.ExitCode); err != nil {
		c.logger.Error("failed to complete deployment run",
			zap.String("run_id", runID.String()),
			zap.Error(err),
		)
	}
}

// recordJobStats records job completion statistics if a worker catalog is configured.
func (c *Coordinator) recordJobStats(workerPubkey string, startTime time.Time, success bool) {
	if c.workerCatalog == nil || workerPubkey == "" {
		return
	}

	durationMs := time.Since(startTime).Milliseconds()
	c.workerCatalog.RecordJobCompletion(workerPubkey, durationMs, success)

	c.logger.Debug("recorded job stats",
		zap.String("worker", workerPubkey),
		zap.Int64("duration_ms", durationMs),
		zap.Bool("success", success),
	)
}

func mapLoomStatus(s string) domain.DeploymentRunStatus {
	switch s {
	case "completed", "succeeded":
		return domain.RunStatusSucceeded
	case "failed":
		return domain.RunStatusFailed
	case "cancelled":
		return domain.RunStatusCancelled
	case "timeout":
		return domain.RunStatusTimeout
	default:
		return domain.RunStatusFailed
	}
}

// SetupEventHandlers registers event-driven workflow triggers.
func (c *Coordinator) SetupEventHandlers(pub events.Publisher) {
	// Auto-execute approved deployment intents.
	pub.Subscribe(events.EventDeploymentIntentApproved, func(ctx context.Context, e events.Event) {
		id, err := uuid.Parse(e.EntityID)
		if err != nil {
			return
		}
		if err := c.ExecuteDeployment(ctx, id); err != nil {
			c.logger.Error("auto-execute deployment failed",
				zap.String("intent_id", e.EntityID),
				zap.Error(err),
			)
		}
	})
}
