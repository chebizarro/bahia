package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	ContextVMMethodServiceAction  = "service/action"
	ContextVMMethodAdoptionScan   = "adoption/scan"
	ContextVMMethodAdoptionImport = "adoption/import"
)

type OperatorContextVMHandlersConfig struct {
	Adoption                       AdoptionOperatorService
	RuntimeLifecycle               RuntimeLifecycleOperatorService
	AdoptionAuthorizedPubkeys      []string
	DirectRuntimeAuthorizedPubkeys []string
	IntentProcessor                *IntentProcessor
	Resources                      DeploymentIntentResourceReader
}

type OperatorContextVMHandlers struct {
	adoption                       AdoptionOperatorService
	runtimeLifecycle               RuntimeLifecycleOperatorService
	adoptionAuthorizedPubkeys      []string
	directRuntimeAuthorizedPubkeys []string
	intentProcessor                *IntentProcessor
	resources                      DeploymentIntentResourceReader
}

func NewOperatorContextVMHandlers(cfg OperatorContextVMHandlersConfig) *OperatorContextVMHandlers {
	return &OperatorContextVMHandlers{
		adoption:                       cfg.Adoption,
		runtimeLifecycle:               cfg.RuntimeLifecycle,
		adoptionAuthorizedPubkeys:      append([]string(nil), cfg.AdoptionAuthorizedPubkeys...),
		directRuntimeAuthorizedPubkeys: append([]string(nil), cfg.DirectRuntimeAuthorizedPubkeys...),
		intentProcessor:                cfg.IntentProcessor,
		resources:                      cfg.Resources,
	}
}

func (h *OperatorContextVMHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	transport.RegisterContextVMHandler(ContextVMMethodServiceAction, h.ServiceAction)
	transport.RegisterContextVMHandler(ContextVMMethodAdoptionScan, h.AdoptionScan)
	transport.RegisterContextVMHandler(ContextVMMethodAdoptionImport, h.AdoptionImport)
}

func (h *OperatorContextVMHandlers) ServiceAction(ctx context.Context, request ContextVMRequest) (any, error) {
	if !authorizedContextVMPubkey(request.Event.PubKey.Hex(), h.directRuntimeAuthorizedPubkeys) {
		return nil, fmt.Errorf("requester not in authorized direct-runtime list")
	}
	if h.runtimeLifecycle == nil {
		return nil, fmt.Errorf("runtime lifecycle service is not configured")
	}
	var raw directRuntimeActionEventRequest
	if err := decodeContextVMParams(request.RPC.Params, &raw); err != nil {
		return nil, err
	}
	req, err := parseDirectRuntimeActionPayload(raw)
	if err != nil {
		return nil, err
	}
	if h.intentProcessor != nil && h.intentProcessor.Handler("runtime") != nil {
		if h.resources == nil {
			return nil, fmt.Errorf("runtime resource registry is not configured")
		}
		svc, err := h.resources.GetService(ctx, req.ServiceID)
		if err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, fmt.Errorf("service not found")
		}
		content := map[string]any{"service_id": req.ServiceID.String(), "environment_id": req.EnvironmentID.String()}
		if req.ArtifactID != nil {
			content["artifact_id"] = req.ArtifactID.String()
		}
		intentID := effectiveIdempotencyKey(request, request.Event.ID.Hex())
		intent := &Intent{Event: request.Event, Domain: "runtime", Op: req.Action, OrgID: svc.OrgID, IntentID: intentID, Coordinate: req.ServiceID.String() + ":" + req.EnvironmentID.String(), Content: content, Actor: request.Event.PubKey.Hex()}
		if err := h.intentProcessor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
		return map[string]any{"status": "accepted", "intent_id": intentID}, nil
	}
	var obs *domain.RuntimeObservation
	switch req.Action {
	case "deploy":
		obs, err = h.runtimeLifecycle.Deploy(ctx, req.ServiceID, req.EnvironmentID, req.ArtifactID)
	case "restart":
		obs, err = h.runtimeLifecycle.Restart(ctx, req.ServiceID, req.EnvironmentID)
	case "stop":
		obs, err = h.runtimeLifecycle.Stop(ctx, req.ServiceID, req.EnvironmentID)
	}
	if err != nil {
		return nil, err
	}
	return dto.RuntimeActionResponseFromDomain(req.Action, req.ServiceID, req.EnvironmentID, obs), nil
}

func (h *OperatorContextVMHandlers) AdoptionScan(ctx context.Context, request ContextVMRequest) (any, error) {
	if !authorizedContextVMPubkey(request.Event.PubKey.Hex(), h.adoptionAuthorizedPubkeys) {
		return nil, fmt.Errorf("requester not in authorized adoption list")
	}
	if h.adoption == nil {
		return nil, fmt.Errorf("adoption service is not configured")
	}
	var raw adoptionScanEventRequest
	if err := decodeContextVMParams(request.RPC.Params, &raw); err != nil {
		return nil, err
	}
	targets, err := mapAdoptionEventTargets(raw.Targets)
	if err != nil {
		return nil, err
	}
	previews, err := h.adoption.Scan(ctx, service.AdoptionScanRequest{Targets: targets})
	if err != nil {
		return nil, err
	}
	return dto.AdoptionPreviewResponsesFromService(previews), nil
}

func (h *OperatorContextVMHandlers) AdoptionImport(ctx context.Context, request ContextVMRequest) (any, error) {
	if !authorizedContextVMPubkey(request.Event.PubKey.Hex(), h.adoptionAuthorizedPubkeys) {
		return nil, fmt.Errorf("requester not in authorized adoption list")
	}
	if h.adoption == nil {
		return nil, fmt.Errorf("adoption service is not configured")
	}
	var raw adoptionImportEventRequest
	if err := decodeContextVMParams(request.RPC.Params, &raw); err != nil {
		return nil, err
	}
	targets, err := mapAdoptionEventTargets(raw.Targets)
	if err != nil {
		return nil, err
	}
	if !raw.ImportAll && len(raw.Selections) == 0 {
		return nil, fmt.Errorf("import requires import_all=true or at least one selection")
	}
	selections, err := mapAdoptionEventSelections(raw.Selections)
	if err != nil {
		return nil, err
	}
	var orgID uuid.UUID
	if strings.TrimSpace(raw.OrgID) != "" {
		orgID, err = uuid.Parse(strings.TrimSpace(raw.OrgID))
		if err != nil {
			return nil, fmt.Errorf("invalid org_id: %w", err)
		}
	}
	if h.intentProcessor != nil && h.intentProcessor.Handler("adoption") != nil {
		content := map[string]any{}
		if err := json.Unmarshal(request.RPC.Params, &content); err != nil {
			return nil, err
		}
		coordinate := "adoption:fleet"
		if orgID != uuid.Nil {
			coordinate = "adoption:" + orgID.String()
		}
		intent := &Intent{Event: request.Event, Domain: "adoption", Op: "import", OrgID: orgID,
			IntentID: effectiveIdempotencyKey(request, request.Event.ID.Hex()), Coordinate: coordinate,
			Content: content, Actor: request.Event.PubKey.Hex()}
		if err := h.intentProcessor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
		return intent.Result["imports"], nil
	}
	results, err := h.adoption.Import(ctx, service.AdoptionImportRequest{Targets: targets, Selections: selections, ImportAll: raw.ImportAll, OrgID: orgID})
	if err != nil {
		return nil, err
	}
	return dto.AdoptionImportResultResponsesFromService(results), nil
}

func parseDirectRuntimeActionPayload(raw directRuntimeActionEventRequest) (parsedDirectRuntimeActionRequest, error) {
	action := strings.ToLower(strings.TrimSpace(raw.Action))
	if !isDirectRuntimeAction(action) {
		return parsedDirectRuntimeActionRequest{}, fmt.Errorf("unsupported action %q", raw.Action)
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(raw.ServiceID))
	if err != nil {
		return parsedDirectRuntimeActionRequest{}, fmt.Errorf("invalid service_id: %w", err)
	}
	environmentID, err := uuid.Parse(strings.TrimSpace(raw.EnvironmentID))
	if err != nil {
		return parsedDirectRuntimeActionRequest{}, fmt.Errorf("invalid environment_id: %w", err)
	}
	var artifactID *uuid.UUID
	if strings.TrimSpace(raw.ArtifactID) != "" {
		parsedArtifactID, err := uuid.Parse(strings.TrimSpace(raw.ArtifactID))
		if err != nil {
			return parsedDirectRuntimeActionRequest{}, fmt.Errorf("invalid artifact_id: %w", err)
		}
		artifactID = &parsedArtifactID
	}
	if action != "deploy" && artifactID != nil {
		return parsedDirectRuntimeActionRequest{}, fmt.Errorf("artifact_id is only valid for deploy actions")
	}
	return parsedDirectRuntimeActionRequest{Action: action, ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID}, nil
}

func authorizedContextVMPubkey(pubkey string, authorized []string) bool {
	pubkey = strings.TrimSpace(pubkey)
	if pubkey == "" || len(authorized) == 0 {
		return false
	}
	return slices.ContainsFunc(authorized, func(allowed string) bool {
		return strings.EqualFold(strings.TrimSpace(allowed), pubkey)
	})
}
