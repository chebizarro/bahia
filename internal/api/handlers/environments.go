package handlers

import (
	"fmt"
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
	if env == nil {
		return nil, fmt.Errorf("environment is nil")
	}
	var units []domain.DeploymentUnit
	if h.units != nil {
		var err error
		units, err = h.units.ListByEnvironment(r.Context(), env.ID)
		if err != nil {
			return nil, fmt.Errorf("list deployment units for environment %s: %w", env.ID, err)
		}
	}
	if len(units) == 0 {
		var (
			implicit *domain.DeploymentUnit
			err      error
		)
		if h.units != nil {
			implicit, err = h.units.ResolveDefault(r.Context(), env)
		} else {
			envCopy := *env
			domain.NormalizeEnvironmentTargeting(&envCopy)
			if envCopy.Targeting.DefaultUnitKey != domain.DefaultDeploymentUnitKey {
				err = fmt.Errorf("configured default deployment unit %q cannot be resolved without a deployment unit repository: %w", envCopy.Targeting.DefaultUnitKey, repository.ErrConflict)
			} else {
				implicit, err = domain.NewImplicitDefaultDeploymentUnit(&envCopy)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("resolve default deployment unit for environment %s: %w", env.ID, err)
		}
		if implicit == nil {
			return nil, fmt.Errorf("default deployment unit was not found for environment %s", env.ID)
		}
		units = []domain.DeploymentUnit{*implicit}
	}
	return &dto.EnvironmentResponse{Environment: *env, DeploymentUnits: units}, nil
}
