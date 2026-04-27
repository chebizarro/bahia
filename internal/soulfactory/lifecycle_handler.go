package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// LifecycleHandler processes soul lifecycle actions (kind:1950) and coordinates
// with bahia for deployment state changes.
type LifecycleHandler struct {
	reactor          *Reactor
	bahiaIntegration *BahiaIntegration
	statusSync       *StatusSyncHandler
	logger           *slog.Logger
}

// NewLifecycleHandler creates a new lifecycle handler.
func NewLifecycleHandler(
	reactor *Reactor,
	bahiaIntegration *BahiaIntegration,
	statusSync *StatusSyncHandler,
	logger *slog.Logger,
) *LifecycleHandler {
	return &LifecycleHandler{
		reactor:          reactor,
		bahiaIntegration: bahiaIntegration,
		statusSync:       statusSync,
		logger:           logger,
	}
}

// HandleAction processes a soul lifecycle action event.
func (h *LifecycleHandler) HandleAction(ctx context.Context, event *nostr.Event) error {
	logger := h.logger.With("event_id", event.ID)

	// Parse action from event
	action, err := h.parseAction(event)
	if err != nil {
		return fmt.Errorf("parse action: %w", err)
	}

	logger = logger.With(
		"action", action.Action,
		"soul_ref", action.SoulRef,
		"initiator", action.Initiator,
	)
	logger.Info("handling lifecycle action")

	// Look up the soul
	soul, err := h.reactor.GetSoul(ctx, action.SoulRef)
	if err != nil {
		return fmt.Errorf("lookup soul: %w", err)
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", action.SoulRef)
	}

	// Verify authorization
	if !h.isAuthorized(event.PubKey, soul) {
		return fmt.Errorf("unauthorized: %s cannot perform %s on soul %s",
			event.PubKey, action.Action, soul.AgentID)
	}

	// Execute the action
	switch action.Action {
	case domain.SoulActionSuspend:
		return h.handleSuspend(ctx, soul, action)
	case domain.SoulActionResume:
		return h.handleResume(ctx, soul, action)
	case domain.SoulActionRevoke:
		return h.handleRevoke(ctx, soul, action)
	case domain.SoulActionRegenerate:
		return h.handleRegenerate(ctx, soul, action)
	case domain.SoulActionRedeploy:
		return h.handleRedeploy(ctx, soul, action)
	default:
		return fmt.Errorf("unknown action: %s", action.Action)
	}
}

// handleSuspend pauses a soul's operation and deployment.
func (h *LifecycleHandler) handleSuspend(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) error {
	logger := h.logger.With("agent_id", soul.AgentID)

	// Update soul status
	soul.Status = domain.SoulStatusSuspended
	now := time.Now()
	soul.SuspendedAt = &now

	// Update bahia deployment
	if h.bahiaIntegration != nil {
		if err := h.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionSuspend); err != nil {
			logger.Warn("bahia suspend failed", "error", err)
		}
	}

	// Revoke Signet access (temporarily)
	// Note: In production, this would call Signet to pause signing capability

	// Publish updated soul event
	if err := h.publishSoulUpdate(ctx, soul, "suspended", action.Reason); err != nil {
		return fmt.Errorf("publish soul update: %w", err)
	}

	// Publish action result
	return h.publishActionResult(ctx, action, "completed", nil)
}

// handleResume resumes a suspended soul.
func (h *LifecycleHandler) handleResume(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) error {
	logger := h.logger.With("agent_id", soul.AgentID)

	if soul.Status != domain.SoulStatusSuspended {
		return fmt.Errorf("cannot resume soul in status %s", soul.Status)
	}

	// Update soul status
	soul.Status = domain.SoulStatusActive
	soul.SuspendedAt = nil

	// Update bahia deployment
	if h.bahiaIntegration != nil {
		if err := h.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionResume); err != nil {
			logger.Warn("bahia resume failed", "error", err)
		}
	}

	// Restore Signet access
	// Note: In production, this would call Signet to restore signing capability

	// Publish updated soul event
	if err := h.publishSoulUpdate(ctx, soul, "active", ""); err != nil {
		return fmt.Errorf("publish soul update: %w", err)
	}

	return h.publishActionResult(ctx, action, "completed", nil)
}

// handleRevoke permanently terminates a soul.
func (h *LifecycleHandler) handleRevoke(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) error {
	logger := h.logger.With("agent_id", soul.AgentID)

	// Update soul status
	soul.Status = domain.SoulStatusRevoked
	now := time.Now()
	soul.RevokedAt = &now

	// Update bahia deployment (terminate)
	if h.bahiaIntegration != nil {
		if err := h.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRevoke); err != nil {
			logger.Warn("bahia revoke failed", "error", err)
		}
	}

	// Permanently revoke Signet access
	if err := h.reactor.signer.RevokeAgent(ctx, soul.AgentID); err != nil {
		logger.Warn("signet revocation failed", "error", err)
	}

	// Unregister from status sync
	if h.statusSync != nil && soul.BahiaServiceID != nil {
		h.statusSync.UnregisterSoul(*soul.BahiaServiceID)
	}

	// Publish updated soul event
	if err := h.publishSoulUpdate(ctx, soul, "revoked", action.Reason); err != nil {
		return fmt.Errorf("publish soul update: %w", err)
	}

	return h.publishActionResult(ctx, action, "completed", nil)
}

// handleRegenerate regenerates a soul's identity with a new brief.
func (h *LifecycleHandler) handleRegenerate(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) error {
	logger := h.logger.With("agent_id", soul.AgentID)

	if action.NewBrief == "" {
		return fmt.Errorf("regenerate requires a new brief")
	}

	// Generate new soul content
	output, err := h.reactor.generator.Generate(ctx, domain.SoulGeneratorInput{
		AgentID: soul.AgentID,
		Name:    soul.Name,
		Brief:   action.NewBrief,
		Tier:    soul.Tier,
	})
	if err != nil {
		return fmt.Errorf("regenerate soul: %w", err)
	}

	// Update soul with new content
	soul.SoulMD = output.SoulMD
	soul.IdentityMD = output.IdentityMD
	soul.AllowedKinds = output.AllowedKinds
	soul.ToolGrants = output.ToolGrants
	soul.OriginalBrief = action.NewBrief

	logger.Info("soul regenerated",
		"new_allowed_kinds", len(output.AllowedKinds),
		"new_tool_grants", len(output.ToolGrants),
	)

	// Publish updated soul event
	if err := h.publishSoulUpdate(ctx, soul, "", ""); err != nil {
		return fmt.Errorf("publish soul update: %w", err)
	}

	return h.publishActionResult(ctx, action, "completed", map[string]interface{}{
		"regenerated": true,
		"new_brief":   action.NewBrief,
	})
}

// handleRedeploy triggers a fresh deployment of the soul.
func (h *LifecycleHandler) handleRedeploy(ctx context.Context, soul *domain.AgentSoul, action *domain.SoulAction) error {
	logger := h.logger.With("agent_id", soul.AgentID)

	if soul.Status != domain.SoulStatusActive {
		return fmt.Errorf("cannot redeploy soul in status %s", soul.Status)
	}

	// Trigger bahia redeploy
	if h.bahiaIntegration != nil {
		if err := h.bahiaIntegration.HandleLifecycleAction(ctx, soul, domain.SoulActionRedeploy); err != nil {
			return fmt.Errorf("bahia redeploy: %w", err)
		}
	}

	logger.Info("soul redeployment triggered")

	return h.publishActionResult(ctx, action, "completed", map[string]interface{}{
		"redeploying": true,
	})
}

// parseAction extracts action details from a kind:1950 event.
func (h *LifecycleHandler) parseAction(event *nostr.Event) (*domain.SoulAction, error) {
	action := &domain.SoulAction{
		EventID:   event.ID,
		Initiator: event.PubKey,
		CreatedAt: event.CreatedAt.Time(),
	}

	// Extract from tags
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "soul":
			action.SoulRef = tag[1]
		case "action":
			action.Action = domain.SoulActionType(tag[1])
		case "reason":
			action.Reason = tag[1]
		}
	}

	// Parse content for additional data
	if event.Content != "" {
		var content struct {
			NewBrief string `json:"new_brief,omitempty"`
			Reason   string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err == nil {
			if content.NewBrief != "" {
				action.NewBrief = content.NewBrief
			}
			if content.Reason != "" && action.Reason == "" {
				action.Reason = content.Reason
			}
		}
	}

	if action.SoulRef == "" {
		return nil, fmt.Errorf("missing soul reference")
	}
	if action.Action == "" {
		return nil, fmt.Errorf("missing action type")
	}

	return action, nil
}

// isAuthorized checks if the initiator can perform the action.
func (h *LifecycleHandler) isAuthorized(pubkey string, soul *domain.AgentSoul) bool {
	// For now, allow if the pubkey matches any authorized key in reactor config
	// In production, this would check:
	// 1. Admin keys
	// 2. Soul owner (creator)
	// 3. Delegated permissions

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
func (h *LifecycleHandler) publishSoulUpdate(ctx context.Context, soul *domain.AgentSoul, statusOverride, reason string) error {
	tags := nostr.Tags{
		{"d", soul.AgentID},
		{"name", soul.Name},
		{"status", string(soul.Status)},
		{"tier", string(soul.Tier)},
		{"npub", soul.NostrNpub},
	}

	if statusOverride != "" {
		// Replace status tag
		for i, tag := range tags {
			if tag[0] == "status" {
				tags[i] = nostr.Tag{"status", statusOverride}
				break
			}
		}
	}

	if soul.DeployStatus != "" {
		tags = append(tags, nostr.Tag{"deploy-status", soul.DeployStatus})
	}
	if soul.AvatarURL != "" {
		tags = append(tags, nostr.Tag{"avatar", soul.AvatarURL})
	}
	if soul.NIP05 != "" {
		tags = append(tags, nostr.Tag{"nip05", soul.NIP05})
	}
	if soul.BahiaServiceID != nil {
		tags = append(tags, nostr.Tag{"bahia-service", soul.BahiaServiceID.String()})
	}
	if reason != "" {
		tags = append(tags, nostr.Tag{"reason", reason})
	}

	event := &nostr.Event{
		Kind:      domain.KindAgentSoul,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   soul.SoulMD,
	}

	if err := h.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign event: %w", err)
	}

	return h.reactor.publish(ctx, event, h.reactor.config.Relays)
}

// publishActionResult publishes the result of an action.
func (h *LifecycleHandler) publishActionResult(ctx context.Context, action *domain.SoulAction, status string, data map[string]interface{}) error {
	tags := nostr.Tags{
		{"e", action.EventID}, // Reference the original action event
		{"soul", action.SoulRef},
		{"action", string(action.Action)},
		{"status", status},
	}

	content := ""
	if data != nil {
		contentBytes, _ := json.Marshal(data)
		content = string(contentBytes)
	}

	event := &nostr.Event{
		Kind:      domain.KindSoulAction + 1, // Action result kind
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   content,
	}

	if err := h.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign result: %w", err)
	}

	return h.reactor.publish(ctx, event, h.reactor.config.Relays)
}
