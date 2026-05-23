package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

const (
	EventContinuityRecipeRunStarted   events.EventType = "continuity.recipe.run_started"
	EventContinuityRecipeStepStarted  events.EventType = "continuity.recipe.step_started"
	EventContinuityRecipeStepProgress events.EventType = "continuity.recipe.step_progress"
	EventContinuityRecipeStepFailed   events.EventType = "continuity.recipe.step_failed"
	EventContinuityRecipeRunCompleted events.EventType = "continuity.recipe.run_completed"
	EventContinuityRecipeRunFailed    events.EventType = "continuity.recipe.run_failed"
)

const (
	ContinuityRecipeRunStatusStarted   = "started"
	ContinuityRecipeRunStatusCompleted = "completed"
	ContinuityRecipeRunStatusFailed    = "failed"

	ContinuityRecipeStepStatusStarted   = "started"
	ContinuityRecipeStepStatusCompleted = "completed"
	ContinuityRecipeStepStatusFailed    = "failed"
)

// ContinuityRecipeExecutor runs shared failover and recovery continuity recipes.
type ContinuityRecipeExecutor interface {
	ExecuteFailover(ctx context.Context, req FailoverExecutionRequest) error
	ExecuteRecovery(ctx context.Context, req RecoveryExecutionRequest) error
}

// FailoverExecutionRequest describes one failover recipe run.
type FailoverExecutionRequest struct {
	ServiceKey            string
	RecipeName            string
	TargetProfile         domain.ContinuityMode
	PrimaryWorkerPubKey   string
	SelectedStandbyPubKey string
	RequestedBy           string
	RunID                 string
	Recipe                domain.ContinuityRecipe
}

// RecoveryExecutionRequest describes one recovery recipe run.
type RecoveryExecutionRequest struct {
	ServiceKey            string
	RecipeName            string
	TargetProfile         domain.ContinuityMode
	PrimaryWorkerPubKey   string
	SelectedStandbyPubKey string
	RequestedBy           string
	RunID                 string
	Recipe                domain.ContinuityRecipe
}

// ContinuityRecipeRunContext is passed to action handlers and emitted with run events.
type ContinuityRecipeRunContext struct {
	ServiceKey            string                      `json:"service_key"`
	RecipeName            string                      `json:"recipe_name"`
	RecipeKind            domain.ContinuityRecipeKind `json:"recipe_kind"`
	TargetProfile         domain.ContinuityMode       `json:"target_profile"`
	PrimaryWorkerPubKey   string                      `json:"primary_worker_pubkey,omitempty"`
	SelectedStandbyPubKey string                      `json:"selected_standby_pubkey,omitempty"`
	RequestedBy           string                      `json:"requested_by"`
	RunID                 string                      `json:"run_id"`
	StepCount             int                         `json:"step_count"`
	StartedAt             time.Time                   `json:"started_at"`
}

// ContinuityRecipeProgressEvent is the internal progress payload emitted during recipe execution.
type ContinuityRecipeProgressEvent struct {
	ContinuityRecipeRunContext
	Status    string    `json:"status"`
	StepIndex int       `json:"step_index,omitempty"`
	StepName  string    `json:"step_name,omitempty"`
	Action    string    `json:"action,omitempty"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

// ContinuityWakeAdapter sends Wake-on-LAN magic packets.
type ContinuityWakeAdapter interface {
	WakeOnLAN(ctx context.Context, macAddress string) error
}

// ContinuityHeartbeatWaiter polls heartbeat status for a worker.
type ContinuityHeartbeatWaiter interface {
	WaitForHeartbeat(ctx context.Context, workerPubKey string, timeout time.Duration) error
}

// ContinuityRuntimeAdapter handles service lifecycle on workers.
type ContinuityRuntimeAdapter interface {
	DeployService(ctx context.Context, serviceKey string, workerPubKey string, params map[string]string) error
	StopService(ctx context.Context, serviceKey string, workerPubKey string, params map[string]string) error
	MoveService(ctx context.Context, serviceKey string, fromWorker string, toWorker string, params map[string]string) error
}

// ContinuityStorageAdapter handles volume and backup operations.
type ContinuityStorageAdapter interface {
	MountVolume(ctx context.Context, source string, params map[string]string) error
	RestoreBackup(ctx context.Context, snapshotID string, params map[string]string) error
	RestoreSCB(ctx context.Context, source string, params map[string]string) error
}

// ContinuityDNSAdapter handles DNS route restoration.
type ContinuityDNSAdapter interface {
	RestoreDNSRoutes(ctx context.Context, serviceKey string, params map[string]string) error
}

// ContinuityRecipeActionHandler executes one recipe step.
type ContinuityRecipeActionHandler func(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error

type continuityRecipeExecutor struct {
	publisher       events.Publisher
	logger          *zap.Logger
	registry        map[string]continuityRecipeAction
	locks           sync.Map
	now             func() time.Time
	wakeAdapter     ContinuityWakeAdapter
	heartbeatWaiter ContinuityHeartbeatWaiter
	runtimeAdapter  ContinuityRuntimeAdapter
	storageAdapter  ContinuityStorageAdapter
	dnsAdapter      ContinuityDNSAdapter
}

type continuityRecipeAction struct {
	handler  ContinuityRecipeActionHandler
	validate continuityActionValidator
}

type continuityRecipeExecutorOption func(*continuityRecipeExecutor)

// WithContinuityRecipeActionHandler replaces or adds the handler for an action.
func WithContinuityRecipeActionHandler(action string, handler ContinuityRecipeActionHandler) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		action = strings.TrimSpace(action)
		if action == "" || handler == nil {
			return
		}
		entry := e.registry[action]
		entry.handler = handler
		e.registry[action] = entry
	}
}

// WithContinuityRecipeLogger sets the executor logger.
func WithContinuityRecipeLogger(logger *zap.Logger) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		if logger != nil {
			e.logger = logger.Named("continuity-recipe-executor")
		}
	}
}

// WithContinuityWakeAdapter sets the adapter used by wake_on_lan actions.
func WithContinuityWakeAdapter(adapter ContinuityWakeAdapter) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		e.wakeAdapter = adapter
	}
}

// WithContinuityHeartbeatWaiter sets the adapter used by wait_for_heartbeat actions.
func WithContinuityHeartbeatWaiter(waiter ContinuityHeartbeatWaiter) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		e.heartbeatWaiter = waiter
	}
}

// WithContinuityRuntimeAdapter sets the adapter used by runtime lifecycle actions.
func WithContinuityRuntimeAdapter(adapter ContinuityRuntimeAdapter) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		e.runtimeAdapter = adapter
	}
}

// WithContinuityStorageAdapter sets the adapter used by storage and restore actions.
func WithContinuityStorageAdapter(adapter ContinuityStorageAdapter) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		e.storageAdapter = adapter
	}
}

// WithContinuityDNSAdapter sets the adapter used by restore_dns_routes actions.
func WithContinuityDNSAdapter(adapter ContinuityDNSAdapter) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		e.dnsAdapter = adapter
	}
}

func withContinuityRecipeClock(now func() time.Time) continuityRecipeExecutorOption {
	return func(e *continuityRecipeExecutor) {
		if now != nil {
			e.now = now
		}
	}
}

// NewContinuityRecipeExecutor creates the shared failover/recovery recipe executor.
func NewContinuityRecipeExecutor(publisher events.Publisher, opts ...continuityRecipeExecutorOption) ContinuityRecipeExecutor {
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	e := &continuityRecipeExecutor{
		publisher: publisher,
		logger:    zap.NewNop().Named("continuity-recipe-executor"),
		registry:  map[string]continuityRecipeAction{},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	e.registry = e.defaultActionRegistry()
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *continuityRecipeExecutor) ExecuteFailover(ctx context.Context, req FailoverExecutionRequest) error {
	return e.execute(ctx, domain.ContinuityRecipeKindFailover, executionRequest{
		ServiceKey:            req.ServiceKey,
		RecipeName:            req.RecipeName,
		TargetProfile:         req.TargetProfile,
		PrimaryWorkerPubKey:   req.PrimaryWorkerPubKey,
		SelectedStandbyPubKey: req.SelectedStandbyPubKey,
		RequestedBy:           req.RequestedBy,
		RunID:                 req.RunID,
		Recipe:                req.Recipe,
	})
}

func (e *continuityRecipeExecutor) ExecuteRecovery(ctx context.Context, req RecoveryExecutionRequest) error {
	return e.execute(ctx, domain.ContinuityRecipeKindRecovery, executionRequest{
		ServiceKey:            req.ServiceKey,
		RecipeName:            req.RecipeName,
		TargetProfile:         req.TargetProfile,
		PrimaryWorkerPubKey:   req.PrimaryWorkerPubKey,
		SelectedStandbyPubKey: req.SelectedStandbyPubKey,
		RequestedBy:           req.RequestedBy,
		RunID:                 req.RunID,
		Recipe:                req.Recipe,
	})
}

type executionRequest struct {
	ServiceKey            string
	RecipeName            string
	TargetProfile         domain.ContinuityMode
	PrimaryWorkerPubKey   string
	SelectedStandbyPubKey string
	RequestedBy           string
	RunID                 string
	Recipe                domain.ContinuityRecipe
}

func (e *continuityRecipeExecutor) execute(ctx context.Context, kind domain.ContinuityRecipeKind, req executionRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	prepared, err := e.prepareRun(kind, req)
	if err != nil {
		return err
	}
	lock := e.serviceLock(prepared.run.ServiceKey)
	lock.Lock()
	defer lock.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	e.publish(ctx, EventContinuityRecipeRunStarted, prepared.run, ContinuityRecipeRunStatusStarted, 0, domain.RecipeStep{}, nil)

	for i, step := range prepared.recipe.Steps {
		if err := ctx.Err(); err != nil {
			e.publish(ctx, EventContinuityRecipeRunFailed, prepared.run, ContinuityRecipeRunStatusFailed, i+1, step, err)
			return err
		}
		e.publish(ctx, EventContinuityRecipeStepStarted, prepared.run, ContinuityRecipeStepStatusStarted, i+1, step, nil)
		entry := e.registry[step.Action]
		stepCtx := ctx
		var cancel context.CancelFunc
		if step.Timeout > 0 {
			stepCtx, cancel = context.WithTimeout(ctx, step.Timeout)
		}
		err := entry.handler(stepCtx, step, prepared.run)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			e.publish(ctx, EventContinuityRecipeStepFailed, prepared.run, ContinuityRecipeStepStatusFailed, i+1, step, err)
			e.publish(ctx, EventContinuityRecipeRunFailed, prepared.run, ContinuityRecipeRunStatusFailed, i+1, step, err)
			return err
		}
		e.publish(ctx, EventContinuityRecipeStepProgress, prepared.run, ContinuityRecipeStepStatusCompleted, i+1, step, nil)
	}

	e.publish(ctx, EventContinuityRecipeRunCompleted, prepared.run, ContinuityRecipeRunStatusCompleted, len(prepared.recipe.Steps), domain.RecipeStep{}, nil)
	return nil
}

type preparedContinuityRun struct {
	recipe domain.ContinuityRecipe
	run    ContinuityRecipeRunContext
}

func (e *continuityRecipeExecutor) prepareRun(kind domain.ContinuityRecipeKind, req executionRequest) (*preparedContinuityRun, error) {
	recipe := req.Recipe
	if err := recipe.Validate(); err != nil {
		return nil, err
	}
	if recipe.Kind != kind {
		return nil, fmt.Errorf("continuity recipe kind %q does not match requested execution kind %q", recipe.Kind, kind)
	}

	serviceKey := strings.TrimSpace(req.ServiceKey)
	if serviceKey == "" {
		serviceKey = recipe.ServiceKey
	}
	if serviceKey == "" {
		return nil, fmt.Errorf("service key is required")
	}
	if recipe.ServiceKey != serviceKey {
		return nil, fmt.Errorf("continuity recipe service %q does not match request service %q", recipe.ServiceKey, serviceKey)
	}

	recipeName := strings.TrimSpace(req.RecipeName)
	if recipeName == "" {
		recipeName = recipe.Name
	}
	if recipe.Name != recipeName {
		return nil, fmt.Errorf("continuity recipe name %q does not match request recipe %q", recipe.Name, recipeName)
	}

	targetProfile := req.TargetProfile
	if targetProfile == "" {
		if kind == domain.ContinuityRecipeKindFailover {
			targetProfile = domain.ContinuityModeDegraded
		} else {
			targetProfile = domain.ContinuityModeFull
		}
	}
	if !targetProfile.IsValid() {
		return nil, fmt.Errorf("continuity target profile %q is not valid", targetProfile)
	}

	run := ContinuityRecipeRunContext{
		ServiceKey:            serviceKey,
		RecipeName:            recipeName,
		RecipeKind:            kind,
		TargetProfile:         targetProfile,
		PrimaryWorkerPubKey:   strings.TrimSpace(req.PrimaryWorkerPubKey),
		SelectedStandbyPubKey: strings.TrimSpace(req.SelectedStandbyPubKey),
		RequestedBy:           strings.TrimSpace(req.RequestedBy),
		RunID:                 strings.TrimSpace(req.RunID),
		StepCount:             len(recipe.Steps),
		StartedAt:             e.now(),
	}
	if run.RequestedBy == "" {
		return nil, fmt.Errorf("requested_by is required")
	}
	if run.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	for i, step := range recipe.Steps {
		entry := e.registry[step.Action]
		if entry.handler == nil {
			return nil, fmt.Errorf("continuity recipe step %d action %q is not registered", i, step.Action)
		}
		if entry.validate != nil {
			if err := entry.validate(step, run); err != nil {
				return nil, fmt.Errorf("validating continuity recipe step %d action %q: %w", i, step.Action, err)
			}
		}
	}
	return &preparedContinuityRun{recipe: recipe, run: run}, nil
}

func (e *continuityRecipeExecutor) serviceLock(serviceKey string) *sync.Mutex {
	lock, _ := e.locks.LoadOrStore(serviceKey, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (e *continuityRecipeExecutor) publish(ctx context.Context, eventType events.EventType, run ContinuityRecipeRunContext, status string, stepIndex int, step domain.RecipeStep, err error) {
	payload := ContinuityRecipeProgressEvent{
		ContinuityRecipeRunContext: run,
		Status:                     status,
		StepIndex:                  stepIndex,
		StepName:                   step.Name,
		Action:                     step.Action,
		At:                         e.now(),
	}
	if err != nil {
		payload.Error = err.Error()
	}
	e.publisher.Publish(ctx, events.Event{Type: eventType, EntityID: run.RunID, Data: payload})
}

func (e *continuityRecipeExecutor) defaultActionRegistry() map[string]continuityRecipeAction {
	return map[string]continuityRecipeAction{
		domain.RecipeActionWakeOnLAN:        e.adapterAction(domain.RecipeActionWakeOnLAN, requireAnyParam("mac_address", "mac"), e.handleWakeOnLAN),
		domain.RecipeActionWaitHeartbeat:    e.adapterAction(domain.RecipeActionWaitHeartbeat, requireAnyParamOrContext("worker", "target"), e.handleWaitForHeartbeat),
		domain.RecipeActionMountVolume:      e.adapterAction(domain.RecipeActionMountVolume, requireAnyParam("source", "volume"), e.handleMountVolume),
		domain.RecipeActionRestoreBackup:    e.adapterAction(domain.RecipeActionRestoreBackup, requireAnyParam("backup_run_id", "snapshot_id", "source"), e.handleRestoreBackup),
		domain.RecipeActionRestoreSCB:       e.adapterAction(domain.RecipeActionRestoreSCB, requireAnyParam("backup_run_id", "snapshot_id", "source", "scb_path"), e.handleRestoreSCB),
		domain.RecipeActionDeployService:    e.adapterAction(domain.RecipeActionDeployService, nil, e.handleDeployService),
		domain.RecipeActionPublishEndpoint:  e.adapterAction(domain.RecipeActionPublishEndpoint, nil, e.handleEventOnlyAction),
		domain.RecipeActionEmitEvent:        e.adapterAction(domain.RecipeActionEmitEvent, requireAnyParam("type", "event_type"), e.handleEventOnlyAction),
		domain.RecipeActionSyncRelayState:   e.adapterAction(domain.RecipeActionSyncRelayState, nil, e.handleEventOnlyAction),
		domain.RecipeActionStopService:      e.adapterAction(domain.RecipeActionStopService, nil, e.handleStopService),
		domain.RecipeActionRestoreDNSRoutes: e.adapterAction(domain.RecipeActionRestoreDNSRoutes, nil, e.handleRestoreDNSRoutes),
		domain.RecipeActionMoveService:      e.adapterAction(domain.RecipeActionMoveService, nil, e.handleMoveService),
		domain.RecipeActionReEnableAgents:   e.adapterAction(domain.RecipeActionReEnableAgents, nil, e.handleEventOnlyAction),
	}
}

type continuityActionValidator func(step domain.RecipeStep, run ContinuityRecipeRunContext) error

func (e *continuityRecipeExecutor) adapterAction(action string, validate continuityActionValidator, handler ContinuityRecipeActionHandler) continuityRecipeAction {
	return continuityRecipeAction{
		validate: validate,
		handler: func(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
			if validate != nil {
				if err := validate(step, run); err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := handler(ctx, step, run); err != nil {
				return fmt.Errorf("%s action failed: %w", action, err)
			}
			return nil
		},
	}
}

func (e *continuityRecipeExecutor) handleWakeOnLAN(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.wakeAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionWakeOnLAN, run, step)
		return nil
	}
	macAddress := firstParam(step.Params, "mac_address", "mac")
	return e.wakeAdapter.WakeOnLAN(ctx, macAddress)
}

func (e *continuityRecipeExecutor) handleWaitForHeartbeat(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.heartbeatWaiter == nil {
		e.logMissingAdapter(domain.RecipeActionWaitHeartbeat, run, step)
		return nil
	}
	workerPubKey := firstContinuityNonEmpty(firstParam(step.Params, "worker", "target"), run.SelectedStandbyPubKey, run.PrimaryWorkerPubKey)
	return e.heartbeatWaiter.WaitForHeartbeat(ctx, workerPubKey, step.Timeout)
}

func (e *continuityRecipeExecutor) handleMountVolume(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.storageAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionMountVolume, run, step)
		return nil
	}
	return e.storageAdapter.MountVolume(ctx, firstParam(step.Params, "source", "volume"), copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleRestoreBackup(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.storageAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionRestoreBackup, run, step)
		return nil
	}
	return e.storageAdapter.RestoreBackup(ctx, firstParam(step.Params, "snapshot_id", "backup_run_id", "source"), copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleRestoreSCB(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.storageAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionRestoreSCB, run, step)
		return nil
	}
	return e.storageAdapter.RestoreSCB(ctx, firstParam(step.Params, "source", "scb_path", "snapshot_id", "backup_run_id"), copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleDeployService(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.runtimeAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionDeployService, run, step)
		return nil
	}
	workerPubKey := firstContinuityNonEmpty(firstParam(step.Params, "worker", "target"), run.SelectedStandbyPubKey, run.PrimaryWorkerPubKey)
	return e.runtimeAdapter.DeployService(ctx, run.ServiceKey, workerPubKey, copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleStopService(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.runtimeAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionStopService, run, step)
		return nil
	}
	workerPubKey := firstContinuityNonEmpty(firstParam(step.Params, "worker", "target"), run.PrimaryWorkerPubKey, run.SelectedStandbyPubKey)
	return e.runtimeAdapter.StopService(ctx, run.ServiceKey, workerPubKey, copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleMoveService(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.runtimeAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionMoveService, run, step)
		return nil
	}
	fromWorker := firstContinuityNonEmpty(firstParam(step.Params, "from_worker", "source_worker", "from", "source"), run.SelectedStandbyPubKey, run.PrimaryWorkerPubKey)
	toWorker := firstContinuityNonEmpty(firstParam(step.Params, "to_worker", "target_worker", "to", "target"), run.PrimaryWorkerPubKey, run.SelectedStandbyPubKey)
	return e.runtimeAdapter.MoveService(ctx, run.ServiceKey, fromWorker, toWorker, copyParams(step.Params))
}

func (e *continuityRecipeExecutor) handleRestoreDNSRoutes(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if e.dnsAdapter == nil {
		e.logMissingAdapter(domain.RecipeActionRestoreDNSRoutes, run, step)
		return nil
	}
	return e.dnsAdapter.RestoreDNSRoutes(ctx, run.ServiceKey, copyParams(step.Params))
}

// Event-only continuity actions publish deterministic internal events so existing
// Nostr forwarding can turn recipe state transitions into relay events.
func (e *continuityRecipeExecutor) handleEventOnlyAction(ctx context.Context, step domain.RecipeStep, run ContinuityRecipeRunContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.publisher.Publish(ctx, events.Event{
		Type:     continuityActionEventType(step),
		EntityID: run.RunID,
		Data: ContinuityRecipeProgressEvent{
			ContinuityRecipeRunContext: run,
			Status:                     ContinuityRecipeStepStatusCompleted,
			StepName:                   step.Name,
			Action:                     step.Action,
			At:                         e.now(),
		},
	})
	return nil
}

func (e *continuityRecipeExecutor) logMissingAdapter(action string, run ContinuityRecipeRunContext, step domain.RecipeStep) {
	e.logger.Warn("continuity recipe action adapter is not configured",
		zap.String("action", action),
		zap.String("run_id", run.RunID),
		zap.String("service_key", run.ServiceKey),
		zap.String("recipe_kind", string(run.RecipeKind)),
		zap.String("recipe_name", run.RecipeName),
		zap.String("step_name", step.Name),
	)
}

func continuityActionEventType(step domain.RecipeStep) events.EventType {
	if step.Action == domain.RecipeActionEmitEvent {
		return events.EventType(firstParam(step.Params, "type", "event_type"))
	}
	return events.EventType("continuity.recipe.action." + step.Action)
}

func copyParams(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
}

func firstParam(params map[string]string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(params[name]); value != "" {
			return value
		}
	}
	return ""
}

func firstContinuityNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func requireAnyParam(names ...string) continuityActionValidator {
	return func(step domain.RecipeStep, _ ContinuityRecipeRunContext) error {
		for _, name := range names {
			if strings.TrimSpace(step.Params[name]) != "" {
				return nil
			}
		}
		return fmt.Errorf("action %q requires one of params: %s", step.Action, strings.Join(names, ", "))
	}
}

func requireAnyParamOrContext(names ...string) continuityActionValidator {
	return func(step domain.RecipeStep, run ContinuityRecipeRunContext) error {
		if err := requireAnyParam(names...)(step, run); err == nil {
			return nil
		}
		if strings.TrimSpace(run.SelectedStandbyPubKey) != "" || strings.TrimSpace(run.PrimaryWorkerPubKey) != "" {
			return nil
		}
		return fmt.Errorf("action %q requires one of params: %s or a worker pubkey in the request", step.Action, strings.Join(names, ", "))
	}
}
