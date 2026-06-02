package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/service"
)

// StateHandler handles HTTP requests for environment service state and observations.
type StateHandler struct {
	registry *service.RegistryService
}

func NewStateHandler(registry *service.RegistryService) *StateHandler {
	return &StateHandler{registry: registry}
}

func (h *StateHandler) GetState(w http.ResponseWriter, r *http.Request) {
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
	envID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return
	}

	states, err := h.registry.ListEnvironmentStates(r.Context(), envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *StateHandler) ListDrifted(w http.ResponseWriter, r *http.Request) {
	states, err := h.registry.ListDriftedStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}

func (h *StateHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	states, err := h.registry.ListAllStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, states)
}
