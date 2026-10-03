package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ContextVM method constants for LLM operations. These match the methods
// published by LLMCommandPublisher (llm_command_publisher.go).
const (
	ContextVMMethodLLMRouteCreate     = "llm/route-create"
	ContextVMMethodLLMReleaseRegister = "llm/release-register"
)

// RegisterLLMContextVMHandlers binds LLM ContextVM methods to the encrypted
// request transport. This is the production consumer for llm/route-create
// (bahia-irsry.55) and llm/release-register.
//
// When the llm domain is enabled in intent_domains, mutations are dual-dispatched
// through the intent processor. When disabled, they fall through to the legacy
// path which calls the registry service directly and publishes canonical 30900
// records via the LLMRouteStatePublisher.
func RegisterLLMContextVMHandlers(
	transport *EncryptedRequestTransport,
	gate *FleetOperatorGate,
	llmRegistry LLMRouteCRUD,
	intentProcessor *IntentProcessor,
	publisher LLMRouteStatePublisher,
) {
	if transport == nil {
		return
	}
	h := &llmContextVMHandlers{
		registry:        llmRegistry,
		intentProcessor: intentProcessor,
		publisher:       publisher,
	}
	transport.RegisterContextVMHandler(ContextVMMethodLLMRouteCreate, gate.wrap(h.routeCreate))
	transport.RegisterContextVMHandler(ContextVMMethodLLMReleaseRegister, gate.wrap(h.releaseRegister))
}

type llmContextVMHandlers struct {
	registry        LLMRouteCRUD
	intentProcessor *IntentProcessor
	publisher       LLMRouteStatePublisher
}

// llmIntentEnabled reports whether the llm domain is routed through the
// intent processor (Phase 3 dual dispatch).
func (h *llmContextVMHandlers) llmIntentEnabled() bool {
	return h.intentProcessor != nil && h.intentProcessor.Handler("llm") != nil
}

// routeCreate handles the llm/route-create ContextVM method.
func (h *llmContextVMHandlers) routeCreate(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("LLM registry is not configured")
	}
	var req struct {
		ID                     string                         `json:"id,omitempty"`
		Name                   string                         `json:"name"`
		Description            string                         `json:"description,omitempty"`
		GatewayConfig          *domain.LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
		DefaultPlacementPolicy *domain.LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
		DefaultPromotionGate   *domain.LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
		Metadata               map[string]any                 `json:"metadata,omitempty"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	routeID, _, err := domain.ResolveCreateEntityID(req.ID)
	if err != nil {
		return nil, err
	}

	route := &domain.LLMRoute{
		ID:                     routeID,
		Name:                   req.Name,
		Description:            strings.TrimSpace(req.Description),
		GatewayConfig:          req.GatewayConfig,
		DefaultPlacementPolicy: req.DefaultPlacementPolicy,
		DefaultPromotionGate:   req.DefaultPromotionGate,
		Metadata:               req.Metadata,
	}

	// Phase 3 dual dispatch: route through the intent processor when the
	// llm domain is enabled. Falls through to the legacy path otherwise.
	if h.llmIntentEnabled() {
		content := llmRouteToIntentContent(route)
		if err := h.llmDualDispatch(ctx, request, "route-create", routeID, content); err != nil {
			return nil, err
		}
		return llmMutationResult("route_create", routeID), nil
	}

	// Legacy path: call the registry directly and publish canonical 30900.
	if err := h.registry.CreateRoute(ctx, route); err != nil {
		return nil, err
	}
	if h.publisher != nil {
		if err := h.publisher(ctx, route, false); err != nil {
			return nil, fmt.Errorf("LLM route created but registry publication failed: %w", err)
		}
	}
	return llmMutationResult("route_create", routeID), nil
}

// releaseRegister handles the llm/release-register ContextVM method.
func (h *llmContextVMHandlers) releaseRegister(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("LLM registry is not configured")
	}
	var req struct {
		RouteID            string                                 `json:"route_id"`
		Version            string                                 `json:"version"`
		ModelRef           string                                 `json:"model_ref"`
		ModelSource        string                                 `json:"model_source"`
		ModelRevision      string                                 `json:"model_revision,omitempty"`
		EstimatedVRAMGB    int                                    `json:"estimated_vram_gb,omitempty"`
		BackendPreferences []domain.LLMBackendKind                `json:"backend_preferences,omitempty"`
		RuntimeBackend     *domain.LLMRuntimeManagedBackendConfig `json:"runtime_backend,omitempty"`
		ExternalBackend    *domain.LLMExternalBackendConfig       `json:"external_backend,omitempty"`
		PlacementPolicy    *domain.LLMPlacementPolicy             `json:"placement_policy,omitempty"`
		PromotionGate      *domain.LLMPromotionGateConfig         `json:"promotion_gate,omitempty"`
		Metadata           map[string]any                         `json:"metadata,omitempty"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	routeID, err := uuid.Parse(strings.TrimSpace(req.RouteID))
	if err != nil || routeID == uuid.Nil {
		return nil, fmt.Errorf("valid route_id is required")
	}

	// Phase 3 dual dispatch.
	if h.llmIntentEnabled() {
		content := map[string]interface{}{
			"route_id":     routeID.String(),
			"version":      req.Version,
			"model_ref":    req.ModelRef,
			"model_source": req.ModelSource,
		}
		if req.ModelRevision != "" {
			content["model_revision"] = req.ModelRevision
		}
		if req.EstimatedVRAMGB > 0 {
			content["estimated_vram_gb"] = req.EstimatedVRAMGB
		}
		if len(req.BackendPreferences) > 0 {
			content["backend_preferences"] = req.BackendPreferences
		}
		if req.RuntimeBackend != nil {
			content["runtime_backend"] = req.RuntimeBackend
		}
		if req.ExternalBackend != nil {
			content["external_backend"] = req.ExternalBackend
		}
		if req.PlacementPolicy != nil {
			content["placement_policy"] = req.PlacementPolicy
		}
		if req.PromotionGate != nil {
			content["promotion_gate"] = req.PromotionGate
		}
		if len(req.Metadata) > 0 {
			content["metadata"] = req.Metadata
		}
		if err := h.llmDualDispatch(ctx, request, "release-register", routeID, content); err != nil {
			return nil, err
		}
		return llmMutationResult("release_register", routeID), nil
	}

	// Legacy path.
	release := &domain.LLMRelease{
		RouteID:            routeID,
		Version:            req.Version,
		ModelRef:           req.ModelRef,
		ModelSource:        req.ModelSource,
		ModelRevision:      req.ModelRevision,
		EstimatedVRAMGB:    req.EstimatedVRAMGB,
		BackendPreferences: req.BackendPreferences,
		RuntimeBackend:     req.RuntimeBackend,
		ExternalBackend:    req.ExternalBackend,
		PlacementPolicy:    req.PlacementPolicy,
		PromotionGate:      req.PromotionGate,
		Metadata:           req.Metadata,
	}
	if err := h.registry.CreateRelease(ctx, release); err != nil {
		return nil, err
	}
	// Publish the updated route registry record on the legacy path.
	if h.publisher != nil {
		route, _ := h.registry.GetRoute(ctx, routeID)
		if route != nil {
			if err := h.publisher(ctx, route, false); err != nil {
				return nil, fmt.Errorf("LLM release registered but registry publication failed: %w", err)
			}
		}
	}
	return llmMutationResult("release_register", routeID), nil
}

// llmDualDispatch routes an LLM mutation through the intent processor for
// dual dispatch when the llm domain is enabled.
func (h *llmContextVMHandlers) llmDualDispatch(ctx context.Context, request ContextVMRequest, op string, entityID uuid.UUID, content map[string]interface{}) error {
	compatibilityKey := ""
	if request.Event != nil {
		compatibilityKey = request.Event.ID.Hex()
	}
	if compatibilityKey == "" {
		compatibilityKey = entityID.String()
	}
	intent := &Intent{
		Event:      request.Event,
		Domain:     "llm",
		Op:         op,
		IntentID:   effectiveIdempotencyKey(request, compatibilityKey),
		Coordinate: entityID.String(),
		Content:    content,
		Actor:      request.Event.PubKey.Hex(),
	}
	return h.intentProcessor.ProcessInProcess(ctx, intent)
}

// llmRouteToIntentContent converts a domain.LLMRoute to the intent content
// map used for dual dispatch.
func llmRouteToIntentContent(route *domain.LLMRoute) map[string]interface{} {
	content := map[string]interface{}{
		"id":   route.ID.String(),
		"name": route.Name,
	}
	if route.Description != "" {
		content["description"] = route.Description
	}
	if route.GatewayConfig != nil {
		content["gateway_config"] = route.GatewayConfig
	}
	if route.DefaultPlacementPolicy != nil {
		content["default_placement_policy"] = route.DefaultPlacementPolicy
	}
	if route.DefaultPromotionGate != nil {
		content["default_promotion_gate"] = route.DefaultPromotionGate
	}
	if len(route.Metadata) > 0 {
		content["metadata"] = route.Metadata
	}
	return content
}

func llmMutationResult(action string, routeID uuid.UUID) map[string]any {
	return map[string]any{"status": "success", "action": action, "route_id": routeID.String()}
}
