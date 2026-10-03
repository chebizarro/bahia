package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// LLMRouteCRUD is the read/write contract the LLM intent handler uses for
// level-triggered reconciliation. service.LLMRegistryService satisfies it.
type LLMRouteCRUD interface {
	CreateRoute(ctx context.Context, route *domain.LLMRoute) error
	GetRoute(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error)
	UpdateRoute(ctx context.Context, route *domain.LLMRoute) error
	CreateRelease(ctx context.Context, release *domain.LLMRelease) error
	CreateDeploymentIntent(ctx context.Context, intent *domain.LLMDeploymentIntent) error
	GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error)
	ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error
	RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error
	RollbackWithMetadata(ctx context.Context, routeID, envID uuid.UUID, requestedBy string, metadata map[string]any) (*domain.LLMDeploymentIntent, error)
}

// LLMRouteStatePublisher signs and publishes a canonical kind-30900 cp-state
// record for an LLM route mutation. The implementation uses
// PublishBeforeCommit for outbox durability (design §3.6).
type LLMRouteStatePublisher func(ctx context.Context, route *domain.LLMRoute, deleted bool) error

// LLMRouteIntentHandler processes kind-30900 intents for the "llm" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// Registered at startup when "llm" is in nostr.intent_domains via
// IntentProcessor.RegisterHandler("llm", handler).
//
// LLM deployment operations retain the Reactor fleet-operator gate. Route
// registry operations accept org membership and the legacy ContextVM fleet
// operator gate during the dual-dispatch window. The handler implements
// SelfAuthorizingHandler to preserve both authorities.
//
// Revision decision: latest-wins. LLMRoute carries UpdatedAt but has
// no concurrent multi-operator editing in practice. When expected_updated_at
// is present in the intent content the handler enforces it; otherwise the
// newest intent wins unconditionally.
//
// See design §7 Wave 3 L1.
type LLMRouteIntentHandler struct {
	routes  LLMRouteCRUD
	publish LLMRouteStatePublisher
	status  *IntentStatusPublisher
	logger  *zap.Logger
}

// LLMRouteIntentHandlerConfig configures the LLM route intent handler.
type LLMRouteIntentHandlerConfig struct {
	Routes  LLMRouteCRUD
	Publish LLMRouteStatePublisher
	Status  *IntentStatusPublisher
	Logger  *zap.Logger
}

// NewLLMRouteIntentHandler constructs the handler.
func NewLLMRouteIntentHandler(cfg LLMRouteIntentHandlerConfig) *LLMRouteIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LLMRouteIntentHandler{
		routes:  cfg.Routes,
		publish: cfg.Publish,
		status:  cfg.Status,
		logger:  logger.Named("llm-intent"),
	}
}

// HandleIntent processes a single LLM intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *LLMRouteIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "deploy":
		return h.handleDeploymentCreate(ctx, intent)
	case "rollback":
		return h.handleDeploymentRollback(ctx, intent)
	case "approve", "reject":
		return h.handleDeploymentDecision(ctx, intent)
	case "release-register":
		return h.handleReleaseRegister(ctx, intent)
	case "delete":
		return h.handleDelete(ctx, intent)
	default:
		// Level-triggered: create and update both reconcile toward desired state.
		return h.handleCreateOrUpdate(ctx, intent)
	}
}

// PermissionFor returns the permission required for LLM operations.
func (h *LLMRouteIntentHandler) PermissionFor(op string) domain.Permission {
	switch op {
	case "deploy", "rollback":
		return domain.PermWriteDeployments
	case "approve", "reject":
		return domain.PermApproveDeployments
	default:
		return domain.PermWriteServices
	}
}

func (h *LLMRouteIntentHandler) checkDeploymentRouteRevision(ctx context.Context, intent *Intent, routeID uuid.UUID) error {
	if intent.ExpectedUpdatedAt == nil {
		return nil
	}
	route, err := h.routes.GetRoute(ctx, routeID)
	if err != nil {
		return err
	}
	if route == nil {
		return fmt.Errorf("LLM route %s not found", routeID)
	}
	expected := time.Unix(0, *intent.ExpectedUpdatedAt)
	if raw, ok := intent.Content["expected_updated_at"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			expected = parsed
		}
	}
	if !domain.SameRevision(route.UpdatedAt, expected) {
		return &revisionConflictError{entityID: routeID, expected: expected, actual: route.UpdatedAt}
	}
	return nil
}

func (h *LLMRouteIntentHandler) handleDeploymentCreate(ctx context.Context, intent *Intent) error {
	routeID, err := uuid.Parse(firstIntentString(intent.Content, "route_id"))
	if err != nil || routeID == uuid.Nil {
		return fmt.Errorf("route_id must be a UUID")
	}
	if err := h.checkDeploymentRouteRevision(ctx, intent, routeID); err != nil {
		return err
	}
	envID, err := uuid.Parse(firstIntentString(intent.Content, "environment_id"))
	if err != nil || envID == uuid.Nil {
		return fmt.Errorf("environment_id must be a UUID")
	}
	releaseID, err := uuid.Parse(firstIntentString(intent.Content, "release_id"))
	if err != nil || releaseID == uuid.Nil {
		return fmt.Errorf("release_id must be a UUID")
	}
	metadata, _ := intent.Content["metadata"].(map[string]any)
	return h.routes.CreateDeploymentIntent(ctx, &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: intent.Actor, SourceKind: domain.SourceKindEventTriggered, Metadata: metadata})
}

func (h *LLMRouteIntentHandler) handleDeploymentRollback(ctx context.Context, intent *Intent) error {
	routeID, err := uuid.Parse(firstIntentString(intent.Content, "route_id"))
	if err != nil || routeID == uuid.Nil {
		return fmt.Errorf("route_id must be a UUID")
	}
	if err := h.checkDeploymentRouteRevision(ctx, intent, routeID); err != nil {
		return err
	}
	envID, err := uuid.Parse(firstIntentString(intent.Content, "environment_id"))
	if err != nil || envID == uuid.Nil {
		return fmt.Errorf("environment_id must be a UUID")
	}
	metadata, _ := intent.Content["metadata"].(map[string]any)
	_, err = h.routes.RollbackWithMetadata(ctx, routeID, envID, intent.Actor, metadata)
	return err
}

func (h *LLMRouteIntentHandler) handleDeploymentDecision(ctx context.Context, intent *Intent) error {
	id, err := uuid.Parse(firstIntentString(intent.Content, "deployment_intent_id", "target_intent_id"))
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("deployment_intent_id must be a UUID")
	}
	current, err := h.routes.GetDeploymentIntent(ctx, id)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("LLM deployment intent %s not found", id)
	}
	if intent.ExpectedUpdatedAt != nil {
		expected := time.Unix(0, *intent.ExpectedUpdatedAt)
		if raw, ok := intent.Content["expected_updated_at"].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
				expected = parsed
			}
		}
		if !domain.SameRevision(current.UpdatedAt, expected) {
			return &revisionConflictError{entityID: id, expected: expected, actual: current.UpdatedAt}
		}
	}
	if intent.Op == "approve" {
		return h.routes.ApproveDeploymentIntent(ctx, id)
	}
	return h.routes.RejectDeploymentIntent(ctx, id)
}

// Deployment operations retain the fleet-operator authority used by the
// existing Reactor; route registry mutations also accept org RBAC.
func (h *LLMRouteIntentHandler) AuthorizeIntent(ctx context.Context, trustSet *TrustSet, intent *Intent) error {
	switch intent.Op {
	case "deploy", "rollback", "approve", "reject":
		for _, pubkey := range trustSet.FleetOps() {
			if pubkey == intent.Actor {
				return nil
			}
		}
		return fmt.Errorf("fleet operator permission required for LLM %s", intent.Op)
	default:
		// The legacy LLM ContextVM surface is fleet-gated; accept that same
		// principal during dual dispatch while relay authors may use org RBAC.
		for _, pubkey := range trustSet.FleetOps() {
			if pubkey == intent.Actor {
				return nil
			}
		}
		if trustSet.HasPermission(ctx, intent.OrgID, intent.Actor, h.PermissionFor(intent.Op)) {
			return nil
		}
		return fmt.Errorf("insufficient permission: %s", h.PermissionFor(intent.Op))
	}
}

// handleCreateOrUpdate reconciles an LLM route toward the intent's desired state.
// Level-triggered: if the entity doesn't exist, it is created regardless of
// whether the op tag says "create" or "update".
func (h *LLMRouteIntentHandler) handleCreateOrUpdate(ctx context.Context, intent *Intent) error {
	route, err := llmRouteFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse LLM route intent content: %w", err)
	}

	// Check expected_updated_at revision if present.
	if intent.ExpectedUpdatedAt != nil {
		return h.updateWithRevision(ctx, route, intent)
	}

	// Level-triggered: try to load existing, create or update accordingly.
	existing, _ := h.routes.GetRoute(ctx, route.ID)
	if existing == nil {
		return h.createRoute(ctx, route, intent)
	}

	// Entity exists: merge and update.
	mergeLLMRouteOntoExisting(existing, route)
	return h.updateRoute(ctx, existing, intent)
}

// handleDelete processes a delete intent for an LLM route.
func (h *LLMRouteIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		id, err = uuid.Parse(intent.Coordinate)
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("delete intent must carry entity id")
		}
	}

	// Publish tombstone. LLM routes have no soft-delete in the registry
	// service today, so we only publish the tombstone record.
	tombstone := &domain.LLMRoute{ID: id, UpdatedAt: time.Now().UTC()}
	if h.publish != nil {
		if err := h.publish(ctx, tombstone, true); err != nil {
			h.logger.Warn("failed to publish LLM route tombstone",
				zap.String("route_id", id.String()),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("LLM route deleted via intent",
		zap.String("route_id", id.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handleReleaseRegister processes a release-register intent.
func (h *LLMRouteIntentHandler) handleReleaseRegister(ctx context.Context, intent *Intent) error {
	release, err := llmReleaseFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse LLM release intent content: %w", err)
	}

	if err := h.routes.CreateRelease(ctx, release); err != nil {
		return fmt.Errorf("register LLM release: %w", err)
	}

	// After registering the release, publish the updated route registry record
	// so the relay has the latest route metadata.
	route, _ := h.routes.GetRoute(ctx, release.RouteID)
	if route != nil && h.publish != nil {
		if err := h.publish(ctx, route, false); err != nil {
			h.logger.Warn("LLM release registered but route publication failed",
				zap.String("route_id", release.RouteID.String()),
				zap.String("release_id", release.ID.String()),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("LLM release registered via intent",
		zap.String("route_id", release.RouteID.String()),
		zap.String("release_id", release.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// createRoute creates a new LLM route and publishes canonical state.
func (h *LLMRouteIntentHandler) createRoute(ctx context.Context, route *domain.LLMRoute, intent *Intent) error {
	if err := h.routes.CreateRoute(ctx, route); err != nil {
		return fmt.Errorf("create LLM route: %w", err)
	}

	if h.publish != nil {
		if err := h.publish(ctx, route, false); err != nil {
			return fmt.Errorf("LLM route created but publication failed: %w", err)
		}
	}

	h.logger.Info("LLM route created via intent",
		zap.String("route_id", route.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// updateRoute updates an existing LLM route and publishes canonical state.
func (h *LLMRouteIntentHandler) updateRoute(ctx context.Context, route *domain.LLMRoute, intent *Intent) error {
	if err := h.routes.UpdateRoute(ctx, route); err != nil {
		return fmt.Errorf("update LLM route: %w", err)
	}

	if h.publish != nil {
		if err := h.publish(ctx, route, false); err != nil {
			return fmt.Errorf("LLM route updated but publication failed: %w", err)
		}
	}

	h.logger.Info("LLM route updated via intent",
		zap.String("route_id", route.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// updateWithRevision updates an LLM route with expected_updated_at revision check.
func (h *LLMRouteIntentHandler) updateWithRevision(ctx context.Context, route *domain.LLMRoute, intent *Intent) error {
	existing, _ := h.routes.GetRoute(ctx, route.ID)
	if existing == nil {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return fmt.Errorf("LLM route %s not found for revisioned update", route.ID)
	}

	// Check revision.
	expectedTime := time.Unix(0, *intent.ExpectedUpdatedAt)
	if raw, ok := intent.Content["expected_updated_at"]; ok {
		if v, ok := raw.(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, v); parseErr == nil {
				expectedTime = parsed
			}
		}
	}
	if !domain.SameRevision(existing.UpdatedAt, expectedTime) {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return &revisionConflictError{
			entityID: route.ID,
			expected: expectedTime,
			actual:   existing.UpdatedAt,
		}
	}

	mergeLLMRouteOntoExisting(existing, route)
	return h.updateRoute(ctx, existing, intent)
}

// llmRouteFromIntentContent parses intent content into a domain.LLMRoute.
func llmRouteFromIntentContent(intent *Intent) (*domain.LLMRoute, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	route := &domain.LLMRoute{}

	// Parse ID from content or coordinate.
	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid route id %q: %w", idStr, err)
		}
		route.ID = id
	}
	if route.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			return nil, fmt.Errorf("cannot derive route id from coordinate %q: %w", intent.Coordinate, err)
		}
		route.ID = id
	}

	// Name.
	if name, ok := content["name"].(string); ok {
		route.Name = strings.TrimSpace(name)
	}

	// Description.
	if desc, ok := content["description"].(string); ok {
		route.Description = strings.TrimSpace(desc)
	}

	// GatewayConfig.
	if gwRaw, ok := content["gateway_config"]; ok && gwRaw != nil {
		gwJSON, err := json.Marshal(gwRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal gateway_config: %w", err)
		}
		var gw domain.LLMGatewayRouteConfig
		if err := json.Unmarshal(gwJSON, &gw); err != nil {
			return nil, fmt.Errorf("parse gateway_config: %w", err)
		}
		route.GatewayConfig = &gw
	}

	// DefaultPlacementPolicy.
	if ppRaw, ok := content["default_placement_policy"]; ok && ppRaw != nil {
		ppJSON, err := json.Marshal(ppRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal default_placement_policy: %w", err)
		}
		var pp domain.LLMPlacementPolicy
		if err := json.Unmarshal(ppJSON, &pp); err != nil {
			return nil, fmt.Errorf("parse default_placement_policy: %w", err)
		}
		route.DefaultPlacementPolicy = &pp
	}

	// DefaultPromotionGate.
	if pgRaw, ok := content["default_promotion_gate"]; ok && pgRaw != nil {
		pgJSON, err := json.Marshal(pgRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal default_promotion_gate: %w", err)
		}
		var pg domain.LLMPromotionGateConfig
		if err := json.Unmarshal(pgJSON, &pg); err != nil {
			return nil, fmt.Errorf("parse default_promotion_gate: %w", err)
		}
		route.DefaultPromotionGate = &pg
	}

	// Metadata.
	if metaRaw, ok := content["metadata"]; ok && metaRaw != nil {
		metaJSON, err := json.Marshal(metaRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}
		var meta map[string]any
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			return nil, fmt.Errorf("parse metadata: %w", err)
		}
		route.Metadata = meta
	}

	return route, nil
}

// llmReleaseFromIntentContent parses intent content into a domain.LLMRelease.
func llmReleaseFromIntentContent(intent *Intent) (*domain.LLMRelease, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	release := &domain.LLMRelease{}

	// RouteID is required.
	routeIDStr, _ := content["route_id"].(string)
	if routeIDStr == "" {
		return nil, fmt.Errorf("route_id is required for release-register")
	}
	routeID, err := uuid.Parse(routeIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid route_id %q: %w", routeIDStr, err)
	}
	release.RouteID = routeID

	if v, ok := content["version"].(string); ok {
		release.Version = strings.TrimSpace(v)
	}
	if v, ok := content["model_ref"].(string); ok {
		release.ModelRef = strings.TrimSpace(v)
	}
	if v, ok := content["model_source"].(string); ok {
		release.ModelSource = strings.TrimSpace(v)
	}
	if v, ok := content["model_revision"].(string); ok {
		release.ModelRevision = strings.TrimSpace(v)
	}
	if v, ok := content["estimated_vram_gb"].(float64); ok {
		release.EstimatedVRAMGB = int(v)
	}

	// BackendPreferences.
	if bpRaw, ok := content["backend_preferences"]; ok && bpRaw != nil {
		bpJSON, err := json.Marshal(bpRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal backend_preferences: %w", err)
		}
		var bp []domain.LLMBackendKind
		if err := json.Unmarshal(bpJSON, &bp); err != nil {
			return nil, fmt.Errorf("parse backend_preferences: %w", err)
		}
		release.BackendPreferences = bp
	}

	// RuntimeBackend.
	if rbRaw, ok := content["runtime_backend"]; ok && rbRaw != nil {
		rbJSON, err := json.Marshal(rbRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal runtime_backend: %w", err)
		}
		var rb domain.LLMRuntimeManagedBackendConfig
		if err := json.Unmarshal(rbJSON, &rb); err != nil {
			return nil, fmt.Errorf("parse runtime_backend: %w", err)
		}
		release.RuntimeBackend = &rb
	}

	// ExternalBackend.
	if ebRaw, ok := content["external_backend"]; ok && ebRaw != nil {
		ebJSON, err := json.Marshal(ebRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal external_backend: %w", err)
		}
		var eb domain.LLMExternalBackendConfig
		if err := json.Unmarshal(ebJSON, &eb); err != nil {
			return nil, fmt.Errorf("parse external_backend: %w", err)
		}
		release.ExternalBackend = &eb
	}

	// PlacementPolicy.
	if ppRaw, ok := content["placement_policy"]; ok && ppRaw != nil {
		ppJSON, err := json.Marshal(ppRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal placement_policy: %w", err)
		}
		var pp domain.LLMPlacementPolicy
		if err := json.Unmarshal(ppJSON, &pp); err != nil {
			return nil, fmt.Errorf("parse placement_policy: %w", err)
		}
		release.PlacementPolicy = &pp
	}

	// PromotionGate.
	if pgRaw, ok := content["promotion_gate"]; ok && pgRaw != nil {
		pgJSON, err := json.Marshal(pgRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal promotion_gate: %w", err)
		}
		var pg domain.LLMPromotionGateConfig
		if err := json.Unmarshal(pgJSON, &pg); err != nil {
			return nil, fmt.Errorf("parse promotion_gate: %w", err)
		}
		release.PromotionGate = &pg
	}

	// Metadata.
	if metaRaw, ok := content["metadata"]; ok && metaRaw != nil {
		metaJSON, err := json.Marshal(metaRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}
		var meta map[string]any
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			return nil, fmt.Errorf("parse metadata: %w", err)
		}
		release.Metadata = meta
	}

	return release, nil
}

// mergeLLMRouteOntoExisting applies the intent's desired state fields onto the
// loaded entity. Every non-zero field replaces the existing one (§1.2).
func mergeLLMRouteOntoExisting(existing, intent *domain.LLMRoute) {
	if intent.Name != "" {
		existing.Name = intent.Name
	}
	if intent.Description != "" {
		existing.Description = intent.Description
	}
	if intent.GatewayConfig != nil {
		existing.GatewayConfig = intent.GatewayConfig
	}
	if intent.DefaultPlacementPolicy != nil {
		existing.DefaultPlacementPolicy = intent.DefaultPlacementPolicy
	}
	if intent.DefaultPromotionGate != nil {
		existing.DefaultPromotionGate = intent.DefaultPromotionGate
	}
	if intent.Metadata != nil {
		existing.Metadata = intent.Metadata
	}
}

// LLMRouteRegistryRecord builds the canonical kind-30900 cp-state tags and
// content JSON for an LLM route mutation. This is the shared builder used by
// both the intent handler (via LLMRouteStatePublisher) and the warm-start
// comparison. It extracts the projector's publishLLMRouteRegistry logic into
// a standalone function (like PolicyRegistryRecord).
func LLMRouteRegistryRecord(route *domain.LLMRoute, deleted bool) (nostr.Tags, string) {
	content := map[string]any{
		"deleted": deleted,
		"id":      route.ID.String(),
	}
	if !deleted {
		content["name"] = route.Name
		content["description"] = route.Description
		content["gateway_config"] = route.GatewayConfig
		content["default_placement_policy"] = route.DefaultPlacementPolicy
		content["default_promotion_gate"] = route.DefaultPromotionGate
		content["metadata"] = route.Metadata
		content["created_at"] = route.CreatedAt.UTC().Format(time.RFC3339)
		content["updated_at"] = route.UpdatedAt.UTC().Format(time.RFC3339)
	} else {
		content["updated_at"] = route.UpdatedAt.UTC().Format(time.RFC3339)
	}
	contentJSON, _ := json.Marshal(content)
	tags := nostr.Tags{{"route", route.ID.String()}}
	if !deleted {
		tags = append(tags, nostr.Tag{"name", route.Name})
		if route.GatewayConfig != nil && route.GatewayConfig.PublicModel != "" {
			tags = append(tags, nostr.Tag{"model", route.GatewayConfig.PublicModel})
		}
	}
	return tags, string(contentJSON)
}

// LLMRouteStateRecord builds the canonical kind-30900 cp-state tags and
// content JSON for an LLM route state mutation. This is the shared builder
// used by the state publisher (injected into the LLM registry service) and
// the warm-start comparison. It extracts the projector's publishLLMRouteState
// logic into a standalone function.
func LLMRouteStateRecord(state *domain.LLMRouteState) (nostr.Tags, string) {
	content := map[string]any{
		"deleted":          false,
		"route_id":         state.RouteID.String(),
		"environment_id":   state.EnvironmentID.String(),
		"drift_status":     string(state.DriftStatus),
		"gateway_status":   string(state.GatewayStatus),
		"backend_kind":     string(state.BackendKind),
		"backend_endpoint": state.BackendEndpoint,
		"backend_health":   string(state.BackendHealth),
		"gateway_target":   state.GatewayTarget,
		"updated_at":       state.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if state.DesiredReleaseID != nil {
		content["desired_release_id"] = state.DesiredReleaseID.String()
	}
	if state.DesiredIntentID != nil {
		content["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.ActiveRunID != nil {
		content["active_run_id"] = state.ActiveRunID.String()
	}
	if state.CurrentObservationID != nil {
		content["current_observation_id"] = state.CurrentObservationID.String()
	}
	if state.LastReconciledAt != nil {
		content["last_reconciled_at"] = state.LastReconciledAt.UTC().Format(time.RFC3339)
	}
	contentJSON, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"route", state.RouteID.String()},
		{"environment", state.EnvironmentID.String()},
		{"drift_status", string(state.DriftStatus)},
		{"gateway_status", string(state.GatewayStatus)},
	}
	if state.DesiredReleaseID != nil {
		tags = append(tags, nostr.Tag{"release", state.DesiredReleaseID.String()})
	}
	if state.DesiredIntentID != nil {
		tags = append(tags, nostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.ActiveRunID != nil {
		tags = append(tags, nostr.Tag{"run", state.ActiveRunID.String()})
	}
	if state.BackendKind != "" {
		tags = append(tags, nostr.Tag{"backend", string(state.BackendKind)})
	}
	return tags, string(contentJSON)
}

// LLMRouteStateDTag is the coordinate builder for LLM route state records.
// Both the live record and its tombstone share this coordinate so they replace
// each other on the relay.
func LLMRouteStateDTag(routeID, environmentID uuid.UUID) string {
	return fmt.Sprintf("%s:%s", routeID, environmentID)
}
