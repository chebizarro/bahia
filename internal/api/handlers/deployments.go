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

func (h *DeploymentHandler) CreateIntent(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteDeployments) {
		return
	}
	var req dto.CreateDeploymentIntentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredUUID(req.ServiceID, "service_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredUUID(req.EnvironmentID, "environment_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredUUID(req.ArtifactID, "artifact_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Resolve actor: auth principal overrides client-supplied value.
	req.RequestedBy = resolveActor(r, req.RequestedBy)

	if err := domain.ValidateRequiredString(req.RequestedBy, "requested_by"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateSourceKind(domain.SourceKind(req.SourceKind)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.validateIntentResourcesInOrg(w, r, req.ServiceID, req.EnvironmentID, req.ArtifactID) {
		return
	}

	sourceKind := domain.SourceKind(req.SourceKind)
	if sourceKind == "" {
		sourceKind = domain.SourceKindManual
	}

	di := &domain.DeploymentIntent{
		ServiceID:     req.ServiceID,
		EnvironmentID: req.EnvironmentID,
		ArtifactID:    req.ArtifactID,
		RequestedBy:   req.RequestedBy,
		SourceKind:    sourceKind,
		Metadata:      req.Metadata,
	}

	if err := h.registry.CreateDeploymentIntent(r.Context(), di); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, di)
}

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

func (h *DeploymentHandler) ApproveIntent(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	if err := h.registry.ApproveDeploymentIntent(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment intent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "deployment intent approved")
}

func (h *DeploymentHandler) RejectIntent(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	if err := h.registry.RejectDeploymentIntent(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment intent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "deployment intent rejected")
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

func (h *DeploymentHandler) validateIntentResourcesInOrg(w http.ResponseWriter, r *http.Request, serviceID, envID, artifactID uuid.UUID) bool {
	if !h.validateServiceEnvInOrg(w, r, serviceID, envID) {
		return false
	}
	artifact, err := h.registry.GetArtifact(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if artifact == nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return false
	}
	if artifact.ServiceID != serviceID {
		writeError(w, http.StatusForbidden, "access denied")
		return false
	}
	return true
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

// --- Rollback ---

func (h *DeploymentHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteDeployments) {
		return
	}
	var req dto.RollbackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Resolve actor: auth principal overrides client-supplied value.
	req.RequestedBy = resolveActor(r, req.RequestedBy)

	if req.RequestedBy == "" {
		writeError(w, http.StatusBadRequest, "requested_by is required")
		return
	}
	if !h.validateServiceEnvInOrg(w, r, req.ServiceID, req.EnvironmentID) {
		return
	}

	intent, err := h.registry.Rollback(r.Context(), req.ServiceID, req.EnvironmentID, req.RequestedBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, intent)
}
