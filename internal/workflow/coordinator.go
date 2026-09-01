// Package workflow coordinates deployment workflows between intents, Loom jobs, and state.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Coordinator manages the lifecycle of deployment workflows.
type deploymentLoomClient interface {
	SubmitJob(context.Context, loom.JobRequest) (string, error)
	PollJobStatusFromWorker(context.Context, string, string, ...loom.StatusCallback) (*loom.JobStatus, error)
}

type Coordinator struct {
	registry        *service.RegistryService
	loom            deploymentLoomClient
	workerPolicy    *service.WorkerPolicyService
	runtimeResolver runtimeadapter.RuntimeResolver
	publisher       events.Publisher
	logger          *zap.Logger

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
	var loomAdapter deploymentLoomClient
	if loomClient != nil {
		loomAdapter = loomClient
	}
	c := &Coordinator{
		registry:  registry,
		loom:      loomAdapter,
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

// WithRuntimeResolver enables direct runtime deployments for resolvable targets.
func WithRuntimeResolver(resolver runtimeadapter.RuntimeResolver) CoordinatorOption {
	return func(c *Coordinator) { c.runtimeResolver = resolver }
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

	resolvedImage := strings.TrimSpace(artifact.ImageRepo)
	if tag := strings.TrimSpace(artifact.ImageTag); tag != "" {
		resolvedImage = resolvedImage + ":" + tag
	}

	if c.shouldUseDirectRuntimeDeployment(svc, artifact) {
		return c.executeDirectRuntimeDeployment(ctx, intentID, intent.DeploymentUnitID, svc, env, resolvedImage)
	}

	// Select a worker using the environment's worker policy (if configured).
	var workerPubkey string
	if c.workerPolicy != nil {
		selected, err := c.workerPolicy.SelectWorker(ctx, env)
		if err != nil {
			return fmt.Errorf("selecting worker for environment %q: %w", env.Name, err)
		}
		workerPubkey = selected.Worker.PubKey
		c.logger.Info("worker selected by policy",
			zap.String("pubkey", workerPubkey),
			zap.String("strategy", selected.Reason),
			zap.Float64("score", selected.Score),
		)
	}

	if c.workerPolicy != nil && workerPubkey != "" {
		decision, err := c.workerPolicy.EvaluateDispatchAdmission(ctx, env, workerPubkey)
		if err != nil {
			return err
		}
		if !decision.Eligible {
			return c.failDeploymentRunForDispatchAdmission(ctx, intentID, workerPubkey, decision)
		}
	}

	// Submit the deploy job to Loom.
	jobReq := loom.JobRequest{
		ID:           uuid.New().String(),
		Type:         "deploy",
		Image:        resolvedImage,
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
		DeploymentUnitID:   intent.DeploymentUnitID,
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
		c.pollForCompletion(c.ctx, run.ID, jobEventID, workerPubkey)
	}()

	return nil
}

func (c *Coordinator) failDeploymentRunForDispatchAdmission(ctx context.Context, intentID uuid.UUID, workerPubkey string, decision service.WorkerAdmissionDecision) error {
	now := time.Now().UTC()
	run := &domain.DeploymentRun{
		DeploymentIntentID: intentID,
		LoomJobID:          "admission:rejected",
		WorkerPubkey:       workerPubkey,
		Status:             domain.RunStatusQueued,
		StartedAt:          &now,
		Metadata: map[string]any{
			"failure_phase":     "dispatch_admission",
			"admission_scope":   string(service.AdmissionScopeServiceDeploy),
			"admission_code":    decision.Code,
			"admission_reason":  decision.Reason,
			"capacity_class":    string(decision.CapacityClass),
			"pressure_level":    string(decision.PressureLevel),
			"cleanup_suggested": decision.CleanupSuggested,
		},
	}
	if err := c.registry.CreateDeploymentRun(ctx, run); err != nil {
		return fmt.Errorf("creating dispatch admission rejection run: %w", err)
	}
	exitCode := 1
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, &exitCode); err != nil {
		return fmt.Errorf("marking dispatch admission rejection failed: %w", err)
	}
	c.logger.Warn("deployment blocked by dispatch admission",
		zap.String("intent_id", intentID.String()),
		zap.String("run_id", run.ID.String()),
		zap.String("worker_pubkey", workerPubkey),
		zap.String("admission_code", decision.Code),
		zap.String("admission_reason", decision.Reason),
	)
	return nil
}

func (c *Coordinator) shouldUseDirectRuntimeDeployment(svc *domain.Service, artifact *domain.Artifact) bool {
	if c.runtimeResolver == nil || svc == nil || artifact == nil {
		return false
	}
	if svc.RuntimeType != domain.RuntimeTypeCompose {
		return false
	}
	return isLocalImageRepo(artifact.ImageRepo)
}

func (c *Coordinator) executeDirectRuntimeDeployment(ctx context.Context, intentID uuid.UUID, deploymentUnitID *uuid.UUID, svc *domain.Service, env *domain.Environment, image string) error {
	rt, err := c.runtimeResolver.Resolve(svc, env)
	if err != nil {
		return fmt.Errorf("resolving runtime for direct deployment: %w", err)
	}

	now := time.Now().UTC()
	run := &domain.DeploymentRun{
		DeploymentIntentID: intentID,
		DeploymentUnitID:   deploymentUnitID,
		LoomJobID:          "runtime:direct",
		Status:             domain.RunStatusQueued,
		StartedAt:          &now,
		Metadata: map[string]any{
			"execution_mode": "direct-runtime",
			"runtime_type":   string(rt.Type()),
		},
	}
	if err := c.registry.CreateDeploymentRun(ctx, run); err != nil {
		return fmt.Errorf("creating deployment run: %w", err)
	}

	deployImage := image
	if rt.Type() == domain.RuntimeTypeCompose && isLocalImageRef(image) {
		deployImage = ""
		run.Metadata["image_resolution"] = "compose-file"
	}

	c.logger.Info("executing direct runtime deployment",
		zap.String("intent_id", intentID.String()),
		zap.String("run_id", run.ID.String()),
		zap.String("service", svc.Name),
		zap.String("environment", env.Name),
		zap.String("runtime_type", string(rt.Type())),
		zap.String("requested_image", image),
		zap.String("deploy_image", deployImage),
	)

	if err := rt.Deploy(ctx, svc.Name, deployImage, runtimeadapter.DeployOptions{
		Labels: map[string]string{"bahia.service": svc.Name},
	}); err != nil {
		exitCode := 1
		completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer completeCancel()
		if completeErr := c.registry.CompleteDeploymentRun(completeCtx, run.ID, domain.RunStatusFailed, &exitCode); completeErr != nil {
			c.logger.Error("failed to mark direct runtime deployment as failed",
				zap.String("run_id", run.ID.String()),
				zap.Error(completeErr),
			)
		}
		return fmt.Errorf("direct runtime deploy failed: %w", err)
	}

	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer completeCancel()
	if err := c.registry.CompleteDeploymentRun(completeCtx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		return fmt.Errorf("completing direct runtime deployment run: %w", err)
	}

	return nil
}

func isLocalImageRepo(repo string) bool {
	return strings.HasPrefix(strings.TrimSpace(repo), "local/")
}

func isLocalImageRef(image string) bool {
	return strings.HasPrefix(strings.TrimSpace(image), "local/")
}

func (c *Coordinator) pollForCompletion(ctx context.Context, runID uuid.UUID, jobEventID string, workerPubkey string) {
	status, err := c.loom.PollJobStatusFromWorker(ctx, jobEventID, workerPubkey)
	if err != nil {
		// Only context cancellation/deadline represents a timeout lifecycle outcome.
		// Relay CLOSED/AUTH/subscription errors are infrastructure failures; do not
		// mutate run state or worker stats from incomplete data.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				c.logger.Info("poll cancelled during shutdown, marking run as timeout",
					zap.String("run_id", runID.String()),
				)
			} else {
				c.logger.Warn("loom job poll timed out",
					zap.String("run_id", runID.String()),
					zap.String("loom_job_id", jobEventID),
					zap.Error(err),
				)
			}

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

		c.logger.Error("failed to poll job status without trusted terminal result",
			zap.String("run_id", runID.String()),
			zap.String("loom_job_id", jobEventID),
			zap.Error(err),
		)
		return
	}

	runStatus := mapLoomStatus(status.Status)

	if err := c.registry.CompleteDeploymentRun(ctx, runID, runStatus, status.ExitCode); err != nil {
		c.logger.Error("failed to complete deployment run",
			zap.String("run_id", runID.String()),
			zap.Error(err),
		)
	}
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
	autoExecute := func(ctx context.Context, e events.Event) {
		id, err := uuid.Parse(e.EntityID)
		if err != nil {
			return
		}

		intent, err := c.registry.GetDeploymentIntent(ctx, id)
		if err != nil {
			c.logger.Error("failed to load deployment intent for auto-execute",
				zap.String("intent_id", e.EntityID),
				zap.Error(err),
			)
			return
		}
		if intent == nil || intent.Status != domain.IntentStatusApproved {
			return
		}

		if err := c.ExecuteDeployment(ctx, id); err != nil {
			c.logger.Error("auto-execute deployment failed",
				zap.String("intent_id", e.EntityID),
				zap.String("event_type", string(e.Type)),
				zap.Error(err),
			)
		}
	}

	// Auto-execute explicitly approved intents and intents that are born approved
	// (for example, non-protected environments).
	pub.Subscribe(events.EventDeploymentIntentCreated, autoExecute)
	pub.Subscribe(events.EventDeploymentIntentApproved, autoExecute)
}
