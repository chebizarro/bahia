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
	DeployDeploymentUnitWithStatus(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		*uuid.UUID,
		*domain.DeploymentUnit,
		*domain.DesiredServiceSpec,
		service.DeployStatusCallback,
	) (*domain.RuntimeObservation, error)
}

type publicRouteLifecycle interface {
	Apply(context.Context, *domain.DesiredPublicRoutePlan) error
}

type Coordinator struct {
	registry         *service.RegistryService
	loom             deploymentLoomClient
	workerPolicy     *service.WorkerPolicyService
	deploymentUnits  repository.DeploymentUnitRepository
	runtimeLifecycle deploymentRuntimeLifecycle
	publicRoutes     publicRouteLifecycle
	publisher        events.Publisher
	logger           *zap.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	executionMu      sync.Mutex
	activeExecutions map[uuid.UUID]struct{}
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
		registry:         registry,
		loom:             loomAdapter,
		publisher:        publisher,
		logger:           logger,
		ctx:              ctx,
		cancel:           cancel,
		activeExecutions: make(map[uuid.UUID]struct{}),
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

// WithPublicRoutes applies signed edge plans after a healthy direct-runtime deploy.
func WithPublicRoutes(routes publicRouteLifecycle) CoordinatorOption {
	return func(c *Coordinator) { c.publicRoutes = routes }
}

// RecoverNonTerminalRuns reattaches completion awaits for persisted Loom jobs
// left queued or running by a previous process instance.
func (c *Coordinator) RecoverNonTerminalRuns(ctx context.Context) error {
	if c.registry == nil {
		return nil
	}
	runs, err := c.registry.ListNonTerminalDeploymentRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing non-terminal deployment runs: %w", err)
	}

	recovered := 0
	for i := range runs {
		run := runs[i]
		switch {
		case strings.TrimSpace(run.LoomJobID) == "runtime:direct":
			c.startDirectRuntimeRecovery(&run)
			recovered++
		case c.loom != nil && isLoomDeploymentJob(run.LoomJobID):
			c.startCompletionAwait(&run)
			recovered++
		}
	}

	approvedWithoutRuns, err := c.registry.ListApprovedDeploymentIntentsWithoutRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing approved deployment intents without runs: %w", err)
	}
	for i := range approvedWithoutRuns {
		c.startExecution(approvedWithoutRuns[i].ID)
		recovered++
	}
	c.logger.Info("deployment run recovery initialized",
		zap.Int("recovered_runs", recovered),
		zap.Int("approved_without_runs", len(approvedWithoutRuns)),
	)
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
	if unit != nil && !deploymentUnitDispatchesViaLoom(unit) {
		return c.executeDirectRuntimeDeployment(ctx, intent, svc, env, unit)
	}
	if intent.DesiredState != nil && intent.DesiredState.PublicRoute != nil {
		return fmt.Errorf("signed public routes require a Bahia-managed Compose deployment unit")
	}
	if c.loom == nil {
		return fmt.Errorf("loom client is not configured")
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
	applyLoomDeploymentUnitRuntimeConfig(&jobReq, unit)

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

func deploymentUnitDispatchesViaLoom(unit *domain.DeploymentUnit) bool {
	if unit == nil || unit.RuntimeConfig == nil {
		return false
	}
	raw, ok := unit.RuntimeConfig["dispatch_mode"]
	if !ok {
		raw, ok = unit.RuntimeConfig["execution_backend"]
	}
	if !ok {
		return false
	}
	mode, ok := raw.(string)
	return ok && strings.EqualFold(strings.TrimSpace(mode), "loom")
}

func applyLoomDeploymentUnitRuntimeConfig(job *loom.JobRequest, unit *domain.DeploymentUnit) {
	if job == nil || unit == nil || unit.RuntimeConfig == nil || !deploymentUnitDispatchesViaLoom(unit) {
		return
	}
	config := unit.RuntimeConfig
	if command := stringSliceFromRuntimeConfig(config, "command"); len(command) > 0 {
		job.Cmd = command[0]
		if len(command) > 1 {
			job.Args = append([]string(nil), command[1:]...)
		}
	}
	if cmd := stringValueFromRuntimeConfig(config, "cmd"); cmd != "" {
		job.Cmd = cmd
	}
	if args := stringSliceFromRuntimeConfig(config, "args"); len(args) > 0 {
		job.Args = args
	}
	if env := stringMapFromRuntimeConfig(config, "env"); len(env) > 0 {
		job.Env = env
	}
	if required := stringSliceFromRuntimeConfig(config, "required_software"); len(required) > 0 {
		job.RequiredSoftware = required
	}
	if arch := stringValueFromRuntimeConfig(config, "required_architecture"); arch != "" {
		job.RequiredArchitecture = arch
	}
	if timeout := durationFromRuntimeConfig(config, "timeout"); timeout > 0 {
		job.Timeout = timeout
	}
}

func stringValueFromRuntimeConfig(config map[string]any, key string) string {
	if value, ok := config[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func stringSliceFromRuntimeConfig(config map[string]any, key string) []string {
	value, ok := config[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				if value = strings.TrimSpace(value); value != "" {
					out = append(out, value)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func stringMapFromRuntimeConfig(config map[string]any, key string) map[string]string {
	value, ok := config[key]
	if !ok {
		return nil
	}
	out := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, value := range typed {
			if key, value = strings.TrimSpace(key), strings.TrimSpace(value); key != "" && value != "" {
				out[key] = value
			}
		}
	case map[string]any:
		for key, raw := range typed {
			if value, ok := raw.(string); ok {
				if key, value = strings.TrimSpace(key), strings.TrimSpace(value); key != "" && value != "" {
					out[key] = value
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func durationFromRuntimeConfig(config map[string]any, key string) time.Duration {
	value := stringValueFromRuntimeConfig(config, key)
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return duration
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
	if unit.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return fmt.Errorf("deployment unit %q is not Bahia-managed", unit.Key)
	}
	if strings.TrimSpace(unit.EndpointRef) == "" {
		return fmt.Errorf("deployment unit %q requires a managed endpoint_ref", unit.Key)
	}
	if unit.RuntimeType == domain.RuntimeTypeCompose && strings.TrimSpace(unit.ComposeDir) == "" {
		return fmt.Errorf("compose deployment unit %q requires compose_dir", unit.Key)
	}
	if c.runtimeLifecycle == nil {
		return fmt.Errorf("runtime lifecycle is required for deployment unit %q", unit.Key)
	}

	now := time.Now().UTC()
	desiredSchemaVersion := ""
	if intent.DesiredState != nil {
		desiredSchemaVersion = intent.DesiredState.SchemaVersion
	}
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
		ApplyMetadata: map[string]any{
			"desired_hash":                 intent.DesiredHash,
			"desired_state_schema_version": desiredSchemaVersion,
			"deployment_unit_key":          unit.Key,
			"endpoint_ref":                 unit.EndpointRef,
			"phase_sequence":               0,
			"phases":                       []map[string]any{},
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

	return c.applyDirectRuntimeRun(ctx, run, intent, unit)
}

func (c *Coordinator) applyDirectRuntimeRun(
	ctx context.Context,
	run *domain.DeploymentRun,
	intent *domain.DeploymentIntent,
	unit *domain.DeploymentUnit,
) error {
	if run == nil || intent == nil || unit == nil {
		return fmt.Errorf("run, intent, and deployment unit are required")
	}
	if run.ApplyMetadata == nil {
		run.ApplyMetadata = map[string]any{}
	}
	statusFn := func(statusCtx context.Context, step service.DeployStep, _ string) {
		c.recordDirectRunPhase(statusCtx, run, string(step), "running")
	}
	var (
		previousDesired      *domain.DesiredServiceSpec
		obs                  *domain.RuntimeObservation
		deployErr            error
		applicationAttempted bool
	)
	if intent.DesiredState != nil && intent.DesiredState.PublicRoute != nil {
		if c.publicRoutes == nil {
			deployErr = fmt.Errorf("public route lifecycle is required for a signed public route")
		} else {
			previousDesired, deployErr = c.latestDeployedDesiredState(ctx, intent)
			if deployErr != nil {
				deployErr = fmt.Errorf("load previous desired state for route compensation: %w", deployErr)
			}
		}
	}
	if deployErr == nil && intent.DesiredState != nil &&
		strings.TrimSpace(intent.DesiredState.DesiredHash) != strings.TrimSpace(intent.DesiredHash) {
		deployErr = fmt.Errorf("persisted desired state hash does not match signed deployment intent hash")
	}
	if deployErr == nil {
		applicationAttempted = true
		obs, deployErr = c.runtimeLifecycle.DeployDeploymentUnitWithStatus(
			ctx,
			intent.ServiceID,
			intent.EnvironmentID,
			&intent.ArtifactID,
			unit,
			intent.DesiredState,
			statusFn,
		)
	}
	if deployErr != nil && errors.Is(deployErr, context.Canceled) {
		return deployErr
	}
	if deployErr != nil && applicationAttempted && previousDesired != nil {
		c.recordDirectRunPhase(context.WithoutCancel(ctx), run, "rollback_application", "running")
		deployErr = c.restorePreviousApplication(intent, unit, previousDesired, deployErr)
	}
	if deployErr == nil && intent.DesiredState != nil && intent.DesiredState.PublicRoute != nil {
		c.recordDirectRunPhase(ctx, run, "routing", "running")
		if routeErr := c.publicRoutes.Apply(ctx, intent.DesiredState.PublicRoute); routeErr != nil {
			deployErr = fmt.Errorf("public route apply failed: %w", routeErr)
			if previousDesired != nil {
				c.recordDirectRunPhase(context.WithoutCancel(ctx), run, "rollback_application", "running")
				deployErr = c.restorePreviousApplication(intent, unit, previousDesired, deployErr)
			}
		}
	}

	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer completeCancel()
	if deployErr != nil {
		code, message := safeDeploymentFailure(deployErr)
		c.finishDirectRunProgress(completeCtx, run, obs, code, message)
		exitCode := 1
		if completeErr := c.registry.CompleteDeploymentRun(completeCtx, run.ID, domain.RunStatusFailed, &exitCode); completeErr != nil {
			c.logger.Error("failed to mark direct runtime deployment as failed",
				zap.String("run_id", run.ID.String()),
				zap.Error(completeErr),
			)
		}
		return fmt.Errorf("direct runtime deploy failed: %w", deployErr)
	}

	c.finishDirectRunProgress(completeCtx, run, obs, "", "")
	if err := c.registry.CompleteDeploymentRun(completeCtx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		return fmt.Errorf("completing direct runtime deployment run: %w", err)
	}
	return nil
}

func (c *Coordinator) restorePreviousApplication(intent *domain.DeploymentIntent, unit *domain.DeploymentUnit, previous *domain.DesiredServiceSpec, cause error) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, rollbackErr := c.runtimeLifecycle.DeployDeploymentUnitWithStatus(
		rollbackCtx,
		intent.ServiceID,
		intent.EnvironmentID,
		&previous.ArtifactID,
		unit,
		previous,
		nil,
	)
	if rollbackErr != nil {
		return fmt.Errorf("%w; application rollback failed: %v", cause, rollbackErr)
	}
	return fmt.Errorf("%w; previous application desired state restored", cause)
}

func (c *Coordinator) latestDeployedDesiredState(ctx context.Context, current *domain.DeploymentIntent) (*domain.DesiredServiceSpec, error) {
	if current == nil {
		return nil, nil
	}
	intents, err := c.registry.ListDeploymentIntents(ctx, current.ServiceID, current.EnvironmentID, 50, 0)
	if err != nil {
		return nil, err
	}
	var latest *domain.DeploymentIntent
	for i := range intents {
		candidate := &intents[i]
		if candidate.ID == current.ID || candidate.Status != domain.IntentStatusDeployed || candidate.DesiredState == nil {
			continue
		}
		if !deploymentUnitIDsEqual(candidate.DeploymentUnitID, current.DeploymentUnitID) {
			continue
		}
		if latest == nil || candidate.CreatedAt.After(latest.CreatedAt) {
			latest = candidate
		}
	}
	if latest == nil {
		return nil, nil
	}
	return latest.DesiredState, nil
}

func deploymentUnitIDsEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (c *Coordinator) recordDirectRunPhase(ctx context.Context, run *domain.DeploymentRun, phase, status string) {
	if run == nil {
		return
	}
	sequence := metadataInt(run.ApplyMetadata["phase_sequence"]) + 1
	now := time.Now().UTC().Format(time.RFC3339Nano)
	phases := metadataPhases(run.ApplyMetadata["phases"])
	if len(phases) > 0 && phases[len(phases)-1]["status"] == "running" {
		phases[len(phases)-1]["status"] = "completed"
		phases[len(phases)-1]["finished_at"] = now
	}
	phases = append(phases, map[string]any{"step": phase, "status": status, "started_at": now})
	run.ApplyMetadata["phase"] = phase
	run.ApplyMetadata["phase_sequence"] = sequence
	run.ApplyMetadata["phases"] = phases
	if err := c.registry.UpdateDeploymentRunApplyMetadata(ctx, run.ID, run.ApplyMetadata); err != nil {
		c.logger.Warn("persist deployment run phase failed", zap.String("run_id", run.ID.String()), zap.Error(err))
	}
}

func (c *Coordinator) finishDirectRunProgress(ctx context.Context, run *domain.DeploymentRun, obs *domain.RuntimeObservation, failureCode, failureMessage string) {
	if run == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	phases := metadataPhases(run.ApplyMetadata["phases"])
	if len(phases) > 0 {
		if failureCode != "" {
			phases[len(phases)-1]["status"] = "failed"
		} else {
			phases[len(phases)-1]["status"] = "completed"
		}
		phases[len(phases)-1]["finished_at"] = now
	}
	run.ApplyMetadata["phases"] = phases
	if obs != nil {
		run.ApplyMetadata["observation_id"] = obs.ID.String()
		run.ApplyMetadata["health_status"] = string(obs.HealthStatus)
	}
	if failureCode != "" {
		run.ApplyMetadata["failure"] = map[string]any{
			"code":    failureCode,
			"message": failureMessage,
			"phase":   run.ApplyMetadata["phase"],
		}
	}
	if err := c.registry.UpdateDeploymentRunApplyMetadata(ctx, run.ID, run.ApplyMetadata); err != nil {
		c.logger.Warn("persist deployment run outcome failed", zap.String("run_id", run.ID.String()), zap.Error(err))
	}
}

func safeDeploymentFailure(err error) (string, string) {
	if healthErr, ok := err.(*service.DeploymentHealthError); ok {
		return healthErr.Code, healthErr.Message
	}
	return "runtime_apply_failed", "Bahia could not apply the reviewed desired state."
}

func metadataInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func metadataPhases(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if phase, ok := item.(map[string]any); ok {
				result = append(result, phase)
			}
		}
		return result
	default:
		return []map[string]any{}
	}
}

func (c *Coordinator) startDirectRuntimeRecovery(run *domain.DeploymentRun) {
	if run == nil {
		return
	}
	runCopy := *run
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		intent, err := c.registry.GetDeploymentIntent(c.ctx, runCopy.DeploymentIntentID)
		if err != nil || intent == nil {
			c.logger.Error("recover direct runtime run: load intent failed", zap.String("run_id", runCopy.ID.String()), zap.Error(err))
			return
		}
		env, err := c.registry.GetEnvironment(c.ctx, intent.EnvironmentID)
		if err != nil || env == nil {
			c.logger.Error("recover direct runtime run: load environment failed", zap.String("run_id", runCopy.ID.String()), zap.Error(err))
			return
		}
		unit, err := c.resolveDeploymentUnit(c.ctx, intent, env)
		if err != nil {
			c.logger.Error("recover direct runtime run: resolve unit failed", zap.String("run_id", runCopy.ID.String()), zap.Error(err))
			return
		}
		if c.directRunAlreadyConverged(c.ctx, intent) {
			completeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.finishDirectRunProgress(completeCtx, &runCopy, nil, "", "")
			if err := c.registry.CompleteDeploymentRun(completeCtx, runCopy.ID, domain.RunStatusSucceeded, nil); err != nil {
				c.logger.Error("recover direct runtime run: completion failed", zap.String("run_id", runCopy.ID.String()), zap.Error(err))
			}
			return
		}
		if err := c.applyDirectRuntimeRun(c.ctx, &runCopy, intent, unit); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Error("recover direct runtime run failed", zap.String("run_id", runCopy.ID.String()), zap.Error(err))
		}
	}()
}

func (c *Coordinator) directRunAlreadyConverged(ctx context.Context, intent *domain.DeploymentIntent) bool {
	if intent == nil || (intent.DesiredState != nil && intent.DesiredState.PublicRoute != nil) {
		// Runtime convergence alone cannot prove DNS, Tunnel, proxy, and HTTPS
		// convergence. Recovery safely replays the idempotent app-first route flow.
		return false
	}
	state, err := c.registry.GetEnvironmentServiceState(ctx, intent.ServiceID, intent.EnvironmentID)
	if err != nil || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID ||
		state.DesiredHash == "" || state.DesiredHash != intent.DesiredHash || state.DriftStatus != domain.DriftStatusInSync {
		return false
	}
	obs, err := c.registry.GetLatestObservation(ctx, intent.ServiceID, intent.EnvironmentID)
	return err == nil && obs != nil && obs.HealthStatus == domain.HealthStatusHealthy
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
	pub.Subscribe(events.EventDeploymentIntentApproved, func(_ context.Context, e events.Event) {
		id, err := uuid.Parse(e.EntityID)
		if err != nil {
			return
		}
		c.startExecution(id)
	})
}

func (c *Coordinator) startExecution(intentID uuid.UUID) {
	c.executionMu.Lock()
	if _, active := c.activeExecutions[intentID]; active {
		c.executionMu.Unlock()
		return
	}
	c.activeExecutions[intentID] = struct{}{}
	c.executionMu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.executionMu.Lock()
			delete(c.activeExecutions, intentID)
			c.executionMu.Unlock()
		}()
		if err := c.ExecuteDeployment(c.ctx, intentID); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Error("auto-execute deployment failed",
				zap.String("intent_id", intentID.String()),
				zap.Error(err),
			)
		}
	}()
}
