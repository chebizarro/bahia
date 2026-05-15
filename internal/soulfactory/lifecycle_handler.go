package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
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

	result, err := h.engine.ExecuteLifecycleAction(ctx, soul, action)
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
		domain.SoulActionUpdate:
		return true
	default:
		return false
	}
}
