package controlplane

import (
	"context"
	"fmt"
	"strings"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
)

func (h *ContinuityRuntime) handleFailoverRequest(ctx context.Context, request ContextVMRequest) (any, error) {
	return h.executeCommand(ctx, request, domain.ContinuityRecipeKindFailover)
}

func (h *ContinuityRuntime) handleRecoveryRequest(ctx context.Context, request ContextVMRequest) (any, error) {
	return h.executeCommand(ctx, request, domain.ContinuityRecipeKindRecovery)
}

func (h *ContinuityRuntime) executeCommand(ctx context.Context, request ContextVMRequest, kind domain.ContinuityRecipeKind) (any, error) {
	var payload nostradapter.ContinuityCommandRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	payload.ServiceKey = strings.TrimSpace(payload.ServiceKey)
	payload.TargetWorkerPubKey = strings.TrimSpace(payload.TargetWorkerPubKey)
	payload.RecipeName = strings.TrimSpace(payload.RecipeName)
	if payload.ServiceKey == "" {
		return nil, fmt.Errorf("service_key is required")
	}
	if !isHexNostrPubKey(payload.TargetWorkerPubKey) {
		return nil, fmt.Errorf("target_worker_pubkey must be a 32-byte lowercase hex Nostr public key")
	}
	if request.ProgressToken == "" {
		return nil, fmt.Errorf("idempotency_key or _meta.progressToken is required")
	}
	if payload.TargetProfile != "" && !payload.TargetProfile.IsValid() {
		return nil, fmt.Errorf("invalid target_profile")
	}
	for tag, value := range map[string]string{"service": payload.ServiceKey, "target": payload.TargetWorkerPubKey, "d": request.ProgressToken, "recipe": payload.RecipeName, "profile": string(payload.TargetProfile)} {
		if !consistentOptionalTag(tagValueNostr(request.Event.Tags, tag), value) {
			return nil, fmt.Errorf("%s tag and params must match", tag)
		}
	}
	if h.definitions == nil || h.executor == nil {
		return nil, fmt.Errorf("continuity runtime is not configured")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.ready:
	}
	command := continuityCommandRequestedEvent(request, &payload)
	recipe, ok := h.definitions.GetRecipe(command.ServiceKey, kind)
	if !ok || (command.RecipeName != "" && command.RecipeName != recipe.Name) {
		return nil, fmt.Errorf("continuity %s recipe not found", kind)
	}
	profile, _ := h.definitions.GetProfile(command.ServiceKey)
	var err error
	if kind == domain.ContinuityRecipeKindFailover {
		err = h.executor.ExecuteFailover(ctx, service.FailoverExecutionRequest{ServiceKey: command.ServiceKey, RecipeName: recipe.Name, TargetProfile: command.TargetProfile, PrimaryWorkerPubKey: profile.PrimaryWorkerPubKey, SelectedStandbyPubKey: command.TargetWorkerPubKey, RequestedBy: command.Source.PubKey, RunID: command.IdempotencyKey, Recipe: recipe})
	} else {
		err = h.executor.ExecuteRecovery(ctx, service.RecoveryExecutionRequest{ServiceKey: command.ServiceKey, RecipeName: recipe.Name, TargetProfile: command.TargetProfile, PrimaryWorkerPubKey: profile.PrimaryWorkerPubKey, SelectedStandbyPubKey: command.TargetWorkerPubKey, RequestedBy: command.Source.PubKey, RunID: command.IdempotencyKey, Recipe: recipe})
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "succeeded", "service_key": command.ServiceKey, "run_id": command.IdempotencyKey, "recipe": recipe.Name}, nil
}

func continuityCommandRequestedEvent(request ContextVMRequest, payload *nostradapter.ContinuityCommandRequest) events.ContinuityCommandRequested {
	return events.ContinuityCommandRequested{
		Source: continuitySource(request.Event), ServiceKey: payload.ServiceKey,
		TargetWorkerPubKey: payload.TargetWorkerPubKey, TargetProfile: payload.TargetProfile,
		RecipeName: payload.RecipeName, IdempotencyKey: request.ProgressToken,
		Reason: payload.Reason, Metadata: payload.Metadata,
	}
}
