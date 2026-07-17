package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// StateHandler handles HTTP requests for environment service state and observations.
type StateHandler struct {
	registry     *service.RegistryService
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
}

func NewStateHandler(registry *service.RegistryService, services repository.ServiceRepository, environments repository.EnvironmentRepository) *StateHandler {
	return &StateHandler{registry: registry, services: services, environments: environments}
}

func (h *StateHandler) GetState(w http.ResponseWriter, r *http.Request) {
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

	state, err := h.registry.GetEnvironmentServiceState(r.Context(), serviceID, envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if state == nil {
		writeError(w, http.StatusNotFound, "no state found for this service/environment")
		return
	}
	writeData(w, http.StatusOK, state)
}

func (h *StateHandler) ListByEnvironment(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	states, err := h.registry.ListEnvironmentStates(r.Context(), envID)
	if err == nil {
		states, err = h.filterToAuthzOrg(r, states)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *StateHandler) ListDrifted(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	states, err := h.registry.ListDriftedStates(r.Context())
	if err == nil {
		states, err = h.filterToAuthzOrg(r, states)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *StateHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	states, err := h.registry.ListAllStates(r.Context())
	if err == nil {
		states, err = h.filterToAuthzOrg(r, states)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *StateHandler) filterToAuthzOrg(r *http.Request, states []domain.EnvironmentServiceState) ([]domain.EnvironmentServiceState, error) {
	orgID := authzOrgID(r)
	if orgID == uuid.Nil {
		return states, nil
	}
	if h.services == nil || h.environments == nil {
		return nil, fmt.Errorf("service and environment repositories are required for tenant-scoped state reads")
	}

	filtered := make([]domain.EnvironmentServiceState, 0, len(states))
	for _, state := range states {
		svc, err := h.services.GetByID(r.Context(), state.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("resolve state service ownership: %w", err)
		}
		env, err := h.environments.GetByID(r.Context(), state.EnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("resolve state environment ownership: %w", err)
		}
		if svc != nil && env != nil && svc.OrgID == orgID && env.OrgID == orgID {
			filtered = append(filtered, state)
		}
	}
	return filtered, nil
}
