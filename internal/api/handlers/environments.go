package handlers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// EnvironmentHandler handles HTTP requests for environments.
type EnvironmentHandler struct {
	registry *service.RegistryService
	units    repository.DeploymentUnitRepository
}

func NewEnvironmentHandler(registry *service.RegistryService, units repository.DeploymentUnitRepository) *EnvironmentHandler {
	return &EnvironmentHandler{registry: registry, units: units}
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
	response, err := h.environmentResponse(r, env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, response)
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
	responses := make([]dto.EnvironmentResponse, 0, len(environments))
	for i := range environments {
		response, responseErr := h.environmentResponse(r, &environments[i])
		if responseErr != nil {
			writeError(w, http.StatusInternalServerError, responseErr.Error())
			return
		}
		responses = append(responses, *response)
	}
	writeData(w, http.StatusOK, responses)
}

func (h *EnvironmentHandler) environmentResponse(r *http.Request, env *domain.Environment) (*dto.EnvironmentResponse, error) {
	return readmodel.EnvironmentResponse(r.Context(), env, h.units)
}
