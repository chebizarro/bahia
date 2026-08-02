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
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Coordinator manages the lifecycle of deployment workflows.
type deploymentLoomClient interface {
	SubmitJob(context.Context, loom.JobRequest) (string, error)
	AwaitJobStatusFromWorker(context.Context, string, string, ...loom.StatusCallback) (*loom.JobStatus, error)
	JobTimeout() time.Duration
}

type deploymentRuntimeLifecycle interface {
	DeployDeploymentUnit(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		*uuid.UUID,
		*domain.DeploymentUnit,
		*domain.DesiredServiceSpec,
	) (*domain.RuntimeObservation, error)
}

type Coordinator struct {
	registry         *service.RegistryService
	loom             deploymentLoomClient
	workerPolicy     *service.WorkerPolicyService
	deploymentUnits  repository.DeploymentUnitRepository
	runtimeLifecycle deploymentRuntimeLifecycle
	publisher        events.Publisher
	logger           *zap.Logger

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

// WithDeploymentUnitRouting enables unit-authoritative direct runtime routing.
func WithDeploymentUnitRouting(units repository.DeploymentUnitRepository, lifecycle deploymentRuntimeLifecycle) CoordinatorOption {
	return func(c *Coordinator) {
		c.deploymentUnits = units
		c.runtimeLifecycle = lifecycle
	}
}

// RecoverNonTerminalRuns reattaches completion awaits for persisted Loom jobs
// left queued or running by a previous process instance.
func (c *Coordinator) RecoverNonTerminalRuns(ctx context.Context) error {
	if c.registry == nil || c.loom == nil {
		return nil
	}
	runs, err := c.registry.ListNonTerminalDeploymentRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing non-terminal deployment runs: %w", err)
	}

	recovered := 0
	for i := range runs {
		run := runs[i]
		if !isLoomDeploymentJob(run.LoomJobID) {
			continue
		}
		c.startCompletionAwait(&run)
		recovered++
	}
	c.logger.Info("deployment run recovery initialized", zap.Int("recovered_runs", recovered))
	return nil
}

func isLoomDeploymentJob(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	return jobID != "" && !strings.HasPrefix(jobID, "runtime:") && !strings.HasPrefix(jobID, "admission:")
}

// Shutdown cancels all in-flight await goroutines and waits for them to finish.
// It should be called during graceful application shutdown.
func (c *Coordinator) Shutdown(timeout time.Duration) {
	c.logger.Info("coordinator shutting down, cancelling in-flight awaits")
	c.cancel()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("coordinator shutdown complete, all awaits finished")
	case <-time.After(timeout):
		c.logger.Warn("coordinator shutdown timed out, some awaits may still be running",
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

	unit, err := c.resolveDeploymentUnit(ctx, intent, env)
	if err != nil {
		return fmt.Errorf("resolving deployment unit: %w", err)
	}
	if unit != nil && unit.RuntimeType == domain.RuntimeTypeCompose {
		return c.executeDirectRuntimeDeployment(ctx, intent, svc, env, unit)
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
	var deploymentUnitID *uuid.UUID
	if unit != nil && unit.ID != uuid.Nil {
		id := unit.ID
		deploymentUnitID = &id
	}
	run := &domain.DeploymentRun{
		DeploymentIntentID: intentID,
		DeploymentUnitID:   deploymentUnitID,
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

	// Await job completion in a tracked goroutine. The coordinator lifecycle
	// context cancels the await without converting shutdown into a job timeout.
	c.startCompletionAwait(run)

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

func (c *Coordinator) resolveDeploymentUnit(
	ctx context.Context,
	intent *domain.DeploymentIntent,
	env *domain.Environment,
) (*domain.DeploymentUnit, error) {
	if intent == nil || env == nil {
		return nil, fmt.Errorf("deployment intent and environment are required")
	}

	var unitID *uuid.UUID
	if intent.DeploymentUnitID != nil && *intent.DeploymentUnitID != uuid.Nil {
		id := *intent.DeploymentUnitID
		unitID = &id
	}
	if desired := intent.DesiredState; desired != nil && desired.DeploymentUnitID != nil && *desired.DeploymentUnitID != uuid.Nil {
		if unitID != nil && *unitID != *desired.DeploymentUnitID {
			return nil, fmt.Errorf("intent deployment unit %s does not match desired-state unit %s", *unitID, *desired.DeploymentUnitID)
		}
		id := *desired.DeploymentUnitID
		unitID = &id
	}

	if c.deploymentUnits == nil {
		if unitID != nil {
			return nil, fmt.Errorf("deployment unit repository is required for explicit unit %s", *unitID)
		}
		return nil, nil
	}

	var (
		unit *domain.DeploymentUnit
		err  error
	)
	if unitID != nil {
		unit, err = c.deploymentUnits.GetByID(ctx, *unitID)
	} else {
		unit, err = c.deploymentUnits.ResolveDefault(ctx, env)
	}
	if err != nil {
		return nil, err
	}
	if unit == nil {
		if unitID != nil {
			return nil, fmt.Errorf("deployment unit %s not found", *unitID)
		}
		return nil, fmt.Errorf("default deployment unit not found for environment %s", env.ID)
	}
	if unit.EnvironmentID != env.ID {
		return nil, fmt.Errorf("deployment unit %q belongs to environment %s, not %s", unit.Key, unit.EnvironmentID, env.ID)
	}

	resolved := *unit
	domain.NormalizeDeploymentUnitTargeting(&resolved)
	if err := domain.ValidateDeploymentUnit(&resolved); err != nil {
		return nil, fmt.Errorf("invalid deployment unit %q: %w", resolved.Key, err)
	}
	if desired := intent.DesiredState; desired != nil {
		if desired.DeploymentUnitID == nil && resolved.ID != uuid.Nil {
			return nil, fmt.Errorf("desired-state implicit deployment unit became explicit unit %s", resolved.ID)
		}
		if desired.DeploymentUnitKey != "" && desired.DeploymentUnitKey != resolved.Key {
			return nil, fmt.Errorf("desired-state unit key %q does not match resolved unit %q", desired.DeploymentUnitKey, resolved.Key)
		}
		if desired.UnitRuntimeType != "" && desired.UnitRuntimeType != resolved.RuntimeType {
			return nil, fmt.Errorf("desired-state runtime type %q does not match resolved unit %q type %q", desired.UnitRuntimeType, resolved.Key, resolved.RuntimeType)
		}
	}
	return &resolved, nil
}

func (c *Coordinator) executeDirectRuntimeDeployment(
	ctx context.Context,
	intent *domain.DeploymentIntent,
	svc *domain.Service,
	env *domain.Environment,
	unit *domain.DeploymentUnit,
) error {
	if unit == nil {
		return fmt.Errorf("deployment unit is required for direct runtime deployment")
	}
	if unit.RuntimeType != domain.RuntimeTypeCompose {
		return fmt.Errorf("deployment unit %q runtime type %q is not compose", unit.Key, unit.RuntimeType)
	}
	if unit.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return fmt.Errorf("compose deployment unit %q is not Bahia-managed", unit.Key)
	}
	if strings.TrimSpace(unit.EndpointRef) == "" {
		return fmt.Errorf("compose deployment unit %q requires a managed endpoint_ref", unit.Key)
	}
	if strings.TrimSpace(unit.ComposeDir) == "" {
		return fmt.Errorf("compose deployment unit %q requires compose_dir", unit.Key)
	}
	if c.runtimeLifecycle == nil {
		return fmt.Errorf("runtime lifecycle is required for compose deployment unit %q", unit.Key)
	}

	now := time.Now().UTC()
	var deploymentUnitID *uuid.UUID
	if unit.ID != uuid.Nil {
		unitID := unit.ID
		deploymentUnitID = &unitID
	}
	run := &domain.DeploymentRun{
		DeploymentIntentID: intent.ID,
		DeploymentUnitID:   deploymentUnitID,
		LoomJobID:          "runtime:direct",
		Status:             domain.RunStatusQueued,
		StartedAt:          &now,
		Metadata: map[string]any{
			"execution_mode":      "direct-runtime",
			"runtime_type":        string(unit.RuntimeType),
			"deployment_unit_key": unit.Key,
			"endpoint_ref":        unit.EndpointRef,
		},
	}
	if err := c.registry.CreateDeploymentRun(ctx, run); err != nil {
		return fmt.Errorf("creating deployment run: %w", err)
	}

	c.logger.Info("executing deployment-unit runtime lifecycle",
		zap.String("intent_id", intent.ID.String()),
		zap.String("run_id", run.ID.String()),
		zap.String("service", svc.Name),
		zap.String("environment", env.Name),
		zap.String("deployment_unit", unit.Key),
		zap.String("runtime_type", string(unit.RuntimeType)),
		zap.String("endpoint_ref", unit.EndpointRef),
	)

	if _, err := c.runtimeLifecycle.DeployDeploymentUnit(
		ctx,
		intent.ServiceID,
		intent.EnvironmentID,
		&intent.ArtifactID,
		unit,
		intent.DesiredState,
	); err != nil {
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

func (c *Coordinator) startCompletionAwait(run *domain.DeploymentRun) {
	if run == nil {
		return
	}
	runCopy := *run
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.awaitCompletion(c.ctx, &runCopy)
	}()
}

func (c *Coordinator) awaitCompletion(ctx context.Context, run *domain.DeploymentRun) {
	if c.loom == nil || run == nil {
		return
	}

	startedAt := run.StartedAt
	if startedAt == nil {
		fallback := run.CreatedAt
		if fallback.IsZero() {
			fallback = time.Now().UTC()
		}
		startedAt = &fallback
	}

	awaitCtx := ctx
	cancel := func() {}
	var jobDeadline time.Time
	if timeout := c.loom.JobTimeout(); timeout > 0 {
		jobDeadline = startedAt.Add(timeout)
		awaitCtx, cancel = context.WithDeadline(ctx, jobDeadline)
	}
	defer cancel()

	status, err := c.loom.AwaitJobStatusFromWorker(awaitCtx, run.LoomJobID, run.WorkerPubkey)
	if err != nil {
		// Process shutdown leaves the run non-terminal so the next process can
		// recover it. Only expiration of StartedAt + JobTimeout is a job timeout.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			c.logger.Info("Loom job await cancelled during shutdown; leaving run non-terminal",
				zap.String("run_id", run.ID.String()),
				zap.String("loom_job_id", run.LoomJobID),
			)
			return
		}
		if errors.Is(err, context.DeadlineExceeded) && !jobDeadline.IsZero() && !time.Now().Before(jobDeadline) {
			c.logger.Warn("Loom job exceeded its wall-clock timeout",
				zap.String("run_id", run.ID.String()),
				zap.String("loom_job_id", run.LoomJobID),
				zap.Time("started_at", *startedAt),
				zap.Time("deadline", jobDeadline),
			)
			c.completeRunDetached(run.ID, domain.RunStatusTimeout, nil)
			return
		}

		c.logger.Error("failed to await job status without trusted terminal result",
			zap.String("run_id", run.ID.String()),
			zap.String("loom_job_id", run.LoomJobID),
			zap.Error(err),
		)
		return
	}

	c.completeRunDetached(run.ID, mapLoomStatus(status.Status), status.ExitCode)
}

func (c *Coordinator) completeRunDetached(runID uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) {
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer completeCancel()
	if err := c.registry.CompleteDeploymentRun(completeCtx, runID, status, exitCode); err != nil {
		c.logger.Error("failed to complete deployment run",
			zap.String("run_id", runID.String()),
			zap.String("status", string(status)),
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
