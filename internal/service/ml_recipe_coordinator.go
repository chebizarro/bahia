package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const defaultMLRecipeRecoveryPollInterval = 30 * time.Second

// MLRecipeRunQueueRepository provides durable queue and lease-recovery primitives for recipe runs.
type MLRecipeRunQueueRepository interface {
	ClaimNextQueuedMLRecipeRun(ctx context.Context) (*domain.MLRecipeRun, error)
	RequeueStaleMLRecipeRuns(ctx context.Context, olderThan time.Duration) (int, error)
}

// MLRecipeRunResponder publishes Nostr-native terminal recipe results. Status/read-model progress is stored in MLRecipeRun checkpoints.
type MLRecipeRunResponder interface {
	PublishRecipeRunStatus(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step, message string) error
	PublishRecipeRunResult(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, status, message string) error
	PublishRecipeRunError(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step string, cause error) error
}

// MLRecipeJobDispatcher is the explicit boundary between recipe orchestration and Loom/container execution.
// RKNN/ONNX adapters should consume artifacts produced by this boundary rather than hiding conversion work internally.
type MLRecipeJobDispatcher interface {
	DispatchStep(ctx context.Context, req MLRecipeJobDispatchRequest) (*MLRecipeJobDispatchResult, error)
}

// MLRecipeJobDispatchRequest is the coordinator-to-worker/container contract for one linear recipe step.
type MLRecipeJobDispatchRequest struct {
	Recipe         *domain.MLRecipe       `json:"-"`
	Run            *domain.MLRecipeRun    `json:"-"`
	Step           domain.MLRecipeStep    `json:"step"`
	StepIndex      int                    `json:"step_index"`
	Action         string                 `json:"action"`
	Runtime        domain.MLRuntimeKind   `json:"runtime,omitempty"`
	InputDigestSet []string               `json:"input_digest_set,omitempty"`
	InputArtifacts []uuid.UUID            `json:"input_artifacts,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Container      *MLRecipeContainerSpec `json:"container,omitempty"`
	Metadata       map[string]any         `json:"metadata,omitempty"`
}

// MLRecipeContainerSpec describes an intentionally generic container job payload.
type MLRecipeContainerSpec struct {
	Image     string            `json:"image,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Workdir   string            `json:"workdir,omitempty"`
	Resources map[string]any    `json:"resources,omitempty"`
}

// MLRecipeJobDispatchResult is terminal output for one dispatched recipe step.
type MLRecipeJobDispatchResult struct {
	Status          domain.DeploymentRunStatus `json:"status,omitempty"`
	Message         string                     `json:"message,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Retryable       bool                       `json:"retryable,omitempty"`
	OutputArtifacts []uuid.UUID                `json:"output_artifacts,omitempty"`
	OutputDigestSet []string                   `json:"output_digest_set,omitempty"`
	Metadata        map[string]any             `json:"metadata,omitempty"`
}

// MLRecipeContainerJobClient is the narrow client boundary used by the Loom/container adapter.
type MLRecipeContainerJobClient interface {
	DispatchRecipeContainerJob(ctx context.Context, job MLRecipeContainerJob) (*MLRecipeJobDispatchResult, error)
}

// MLRecipeContainerJob is the adapter-facing container execution payload.
type MLRecipeContainerJob struct {
	RunID          uuid.UUID              `json:"run_id"`
	RecipeID       uuid.UUID              `json:"recipe_id"`
	StepIndex      int                    `json:"step_index"`
	Action         string                 `json:"action"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Container      *MLRecipeContainerSpec `json:"container,omitempty"`
	Inputs         map[string]any         `json:"inputs,omitempty"`
	Parameters     map[string]any         `json:"parameters,omitempty"`
	StepInputs     map[string]any         `json:"step_inputs,omitempty"`
	Metadata       map[string]any         `json:"metadata,omitempty"`
}

// LoomContainerRecipeDispatchAdapter keeps Loom/container dispatch explicit and swappable.
type LoomContainerRecipeDispatchAdapter struct {
	client MLRecipeContainerJobClient
}

func NewLoomContainerRecipeDispatchAdapter(client MLRecipeContainerJobClient) *LoomContainerRecipeDispatchAdapter {
	return &LoomContainerRecipeDispatchAdapter{client: client}
}

func (a *LoomContainerRecipeDispatchAdapter) DispatchStep(ctx context.Context, req MLRecipeJobDispatchRequest) (*MLRecipeJobDispatchResult, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("Loom/container recipe dispatch client is not configured")
	}
	job := MLRecipeContainerJob{
		StepIndex:      req.StepIndex,
		Action:         req.Action,
		IdempotencyKey: req.IdempotencyKey,
		Container:      req.Container,
		StepInputs:     req.Step.Inputs,
		Metadata:       req.Metadata,
	}
	if req.Run != nil {
		job.RunID = req.Run.ID
		job.Inputs = req.Run.Inputs
		job.Parameters = req.Run.Parameters
	}
	if req.Recipe != nil {
		job.RecipeID = req.Recipe.ID
	}
	return a.client.DispatchRecipeContainerJob(ctx, job)
}

// MLRecipeCoordinatorConfig controls durable lease recovery.
type MLRecipeCoordinatorConfig struct {
	RecoveryPollInterval time.Duration
	StaleRunTimeout      time.Duration
}

// MLRecipeCoordinator executes checkpointed, ordered linear ML recipes.
type MLRecipeCoordinator struct {
	registry   *MLRegistryService
	queue      MLRecipeRunQueueRepository
	dispatcher MLRecipeJobDispatcher
	responder  MLRecipeRunResponder
	logger     *zap.Logger
	config     MLRecipeCoordinatorConfig

	runGroup singleflight.Group
	locksMu  sync.Mutex
	runLocks map[uuid.UUID]*sync.Mutex
}

type MLRecipeCoordinatorOption func(*MLRecipeCoordinator)

func WithMLRecipeResponder(responder MLRecipeRunResponder) MLRecipeCoordinatorOption {
	return func(c *MLRecipeCoordinator) { c.responder = responder }
}

func WithMLRecipeCoordinatorConfig(cfg MLRecipeCoordinatorConfig) MLRecipeCoordinatorOption {
	return func(c *MLRecipeCoordinator) {
		if cfg.RecoveryPollInterval > 0 {
			c.config.RecoveryPollInterval = cfg.RecoveryPollInterval
		}
		if cfg.StaleRunTimeout > 0 {
			c.config.StaleRunTimeout = cfg.StaleRunTimeout
		}
	}
}

func NewMLRecipeCoordinator(registry *MLRegistryService, dispatcher MLRecipeJobDispatcher, logger *zap.Logger, opts ...MLRecipeCoordinatorOption) *MLRecipeCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &MLRecipeCoordinator{
		registry:   registry,
		dispatcher: dispatcher,
		logger:     logger.Named("ml-recipe-coordinator"),
		config: MLRecipeCoordinatorConfig{
			RecoveryPollInterval: defaultMLRecipeRecoveryPollInterval,
			StaleRunTimeout:      15 * time.Minute,
		},
		runLocks: make(map[uuid.UUID]*sync.Mutex),
	}
	if registry != nil && registry.repo != nil {
		if queue, ok := registry.repo.(MLRecipeRunQueueRepository); ok {
			c.queue = queue
		}
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *MLRecipeCoordinator) Name() string { return "ml-recipe-recovery" }

// Run performs explicit durable recovery for stored recipe work. It does not poll for Nostr messages.
func (c *MLRecipeCoordinator) Run(ctx context.Context) error {
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

func (c *MLRecipeCoordinator) runRecoveryOnce(ctx context.Context) {
	if err := c.ProcessOnce(ctx); err != nil {
		c.logger.Warn("ML recipe recovery scan failed", zap.Error(err))
	}
}

// ProcessOnce performs one stale-lease recovery and queued-run claim/process cycle.
func (c *MLRecipeCoordinator) ProcessOnce(ctx context.Context) error {
	if c == nil || c.registry == nil || c.queue == nil {
		return nil
	}
	if c.config.StaleRunTimeout > 0 {
		if n, err := c.queue.RequeueStaleMLRecipeRuns(ctx, c.config.StaleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale ML recipe runs", zap.Int("count", n))
		}
	}
	run, err := c.queue.ClaimNextQueuedMLRecipeRun(ctx)
	if err != nil || run == nil {
		return err
	}
	return c.ProcessRecipeRun(ctx, run.ID)
}

// ProcessRecipeRun executes or resumes a non-terminal recipe run from persisted step checkpoints.
func (c *MLRecipeCoordinator) ProcessRecipeRun(ctx context.Context, runID uuid.UUID) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("ML registry is not configured")
	}
	_, err, _ := c.runGroup.Do(runID.String(), func() (any, error) {
		return nil, c.withRunLock(runID, func() error { return c.processRecipeRunLocked(ctx, runID) })
	})
	return err
}

// ProcessRun is an alias for callers that use generic run terminology.
func (c *MLRecipeCoordinator) ProcessRun(ctx context.Context, runID uuid.UUID) error {
	return c.ProcessRecipeRun(ctx, runID)
}

func (c *MLRecipeCoordinator) processRecipeRunLocked(ctx context.Context, runID uuid.UUID) error {
	run, err := c.registry.GetRecipeRun(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("ML recipe run %s not found", runID)
	}
	if isTerminalRunStatus(run.Status) {
		return nil
	}
	recipe, err := c.registry.GetRecipe(ctx, run.RecipeID)
	if err != nil {
		return c.failRecipeRun(ctx, nil, run, "load_recipe", err)
	}
	if recipe == nil {
		return c.failRecipeRun(ctx, nil, run, "load_recipe", fmt.Errorf("ML recipe %s not found", run.RecipeID))
	}
	if c.dispatcher == nil && len(recipe.Steps) > 0 {
		return c.failRecipeRun(ctx, recipe, run, "job_dispatch_unavailable", fmt.Errorf("ML recipe job dispatcher is not configured"))
	}
	resumeRequested := metadataBool(run.Metadata, "manual_resume")
	if run.Status == domain.RunStatusFailed && !resumeRequested {
		return nil
	}
	c.ensureStepStates(recipe, run)
	c.recoverLeasedStepIfNeeded(run)
	if resumeRequested {
		delete(run.Metadata, "manual_resume")
		delete(run.Metadata, "resume_from_step")
		setRunMetadata(run, map[string]any{"manual_resume_consumed_at": time.Now().UTC().Format(time.RFC3339)})
	}
	if run.Status != domain.RunStatusRunning {
		now := time.Now().UTC()
		run.Status = domain.RunStatusRunning
		if run.StartedAt == nil {
			run.StartedAt = &now
		}
		if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
			return err
		}
	}
	if len(recipe.Steps) == 0 {
		return c.succeedRecipeRun(ctx, recipe, run)
	}
	for idx := range recipe.Steps {
		state := &run.StepStates[idx]
		if state.Status == string(domain.RunStatusSucceeded) {
			continue
		}
		if state.Status == string(domain.RunStatusFailed) && !metadataBool(run.Metadata, "manual_resume") {
			return c.failRecipeRun(ctx, recipe, run, "step_failed", fmt.Errorf("recipe step %d is failed and requires explicit retry or manual resume", idx))
		}
		if err := c.executeStep(ctx, recipe, run, idx); err != nil {
			return err
		}
	}
	return c.succeedRecipeRun(ctx, recipe, run)
}

func (c *MLRecipeCoordinator) executeStep(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, idx int) error {
	step := recipe.Steps[idx]
	maxAttempts := explicitStepMaxAttempts(step.RetryPolicy)
	state := &run.StepStates[idx]
	for {
		attempt := metadataInt(state.Metadata, "attempts") + 1
		inputArtifacts := collectRecipeInputArtifacts(run, idx)
		inputDigestSet := buildRecipeInputDigestSet(run, step, idx, inputArtifacts)
		now := time.Now().UTC()
		state.Status = string(domain.RunStatusRunning)
		state.StartedAt = &now
		state.FinishedAt = nil
		state.Error = ""
		state.InputArtifacts = inputArtifacts
		state.InputDigestSet = inputDigestSet
		setStepMetadata(state, map[string]any{"attempts": attempt, "idempotency_key": recipeStepIdempotencyKey(run.ID, idx, inputDigestSet)})
		if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
			return err
		}
		c.publishRecipeStatus(ctx, recipe, run, step.Action, "dispatching ML recipe step")

		var result *MLRecipeJobDispatchResult
		err := c.withRunHeartbeat(ctx, run.ID, func() error {
			var dispatchErr error
			result, dispatchErr = c.dispatcher.DispatchStep(ctx, MLRecipeJobDispatchRequest{
				Recipe:         recipe,
				Run:            run,
				Step:           step,
				StepIndex:      idx,
				Action:         step.Action,
				Runtime:        step.Runtime,
				InputDigestSet: inputDigestSet,
				InputArtifacts: inputArtifacts,
				IdempotencyKey: recipeStepIdempotencyKey(run.ID, idx, inputDigestSet),
				Container:      containerSpecFromStep(step),
				Metadata:       map[string]any{"recipe": recipe.Name, "recipe_version": recipe.Version},
			})
			return dispatchErr
		})
		if err == nil && result == nil {
			err = fmt.Errorf("ML recipe dispatcher returned no result")
		}
		if err == nil && result.Error != "" {
			err = fmt.Errorf("%s", result.Error)
		}
		if err == nil && result.Status != "" && result.Status != domain.RunStatusSucceeded {
			err = fmt.Errorf("%s", firstNonEmptyString(result.Error, result.Message, fmt.Sprintf("ML recipe step returned status %s", result.Status)))
		}
		retryable := result == nil || result.Retryable
		if err == nil {
			finished := time.Now().UTC()
			state.Status = string(domain.RunStatusSucceeded)
			state.FinishedAt = &finished
			state.OutputArtifacts = append([]uuid.UUID(nil), result.OutputArtifacts...)
			setStepMetadata(state, map[string]any{"message": result.Message, "output_digest_set": append([]string(nil), result.OutputDigestSet...), "dispatch": result.Metadata})
			if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
				return err
			}
			return nil
		}

		finished := time.Now().UTC()
		state.Status = string(domain.RunStatusFailed)
		state.FinishedAt = &finished
		state.Error = err.Error()
		setStepMetadata(state, map[string]any{"last_error": err.Error()})
		if attempt < maxAttempts && retryable {
			state.Status = string(domain.RunStatusQueued)
			state.FinishedAt = nil
			if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
				return err
			}
			continue
		}
		_ = c.registry.CreateOrUpdateRecipeRun(ctx, run)
		return c.failRecipeRun(ctx, recipe, run, step.Action, err)
	}
}

// ResumeRecipeRun resumes deterministically from the first non-succeeded step.
func (c *MLRecipeCoordinator) ResumeRecipeRun(ctx context.Context, runID uuid.UUID) error {
	return c.ResumeRecipeRunFromStep(ctx, runID, -1)
}

// RetryRecipeRunStep explicitly retries a failed step and clears later checkpoints.
func (c *MLRecipeCoordinator) RetryRecipeRunStep(ctx context.Context, runID uuid.UUID, stepIndex int) error {
	return c.ResumeRecipeRunFromStep(ctx, runID, stepIndex)
}

func (c *MLRecipeCoordinator) ResumeRecipeRunFromStep(ctx context.Context, runID uuid.UUID, stepIndex int) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("ML registry is not configured")
	}
	if err := c.withRunLock(runID, func() error {
		run, err := c.registry.GetRecipeRun(ctx, runID)
		if err != nil {
			return err
		}
		if run == nil {
			return fmt.Errorf("ML recipe run %s not found", runID)
		}
		recipe, err := c.registry.GetRecipe(ctx, run.RecipeID)
		if err != nil {
			return err
		}
		if recipe == nil {
			return fmt.Errorf("ML recipe %s not found", run.RecipeID)
		}
		c.ensureStepStates(recipe, run)
		if stepIndex < 0 {
			stepIndex = firstNonSucceededStep(run.StepStates)
		}
		if stepIndex < 0 || stepIndex >= len(run.StepStates) {
			return fmt.Errorf("resume step index %d is out of range", stepIndex)
		}
		for i := stepIndex; i < len(run.StepStates); i++ {
			run.StepStates[i].Status = string(domain.RunStatusQueued)
			run.StepStates[i].StartedAt = nil
			run.StepStates[i].FinishedAt = nil
			run.StepStates[i].Error = ""
			if i > stepIndex {
				run.StepStates[i].OutputArtifacts = nil
			}
		}
		run.Status = domain.RunStatusQueued
		run.Error = ""
		run.FinishedAt = nil
		setRunMetadata(run, map[string]any{"manual_resume": true, "resume_from_step": stepIndex})
		return c.registry.CreateOrUpdateRecipeRun(ctx, run)
	}); err != nil {
		return err
	}
	return c.ProcessRecipeRun(ctx, runID)
}

func (c *MLRecipeCoordinator) ensureStepStates(recipe *domain.MLRecipe, run *domain.MLRecipeRun) {
	if len(run.StepStates) != len(recipe.Steps) {
		states := make([]domain.MLRecipeRunStepState, len(recipe.Steps))
		copy(states, run.StepStates)
		run.StepStates = states
	}
	for i := range recipe.Steps {
		step := recipe.Steps[i]
		state := &run.StepStates[i]
		state.Index = i
		state.Name = step.Name
		state.Action = step.Action
		if state.Status == "" {
			state.Status = string(domain.RunStatusQueued)
		}
	}
}

func (c *MLRecipeCoordinator) recoverLeasedStepIfNeeded(run *domain.MLRecipeRun) {
	if !metadataBool(run.Metadata, "lease_recovered") {
		return
	}
	for i := range run.StepStates {
		if run.StepStates[i].Status == string(domain.RunStatusRunning) {
			run.StepStates[i].Status = string(domain.RunStatusQueued)
			run.StepStates[i].StartedAt = nil
			setStepMetadata(&run.StepStates[i], map[string]any{"lease_recovered": true})
			return
		}
	}
}

func (c *MLRecipeCoordinator) succeedRecipeRun(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun) error {
	now := time.Now().UTC()
	run.Status = domain.RunStatusSucceeded
	run.Error = ""
	run.FinishedAt = &now
	run.Result = map[string]any{"status": "succeeded", "outputs": recipe.Outputs, "step_count": len(run.StepStates)}
	if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
		return err
	}
	c.publishRecipeResult(ctx, recipe, run, "succeeded", "ML recipe run completed")
	return nil
}

func (c *MLRecipeCoordinator) failRecipeRun(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("ML recipe run failed")
	}
	now := time.Now().UTC()
	run.Status = domain.RunStatusFailed
	run.Error = cause.Error()
	run.FinishedAt = &now
	setRunMetadata(run, map[string]any{"failed_step": step})
	_ = c.registry.CreateOrUpdateRecipeRun(ctx, run)
	c.publishRecipeError(ctx, recipe, run, step, cause)
	return cause
}

func (c *MLRecipeCoordinator) withRunHeartbeat(ctx context.Context, runID uuid.UUID, fn func() error) error {
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
				c.touchRecipeRun(heartbeatCtx, runID)
			}
		}
	}()
	err := fn()
	cancel()
	<-done
	return err
}

func (c *MLRecipeCoordinator) touchRecipeRun(ctx context.Context, runID uuid.UUID) {
	run, err := c.registry.GetRecipeRun(ctx, runID)
	if err != nil || run == nil || run.Status != domain.RunStatusRunning {
		return
	}
	if err := c.registry.CreateOrUpdateRecipeRun(ctx, run); err != nil {
		c.logger.Warn("failed to heartbeat ML recipe run", zap.String("run_id", runID.String()), zap.Error(err))
	}
}

func (c *MLRecipeCoordinator) withRunLock(runID uuid.UUID, fn func() error) error {
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

func (c *MLRecipeCoordinator) publishRecipeStatus(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishRecipeRunStatus(ctx, recipe, run, step, message); err != nil {
		c.logger.Warn("publish ML recipe status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *MLRecipeCoordinator) publishRecipeResult(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, status, message string) {
	if c.responder == nil {
		return
	}
	if err := c.responder.PublishRecipeRunResult(ctx, recipe, run, status, message); err != nil {
		c.logger.Warn("publish ML recipe result failed", zap.String("status", status), zap.Error(err))
	}
}

func (c *MLRecipeCoordinator) publishRecipeError(ctx context.Context, recipe *domain.MLRecipe, run *domain.MLRecipeRun, step string, cause error) {
	if c.responder == nil || cause == nil {
		return
	}
	if err := c.responder.PublishRecipeRunError(ctx, recipe, run, step, cause); err != nil {
		c.logger.Warn("publish ML recipe error failed", zap.String("step", step), zap.Error(err))
	}
}

func explicitStepMaxAttempts(policy map[string]any) int {
	if policy == nil {
		return 1
	}
	for _, key := range []string{"max_attempts", "attempts"} {
		if v, ok := intValue(policy[key]); ok && v > 0 {
			return v
		}
	}
	for _, key := range []string{"max_retries", "retries"} {
		if v, ok := intValue(policy[key]); ok && v >= 0 {
			return v + 1
		}
	}
	return 1
}

func recipeStepIdempotencyKey(runID uuid.UUID, idx int, inputDigestSet []string) string {
	joined := strings.Join(inputDigestSet, ",")
	if len(joined) > 16 {
		joined = joined[:16]
	}
	return fmt.Sprintf("ml-recipe:%s:%d:%s", runID.String(), idx, joined)
}

func buildRecipeInputDigestSet(run *domain.MLRecipeRun, step domain.MLRecipeStep, idx int, artifactIDs []uuid.UUID) []string {
	payload := map[string]any{"run_inputs": run.Inputs, "parameters": run.Parameters, "step_inputs": step.Inputs, "step_index": idx, "input_artifacts": artifactIDs}
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	return []string{hex.EncodeToString(sum[:])}
}

func collectRecipeInputArtifacts(run *domain.MLRecipeRun, uptoStep int) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	var appendFrom func(raw any)
	appendFrom = func(raw any) {
		switch v := raw.(type) {
		case string:
			if id, err := uuid.Parse(v); err == nil {
				seen[id] = struct{}{}
			}
		case []string:
			for _, item := range v {
				appendFrom(item)
			}
		case []any:
			for _, item := range v {
				appendFrom(item)
			}
		}
	}
	if run != nil {
		appendFrom(run.Inputs["artifact_id"])
		appendFrom(run.Inputs["artifact_ids"])
		for i := 0; i < uptoStep && i < len(run.StepStates); i++ {
			for _, id := range run.StepStates[i].OutputArtifacts {
				seen[id] = struct{}{}
			}
		}
	}
	out := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func containerSpecFromStep(step domain.MLRecipeStep) *MLRecipeContainerSpec {
	raw, ok := step.Metadata["container"].(map[string]any)
	if !ok {
		raw, _ = step.Inputs["container"].(map[string]any)
	}
	if raw == nil {
		return nil
	}
	spec := &MLRecipeContainerSpec{Resources: map[string]any{}}
	if v, ok := stringValue(raw["image"]); ok {
		spec.Image = v
	}
	if v, ok := stringSliceValue(raw["command"]); ok {
		spec.Command = v
	}
	if v, ok := stringValue(raw["workdir"]); ok {
		spec.Workdir = v
	}
	if env, ok := raw["env"].(map[string]any); ok {
		spec.Env = map[string]string{}
		for k, v := range env {
			if s, ok := stringValue(v); ok {
				spec.Env[k] = s
			}
		}
	}
	if resources, ok := raw["resources"].(map[string]any); ok {
		spec.Resources = resources
	}
	if len(spec.Resources) == 0 {
		spec.Resources = nil
	}
	return spec
}

func firstNonSucceededStep(states []domain.MLRecipeRunStepState) int {
	for i := range states {
		if states[i].Status != string(domain.RunStatusSucceeded) {
			return i
		}
	}
	return -1
}

func setRunMetadata(run *domain.MLRecipeRun, values map[string]any) {
	if run.Metadata == nil {
		run.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			run.Metadata[k] = v
		}
	}
}

func setStepMetadata(state *domain.MLRecipeRunStepState, values map[string]any) {
	if state.Metadata == nil {
		state.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			state.Metadata[k] = v
		}
	}
}

func metadataBool(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	switch v := values[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	default:
		return false
	}
}

func metadataInt(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	if v, ok := intValue(values[key]); ok {
		return v
	}
	return 0
}
