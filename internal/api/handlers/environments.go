package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// EnvironmentHandler handles HTTP requests for environments.
type EnvironmentHandler struct {
	registry *service.RegistryService
}

func NewEnvironmentHandler(registry *service.RegistryService) *EnvironmentHandler {
	return &EnvironmentHandler{registry: registry}
}

func (h *EnvironmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteEnvironments) {
		return
	}
	var req dto.CreateEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := domain.ValidateRequiredString(req.Name, "name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateDeployStrategy(domain.DeployStrategy(req.DeployStrategy)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	env := &domain.Environment{
		OrgID:              authzOrgID(r),
		Name:               req.Name,
		LoomWorkerSelector: req.LoomWorkerSelector,
		RuntimeConfig:      req.RuntimeConfig,
		DeployStrategy:     domain.DeployStrategy(req.DeployStrategy),
		Protected:          req.Protected,
	}

	if err := h.registry.CreateEnvironment(r.Context(), env); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, env)
}

func (h *EnvironmentHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	env, err := h.registry.GetEnvironment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if env == nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	writeData(w, http.StatusOK, env)
}

func (h *EnvironmentHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	var environments []domain.Environment
	var err error
	if orgID := authzOrgID(r); orgID != uuid.Nil {
		environments, err = h.registry.ListEnvironmentsByOrg(r.Context(), orgID)
	} else {
		environments, err = h.registry.ListEnvironments(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, environments)
}

func (h *EnvironmentHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteEnvironments) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	env, err := h.registry.GetEnvironment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if env == nil {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}

	var req dto.UpdateEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != nil {
		env.Name = *req.Name
	}
	if req.LoomWorkerSelector != nil {
		env.LoomWorkerSelector = *req.LoomWorkerSelector
	}
	if req.RuntimeConfig != nil {
		env.RuntimeConfig = *req.RuntimeConfig
	}
	if req.DeployStrategy != nil {
		if err := domain.ValidateDeployStrategy(domain.DeployStrategy(*req.DeployStrategy)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		env.DeployStrategy = domain.DeployStrategy(*req.DeployStrategy)
	}
	if req.Protected != nil {
		env.Protected = *req.Protected
	}

	if err := h.registry.UpdateEnvironment(r.Context(), env); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, env)
}

func (h *EnvironmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteEnvironments) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := h.registry.DeleteEnvironment(r.Context(), id, force); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "environment not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "environment deleted")
}
