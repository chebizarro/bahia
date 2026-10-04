package handlers

import (
	"errors"
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// LLMHandler handles the retained LLM route update compatibility request.
type LLMHandler struct {
	registry *service.LLMRegistryService
}

func NewLLMHandler(registry *service.LLMRegistryService) *LLMHandler {
	return &LLMHandler{registry: registry}
}

func (h *LLMHandler) UpdateRoute(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route id")
		return
	}
	existing, err := h.registry.GetRoute(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "LLM route not found")
		return
	}
	var req dto.UpdateLLMRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.GatewayConfig != nil {
		existing.GatewayConfig = gatewayConfig(req.GatewayConfig)
	}
	if req.DefaultPlacementPolicy != nil {
		existing.DefaultPlacementPolicy = placementPolicy(req.DefaultPlacementPolicy)
	}
	if req.DefaultPromotionGate != nil {
		existing.DefaultPromotionGate = promotionGate(req.DefaultPromotionGate)
	}
	if req.Metadata != nil {
		existing.Metadata = *req.Metadata
	}
	if err := h.registry.UpdateRoute(r.Context(), existing); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, existing)
}

func (h *LLMHandler) CreateIntent(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateLLMDeploymentIntentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.RequestedBy = resolveActor(r, req.RequestedBy)
	intent := &domain.LLMDeploymentIntent{RouteID: req.RouteID, EnvironmentID: req.EnvironmentID, ReleaseID: req.ReleaseID, RequestedBy: req.RequestedBy, SourceKind: domain.SourceKind(req.SourceKind), Metadata: req.Metadata}
	if err := h.registry.CreateDeploymentIntent(r.Context(), intent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, intent)
}

func (h *LLMHandler) ApproveIntent(w http.ResponseWriter, r *http.Request) {
	h.intentApproval(w, r, true)
}

func (h *LLMHandler) RejectIntent(w http.ResponseWriter, r *http.Request) {
	h.intentApproval(w, r, false)
}

func (h *LLMHandler) intentApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	if approve {
		err = h.registry.ApproveDeploymentIntent(r.Context(), id)
	} else {
		err = h.registry.RejectDeploymentIntent(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "LLM deployment intent not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if approve {
		writeMessage(w, http.StatusOK, "LLM deployment intent approved")
	} else {
		writeMessage(w, http.StatusOK, "LLM deployment intent rejected")
	}
}

func (h *LLMHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var req dto.RollbackLLMRouteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.RequestedBy = resolveActor(r, req.RequestedBy)
	intent, err := h.registry.Rollback(r.Context(), req.RouteID, req.EnvironmentID, req.RequestedBy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, intent)
}

func (h *LLMHandler) RecordObservation(w http.ResponseWriter, r *http.Request) {
	var req dto.RecordLLMRouteObservationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	obs := &domain.LLMRouteObservation{RouteID: req.RouteID, EnvironmentID: req.EnvironmentID, ObservedReleaseID: req.ObservedReleaseID, ObservedRunID: req.ObservedRunID, BackendKind: domain.LLMBackendKind(req.BackendKind), BackendEndpoint: req.BackendEndpoint, BackendHealth: domain.HealthStatus(req.BackendHealth), GatewayStatus: domain.GatewayRouteStatus(req.GatewayStatus), GatewayTarget: req.GatewayTarget, GatewayConfigHash: req.GatewayConfigHash, Source: req.Source, Metadata: req.Metadata}
	if err := domain.ValidateHealthStatus(obs.BackendHealth); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateGatewayRouteStatus(obs.GatewayStatus); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateLLMBackendKind(obs.BackendKind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.registry.RecordObservation(r.Context(), obs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, obs)
}

func gatewayConfig(req *dto.LLMGatewayConfigRequest) *domain.LLMGatewayRouteConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMGatewayRouteConfig{PublicModel: req.PublicModel, Path: req.Path, TimeoutSeconds: req.TimeoutSeconds, Headers: req.Headers, HeaderSecretRefs: req.HeaderSecretRefs}
}
func promotionGate(req *dto.LLMPromotionGateRequest) *domain.LLMPromotionGateConfig {
	if req == nil {
		return nil
	}
	return &domain.LLMPromotionGateConfig{IntervalSeconds: req.IntervalSeconds, TimeoutSeconds: req.TimeoutSeconds, SuccessThreshold: req.SuccessThreshold, FailureThreshold: req.FailureThreshold}
}
func placementPolicy(req *dto.LLMPlacementPolicyRequest) *domain.LLMPlacementPolicy {
	if req == nil {
		return nil
	}
	return &domain.LLMPlacementPolicy{PreferredKinds: backendKinds(req.PreferredKinds), WorkerSelector: req.WorkerSelector, MinGPUCount: req.MinGPUCount, MinGPUMemoryGB: req.MinGPUMemoryGB, MinSystemMemoryGB: req.MinSystemMemoryGB, MaxPrice: req.MaxPrice, AllowExternal: req.AllowExternal}
}
func backendKinds(values []string) []domain.LLMBackendKind {
	out := make([]domain.LLMBackendKind, 0, len(values))
	for _, v := range values {
		out = append(out, domain.LLMBackendKind(v))
	}
	return out
}
