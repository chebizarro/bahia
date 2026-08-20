package saga

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Reality is the inspected external state before a possible mutation.
type Reality string

const (
	RealityAbsent   Reality = "absent"
	RealityMatching Reality = "matching"
	RealityConflict Reality = "conflict"
)

// Observation describes actual external state. A matching observation must include lineage.
type Observation struct {
	Reality   Reality    `json:"reality"`
	Resources []Resource `json:"resources,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

// StageDriver is the clean integration boundary for Signet, runtime, config, relay, verification, and projection siblings.
// Implementations inspect before Apply and must correlate ambiguous mutations by the supplied idempotency key.
type StageDriver interface {
	Stage() Stage
	Inspect(context.Context, Snapshot, *Resource) (Observation, error)
	Apply(context.Context, Snapshot, string) error
	Compensate(context.Context, Snapshot, Resource, string) error
}

// TerminalDriver keeps the correlated kind 7950 result and kind 31951 projection aligned with terminal saga state.
type TerminalDriver interface {
	InspectTerminal(context.Context, Snapshot, Stage, *Failure) (Observation, error)
	PublishTerminal(context.Context, Snapshot, Stage, *Failure, string) error
}

// Snapshot prevents drivers from mutating authoritative state in memory.
type Snapshot struct {
	RequestID string
	RunID     string
	AgentID   string
	SpecHash  string
	Stage     Stage
	Resources []Resource
}

func snapshot(run *Run) Snapshot {
	return Snapshot{RequestID: run.RequestID, RunID: run.RunID, AgentID: run.AgentID, SpecHash: run.SpecHash, Stage: run.Stage, Resources: append([]Resource(nil), run.Resources...)}
}

// SafeError is the only adapter error whose message is persisted or published.
type SafeError struct {
	Code, Message string
	Retryable     bool
	Cause         error
}

func (e *SafeError) Error() string {
	if e == nil {
		return ""
	}
	return safePublicMessage(safePublicCode(e.Code))
}
func (e *SafeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Report is secret-free operator inspection/dry-run output.
type Report struct {
	RequestID string   `json:"request_id"`
	RunID     string   `json:"run_id"`
	Stage     Stage    `json:"stage"`
	Version   uint64   `json:"version"`
	DryRun    bool     `json:"dry_run"`
	Actions   []Action `json:"actions,omitempty"`
	Failure   *Failure `json:"failure,omitempty"`
}

type Action struct {
	Stage       Stage  `json:"stage"`
	Operation   string `json:"operation"`
	ResourceRef string `json:"resource_ref,omitempty"`
}

// Engine reconciles persisted intent against external reality one checkpoint at a time.
type Engine struct {
	store     Store
	drivers   map[Stage]StageDriver
	now       func() time.Time
	retention RetentionPolicy
	terminal  TerminalDriver
}

type Option func(*Engine)

func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}
func WithRetention(policy RetentionPolicy) Option { return func(e *Engine) { e.retention = policy } }

func NewEngine(store Store, drivers []StageDriver, opts ...Option) (*Engine, error) {
	if store == nil {
		return nil, errors.New("saga store is required")
	}
	e := &Engine{store: store, drivers: make(map[Stage]StageDriver), now: time.Now, retention: RetentionPolicy{FailedFor: 30 * 24 * time.Hour, RolledBackFor: 7 * 24 * time.Hour}}
	for _, driver := range drivers {
		if driver == nil {
			return nil, errors.New("nil saga stage driver")
		}
		stage := driver.Stage()
		if !isForwardStage(stage) {
			return nil, fmt.Errorf("driver stage %q is not a forward stage", stage)
		}
		if _, exists := e.drivers[stage]; exists {
			return nil, fmt.Errorf("duplicate driver for stage %q", stage)
		}
		e.drivers[stage] = driver
	}
	for _, stage := range forwardStages {
		if e.drivers[stage] == nil {
			return nil, fmt.Errorf("missing driver for stage %q", stage)
		}
	}
	terminal, ok := e.drivers[StageRunning].(TerminalDriver)
	if !ok {
		return nil, errors.New("running stage driver must implement terminal projection reconciliation")
	}
	e.terminal = terminal
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e, nil
}

func isForwardStage(stage Stage) bool {
	for _, candidate := range forwardStages {
		if candidate == stage {
			return true
		}
	}
	return false
}

// Start durably records requested before any external inspection or mutation.
func (e *Engine) Start(ctx context.Context, requestID, runID, agentID, specHash string) (*Run, error) {
	run, err := NewRun(requestID, runID, agentID, specHash, e.now())
	if err != nil {
		return nil, err
	}
	if err := e.store.Create(ctx, run); err == nil {
		return run.clone(), nil
	} else if !errors.Is(err, ErrConflict) {
		return nil, err
	}
	existing, err := e.store.Load(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if existing.RunID != runID || existing.AgentID != agentID || existing.SpecHash != specHash {
		return nil, fmt.Errorf("%w: request identity or spec differs", ErrConflict)
	}
	return existing, nil
}

func (e *Engine) Inspect(ctx context.Context, requestID string) (*Report, error) {
	run, err := e.store.Load(ctx, requestID)
	if err != nil {
		return nil, err
	}
	report := reportFor(run, true)
	if stage := nextStage(run); stage != "" {
		obs, inspectErr := e.drivers[stage].Inspect(ctx, snapshot(run), nil)
		if inspectErr != nil {
			report.Actions = append(report.Actions, Action{Stage: stage, Operation: "inspect_failed"})
		} else {
			obs = enforceObservation(run, stage, obs)
			report.Actions = append(report.Actions, observationAction(stage, obs))
		}
	}
	return report, nil
}

// Reconcile resumes from inspected reality. Dry-run performs inspection and reports the next mutation without changing state.
func (e *Engine) Reconcile(ctx context.Context, requestID string, dryRun bool) (*Report, error) {
	run, err := e.store.Load(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if run.Stage == StageRollbackPending {
		return e.SafeAbort(ctx, requestID, dryRun)
	}
	if run.Stage == StageRolledBack || run.Stage == StageFailedTerminal || run.Stage == StageRunning {
		if dryRun {
			return e.inspectTerminal(ctx, run)
		}
		if err := e.ensureTerminal(ctx, run); err != nil {
			return reportFor(run, false), err
		}
		return reportFor(run, false), nil
	}
	if run.Stage == StageFailedRecoverable {
		if dryRun {
			report := reportFor(run, true)
			stage := run.ResumeStage
			obs, inspectErr := e.drivers[stage].Inspect(ctx, snapshot(run), nil)
			if inspectErr != nil {
				report.Actions = append(report.Actions, Action{Stage: stage, Operation: "inspect_failed"})
			} else {
				obs = enforceObservation(run, stage, obs)
				report.Actions = append(report.Actions, observationAction(stage, obs))
			}
			return report, nil
		}
		if err := e.transition(ctx, run, previousStage(run.ResumeStage)); err != nil {
			return nil, err
		}
	}
	for {
		stage := nextStage(run)
		if stage == "" {
			return reportFor(run, dryRun), nil
		}
		driver := e.drivers[stage]
		obs, inspectErr := driver.Inspect(ctx, snapshot(run), nil)
		if inspectErr != nil {
			if dryRun {
				report := reportFor(run, true)
				report.Actions = append(report.Actions, Action{Stage: stage, Operation: "inspect_failed"})
				return report, nil
			}
			return e.fail(ctx, run, stage, inspectErr, true)
		}
		obs = enforceObservation(run, stage, obs)
		if obs.Reality == RealityConflict {
			if !dryRun {
				if err := e.checkpointConflictResources(ctx, run, stage, obs.Resources); err != nil {
					return nil, err
				}
			}
			if dryRun {
				report := reportFor(run, true)
				report.Actions = append(report.Actions, observationAction(stage, obs))
				return report, nil
			}
			return e.failAndRollback(ctx, run, stage, &SafeError{Code: "ownership_conflict", Message: "external resource ownership or spec conflicts with requested saga", Retryable: false})
		}
		if obs.Reality == RealityMatching {
			if len(obs.Resources) == 0 {
				return nil, errors.New("matching inspection omitted resource lineage")
			}
			if dryRun {
				report := reportFor(run, true)
				report.Actions = append(report.Actions, observationAction(stage, obs))
				return report, nil
			}
			if err := e.checkpointResources(ctx, run, stage, obs.Resources); err != nil {
				return nil, err
			}
			if err := e.transition(ctx, run, stage); err != nil {
				return nil, err
			}
			if stage == StageRunning {
				return reportFor(run, false), nil
			}
			continue
		}
		if obs.Reality != RealityAbsent {
			return nil, fmt.Errorf("invalid inspection reality %q", obs.Reality)
		}
		if dryRun {
			report := reportFor(run, true)
			report.Actions = append(report.Actions, observationAction(stage, obs))
			return report, nil
		}
		applyErr := driver.Apply(ctx, snapshot(run), run.StageKey(stage))
		// Response loss is never answered by blind repeat: inspect and correlate first.
		verified, verifyErr := driver.Inspect(ctx, snapshot(run), nil)
		if verifyErr == nil {
			verified = enforceObservation(run, stage, verified)
		}
		if verifyErr == nil && verified.Reality == RealityMatching && len(verified.Resources) > 0 {
			if err := e.checkpointResources(ctx, run, stage, verified.Resources); err != nil {
				return nil, err
			}
			if err := e.transition(ctx, run, stage); err != nil {
				return nil, err
			}
			if stage == StageRunning {
				return reportFor(run, false), nil
			}
			continue
		}
		if verifyErr == nil && verified.Reality == RealityConflict {
			if err := e.checkpointConflictResources(ctx, run, stage, verified.Resources); err != nil {
				return nil, err
			}
			return e.failAndRollback(ctx, run, stage, &SafeError{Code: "ownership_conflict", Retryable: false})
		}
		if applyErr != nil {
			if retryable(applyErr) {
				return e.fail(ctx, run, stage, applyErr, true)
			}
			return e.failAndRollback(ctx, run, stage, applyErr)
		}
		if verifyErr != nil {
			return e.fail(ctx, run, stage, verifyErr, true)
		}
		return e.fail(ctx, run, stage, &SafeError{Code: "postcondition_mismatch", Message: "external state did not match after mutation", Retryable: true}, true)
	}
}

func (e *Engine) Retry(ctx context.Context, requestID string, dryRun bool) (*Report, error) {
	return e.Reconcile(ctx, requestID, dryRun)
}

// SafeAbort compensates only saga-created resources, after inspecting each one, in fixed reverse dependency order.
func (e *Engine) SafeAbort(ctx context.Context, requestID string, dryRun bool) (*Report, error) {
	run, err := e.store.Load(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if run.Stage == StageRunning || run.Stage == StageRolledBack || run.Stage == StageFailedTerminal {
		return reportFor(run, dryRun), nil
	}
	if dryRun {
		report := reportFor(run, true)
		for _, resource := range compensable(run) {
			obs, inspectErr := e.drivers[resource.Stage].Inspect(ctx, snapshot(run), &resource)
			operation := "inspect_failed"
			if inspectErr == nil {
				operation = "skip_not_owned"
				if observed, ok := observedResource(obs.Resources, resource.key()); obs.Reality == RealityMatching && ok && observed.Ownership == OwnershipCreated && observed.OwnerRunID == run.RunID && observed.SpecHash == resource.SpecHash {
					operation = "compensate"
				}
			}
			report.Actions = append(report.Actions, Action{Stage: resource.Stage, Operation: operation, ResourceRef: resource.ExternalID})
		}
		return report, nil
	}
	if run.Stage != StageRollbackPending {
		if err := e.transition(ctx, run, StageRollbackPending); err != nil {
			return nil, err
		}
	}
	for _, resource := range compensable(run) {
		if compensated(run, resource.key()) {
			continue
		}
		driver := e.drivers[resource.Stage]
		obs, inspectErr := driver.Inspect(ctx, snapshot(run), &resource)
		if inspectErr != nil {
			return e.rollbackFail(ctx, run, resource.Stage, inspectErr, true)
		}
		if obs.Reality == RealityAbsent {
			if err := e.recordCompensation(ctx, run, resource, "already_absent"); err != nil {
				return nil, err
			}
			continue
		}
		observed, ok := observedResource(obs.Resources, resource.key())
		if obs.Reality != RealityMatching || !ok || observed.Ownership != OwnershipCreated || observed.OwnerRunID != run.RunID || (!resource.Conflict && observed.SpecHash != resource.SpecHash) {
			return e.rollbackFail(ctx, run, resource.Stage, &SafeError{Code: "rollback_ownership_conflict", Message: "resource ownership changed; compensation refused", Retryable: false}, false)
		}
		if err := driver.Compensate(ctx, snapshot(run), resource, run.CompensationKey(resource)); err != nil {
			return e.rollbackFail(ctx, run, resource.Stage, err, retryable(err))
		}
		post, inspectErr := driver.Inspect(ctx, snapshot(run), &resource)
		if inspectErr != nil {
			return e.rollbackFail(ctx, run, resource.Stage, inspectErr, true)
		}
		if post.Reality != RealityAbsent {
			return e.rollbackFail(ctx, run, resource.Stage, &SafeError{Code: "compensation_postcondition_mismatch", Message: "resource remains after compensation", Retryable: true}, true)
		}
		if err := e.recordCompensation(ctx, run, resource, "removed"); err != nil {
			return nil, err
		}
	}
	if err := e.transition(ctx, run, StageRolledBack); err != nil {
		return nil, err
	}
	e.retention.Mark(run, e.now())
	if run.RetainUntil != nil {
		if err := e.save(ctx, run); err != nil {
			return nil, err
		}
	}
	if err := e.ensureTerminal(ctx, run); err != nil {
		return reportFor(run, false), err
	}
	return reportFor(run, false), nil
}

func nextStage(run *Run) Stage {
	if run.Stage == StageFailedRecoverable {
		return run.ResumeStage
	}
	if run.Stage == StageRequested {
		return forwardStages[0]
	}
	for i, stage := range forwardStages {
		if run.Stage == stage && i+1 < len(forwardStages) {
			return forwardStages[i+1]
		}
	}
	return ""
}

func previousStage(stage Stage) Stage {
	for i, candidate := range forwardStages {
		if candidate != stage {
			continue
		}
		if i == 0 {
			return StageRequested
		}
		return forwardStages[i-1]
	}
	return StageRequested
}

func (e *Engine) checkpointResource(ctx context.Context, run *Run, stage Stage, resource Resource) error {
	resource.Stage = stage
	resource.IdempotencyKey = run.StageKey(stage)
	resource.ExternalID = PublicResourceRef(resource.System, resource.Kind, resource.ExternalID)
	if resource.RecordedAt.IsZero() {
		resource.RecordedAt = e.now().UTC()
	}
	if err := resource.validate(run.RunID); err != nil {
		return err
	}
	if resource.SpecHash != run.SpecHash {
		return errors.New("inspected resource spec does not match requested saga spec")
	}
	if resource.CorrelationID != run.RequestID {
		return errors.New("inspected resource correlation does not match saga request")
	}
	for _, current := range run.Resources {
		if current.key() != resource.key() {
			continue
		}
		if current.SpecHash != resource.SpecHash || current.Ownership != resource.Ownership || current.OwnerRunID != resource.OwnerRunID {
			return ErrConflict
		}
		return nil
	}
	run.Resources = append(run.Resources, resource)
	return e.save(ctx, run)
}

func (e *Engine) checkpointResources(ctx context.Context, run *Run, stage Stage, resources []Resource) error {
	if stage.terminalProjection() && !hasTerminalProjection(resources, run, stage) {
		return errors.New("running stage omitted correlated Bahia terminal projection lineage")
	}
	for _, resource := range resources {
		if err := e.checkpointResource(ctx, run, stage, resource); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) checkpointConflictResources(ctx context.Context, run *Run, stage Stage, resources []Resource) error {
	for _, resource := range resources {
		if resource.Ownership != OwnershipCreated || resource.OwnerRunID != run.RunID {
			continue
		}
		resource.Stage = stage
		resource.Conflict = true
		resource.IdempotencyKey = run.StageKey(stage)
		resource.ExternalID = PublicResourceRef(resource.System, resource.Kind, resource.ExternalID)
		resource.SpecHash = DeriveKey(run.RootKey, "conflict-spec/"+resource.SpecHash)
		resource.CorrelationID = DeriveKey(run.RootKey, "conflict-correlation/"+resource.CorrelationID)
		if resource.RecordedAt.IsZero() {
			resource.RecordedAt = e.now().UTC()
		}
		if err := resource.validate(run.RunID); err != nil {
			return err
		}
		run.Resources = append(run.Resources, resource)
		if err := e.save(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) transition(ctx context.Context, run *Run, to Stage) error {
	if !validStage(to) {
		return fmt.Errorf("invalid saga transition target %q", to)
	}
	from := run.Stage
	run.Stage = to
	run.Failure = nil
	run.ResumeStage = ""
	run.Transitions = append(run.Transitions, Transition{From: from, To: to, At: e.now().UTC()})
	return e.save(ctx, run)
}

func (e *Engine) save(ctx context.Context, run *Run) error {
	expected := run.Version
	run.Version++
	run.UpdatedAt = e.now().UTC()
	if err := e.store.Save(ctx, run, expected); err != nil {
		run.Version = expected
		return err
	}
	return nil
}

func (e *Engine) fail(ctx context.Context, run *Run, stage Stage, cause error, canRetry bool) (*Report, error) {
	code, message := publicFailure(cause)
	target := StageFailedTerminal
	if canRetry {
		target = StageFailedRecoverable
		run.ResumeStage = stage
	}
	from := run.Stage
	run.Stage = target
	run.Failure = &Failure{Stage: stage, Code: code, Message: message, Retryable: canRetry, At: e.now().UTC()}
	run.Failures = append(run.Failures, *run.Failure)
	run.Transitions = append(run.Transitions, Transition{From: from, To: target, At: e.now().UTC()})
	if target == StageFailedTerminal || target == StageFailedRecoverable {
		e.retention.Mark(run, e.now())
	}
	if err := e.save(ctx, run); err != nil {
		return nil, err
	}
	if run.Stage == StageFailedTerminal {
		if terminalErr := e.ensureTerminal(ctx, run); terminalErr != nil {
			return reportFor(run, false), &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
		}
	}
	return reportFor(run, false), &SafeError{Code: code, Message: message, Retryable: canRetry}
}

func (e *Engine) ensureTerminal(ctx context.Context, run *Run) error {
	obs, err := e.terminal.InspectTerminal(ctx, snapshot(run), run.Stage, run.Failure)
	if err != nil {
		return &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
	}
	if obs.Reality == RealityConflict {
		return &SafeError{Code: "terminal_projection_conflict", Retryable: false}
	}
	if obs.Reality == RealityAbsent {
		key := DeriveKey(run.RootKey, "terminal/"+string(run.Stage))
		applyErr := e.terminal.PublishTerminal(ctx, snapshot(run), run.Stage, run.Failure, key)
		obs, err = e.terminal.InspectTerminal(ctx, snapshot(run), run.Stage, run.Failure)
		if err != nil {
			return &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
		}
		if obs.Reality != RealityMatching {
			if applyErr != nil {
				return &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
			}
			return &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
		}
	}
	if obs.Reality != RealityMatching || !hasTerminalProjection(obs.Resources, run, run.Stage) {
		return &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
	}
	return e.checkpointResources(ctx, run, run.Stage, obs.Resources)
}

func (e *Engine) inspectTerminal(ctx context.Context, run *Run) (*Report, error) {
	report := reportFor(run, true)
	obs, err := e.terminal.InspectTerminal(ctx, snapshot(run), run.Stage, run.Failure)
	if err != nil {
		report.Actions = append(report.Actions, Action{Stage: run.Stage, Operation: "inspect_failed"})
		return report, nil
	}
	report.Actions = append(report.Actions, observationAction(run.Stage, obs))
	return report, nil
}

func (e *Engine) failAndRollback(ctx context.Context, run *Run, stage Stage, cause error) (*Report, error) {
	code, message := publicFailure(cause)
	from := run.Stage
	run.Stage = StageRollbackPending
	run.Failure = &Failure{Stage: stage, Code: code, Message: message, Retryable: false, At: e.now().UTC()}
	run.Failures = append(run.Failures, *run.Failure)
	run.Transitions = append(run.Transitions, Transition{From: from, To: StageRollbackPending, At: e.now().UTC()})
	if err := e.save(ctx, run); err != nil {
		return nil, err
	}
	report, abortErr := e.SafeAbort(ctx, run.RequestID, false)
	if abortErr != nil {
		return report, &SafeError{Code: "stage_failed", Retryable: false}
	}
	return report, &SafeError{Code: code, Message: message, Retryable: false}
}

func (e *Engine) rollbackFail(ctx context.Context, run *Run, stage Stage, cause error, canRetry bool) (*Report, error) {
	code, message := publicFailure(cause)
	if canRetry {
		run.Stage = StageRollbackPending
		run.Failure = &Failure{Stage: stage, Code: code, Message: message, Retryable: true, At: e.now().UTC()}
	} else {
		from := run.Stage
		run.Stage = StageFailedTerminal
		run.Failure = &Failure{Stage: stage, Code: code, Message: message, Retryable: false, At: e.now().UTC()}
		run.Transitions = append(run.Transitions, Transition{From: from, To: StageFailedTerminal, At: e.now().UTC()})
		e.retention.Mark(run, e.now())
	}
	run.Failures = append(run.Failures, *run.Failure)
	if err := e.save(ctx, run); err != nil {
		return nil, err
	}
	if run.Stage == StageFailedTerminal {
		if terminalErr := e.ensureTerminal(ctx, run); terminalErr != nil {
			return reportFor(run, false), &SafeError{Code: "terminal_projection_mismatch", Retryable: true}
		}
	}
	return reportFor(run, false), &SafeError{Code: code, Message: message, Retryable: canRetry}
}

func publicFailure(err error) (string, string) {
	var safe *SafeError
	if errors.As(err, &safe) {
		code := strings.TrimSpace(safe.Code)
		if code == "" {
			code = "stage_failed"
		}
		code = safePublicCode(code)
		return code, safePublicMessage(code)
	}
	return "stage_failed", "provisioning stage failed; inspect service logs using request and run identifiers"
}

func safePublicCode(code string) string {
	switch code {
	case "ownership_conflict", "policy_denied", "postcondition_mismatch", "response_lost", "rollback_ownership_conflict", "compensation_postcondition_mismatch", "terminal_projection_conflict", "terminal_projection_mismatch":
		return code
	default:
		return "stage_failed"
	}
}

func safePublicMessage(code string) string {
	switch code {
	case "ownership_conflict":
		return "external resource ownership or specification conflicts with this provisioning run"
	case "policy_denied":
		return "provisioning policy denied the requested operation"
	case "postcondition_mismatch":
		return "external state did not match after mutation"
	case "response_lost":
		return "external mutation response was ambiguous and remains uncorrelated"
	case "rollback_ownership_conflict":
		return "resource ownership changed; compensation was refused"
	case "compensation_postcondition_mismatch":
		return "resource remains after compensation"
	default:
		return "provisioning stage failed; inspect service logs using request and run identifiers"
	}
}
func retryable(err error) bool { var safe *SafeError; return errors.As(err, &safe) && safe.Retryable }
func reportFor(run *Run, dry bool) *Report {
	report := &Report{RequestID: run.RequestID, RunID: run.RunID, Stage: run.Stage, Version: run.Version, DryRun: dry}
	if run.Failure != nil {
		failure := *run.Failure
		report.Failure = &failure
	}
	return report
}
func observationAction(stage Stage, obs Observation) Action {
	action := Action{Stage: stage}
	switch obs.Reality {
	case RealityAbsent:
		action.Operation = "create"
	case RealityMatching:
		action.Operation = "adopt_or_confirm"
	case RealityConflict:
		action.Operation = "reject_conflict"
	}
	if len(obs.Resources) > 0 {
		action.ResourceRef = PublicResourceRef(obs.Resources[0].System, obs.Resources[0].Kind, obs.Resources[0].ExternalID)
	}
	return action
}

func observedResource(resources []Resource, key string) (Resource, bool) {
	for _, resource := range resources {
		candidate := resource
		candidate.ExternalID = PublicResourceRef(resource.System, resource.Kind, resource.ExternalID)
		if candidate.key() == key {
			return resource, true
		}
	}
	return Resource{}, false
}

func enforceObservation(run *Run, stage Stage, obs Observation) Observation {
	if obs.Reality != RealityMatching {
		return obs
	}
	if len(obs.Resources) == 0 {
		obs.Reality = RealityConflict
		return obs
	}
	for _, resource := range obs.Resources {
		if resource.SpecHash != run.SpecHash || resource.CorrelationID != run.RequestID {
			obs.Reality = RealityConflict
			return obs
		}
	}
	if stage.terminalProjection() && !hasTerminalProjection(obs.Resources, run, stage) {
		obs.Reality = RealityConflict
		return obs
	}
	return obs
}
func compensable(run *Run) []Resource {
	resources := make([]Resource, 0)
	for _, resource := range run.Resources {
		if resource.Ownership == OwnershipCreated && resource.OwnerRunID == run.RunID {
			resources = append(resources, resource)
		}
	}
	sortForCompensation(resources)
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].CompensationOrder != resources[j].CompensationOrder {
			return resources[i].CompensationOrder < resources[j].CompensationOrder
		}
		return resources[i].RecordedAt.After(resources[j].RecordedAt)
	})
	return resources
}
func compensated(run *Run, key string) bool {
	for _, item := range run.Compensations {
		if item.ResourceKey == key {
			return true
		}
	}
	return false
}
func (e *Engine) recordCompensation(ctx context.Context, run *Run, resource Resource, outcome string) error {
	run.Compensations = append(run.Compensations, Compensation{ResourceKey: resource.key(), IdempotencyKey: run.CompensationKey(resource), Outcome: outcome, At: e.now().UTC()})
	return e.save(ctx, run)
}
