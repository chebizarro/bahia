package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// DeploymentHandler handles HTTP requests for deployment intents and runs.
type DeploymentHandler struct {
	registry *service.RegistryService
}

func NewDeploymentHandler(registry *service.RegistryService) *DeploymentHandler {
	return &DeploymentHandler{registry: registry}
}

// resolveActor determines the actor identity for a request.
// When the caller is authenticated, the Principal.Subject is authoritative
// and overrides any client-supplied value (preventing identity spoofing).
// When auth is disabled (no Principal), the client-supplied value is used.
func resolveActor(r *http.Request, clientSupplied string) string {
	if p := auth.GetPrincipal(r.Context()); p != nil && p.IsAuthenticated() {
		return p.Subject
	}
	return clientSupplied
}

// --- Deployment Intents ---

func (h *DeploymentHandler) GetIntent(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}

	di, err := h.registry.GetDeploymentIntent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if di == nil {
		writeError(w, http.StatusNotFound, "deployment intent not found")
		return
	}
	writeData(w, http.StatusOK, di)
}

func (h *DeploymentHandler) ListIntents(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	intents, err := h.registry.ListDeploymentIntents(r.Context(), serviceID, envID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: intents, Limit: limit, Offset: offset})
}

func (h *DeploymentHandler) validateServiceInOrg(w http.ResponseWriter, r *http.Request, serviceID uuid.UUID) bool {
	if authzOrgID(r) == uuid.Nil {
		return true
	}
	svc, err := h.registry.GetService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return false
	}
	return serviceInAuthzOrg(w, r, svc.OrgID)
}

func (h *DeploymentHandler) validateServiceEnvInOrg(w http.ResponseWriter, r *http.Request, serviceID, envID uuid.UUID) bool {
	if authzOrgID(r) == uuid.Nil {
		return true
	}
	svc, err := h.registry.GetService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return false
	}
	env, err := h.registry.GetEnvironment(r.Context(), envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if env == nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return false
	}
	if svc.OrgID != env.OrgID {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	return serviceInAuthzOrg(w, r, svc.OrgID)
}

// --- Deployment Runs ---

func (h *DeploymentHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteDeployments) {
		return
	}
	var req dto.CreateDeploymentRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredUUID(req.DeploymentIntentID, "deployment_intent_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	intent, err := h.registry.GetDeploymentIntent(r.Context(), req.DeploymentIntentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if intent == nil {
		writeError(w, http.StatusNotFound, "deployment intent not found")
		return
	}
	if !h.validateServiceInOrg(w, r, intent.ServiceID) {
		return
	}

	dr := &domain.DeploymentRun{
		DeploymentIntentID: req.DeploymentIntentID,
		DeploymentUnitID:   req.DeploymentUnitID,
		LoomJobID:          req.LoomJobID,
		WorkerPubkey:       req.WorkerPubkey,
		WorkerName:         req.WorkerName,
		Metadata:           req.Metadata,
	}

	if err := h.registry.CreateDeploymentRun(r.Context(), dr); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, dr)
}

func (h *DeploymentHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}

	dr, err := h.registry.GetDeploymentRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dr == nil {
		writeError(w, http.StatusNotFound, "deployment run not found")
		return
	}
	writeData(w, http.StatusOK, dr)
}

func (h *DeploymentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	intentID, err := uuidParam(r, "intentId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}

	runs, err := h.registry.ListDeploymentRuns(r.Context(), intentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, runs)
}

func (h *DeploymentHandler) CompleteRun(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteDeployments) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}

	var req dto.CompleteDeploymentRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredString(req.Status, "status"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateDeploymentRunStatus(domain.DeploymentRunStatus(req.Status)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.registry.CompleteDeploymentRun(r.Context(), id, domain.DeploymentRunStatus(req.Status), req.ExitCode); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "deployment run completed")
}
