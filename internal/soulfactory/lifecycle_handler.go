package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"

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
// canonical 7950 terminal results. The 1951 legacy result alias is optional and
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

// HandleAction processes a soul lifecycle action event.
func (h *LifecycleHandler) HandleAction(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("nil lifecycle action event")
	}
	logger := h.logger.With("event_id", event.ID)

	// Parse action from event.
	action, err := h.parseAction(event)
	if err != nil {
		fallback := actionFromEventForError(event)
		_ = h.publishActionResult(ctx, fallback, "error", map[string]interface{}{"error": err.Error()}, "")
		return fmt.Errorf("parse action: %w", err)
	}

	logger = logger.With(
		"action", action.Action,
		"soul_ref", action.SoulRef,
		"initiator", action.Initiator,
	)
	logger.Info("handling lifecycle action")

	// Look up the soul.
	soul, err := h.reactor.GetSoul(ctx, action.SoulRef)
	if err != nil {
		return fmt.Errorf("lookup soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", action.SoulRef)
	}

	// Verify authorization.
	if !h.isAuthorized(event.PubKey, soul) {
		err := fmt.Errorf("unauthorized: %s cannot perform %s on soul %s",
			event.PubKey, action.Action, soul.AgentID)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}
	if !isSupportedLifecycleAction(action.Action) {
		err := fmt.Errorf("unknown action: %s", action.Action)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}

	if existing, err := h.findExistingTerminalResult(ctx, action.EventID); err != nil {
		logger.Warn("failed to check existing lifecycle terminal result", "error", err)
	} else if existing != nil {
		logger.Info("ignoring lifecycle action with existing terminal result", "result_event", existing.ID)
		h.beginAction(action.EventID)
		return nil
	}

	if !h.beginAction(action.EventID) {
		logger.Info("ignoring replayed lifecycle action")
		return nil
	}

	if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("processing %s action", action.Action), soul.AgentID); err != nil {
		h.clearAction(action.EventID)
		return fmt.Errorf("publish action progress: %w", err)
	}

	var result *LifecycleExecutionResult
	if action.Action == domain.SoulActionHotReload {
		result, err = h.handleHotReload(ctx, soul, action)
	} else if action.Action == domain.SoulActionRollback {
		result, err = h.handleRollback(ctx, soul, action)
	} else {
		result, err = h.engine.ExecuteLifecycleAction(ctx, soul, action)
	}
	if err != nil {
		logger.Error("lifecycle action failed", "error", err)
		_ = h.publishActionResult(ctx, action, "error", map[string]interface{}{"error": err.Error()}, soul.AgentID)
		return err
	}
	if result == nil {
		result = &LifecycleExecutionResult{PublishSoul: true}
	}

	if result.PublishSoul {
		if err := h.publishSoulUpdate(ctx, soul); err != nil {
			return fmt.Errorf("publish soul update: %w", err)
		}
	}

	return h.publishActionResult(ctx, action, "completed", result.Data, soul.AgentID)
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

func (h *LifecycleHandler) findExistingTerminalResult(ctx context.Context, eventID string) (*nostr.Event, error) {
	if h.reactor.findLifecycleResultFn != nil {
		return h.reactor.findLifecycleResultFn(ctx, eventID)
	}
	bus := h.reactor.relayBus
	if bus == nil {
		return nil, nil
	}
	results, err := bus.Query(ctx, []nostr.Filter{{
		Kinds: []int{domain.KindProvisioningResult, domain.KindSoulActionLegacyResult},
		Tags:  nostr.TagMap{"e": []string{eventID}},
		Limit: 1,
	}})
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result != nil && domain.IsLifecycleResultKind(result.Kind) && tagValue(result.Tags, tagRequestKind) == fmt.Sprint(domain.KindSoulAction) {
			return result, nil
		}
	}
	return nil, nil
}

// parseAction extracts action details from a kind:1950 event.
func (h *LifecycleHandler) parseAction(event *nostr.Event) (*domain.SoulAction, error) {
	return ParseSoulActionEvent(event)
}

// isAuthorized checks if the initiator can perform the action.
func (h *LifecycleHandler) isAuthorized(pubkey string, soul *domain.AgentSoul) bool {
	// For now, allow if the pubkey matches any authorized key in reactor config.
	// In production, this would check admin keys, ownership, and delegated permissions.
	authorizedKeys := h.reactor.config.AuthorizedPubkeys
	if len(authorizedKeys) == 0 {
		authorizedKeys = AuthorizedProvisioners
	}

	for _, authorizedKey := range authorizedKeys {
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
	event := BuildActionStatusEvent(action, status, message, agentID)
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

	if len(calls) > 0 {
		adapter, target, runtimePubkey, err := h.selectHotReloadRuntime(soul, proposed)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applying %s hot-reload via %s", call.Section, call.Method), soul.AgentID); err != nil {
				return nil, fmt.Errorf("publish %s hot-reload progress: %w", call.Section, err)
			}
			result, err := adapter.Execute(ctx, RuntimeAdapterRequest{
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
			if err != nil {
				rollbackSpecHash := firstNonEmpty(previousSpecHash, current.SpecHash, computeDraftContentHash(current))
				rollbackErr := h.rollbackRuntimeHotReload(ctx, soul, action, adapter, target, runtimePubkey, proposedDraft, current, proposed, diff, rollbackSpecHash)
				if rollbackErr != nil {
					return nil, fmt.Errorf("hot-reload %s via %s: %w; rollback failed: %v", call.Section, call.Method, err, rollbackErr)
				}
				return nil, fmt.Errorf("hot-reload %s via %s: %w", call.Section, call.Method, err)
			}
			applied = append(applied, map[string]interface{}{
				"section": call.Section,
				"method":  call.Method,
				"status":  result.Status,
				"result":  result.Result,
			})
			if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("applied %s hot-reload", call.Section), soul.AgentID); err != nil {
				return nil, fmt.Errorf("publish %s hot-reload applied progress: %w", call.Section, err)
			}
		}
	}

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
	result, err := h.handleHotReload(ctx, soul, &rollbackAction)
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

func (h *LifecycleHandler) rollbackRuntimeHotReload(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction, adapter RuntimeAdapter, target domain.RuntimeTarget, runtimePubkey string, draft *domain.SoulDraft, previous, failed domain.SoulDraftContent, diff HotReloadDraftDiff, rollbackSpecHash string) error {
	if adapter == nil {
		return fmt.Errorf("rollback requires a runtime adapter")
	}
	calls := buildHotReloadRuntimeCalls(failed, previous, diff, draft, action, rollbackSpecHash, failed.SpecHash)
	for _, call := range calls {
		if err := h.publishActionProgress(ctx, action, "processing", fmt.Sprintf("rolling back %s hot-reload via %s", call.Section, call.Method), soul.AgentID); err != nil {
			return err
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
			return err
		}
	}
	return nil
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
	for _, change := range applied {
		result, _ := change["result"].(map[string]interface{})
		if result == nil {
			continue
		}
		soul.Runtime.RuntimePubkey = firstNonEmpty(stringResult(result, "runtime_pubkey"), soul.Runtime.RuntimePubkey)
		soul.Runtime.RuntimeBinding = firstNonEmpty(stringResult(result, "runtime_binding"), soul.Runtime.RuntimeBinding)
		soul.Runtime.State = firstNonEmpty(stringResult(result, "state"), soul.Runtime.State)
		soul.Runtime.CapabilityRef = firstNonEmpty(stringResult(result, "capability_ref"), soul.Runtime.CapabilityRef)
		soul.CapabilityRef = firstNonEmpty(soul.Runtime.CapabilityRef, soul.CapabilityRef)
	}
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
		return e.handleUpdate(ctx, soul, action)
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

// handleUpdate records additive lifecycle customization refs without invoking
// runtime adapters. Draft-backed execution is intentionally left to bahia-a1so.3.
func (e *localLifecycleEngine) handleUpdate(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) (*LifecycleExecutionResult, error) {
	if action.DraftRef == "" && action.SpecHash == "" {
		return nil, fmt.Errorf("update requires draft_ref or spec_hash; patch-only updates are not applied in this slice")
	}
	if action.DraftRef != "" {
		soul.DraftRef = action.DraftRef
	}
	if action.PreviousSpecHash != "" {
		soul.PreviousSpecHash = action.PreviousSpecHash
	}
	if action.SpecHash != "" {
		soul.SpecHash = action.SpecHash
	}
	return &LifecycleExecutionResult{
		PublishSoul: true,
		Data: map[string]interface{}{
			"updated":            true,
			"draft_ref":          action.DraftRef,
			"spec_hash":          action.SpecHash,
			"previous_spec_hash": action.PreviousSpecHash,
		},
	}, nil
}

func actionFromEventForError(event *nostr.Event) *domain.SoulAction {
	return &domain.SoulAction{
		EventID:   event.ID,
		SoulRef:   tagValue(event.Tags, tagSoul),
		Action:    domain.SoulActionType(tagValue(event.Tags, tagAction)),
		Initiator: event.PubKey,
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
		domain.SoulActionRollback:
		return true
	default:
		return false
	}
}
