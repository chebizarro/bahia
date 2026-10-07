package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// LifecycleExecutionResult describes side-effect execution output. Handlers own
// parsing, authorization, lifecycle progress/results, and read-model publishing;
// engines only perform action-specific state changes and external side effects.
type LifecycleExecutionResult struct {
	PublishSoul bool
	Data        map[string]interface{}
}

// LifecycleEngine executes lifecycle side effects for an already parsed and
// authorized kind:1950 action. Implementations must not publish Nostr results.
type LifecycleEngine interface {
	ExecuteLifecycleAction(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error)
}

// LifecycleHandler processes soul lifecycle actions (kind:1950). It is the only
// SoulFactory orchestrator for lifecycle/customization requests: it parses the
// request, authorizes it, deduplicates replays, publishes 6950 progress, invokes
// an execution engine, publishes the 31951 read model when needed, and publishes
// canonical 7950 terminal results. The 1951 compatibility result alias is optional and
// migration-only.
type LifecycleHandler struct {
	reactor          *Reactor
	bahiaIntegration *BahiaIntegration
	statusSync       *StatusSyncHandler
	logger           *slog.Logger
	engine           LifecycleEngine
	runtimeAdapters  map[domain.RuntimeTarget]RuntimeAdapter

	mu               sync.Mutex
	processedActions map[string]struct{}
}

// NewLifecycleHandler creates a new lifecycle handler.
func NewLifecycleHandler(
	reactor *Reactor,
	bahiaIntegration *BahiaIntegration,
	statusSync *StatusSyncHandler,
	logger *slog.Logger,
) *LifecycleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &LifecycleHandler{
		reactor:          reactor,
		bahiaIntegration: bahiaIntegration,
		statusSync:       statusSync,
		logger:           logger,
		processedActions: make(map[string]struct{}),
	}
	h.engine = &localLifecycleEngine{
		reactor:          reactor,
		bahiaIntegration: bahiaIntegration,
		statusSync:       statusSync,
		logger:           logger,
	}
	return h
}

// HandleAction processes a soul lifecycle action event. An authorized action
// runs holding its soul (soulOperationGate): while another lifecycle action or
// a fleet reload holds the soul, including one parked on a runtime terminal
// result, the action is deferred and runs in arrival order once that
// operation finishes, as it would have queued behind a timely result.
func (h *LifecycleHandler) HandleAction(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("nil lifecycle action event")
	}

	// Parse action from event.
	action, err := h.parseAction(event)
	if err != nil {
		fallback := actionFromEventForError(event)
		_ = h.publishActionResult(ctx, fallback, "error", map[string]interface{}{"error": err.Error()}, "")
		return fmt.Errorf("parse action: %w", err)
	}

	agentID := normalizeSoulLookupRef(action.SoulRef)
	if agentID == "" || !h.isAuthorized(event.PubKey.Hex(), nil) || !isSupportedLifecycleAction(action.Action) {
		// Rejected before any side effect, so there is nothing to serialize.
		_, err := h.runAction(ctx, event, action, nil)
		return err
	}
	// Abandon bypasses the soul gate: it acts on the stuck operation that
	// currently holds the soul, so going through the gate would defer it
	// behind the very operation it intends to release.
	if action.Action == domain.SoulActionAbandon {
		return h.handleAbandon(ctx, event, action, agentID)
	}

	var runErr error
	now := soulOperation{key: lifecycleOperationKey(action.EventID), run: func(ctx context.Context, hold *soulHold) bool {
		parked, err := h.runAction(ctx, event, action, hold)
		runErr = err
		return parked
	}}
	switch h.reactor.soulOperations().do(ctx, agentID, now, h.deferredAction(event, action)) {
	case soulOperationDeferred:
		h.logger.Info("deferring lifecycle action until the soul's current operation finishes",
			"event_id", event.ID, "action", action.Action, "agent_id", agentID)
	case soulOperationDuplicate:
		h.logger.Info("ignoring lifecycle action already in progress or deferred",
			"event_id", event.ID, "action", action.Action, "agent_id", agentID)
	}
	return runErr
}

// deferredAction is the soul operation that runs event later: deferred behind
// another operation, or re-driven after a restart (rebuildParkedOperations).
// It re-reads the soul when it runs.
func (h *LifecycleHandler) deferredAction(event *nostr.Event, action *domain.SoulAction) soulOperation {
	return soulOperation{key: lifecycleOperationKey(action.EventID), run: func(ctx context.Context, hold *soulHold) bool {
		parked, err := h.runAction(ctx, event, action, hold)
		if err != nil {
			h.logger.Error("deferred lifecycle action failed", "event_id", event.ID, "action", action.Action, "error", err)
		}
		return parked
	}}
}

// handleAbandon processes an operator abandon action for a soul stuck in
// awaiting_terminal. It does not go through the soul gate: the stuck operation
// holds the soul, and the abandon releases it. The sequence is:
//
// 1. Authorize the abandon.
// 2. Look up the soul and confirm it is held.
// 3. Replace the parked operation's resume with a record-only callback: a
// late result is logged and published as progress but NOT applied.
// 4. Force-release the soul's hold, so deferred work proceeds.
// 5. Publish the abandoned terminal result.
//
// Event contract (kind:1950):
//
//	tags: ["soul", "<parameterized coordinate>"], ["action", "abandon"]
//	 optional: ["reason", "<operator reason>"]
//	authorization: the signing pubkey must be in Config.AuthorizedPubkeys.
func (h *LifecycleHandler) handleAbandon(ctx context.Context, event *nostr.Event, action *domain.SoulAction, agentID string) error {
	logger := h.logger.With("event_id", event.ID, "action", action.Action, "agent_id", agentID)

	soul, err := h.reactor.GetSoul(ctx, action.SoulRef)
	if err != nil {
		return fmt.Errorf("abandon: lookup soul: %w", err)
	}
	if soul == nil {
		err := fmt.Errorf("abandon: soul not found: %s", action.SoulRef)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, "")
		return err
	}
	if !h.isAuthorized(event.PubKey.Hex(), soul) {
		err := fmt.Errorf("unauthorized: %s cannot abandon soul %s", event.PubKey.Hex(), soul.AgentID)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}

	held, _ := h.reactor.soulOperations().held(agentID)
	if !held {
		err := fmt.Errorf("abandon: soul %s is not held by a stuck operation", soul.AgentID)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}

	// Replace the parked continuation with a record-only callback. The
	// callback reads the request ID from the late result directly (not from
	// a captured variable set after abandon returns) to avoid a data race
	// between the assignment and a concurrent deliver call.
	abandoned := h.reactor.resultWaiters().abandon(agentID, func(ctx context.Context, late *RuntimeControlResultEnvelope) {
		status, requestID := "unknown", "unknown"
		if late != nil {
			status = late.Status
			requestID = late.RequestEvent
		}
		logger.Info("late runtime result for abandoned operation recorded (not applied)",
			"abandoned_request", requestID, "late_status", status)
		message := fmt.Sprintf("late runtime result for abandoned operation: status=%s (not applied)", status)
		_ = h.publishActionProgressTags(ctx, action, "abandoned_late_result", message, soul.AgentID, nostr.Tags{
			{tagEvent, action.EventID},
		})
	})
	if abandoned == nil {
		// The soul is held (by soulOperationGate) but no awaiting_terminal
		// operation is parked. The run func is still executing; forcing the
		// release would allow a second operation to run concurrently on the
		// same soul.
		err := fmt.Errorf("abandon: soul %s is held but its operation is still executing; abandon applies only to operations awaiting a runtime terminal result", soul.AgentID)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}

	if err := h.publishActionProgress(ctx, action, "processing", "abandoning stuck operation", soul.AgentID); err != nil {
		logger.Warn("failed to publish abandon progress", "error", err)
	}

	// Publish a terminal result for the abandoned request so the restart
	// rebuild (rebuildParkedOperations -> withoutTerminalResults) sees it as
	// finished and does not re-hold the soul or re-drive the operation.
	if err := h.publishAbandonedRequestResult(ctx, abandoned, action, soul.AgentID); err != nil {
		logger.Warn("failed to publish abandoned-request terminal result", "error", err)
	}

	// Release the soul's hold; deferred work (e.g. a rollback action or a
	// newer fleet revision) proceeds.
	h.reactor.soulOperations().forceRelease(ctx, agentID)

	data := map[string]interface{}{
		"agent_id":          soul.AgentID,
		"abandoned_request": abandoned.actionEventID,
		"reason":            action.Reason,
	}
	if err := h.publishActionResult(ctx, action, "completed", data, soul.AgentID); err != nil {
		return fmt.Errorf("abandon: publish result: %w", err)
	}
	logger.Info("soul operation abandoned; serialization lock released",
		"abandoned_runtime_request", abandoned.runtimeRequestID,
		"abandoned_action", abandoned.actionEventID)
	return nil
}

// publishAbandonedRequestResult publishes a kind:7950 terminal result for the
// abandoned request, so the restart rebuild (rebuildParkedOperations ->
// withoutTerminalResults) sees it as finished and does not re-drive it. The
// event's tags match the outstandingOperation key structure used by the rebuild:
// tagEvent is the originating action or fleet-revision event ID, tagRequestKind
// and tagAgentID match the awaiting_terminal progress.
func (h *LifecycleHandler) publishAbandonedRequestResult(ctx context.Context, abandoned *abandonedEntry, abandonAction *domain.SoulAction, agentID string) error {
	// Build a synthetic action for the abandoned request to reuse
	// BuildActionResultEvent, which owns the canonical result kind.
	syntheticAction := &domain.SoulAction{
		EventID:   abandoned.actionEventID,
		SoulRef:   abandonAction.SoulRef,
		Action:    domain.SoulActionType("abandoned"),
		Initiator: abandonAction.Initiator,
	}
	data := map[string]interface{}{
		"abandoned_by": abandonAction.EventID,
		"operator":     abandonAction.Initiator,
		"reason":       abandonAction.Reason,
	}
	event, err := BuildActionResultEvent(syntheticAction, "abandoned", data, ActionResultCanonical, agentID)
	if err != nil {
		return fmt.Errorf("build abandoned result: %w", err)
	}
	// Override the request-kind: the abandoned request was a lifecycle action
	// or fleet revision, not the synthetic action.
	setTagValue(&event.Tags, tagRequestKind, strconv.Itoa(abandoned.requestKind))
	if err := h.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign abandoned result: %w", err)
	}
	return h.reactor.publish(ctx, event, h.lifecycleRelays())
}

// runAction executes a parsed lifecycle action. hold is the action's hold on
// its soul (nil for an action rejected before any side effect). parked reports
// that the action is awaiting a runtime terminal result and keeps the hold.
func (h *LifecycleHandler) runAction(ctx context.Context, event *nostr.Event, action *domain.SoulAction, hold *soulHold) (parked bool, err error) {
	logger := h.logger.With(
		"event_id", event.ID,
		"action", action.Action,
		"soul_ref", action.SoulRef,
		"initiator", action.Initiator,
	)
	logger.Info("handling lifecycle action")

	// Look up the soul.
	soul, err := h.reactor.GetSoul(ctx, action.SoulRef)
	if err != nil {
		return false, fmt.Errorf("lookup soul: %w", err)
	}
	if soul == nil {
		return false, fmt.Errorf("soul not found: %s", action.SoulRef)
	}

	// Verify authorization.
	if !h.isAuthorized(event.PubKey.Hex(), soul) {
		err := fmt.Errorf("unauthorized: %s cannot perform %s on soul %s",
			event.PubKey.Hex(), action.Action, soul.AgentID)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return false, err
	}
	if !isSupportedLifecycleAction(action.Action) {
		err := fmt.Errorf("unknown action: %s", action.Action)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return false, err
	}

	if existing, err := h.findExistingTerminalResult(ctx, action.EventID); err != nil {
		logger.Warn("failed to check existing lifecycle terminal result", "error", err)
	} else if existing != nil {
		logger.Info("ignoring lifecycle action with existing terminal result", "result_event", existing.ID)
		h.beginAction(action.EventID)
		return false, nil
	}

	if !h.beginAction(action.EventID) {
		logger.Info("ignoring replayed lifecycle action")
		return false, nil
	}

	if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("processing %s action", action.Action), soul.AgentID); err != nil {
		h.clearAction(action.EventID)
		return false, fmt.Errorf("publish action progress: %w", err)
	}

	var result *LifecycleExecutionResult
	switch action.Action {
	case domain.SoulActionHotReload:
		result, err = h.handleHotReload(ctx, soul, action)
	case domain.SoulActionRollback:
		result, err = h.handleRollback(ctx, soul, action)
	case domain.SoulActionUpdate:
		result, err = h.handleUpdate(ctx, soul, action)
	default:
		result, err = h.engine.ExecuteLifecycleAction(ctx, soul, action)
	}
	return h.finishAction(ctx, lifecycleRun{action: action, soul: soul, hold: hold, shardKey: h.reactor.handlerShardKey(event)}, result, err)
}

// lifecycleRun is one action in progress: what finishAction and its parked
// continuations need.
type lifecycleRun struct {
	action *domain.SoulAction
	soul   *domain.AgentSoul
	// hold is the action's hold on its soul; a parked action keeps it until
	// its continuation finishes.
	hold *soulHold
	// shardKey is the reactor handler shard the continuation runs under.
	shardKey string
}

// awaitingRuntimeResult is a lifecycle step's error when a runtime request was
// accepted but its terminal kind:38386 was not observed within the wait. The
// outcome is unknown, so the step neither failed nor rolled back. resume
// continues the action with the terminal result once it is observed, exactly
// as the step would have with a timely result.
type awaitingRuntimeResult struct {
	cause   error
	pending *runtimeResultPending
	resume  func(context.Context, *RuntimeControlResultEnvelope) (*LifecycleExecutionResult, error)
}

func (e *awaitingRuntimeResult) Error() string {
	return "runtime outcome unknown, awaiting terminal result: " + e.cause.Error()
}

func (e *awaitingRuntimeResult) Unwrap() error { return e.cause }

// then returns e with f applied to whatever resume eventually returns.
func (e *awaitingRuntimeResult) then(f func(*LifecycleExecutionResult, error) (*LifecycleExecutionResult, error)) *awaitingRuntimeResult {
	next := *e
	next.resume = func(ctx context.Context, late *RuntimeControlResultEnvelope) (*LifecycleExecutionResult, error) {
		return f(e.resume(ctx, late))
	}
	return &next
}

// awaitRuntimeStep turns an Execute outcome-unknown error into an
// awaitingRuntimeResult whose resume hands the late result to next.
func awaitRuntimeStep(executeErr error, next func(context.Context, *RuntimeControlResultEnvelope) (*LifecycleExecutionResult, error)) (*awaitingRuntimeResult, bool) {
	pending, unknown := runtimeOutcomeUnknown(executeErr)
	if !unknown {
		return nil, false
	}
	return &awaitingRuntimeResult{cause: executeErr, pending: pending, resume: next}, true
}

// finishAction publishes an action's outcome. An action waiting on a runtime
// terminal result publishes awaiting_terminal progress and is parked: it gets
// no terminal result and no rollback until the late result is observed, and
// the reactor's result subscription then resumes it under run.shardKey. A
// parked action keeps its soul, so later work for the soul waits behind it;
// the continuation releases the soul when the action finishes. parked reports
// that the action is awaiting.
func (h *LifecycleHandler) finishAction(ctx context.Context, run lifecycleRun, result *LifecycleExecutionResult, err error) (parked bool, _ error) {
	action, soul := run.action, run.soul
	logger := h.logger.With("event_id", action.EventID, "action", action.Action, "agent_id", soul.AgentID)
	for {
		var awaiting *awaitingRuntimeResult
		if !errors.As(err, &awaiting) {
			break
		}
		message := fmt.Sprintf("%s: %v; no rollback without an observed runtime failure", actionStatusAwaitingTerminal, awaiting.cause)
		if publishErr := h.publishActionProgressTags(ctx, action, actionStatusAwaitingTerminal, message, soul.AgentID, awaitingTerminalTags()); publishErr != nil {
			logger.Warn("failed to publish awaiting_terminal progress", "error", publishErr)
		}
		if awaiting.pending == nil {
			logger.Warn("runtime outcome unknown and not correlatable; the action stays awaiting_terminal until a restart re-drives it", "error", awaiting.cause)
			return false, nil
		}
		late, observed := h.reactor.resultWaiters().park(awaiting.pending, parkedOperation{
			shardKey:      run.shardKey,
			holdsSoul:     run.hold != nil,
			actionEventID: run.action.EventID,
			requestKind:   domain.KindSoulAction,
			resume: func(ctx context.Context, late *RuntimeControlResultEnvelope) {
				result, err := awaiting.resume(ctx, late)
				parked, err := h.finishAction(ctx, run, result, err)
				if err != nil {
					logger.Error("lifecycle action failed after late runtime result", "error", err)
				}
				if !parked {
					h.reactor.soulOperations().release(ctx, run.hold)
				}
			},
		})
		if !observed {
			logger.Info("lifecycle action awaiting runtime terminal result", "request_event", awaiting.pending.requestID())
			return true, nil
		}
		result, err = awaiting.resume(ctx, late)
	}
	if err != nil {
		logger.Error("lifecycle action failed", "error", err)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return false, err
	}
	if result == nil {
		result = &LifecycleExecutionResult{PublishSoul: true}
	}

	if result.PublishSoul {
		if err := h.publishSoulUpdate(ctx, soul); err != nil {
			return false, fmt.Errorf("publish soul update: %w", err)
		}
	}

	return false, h.publishActionResult(ctx, action, "completed", result.Data, soul.AgentID)
}

func (h *LifecycleHandler) beginAction(eventID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.processedActions[eventID]; ok {
		return false
	}
	h.processedActions[eventID] = struct{}{}
	return true
}

func (h *LifecycleHandler) clearAction(eventID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.processedActions, eventID)
}

// lifecycleTerminalResultKinds are the kinds of lifecycle and fleet
// reconciliation terminal results: the canonical 7950 and the migration-only
// 1951 alias.
var lifecycleTerminalResultKinds = []nostr.Kind{nostr.Kind(domain.KindProvisioningResult), nostr.Kind(domain.KindSoulActionLegacyResult)}

func (h *LifecycleHandler) findExistingTerminalResult(ctx context.Context, eventID string) (*nostr.Event, error) {
	if h.reactor.findLifecycleResultFn != nil {
		return h.reactor.findLifecycleResultFn(ctx, eventID)
	}
	relayClient := h.reactor.relayClient
	if relayClient == nil {
		return nil, nil
	}
	terminal := func(result *nostr.Event) bool {
		return result != nil && domain.IsLifecycleResultKind(int(result.Kind)) && tagValue(result.Tags, tagRequestKind) == fmt.Sprint(domain.KindSoulAction)
	}
	// Idempotency check: a found terminal result is final, but absence would
	// re-run the action, so it needs every relay. See RelayReadPolicy.
	read, err := relayClient.QueryWithPolicy(ctx, "lifecycle.terminal_result", RelayReadFound(terminal), []nostr.Filter{{
		Kinds: lifecycleTerminalResultKinds,
		Tags:  nostr.TagMap{tagEvent: []string{eventID}},
		Limit: 1,
	}})
	if err != nil {
		return nil, err
	}
	for _, result := range read.Events {
		if terminal(result) {
			return result, nil
		}
	}
	return nil, nil
}

// parseAction extracts action details from a kind:1950 event.
func (h *LifecycleHandler) parseAction(event *nostr.Event) (*domain.SoulAction, error) {
	return ParseSoulActionEvent(event)
}

// isAuthorized checks whether the signing pubkey is explicitly configured
// for SoulFactory provisioning and lifecycle control.
func (h *LifecycleHandler) isAuthorized(pubkey string, soul *domain.AgentSoul) bool {
	for _, authorizedKey := range h.reactor.config.AuthorizedPubkeys {
		if pubkey == authorizedKey {
			return true
		}
	}

	return false
}

// publishSoulUpdate publishes an updated soul event.
func (h *LifecycleHandler) publishSoulUpdate(ctx context.Context, soul *domain.AgentSoul) error {
	return h.reactor.PublishSoul(ctx, soul)
}

func (h *LifecycleHandler) publishActionProgress(ctx context.Context, action *domain.SoulAction, status, message, agentID string) error {
	return h.publishActionProgressTags(ctx, action, status, message, agentID, nil)
}

// publishActionProgressTags publishes kind:6950 progress carrying extra tags.
func (h *LifecycleHandler) publishActionProgressTags(ctx context.Context, action *domain.SoulAction, status, message, agentID string, extra nostr.Tags) error {
	event := BuildActionStatusEvent(action, status, message, agentID)
	event.Tags = append(event.Tags, extra...)
	if err := h.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign action status: %w", err)
	}
	return h.reactor.publish(ctx, event, h.lifecycleRelays())
}

// publishActionResult publishes the terminal canonical 7950 result for an action.
func (h *LifecycleHandler) publishActionResult(ctx context.Context, action *domain.SoulAction, status string, data map[string]interface{}, agentID string) error {
	event, err := BuildActionResultEvent(action, status, data, ActionResultCanonical, agentID)
	if err != nil {
		return err
	}
	if err := h.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign result: %w", err)
	}
	if err := h.reactor.publish(ctx, event, h.lifecycleRelays()); err != nil {
		return err
	}
	if h.reactor.config.PublishLegacyLifecycleResults {
		legacy, err := BuildActionResultEvent(action, status, data, ActionResultLegacy, agentID)
		if err != nil {
			return err
		}
		if err := h.reactor.signer.Sign(ctx, legacy); err != nil {
			return fmt.Errorf("sign legacy result: %w", err)
		}
		return h.reactor.publish(ctx, legacy, h.lifecycleRelays())
	}
	return nil
}

func (h *LifecycleHandler) lifecycleRelays() []string {
	return normalizeSoulRelays(append(append([]string{}, h.reactor.config.AdditionalRelays...), h.reactor.config.Relays...))
}

// SetRuntimeAdapters installs runtime-control adapters used by hot-reload.
func (h *LifecycleHandler) SetRuntimeAdapters(adapters map[domain.RuntimeTarget]RuntimeAdapter) {
	h.runtimeAdapters = cloneRuntimeAdapters(adapters)
}

// HotReloadDraftDiff is the draft-section delta used to decide which runtime
// control requests a hot-reload action must emit.
type HotReloadDraftDiff struct {
	Avatar          bool     `json:"avatar"`
	Voice           bool     `json:"voice"`
	Memory          bool     `json:"memory"`
	Persona         bool     `json:"persona"`
	ChangedSections []string `json:"changed_sections"`
}

// DiffHotReloadDrafts compares current and proposed draft content at the live
// customization section boundary. Identity and generated prompt markdown are
// treated as persona-affecting because they shape runtime prompt/identity state.
func DiffHotReloadDrafts(current, proposed domain.SoulDraftContent) HotReloadDraftDiff {
	current = current.MigrateToLatest()
	proposed = proposed.MigrateToLatest()

	diff := HotReloadDraftDiff{}
	if draftSectionChanged(hotReloadAvatarSection(current), hotReloadAvatarSection(proposed)) {
		diff.Avatar = true
		diff.ChangedSections = append(diff.ChangedSections, "avatar")
	}
	if draftSectionChanged(hotReloadVoiceSection(current), hotReloadVoiceSection(proposed)) {
		diff.Voice = true
		diff.ChangedSections = append(diff.ChangedSections, "voice")
	}
	if draftSectionChanged(current.Memory, proposed.Memory) {
		diff.Memory = true
		diff.ChangedSections = append(diff.ChangedSections, "memory")
	}
	if draftSectionChanged(hotReloadPersonaSection(current), hotReloadPersonaSection(proposed)) {
		diff.Persona = true
		diff.ChangedSections = append(diff.ChangedSections, "persona")
	}
	return diff
}

type hotReloadRuntimeCall struct {
	Section string
	Method  string
	Params  map[string]interface{}
}

type lifecycleUpdateDiff struct {
	ChangedSections []string
	Persona         bool
}

func diffLifecycleUpdateDrafts(current, proposed domain.SoulDraftContent) lifecycleUpdateDiff {
	currentSpec := BuildProvisionRuntimeParamsFromDraft(current)
	proposedSpec := BuildProvisionRuntimeParamsFromDraft(proposed)
	sections := []string{"identity", "persona", "avatar", "voice", "memory", "runtime", "permissions", "relay_policy", "workspace", "assets"}
	diff := lifecycleUpdateDiff{Persona: draftSectionChanged(current.Persona, proposed.Persona)}
	for _, section := range sections {
		if draftSectionChanged(currentSpec[section], proposedSpec[section]) {
			diff.ChangedSections = append(diff.ChangedSections, section)
		}
	}
	return diff
}

func (h *LifecycleHandler) handleUpdate(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if action.DraftRef == "" && action.DraftEventID == "" {
		return nil, fmt.Errorf("update requires draft_ref or draft_event_id")
	}
	if soul.Status == domain.SoulStatusRevoked {
		return nil, fmt.Errorf("cannot update revoked soul")
	}

	proposedDraft, err := h.lookupHotReloadDraft(ctx, action.DraftRef, action.DraftEventID)
	if err != nil {
		return nil, fmt.Errorf("lookup proposed update draft: %w", err)
	}
	if proposedDraft == nil {
		return nil, fmt.Errorf("proposed update draft not found")
	}
	if proposedDraft.AgentID != soul.AgentID {
		return nil, fmt.Errorf("update draft agent %q does not match soul %q", proposedDraft.AgentID, soul.AgentID)
	}

	current, currentDraft, err := h.currentUpdateContent(ctx, soul)
	if err != nil {
		return nil, err
	}
	if currentDraft != nil && currentDraft.AgentID != soul.AgentID {
		return nil, fmt.Errorf("current update draft agent %q does not match soul %q", currentDraft.AgentID, soul.AgentID)
	}
	if soul.SpecHash != "" && current.SpecHash != "" && current.SpecHash != soul.SpecHash {
		return nil, fmt.Errorf("current update draft spec_hash %q does not match soul %q", current.SpecHash, soul.SpecHash)
	}
	proposed := proposedDraft.Content.MigrateToLatest()
	if proposed.Runtime.Target != "" && soul.Runtime.Target != "" && proposed.Runtime.Target != soul.Runtime.Target {
		return nil, fmt.Errorf("update cannot change runtime target from %s to %s", soul.Runtime.Target, proposed.Runtime.Target)
	}
	if proposed.Runtime.RuntimePubkey != "" && soul.Runtime.RuntimePubkey != "" && proposed.Runtime.RuntimePubkey != soul.Runtime.RuntimePubkey {
		return nil, fmt.Errorf("update cannot change runtime pubkey")
	}

	previousSpecHash := firstNonEmpty(soul.SpecHash, current.SpecHash, computeDraftContentHash(current))
	if previousSpecHash == "" {
		return nil, fmt.Errorf("update requires current spec hash")
	}
	if action.PreviousSpecHash != "" && action.PreviousSpecHash != previousSpecHash {
		return nil, fmt.Errorf("update previous_spec_hash %q does not match current %q", action.PreviousSpecHash, previousSpecHash)
	}
	if proposed.PreviousSpecHash != "" && proposed.PreviousSpecHash != previousSpecHash {
		return nil, fmt.Errorf("update draft previous_spec_hash %q does not match current %q", proposed.PreviousSpecHash, previousSpecHash)
	}
	if action.SpecHash != "" && proposed.SpecHash != "" && action.SpecHash != proposed.SpecHash {
		return nil, fmt.Errorf("update spec_hash %q does not match draft %q", action.SpecHash, proposed.SpecHash)
	}
	newSpecHash := firstNonEmpty(proposed.SpecHash, action.SpecHash, computeDraftContentHash(proposed))
	if newSpecHash == "" {
		return nil, fmt.Errorf("update requires proposed spec hash")
	}

	diff := diffLifecycleUpdateDrafts(current, proposed)
	if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("update diff computed: %s", hotReloadSectionsText(diff.ChangedSections)), soul.AgentID); err != nil {
		return nil, fmt.Errorf("publish update diff progress: %w", err)
	}

	adapter, target, runtimePubkey, err := h.selectHotReloadRuntime(soul, proposed)
	if err != nil {
		return nil, err
	}
	proposedDraftRef := firstNonEmpty(action.DraftRef, parameterizedCoordinate(domain.KindSoulDraft, proposedDraft.CreatedBy, proposedDraft.AgentID))
	proposedDraftEventID := firstNonEmpty(proposedDraft.EventID, action.DraftEventID)
	updateParams := buildLifecycleUpdateParams(proposed, previousSpecHash, newSpecHash, proposedDraftRef, proposedDraftEventID, diff.ChangedSections)
	rollbackDraftRef := soul.DraftRef
	rollbackDraftEventID := soul.DraftEventID
	if currentDraft != nil {
		rollbackDraftRef = firstNonEmpty(rollbackDraftRef, parameterizedCoordinate(domain.KindSoulDraft, currentDraft.CreatedBy, currentDraft.AgentID))
		rollbackDraftEventID = firstNonEmpty(rollbackDraftEventID, currentDraft.EventID)
	}
	rollbackParams := buildLifecycleUpdateParams(current, previousSpecHash, previousSpecHash, rollbackDraftRef, rollbackDraftEventID, diff.ChangedSections)

	var proposedPersonaParams, rollbackPersonaParams map[string]interface{}
	if diff.Persona {
		proposedPersonaParams, err = BuildPersonaRuntimeControlParams(proposed.Persona)
		if err != nil {
			return nil, fmt.Errorf("build proposed persona update: %w", err)
		}
		rollbackPersonaParams, err = BuildPersonaRuntimeControlParams(current.Persona)
		if err != nil {
			return nil, fmt.Errorf("build rollback persona update: %w", err)
		}
	}

	calls := []hotReloadRuntimeCall{{Section: "spec", Method: RuntimeMethodUpdate, Params: updateParams}}
	if diff.Persona {
		calls = append(calls, hotReloadRuntimeCall{Section: "persona", Method: RuntimeMethodPersonaUpdate, Params: proposedPersonaParams})
	}
	applied := make([]map[string]interface{}, 0, len(calls))
	updateApplied := false
	personaAttempted := false
	// Rollback runs only on an observed failure; a step whose result was not
	// observed parks the update instead (awaitRuntimeStep).
	withRollback := func(ctx context.Context, cause error) error {
		rollbackErr := h.rollbackRuntimeUpdate(ctx, soul, action, adapter, target, runtimePubkey, previousSpecHash, newSpecHash, rollbackDraftRef, rollbackDraftEventID, current.RelayPolicy, rollbackParams, rollbackPersonaParams, updateApplied, personaAttempted)
		if rollbackErr != nil {
			return fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		return cause
	}
	// record applies call i's terminal result, whether observed in time or late.
	record := func(ctx context.Context, i int, result *RuntimeControlResultEnvelope, executeErr error) error {
		call := calls[i]
		if executeErr == nil && result == nil {
			executeErr = fmt.Errorf("runtime returned no result")
		}
		if executeErr != nil {
			return withRollback(ctx, fmt.Errorf("update %s via %s: %w", call.Section, call.Method, executeErr))
		}
		if call.Method == RuntimeMethodUpdate {
			updateApplied = true
		}
		applied = append(applied, map[string]interface{}{"section": call.Section, "method": call.Method, "status": result.Status, "result": result.Result})
		if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applied update via %s", call.Method), soul.AgentID); err != nil {
			return withRollback(ctx, fmt.Errorf("publish %s update applied progress: %w", call.Section, err))
		}
		return nil
	}
	var runFrom func(ctx context.Context, start int) (*LifecycleExecutionResult, error)
	runFrom = func(ctx context.Context, start int) (*LifecycleExecutionResult, error) {
		for i := start; i < len(calls); i++ {
			call := calls[i]
			if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applying update via %s", call.Method), soul.AgentID); err != nil {
				progressErr := fmt.Errorf("publish %s update progress: %w", call.Section, err)
				if updateApplied {
					return nil, withRollback(ctx, progressErr)
				}
				return nil, progressErr
			}
			if call.Method == RuntimeMethodPersonaUpdate {
				personaAttempted = true
			}
			result, executeErr := adapter.Execute(ctx, RuntimeAdapterRequest{
				Method:      call.Method,
				Operator:    RuntimeOperatorRef{Pubkey: action.Initiator, RequestEvent: action.EventID},
				Soul:        RuntimeSoulRef{ID: soul.AgentID, Draft: proposedDraftEventID, SpecHash: newSpecHash},
				Target:      RuntimeTargetRef{Runtime: target, RuntimePubkey: runtimePubkey, AgentID: soul.AgentID},
				Params:      call.Params,
				DraftPolicy: proposed.RelayPolicy,
				RequestKind: domain.KindSoulAction,
				Action:      domain.SoulActionUpdate,
			})
			if awaiting, ok := awaitRuntimeStep(executeErr, func(ctx context.Context, late *RuntimeControlResultEnvelope) (*LifecycleExecutionResult, error) {
				if err := record(ctx, i, late, runtimeResultFailure(late)); err != nil {
					return nil, err
				}
				return runFrom(ctx, i+1)
			}); ok {
				return nil, awaiting
			}
			if err := record(ctx, i, result, executeErr); err != nil {
				return nil, err
			}
		}

		applyUpdateDraftToSoul(soul, proposedDraft, proposed, action, newSpecHash, previousSpecHash, applied)
		return &LifecycleExecutionResult{
			PublishSoul: true,
			Data: map[string]interface{}{
				"updated": true, "draft_ref": proposedDraftRef, "draft_event_id": proposedDraftEventID,
				"spec_hash": newSpecHash, "previous_spec_hash": previousSpecHash,
				"changed_sections": diff.ChangedSections, "persona_updated": diff.Persona,
				"applied_changes": applied, "applied_change_count": len(applied),
			},
		}, nil
	}
	return runFrom(ctx, 0)
}

func buildLifecycleUpdateParams(content domain.SoulDraftContent, previousSpecHash, newSpecHash, draftRef, draftEventID string, changedSections []string) map[string]interface{} {
	return map[string]interface{}{
		"schema": domain.SoulFactoryDraftSchemaLatest, "previous_spec_hash": previousSpecHash,
		"new_spec_hash": newSpecHash, "update_mode": "replace",
		"resolved_spec": BuildProvisionRuntimeParamsFromDraft(content),
		"draft_ref":     draftRef, "draft_event_id": draftEventID,
		"changed_sections": append([]string(nil), changedSections...),
	}
}

func (h *LifecycleHandler) currentUpdateContent(ctx context.Context, soul *domain.AgentSoul) (domain.SoulDraftContent, *domain.SoulDraft, error) {
	if soul.DraftRef != "" || soul.DraftEventID != "" {
		draft, err := h.lookupHotReloadDraft(ctx, soul.DraftRef, soul.DraftEventID)
		if err != nil {
			return domain.SoulDraftContent{}, nil, fmt.Errorf("lookup current update draft: %w", err)
		}
		if draft == nil {
			return domain.SoulDraftContent{}, nil, fmt.Errorf("current update draft not found")
		}
		return draft.Content.MigrateToLatest(), draft, nil
	}
	return synthesizeDraftContentFromSoul(soul), nil, nil
}

func (h *LifecycleHandler) rollbackRuntimeUpdate(
	ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction, adapter RuntimeAdapter,
	target domain.RuntimeTarget, runtimePubkey, previousSpecHash, newSpecHash, draftRef, draftEventID string,
	policy domain.SoulRelayPolicySpec, updateParams, personaParams map[string]interface{},
	updateApplied, personaAttempted bool,
) error {
	rollbackParams := cloneDraftJSONMap(updateParams)
	if updateApplied {
		rollbackParams["previous_spec_hash"] = newSpecHash
	}
	var rollbackErrors []error
	if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("rolling back update via %s", RuntimeMethodUpdate), soul.AgentID); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("publish rollback update progress: %w", err))
	}
	result, err := adapter.Execute(ctx, RuntimeAdapterRequest{
		Method: RuntimeMethodUpdate, Operator: RuntimeOperatorRef{Pubkey: action.Initiator, RequestEvent: action.EventID},
		Soul:   RuntimeSoulRef{ID: soul.AgentID, Draft: firstNonEmpty(draftEventID, draftRef), SpecHash: previousSpecHash},
		Target: RuntimeTargetRef{Runtime: target, RuntimePubkey: runtimePubkey, AgentID: soul.AgentID},
		Params: rollbackParams, DraftPolicy: policy, RequestKind: domain.KindSoulAction, Action: domain.SoulActionRollback,
	})
	result, err = h.observeRollback(action, soul.AgentID, "rollback update", result, err)
	if err != nil {
		rollbackErrors = append(rollbackErrors, rollbackStepError("rollback update", err))
	} else if result == nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("runtime returned no rollback update result"))
	}
	if personaAttempted {
		if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("rolling back update via %s", RuntimeMethodPersonaUpdate), soul.AgentID); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("publish rollback persona progress: %w", err))
		}
		result, err = adapter.Execute(ctx, RuntimeAdapterRequest{
			Method: RuntimeMethodPersonaUpdate, Operator: RuntimeOperatorRef{Pubkey: action.Initiator, RequestEvent: action.EventID},
			Soul:   RuntimeSoulRef{ID: soul.AgentID, Draft: firstNonEmpty(draftEventID, draftRef), SpecHash: previousSpecHash},
			Target: RuntimeTargetRef{Runtime: target, RuntimePubkey: runtimePubkey, AgentID: soul.AgentID},
			Params: personaParams, DraftPolicy: policy, RequestKind: domain.KindSoulAction, Action: domain.SoulActionRollback,
		})
		result, err = h.observeRollback(action, soul.AgentID, "rollback persona", result, err)
		if err != nil {
			rollbackErrors = append(rollbackErrors, rollbackStepError("rollback persona", err))
		} else if result == nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("runtime returned no rollback persona result"))
		}
	}
	return errors.Join(rollbackErrors...)
}

// observeRollback applies observeLateRollback to a lifecycle rollback request:
// a late result is reported as rollback_resolved progress on the action.
func (h *LifecycleHandler) observeRollback(action *domain.SoulAction, agentID, step string, result *RuntimeControlResultEnvelope, err error) (*RuntimeControlResultEnvelope, error) {
	return h.reactor.resultWaiters().observeLateRollback(agentID, result, err, func(ctx context.Context, late *RuntimeControlResultEnvelope) {
		status, message := lateRollbackProgress(step, late)
		if err := h.publishActionProgressTags(ctx, action, actionStatusRollbackResolved, message, agentID, nostr.Tags{{tagRollbackStatus, status}}); err != nil {
			h.logger.Warn("failed to publish late rollback progress", "event_id", action.EventID, "step", step, "error", err)
		}
	})
}

// rollbackStepError names a failed rollback request, distinguishing one whose
// terminal result was not observed: that rollback's outcome is unknown.
func rollbackStepError(step string, err error) error {
	if _, unknown := runtimeOutcomeUnknown(err); unknown {
		return fmt.Errorf("%s: %s: %w", step, rollbackStatusOutcomeUnknown, err)
	}
	return fmt.Errorf("%s: %w", step, err)
}

func applyUpdateDraftToSoul(soul *domain.AgentSoul, draft *domain.SoulDraft, proposed domain.SoulDraftContent, action *domain.SoulAction, newSpecHash, previousSpecHash string, applied []map[string]interface{}) {
	currentDraftRef, currentDraftEventID := soul.DraftRef, soul.DraftEventID
	soul.PreviousDraftRef, soul.PreviousDraftEventID = currentDraftRef, currentDraftEventID
	soul.DraftRef = firstNonEmpty(action.DraftRef, parameterizedCoordinate(domain.KindSoulDraft, draft.CreatedBy, draft.AgentID))
	soul.DraftEventID, soul.PreviousSpecHash, soul.SpecHash = draft.EventID, previousSpecHash, newSpecHash
	soul.Name, soul.Purpose, soul.Tier = proposed.Identity.Name, firstNonEmpty(proposed.Identity.Purpose, proposed.Brief), proposed.Identity.Tier
	soul.NIP05, soul.SoulMD, soul.IdentityMD = proposed.Identity.NIP05, proposed.SoulMD, proposed.IdentityMD
	soul.AllowedKinds = append([]int(nil), proposed.Permissions.AllowedKinds...)
	soul.ToolGrants = cloneUpdateToolGrants(proposed.Permissions.ToolGrants)
	soul.PermissionSpec = proposed.Permissions
	soul.PermissionSpec.AllowedKinds = append([]int(nil), proposed.Permissions.AllowedKinds...)
	soul.PermissionSpec.ToolGrants = cloneUpdateToolGrants(proposed.Permissions.ToolGrants)
	soul.RelayPolicy = proposed.RelayPolicy
	soul.RelayPolicy.Read = append([]string(nil), proposed.RelayPolicy.Read...)
	soul.RelayPolicy.Write = append([]string(nil), proposed.RelayPolicy.Write...)
	soul.RelayPolicy.Control = append([]string(nil), proposed.RelayPolicy.Control...)
	soul.Workspace, soul.Assets = proposed.Workspace, proposed.Assets
	if proposed.Runtime.Target != "" {
		soul.Runtime.Target = proposed.Runtime.Target
	}
	if proposed.Runtime.RuntimePubkey != "" {
		soul.Runtime.RuntimePubkey = proposed.Runtime.RuntimePubkey
	}
	if proposed.Runtime.CapabilityRef != "" {
		soul.Runtime.CapabilityRef = proposed.Runtime.CapabilityRef
	}
	applyRuntimeResultsToSoul(soul, applied)
}

func cloneUpdateToolGrants(grants []domain.ToolGrant) []domain.ToolGrant {
	out := make([]domain.ToolGrant, len(grants))
	for i, grant := range grants {
		out[i] = grant
		out[i].Scopes = append([]string(nil), grant.Scopes...)
	}
	return out
}

func applyRuntimeResultsToSoul(soul *domain.AgentSoul, applied []map[string]interface{}) {
	for _, change := range applied {
		result, _ := change["result"].(map[string]interface{})
		if result == nil {
			continue
		}
		soul.Runtime.RuntimePubkey = firstNonEmpty(stringResult(result, "runtime_pubkey"), soul.Runtime.RuntimePubkey)
		soul.Runtime.RuntimeBinding = firstNonEmpty(stringResult(result, "runtime_binding"), soul.Runtime.RuntimeBinding)
		soul.Runtime.State = firstNonEmpty(stringResult(result, "state"), soul.Runtime.State)
		soul.Runtime.CapabilityRef = firstNonEmpty(stringResult(result, "capability_ref"), soul.Runtime.CapabilityRef)
		soul.Runtime.Provider = firstNonEmpty(stringResult(result, "provider"), soul.Runtime.Provider)
		soul.Runtime.Model = firstNonEmpty(stringResult(result, "model"), soul.Runtime.Model)
		soul.CapabilityRef = firstNonEmpty(soul.Runtime.CapabilityRef, soul.CapabilityRef)
	}
}

func (h *LifecycleHandler) handleHotReload(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if action.DraftRef == "" && action.DraftEventID == "" {
		return nil, fmt.Errorf("hot-reload requires draft_ref or draft_event_id")
	}
	if soul.Status == domain.SoulStatusRevoked {
		return nil, fmt.Errorf("cannot hot-reload revoked soul")
	}

	proposedDraft, err := h.lookupHotReloadDraft(ctx, action.DraftRef, action.DraftEventID)
	if err != nil {
		return nil, fmt.Errorf("lookup proposed draft: %w", err)
	}
	if proposedDraft == nil {
		return nil, fmt.Errorf("proposed draft not found")
	}
	proposed := proposedDraft.Content.MigrateToLatest()
	current, err := h.currentHotReloadContent(ctx, soul)
	if err != nil {
		return nil, err
	}
	diff := DiffHotReloadDrafts(current, proposed)
	if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("hot-reload diff computed: %s", hotReloadSectionsText(diff.ChangedSections)), soul.AgentID); err != nil {
		return nil, fmt.Errorf("publish hot-reload diff progress: %w", err)
	}

	newSpecHash := firstNonEmpty(action.SpecHash, proposed.SpecHash, computeDraftContentHash(proposed))
	previousSpecHash := firstNonEmpty(action.PreviousSpecHash, proposed.PreviousSpecHash, soul.SpecHash)
	calls := buildHotReloadRuntimeCalls(current, proposed, diff, proposedDraft, action, newSpecHash, previousSpecHash)
	applied := make([]map[string]interface{}, 0, len(calls))

	finish := func() (*LifecycleExecutionResult, error) {
		applyHotReloadDraftToSoul(soul, proposedDraft, proposed, action, newSpecHash, previousSpecHash, applied)
		data := map[string]interface{}{
			"hot_reload":           true,
			"draft_ref":            firstNonEmpty(action.DraftRef, parameterizedCoordinate(domain.KindSoulDraft, proposedDraft.CreatedBy, proposedDraft.AgentID)),
			"draft_event_id":       proposedDraft.EventID,
			"spec_hash":            newSpecHash,
			"previous_spec_hash":   previousSpecHash,
			"changed_sections":     diff.ChangedSections,
			"applied_changes":      applied,
			"applied_change_count": len(applied),
		}
		return &LifecycleExecutionResult{PublishSoul: true, Data: data}, nil
	}
	if len(calls) == 0 {
		return finish()
	}

	adapter, target, runtimePubkey, err := h.selectHotReloadRuntime(soul, proposed)
	if err != nil {
		return nil, err
	}
	// record applies call i's terminal result, whether observed in time or
	// late. Rollback runs only on an observed failure; a call whose result was
	// not observed parks the hot-reload instead (awaitRuntimeStep).
	record := func(ctx context.Context, i int, result *RuntimeControlResultEnvelope, executeErr error) error {
		call := calls[i]
		if executeErr == nil && result == nil {
			executeErr = fmt.Errorf("runtime returned no result")
		}
		if executeErr != nil {
			rollbackSpecHash := firstNonEmpty(previousSpecHash, current.SpecHash, computeDraftContentHash(current))
			rollbackErr := h.rollbackRuntimeHotReload(ctx, soul, action, adapter, target, runtimePubkey, proposedDraft, current, proposed, diff, rollbackSpecHash)
			if rollbackErr != nil {
				return fmt.Errorf("hot-reload %s via %s: %w; rollback failed: %v", call.Section, call.Method, executeErr, rollbackErr)
			}
			return fmt.Errorf("hot-reload %s via %s: %w", call.Section, call.Method, executeErr)
		}
		applied = append(applied, map[string]interface{}{
			"section": call.Section,
			"method":  call.Method,
			"status":  result.Status,
			"result":  result.Result,
		})
		if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applied %s hot-reload", call.Section), soul.AgentID); err != nil {
			return fmt.Errorf("publish %s hot-reload applied progress: %w", call.Section, err)
		}
		return nil
	}
	var runFrom func(ctx context.Context, start int) (*LifecycleExecutionResult, error)
	runFrom = func(ctx context.Context, start int) (*LifecycleExecutionResult, error) {
		for i := start; i < len(calls); i++ {
			call := calls[i]
			if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applying %s hot-reload via %s", call.Section, call.Method), soul.AgentID); err != nil {
				return nil, fmt.Errorf("publish %s hot-reload progress: %w", call.Section, err)
			}
			result, executeErr := adapter.Execute(ctx, RuntimeAdapterRequest{
				Method: call.Method,
				Operator: RuntimeOperatorRef{
					Pubkey:       action.Initiator,
					RequestEvent: action.EventID,
				},
				Soul: RuntimeSoulRef{
					ID:       soul.AgentID,
					Draft:    firstNonEmpty(proposedDraft.EventID, action.DraftEventID, action.DraftRef),
					SpecHash: newSpecHash,
				},
				Target: RuntimeTargetRef{
					Runtime:       target,
					RuntimePubkey: runtimePubkey,
					AgentID:       soul.AgentID,
				},
				Params:      call.Params,
				DraftPolicy: proposed.RelayPolicy,
				RequestKind: domain.KindSoulAction,
				Action:      action.Action,
			})
			if awaiting, ok := awaitRuntimeStep(executeErr, func(ctx context.Context, late *RuntimeControlResultEnvelope) (*LifecycleExecutionResult, error) {
				if err := record(ctx, i, late, runtimeResultFailure(late)); err != nil {
					return nil, err
				}
				return runFrom(ctx, i+1)
			}); ok {
				return nil, awaiting
			}
			if err := record(ctx, i, result, executeErr); err != nil {
				return nil, err
			}
		}
		return finish()
	}
	return runFrom(ctx, 0)
}

func (h *LifecycleHandler) handleRollback(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	rollbackDraftRef := firstNonEmpty(action.DraftRef, soul.PreviousDraftRef)
	rollbackDraftEventID := firstNonEmpty(action.DraftEventID, soul.PreviousDraftEventID)
	if rollbackDraftRef == "" && rollbackDraftEventID == "" {
		return nil, fmt.Errorf("rollback requires previous draft_ref or draft_event_id")
	}
	rollbackAction := *action
	rollbackAction.Action = domain.SoulActionHotReload
	rollbackAction.DraftRef = rollbackDraftRef
	rollbackAction.DraftEventID = rollbackDraftEventID
	rollbackAction.SpecHash = firstNonEmpty(action.SpecHash, soul.PreviousSpecHash)
	rollbackAction.PreviousSpecHash = firstNonEmpty(action.PreviousSpecHash, soul.SpecHash)
	var markRollback func(*LifecycleExecutionResult, error) (*LifecycleExecutionResult, error)
	markRollback = func(result *LifecycleExecutionResult, err error) (*LifecycleExecutionResult, error) {
		var awaiting *awaitingRuntimeResult
		if errors.As(err, &awaiting) {
			return nil, awaiting.then(markRollback)
		}
		if err != nil {
			return nil, err
		}
		if result.Data == nil {
			result.Data = map[string]interface{}{}
		}
		result.Data["rollback"] = true
		result.Data["rollback_draft_ref"] = rollbackDraftRef
		result.Data["rollback_draft_event_id"] = rollbackDraftEventID
		return result, nil
	}
	return markRollback(h.handleHotReload(ctx, soul, &rollbackAction))
}

func (h *LifecycleHandler) rollbackRuntimeHotReload(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction, adapter RuntimeAdapter, target domain.RuntimeTarget, runtimePubkey string, draft *domain.SoulDraft, previous, failed domain.SoulDraftContent, diff HotReloadDraftDiff, rollbackSpecHash string) error {
	if adapter == nil {
		return fmt.Errorf("rollback requires a runtime adapter")
	}
	calls := buildHotReloadRuntimeCalls(failed, previous, diff, draft, action, rollbackSpecHash, failed.SpecHash)
	// Every section is rolled back even when one rollback fails or its outcome
	// is unknown, so a rollback is never abandoned half-applied.
	var rollbackErrors []error
	for _, call := range calls {
		if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("rolling back %s hot-reload via %s", call.Section, call.Method), soul.AgentID); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("publish %s rollback progress: %w", call.Section, err))
		}
		_, err := adapter.Execute(ctx, RuntimeAdapterRequest{
			Method:      call.Method,
			Operator:    RuntimeOperatorRef{Pubkey: action.Initiator, RequestEvent: action.EventID},
			Soul:        RuntimeSoulRef{ID: soul.AgentID, Draft: firstNonEmpty(soul.DraftEventID, soul.DraftRef), SpecHash: rollbackSpecHash},
			Target:      RuntimeTargetRef{Runtime: target, RuntimePubkey: runtimePubkey, AgentID: soul.AgentID},
			Params:      call.Params,
			DraftPolicy: previous.RelayPolicy,
			RequestKind: domain.KindSoulAction,
			Action:      domain.SoulActionRollback,
		})
		if err != nil {
			rollbackErrors = append(rollbackErrors, rollbackStepError("rollback "+call.Section, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (h *LifecycleHandler) lookupHotReloadDraft(ctx context.Context, draftRef, draftEventID string) (*domain.SoulDraft, error) {
	return h.reactor.getProvisioningDraft(ctx, draftRef, draftEventID)
}

func (h *LifecycleHandler) currentHotReloadContent(ctx context.Context, soul *domain.AgentSoul) (domain.SoulDraftContent, error) {
	if soul.DraftRef != "" || soul.DraftEventID != "" {
		draft, err := h.lookupHotReloadDraft(ctx, soul.DraftRef, soul.DraftEventID)
		if err != nil {
			return domain.SoulDraftContent{}, fmt.Errorf("lookup current draft: %w", err)
		}
		if draft != nil {
			return draft.Content.MigrateToLatest(), nil
		}
	}
	return synthesizeDraftContentFromSoul(soul), nil
}

func (h *LifecycleHandler) selectHotReloadRuntime(soul *domain.AgentSoul, proposed domain.SoulDraftContent) (RuntimeAdapter, domain.RuntimeTarget, string, error) {
	adapters := h.runtimeAdapters
	if len(adapters) == 0 && h.reactor != nil {
		if full, ok := h.reactor.provisioner.(*FullProvisioner); ok && full != nil {
			adapters = full.runtimeAdapters
		}
	}
	if len(adapters) == 0 {
		return nil, "", "", fmt.Errorf("hot-reload requires a runtime adapter")
	}
	target := proposed.Runtime.Target
	if target == "" {
		target = soul.Runtime.Target
	}
	if target == "" && len(adapters) == 1 {
		for candidate := range adapters {
			target = candidate
		}
	}
	if target == "" {
		return nil, "", "", fmt.Errorf("hot-reload requires a runtime target")
	}
	adapter := adapters[target]
	if adapter == nil {
		return nil, "", "", fmt.Errorf("no runtime adapter configured for %s", target)
	}
	runtimePubkey := firstNonEmpty(proposed.Runtime.RuntimePubkey, soul.Runtime.RuntimePubkey)
	return adapter, target, runtimePubkey, nil
}

func buildHotReloadRuntimeCalls(current, proposed domain.SoulDraftContent, diff HotReloadDraftDiff, draft *domain.SoulDraft, action *domain.SoulAction, newSpecHash, previousSpecHash string) []hotReloadRuntimeCall {
	calls := make([]hotReloadRuntimeCall, 0, len(diff.ChangedSections))
	base := func(section string) map[string]interface{} {
		return map[string]interface{}{
			"schema":             domain.SoulFactoryDraftSchemaLatest,
			"section":            section,
			"draft_ref":          firstNonEmpty(action.DraftRef, parameterizedCoordinate(domain.KindSoulDraft, draft.CreatedBy, draft.AgentID)),
			"draft_event_id":     draft.EventID,
			"previous_spec_hash": previousSpecHash,
			"new_spec_hash":      newSpecHash,
		}
	}
	if diff.Avatar {
		params := base("avatar")
		params["previous"] = hotReloadAvatarSection(current)
		params["proposed"] = hotReloadAvatarSection(proposed)
		method := RuntimeMethodAvatarSet
		if proposed.Avatar.Generation != nil && draftSectionChanged(current.Avatar.Generation, proposed.Avatar.Generation) {
			method = RuntimeMethodAvatarGenerate
		}
		calls = append(calls, hotReloadRuntimeCall{Section: "avatar", Method: method, Params: params})
	}
	if diff.Voice {
		params := base("voice")
		params["previous"] = hotReloadVoiceSection(current)
		params["proposed"] = hotReloadVoiceSection(proposed)
		calls = append(calls, hotReloadRuntimeCall{Section: "voice", Method: RuntimeMethodVoiceConfigure, Params: params})
	}
	if diff.Memory {
		params := base("memory")
		params["previous"] = current.Memory
		params["proposed"] = proposed.Memory
		calls = append(calls, hotReloadRuntimeCall{Section: "memory", Method: RuntimeMethodMemoryConfigure, Params: params})
		if proposed.Memory.AutoIndex {
			reindexParams, err := BuildMemoryReindexRuntimeParams(proposed.Memory, MemoryReindexModeIncremental, "hot-reload memory config changed", previousSpecHash, newSpecHash, params["draft_ref"].(string), draft.EventID)
			if err == nil {
				calls = append(calls, hotReloadRuntimeCall{Section: "memory", Method: RuntimeMethodMemoryReindex, Params: reindexParams})
			}
		}
	}
	if diff.Persona {
		params := base("persona")
		params["previous"] = hotReloadPersonaSection(current)
		params["proposed"] = hotReloadPersonaSection(proposed)
		calls = append(calls, hotReloadRuntimeCall{Section: "persona", Method: RuntimeMethodPersonaUpdate, Params: params})
	}
	return calls
}

func applyHotReloadDraftToSoul(soul *domain.AgentSoul, draft *domain.SoulDraft, proposed domain.SoulDraftContent, action *domain.SoulAction, newSpecHash, previousSpecHash string, applied []map[string]interface{}) {
	currentDraftRef := soul.DraftRef
	currentDraftEventID := soul.DraftEventID
	if action.DraftRef != "" {
		soul.DraftRef = action.DraftRef
	} else if draft != nil {
		soul.DraftRef = parameterizedCoordinate(domain.KindSoulDraft, draft.CreatedBy, draft.AgentID)
	}
	if draft != nil {
		soul.DraftEventID = draft.EventID
	}
	if currentDraftRef != "" {
		soul.PreviousDraftRef = currentDraftRef
	}
	if currentDraftEventID != "" {
		soul.PreviousDraftEventID = currentDraftEventID
	}
	if previousSpecHash != "" {
		soul.PreviousSpecHash = previousSpecHash
	}
	if newSpecHash != "" {
		soul.SpecHash = newSpecHash
	}
	soul.Name = firstNonEmpty(proposed.Identity.Name, soul.Name)
	soul.Purpose = firstNonEmpty(proposed.Identity.Purpose, proposed.Brief, soul.Purpose)
	if proposed.Identity.Tier != "" {
		soul.Tier = proposed.Identity.Tier
	}
	soul.NIP05 = firstNonEmpty(proposed.Identity.NIP05, soul.NIP05)
	soul.SoulMD = firstNonEmpty(proposed.SoulMD, soul.SoulMD)
	soul.IdentityMD = firstNonEmpty(proposed.IdentityMD, soul.IdentityMD)
	if len(proposed.Permissions.AllowedKinds) > 0 {
		soul.AllowedKinds = append([]int{}, proposed.Permissions.AllowedKinds...)
	}
	if len(proposed.Permissions.ToolGrants) > 0 {
		soul.ToolGrants = append([]domain.ToolGrant{}, proposed.Permissions.ToolGrants...)
	}
	soul.PermissionSpec = proposed.Permissions
	soul.RelayPolicy = proposed.RelayPolicy
	soul.Workspace = proposed.Workspace
	if proposed.Runtime.Target != "" {
		soul.Runtime.Target = proposed.Runtime.Target
	}
	soul.Runtime.RuntimePubkey = firstNonEmpty(proposed.Runtime.RuntimePubkey, soul.Runtime.RuntimePubkey)
	soul.Runtime.CapabilityRef = firstNonEmpty(proposed.Runtime.CapabilityRef, soul.Runtime.CapabilityRef)
	soul.Runtime.RuntimeBinding = firstNonEmpty(proposed.Runtime.RuntimeBinding, soul.Runtime.RuntimeBinding)
	soul.Runtime.State = firstNonEmpty(proposed.Runtime.State, soul.Runtime.State)
	if avatarRef := selectedAvatarRef(proposed); avatarRef != "" {
		soul.Assets.AvatarRef = avatarRef
	}
	if voiceRef := firstNonEmpty(proposed.Assets.VoiceRef, proposed.Voice.PersonaID); voiceRef != "" {
		soul.Assets.VoiceRef = voiceRef
	}
	applyRuntimeResultsToSoul(soul, applied)
}

func hotReloadAvatarSection(content domain.SoulDraftContent) map[string]interface{} {
	return map[string]interface{}{
		"avatar":        content.Avatar,
		"avatar_prompt": content.AvatarPrompt,
		"asset_ref":     content.Assets.AvatarRef,
	}
}

func hotReloadVoiceSection(content domain.SoulDraftContent) map[string]interface{} {
	return map[string]interface{}{
		"voice":     content.Voice,
		"asset_ref": content.Assets.VoiceRef,
	}
}

func hotReloadPersonaSection(content domain.SoulDraftContent) map[string]interface{} {
	return map[string]interface{}{
		"identity":    content.Identity,
		"persona":     content.Persona,
		"soul_md":     content.SoulMD,
		"identity_md": content.IdentityMD,
	}
}

func draftSectionChanged(current, proposed interface{}) bool {
	if reflect.DeepEqual(current, proposed) {
		return false
	}
	currentJSON, currentErr := json.Marshal(current)
	proposedJSON, proposedErr := json.Marshal(proposed)
	if currentErr != nil || proposedErr != nil {
		return true
	}
	return string(currentJSON) != string(proposedJSON)
}

func selectedAvatarRef(content domain.SoulDraftContent) string {
	switch content.Avatar.Current {
	case "uploaded":
		return firstNonEmpty(content.Avatar.UploadedRef, content.Assets.AvatarRef)
	case "generated":
		return firstNonEmpty(content.Avatar.GeneratedRef, content.Assets.AvatarRef)
	default:
		return firstNonEmpty(content.Assets.AvatarRef, content.Avatar.UploadedRef, content.Avatar.GeneratedRef)
	}
}

func synthesizeDraftContentFromSoul(soul *domain.AgentSoul) domain.SoulDraftContent {
	if soul == nil {
		return domain.SoulDraftContent{Schema: domain.SoulFactoryDraftSchemaLatest}
	}
	return domain.SoulDraftContent{
		Schema:     domain.SoulFactoryDraftSchemaLatest,
		Brief:      soul.OriginalBrief,
		SoulMD:     soul.SoulMD,
		IdentityMD: soul.IdentityMD,
		Identity: domain.SoulIdentitySpec{
			Name:    soul.Name,
			Purpose: soul.Purpose,
			Tier:    soul.Tier,
			NIP05:   soul.NIP05,
		},
		Runtime:          soul.Runtime,
		Permissions:      soul.PermissionSpec,
		RelayPolicy:      soul.RelayPolicy,
		Workspace:        soul.Workspace,
		Assets:           soul.Assets,
		SpecHash:         soul.SpecHash,
		PreviousSpecHash: soul.PreviousSpecHash,
	}
}

func computeDraftContentHash(content domain.SoulDraftContent) string {
	content = content.MigrateToLatest()
	content.SpecHash = ""
	content.PreviousSpecHash = ""
	data, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func hotReloadSectionsText(sections []string) string {
	if len(sections) == 0 {
		return "none"
	}
	return strings.Join(sections, ",")
}

type localLifecycleEngine struct {
	reactor          *Reactor
	bahiaIntegration *BahiaIntegration
	statusSync       *StatusSyncHandler
	logger           *slog.Logger
}

func (e *localLifecycleEngine) ExecuteLifecycleAction(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	switch action.Action {
	case domain.SoulActionSuspend:
		return e.handleSuspend(ctx, soul, action)
	case domain.SoulActionResume:
		return e.handleResume(ctx, soul, action)
	case domain.SoulActionRevoke:
		return e.handleRevoke(ctx, soul, action)
	case domain.SoulActionRegenerate:
		return e.handleRegenerate(ctx, soul, action)
	case domain.SoulActionRedeploy:
		return e.handleRedeploy(ctx, soul, action)
	case domain.SoulActionUpdate:
		return nil, fmt.Errorf("update is orchestrated by lifecycle handler")
	case domain.SoulActionHotReload:
		return nil, fmt.Errorf("hot-reload is orchestrated by lifecycle handler")
	case domain.SoulActionRollback:
		return nil, fmt.Errorf("rollback is orchestrated by lifecycle handler")
	default:
		return nil, fmt.Errorf("unknown action: %s", action.Action)
	}
}

// handleSuspend pauses a soul's operation and deployment.
func (e *localLifecycleEngine) handleSuspend(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if e.bahiaIntegration != nil {
		if err := e.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionSuspend); err != nil {
			return nil, fmt.Errorf("bahia suspend: %w", err)
		}
	}
	if soul.NostrPubkey != "" {
		if err := e.reactor.signer.SuspendAgent(ctx, soul.NostrPubkey); err != nil {
			return nil, fmt.Errorf("suspend signer access: %w", err)
		}
	}

	soul.Status = domain.SoulStatusSuspended
	soul.DeployStatus = "stopped"
	now := time.Now().UTC()
	soul.SuspendedAt = &now
	return &LifecycleExecutionResult{PublishSoul: true}, nil
}

// handleResume resumes a suspended soul.
func (e *localLifecycleEngine) handleResume(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if soul.Status != domain.SoulStatusSuspended {
		return nil, fmt.Errorf("cannot resume soul in status %s", soul.Status)
	}
	if e.bahiaIntegration != nil {
		if err := e.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionResume); err != nil {
			return nil, fmt.Errorf("bahia resume: %w", err)
		}
	}
	if soul.NostrPubkey != "" {
		if err := e.reactor.signer.ResumeAgent(ctx, soul.NostrPubkey); err != nil {
			return nil, fmt.Errorf("resume signer access: %w", err)
		}
	}

	soul.Status = domain.SoulStatusActive
	soul.DeployStatus = "deploying"
	soul.SuspendedAt = nil
	return &LifecycleExecutionResult{PublishSoul: true}, nil
}

// handleRevoke permanently terminates a soul.
func (e *localLifecycleEngine) handleRevoke(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if e.bahiaIntegration != nil {
		if err := e.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRevoke); err != nil {
			return nil, fmt.Errorf("bahia revoke: %w", err)
		}
	}
	if soul.NostrPubkey != "" {
		if err := e.reactor.signer.RevokeAgent(ctx, soul.NostrPubkey); err != nil {
			return nil, fmt.Errorf("revoke signer access: %w", err)
		}
	}

	soul.Status = domain.SoulStatusRevoked
	soul.DeployStatus = "stopped"
	now := time.Now().UTC()
	soul.RevokedAt = &now
	if e.statusSync != nil && soul.BahiaServiceID != nil {
		e.statusSync.UnregisterSoul(*soul.BahiaServiceID)
	}
	return &LifecycleExecutionResult{PublishSoul: true}, nil
}

// handleRegenerate regenerates a soul's identity with a new brief.
func (e *localLifecycleEngine) handleRegenerate(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	logger := e.logger.With("agent_id", soul.AgentID)
	if action.NewBrief == "" {
		return nil, fmt.Errorf("regenerate requires a new brief")
	}
	if soul.Status == domain.SoulStatusRevoked {
		return nil, fmt.Errorf("cannot regenerate revoked soul")
	}

	output, err := e.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{
		AgentID: soul.AgentID,
		Name:    soul.Name,
		Brief:   action.NewBrief,
		Tier:    soul.Tier,
	})
	if err != nil {
		return nil, fmt.Errorf("regenerate soul: %w", err)
	}

	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	soul.OriginalBrief = action.NewBrief

	logger.Info("soul regenerated",
		"new_allowed_kinds", len(output.AllowedKinds),
		"new_tool_grants", len(output.ToolGrants),
	)
	return &LifecycleExecutionResult{
		PublishSoul: true,
		Data: map[string]interface{}{
			"regenerated": true,
			"new_brief":   action.NewBrief,
		},
	}, nil
}

// handleRedeploy triggers a fresh deployment of the soul.
func (e *localLifecycleEngine) handleRedeploy(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if soul.Status != domain.SoulStatusActive {
		return nil, fmt.Errorf("cannot redeploy soul in status %s", soul.Status)
	}
	if e.bahiaIntegration != nil {
		if err := e.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRedeploy); err != nil {
			return nil, fmt.Errorf("bahia redeploy: %w", err)
		}
	}
	soul.DeployStatus = "deploying"
	return &LifecycleExecutionResult{
		PublishSoul: true,
		Data:        map[string]interface{}{"redeploying": true},
	}, nil
}

func actionFromEventForError(event *nostr.Event) *domain.SoulAction {
	return &domain.SoulAction{
		EventID:   event.ID.Hex(),
		SoulRef:   tagValue(event.Tags, tagSoul),
		Action:    domain.SoulActionType(tagValue(event.Tags, tagAction)),
		Initiator: event.PubKey.Hex(),
		CreatedAt: event.CreatedAt.Time(),
	}
}

func isSupportedLifecycleAction(action domain.SoulActionType) bool {
	switch action {
	case domain.SoulActionSuspend,
		domain.SoulActionResume,
		domain.SoulActionRevoke,
		domain.SoulActionRegenerate,
		domain.SoulActionRedeploy,
		domain.SoulActionUpdate,
		domain.SoulActionHotReload,
		domain.SoulActionRollback,
		domain.SoulActionAbandon:
		return true
	default:
		return false
	}
}
