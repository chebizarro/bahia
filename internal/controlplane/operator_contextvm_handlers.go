package controlplane

import (
	"context"
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
}

type OperatorContextVMHandlers struct {
	adoption                       AdoptionOperatorService
	runtimeLifecycle               RuntimeLifecycleOperatorService
	adoptionAuthorizedPubkeys      []string
	directRuntimeAuthorizedPubkeys []string
}

func NewOperatorContextVMHandlers(cfg OperatorContextVMHandlersConfig) *OperatorContextVMHandlers {
	return &OperatorContextVMHandlers{
		adoption:                       cfg.Adoption,
		runtimeLifecycle:               cfg.RuntimeLifecycle,
		adoptionAuthorizedPubkeys:      append([]string(nil), cfg.AdoptionAuthorizedPubkeys...),
		directRuntimeAuthorizedPubkeys: append([]string(nil), cfg.DirectRuntimeAuthorizedPubkeys...),
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
	results, err := h.adoption.Import(ctx, service.AdoptionImportRequest{Targets: targets, Selections: selections, ImportAll: raw.ImportAll})
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
	if pubkey == "" {
		return false
	}
	if len(authorized) == 0 {
		return true
	}
	return slices.Contains(authorized, pubkey)
}
