package handlers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ServiceHandler handles HTTP requests for services.
type ServiceHandler struct {
	registry *service.RegistryService
}

func NewServiceHandler(registry *service.RegistryService) *ServiceHandler {
	return &ServiceHandler{registry: registry}
}

func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}

	svc, err := h.registry.GetService(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	writeData(w, http.StatusOK, svc)
}

func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	var services []domain.Service
	var err error
	if orgID := authzOrgID(r); orgID != uuid.Nil {
		services, err = h.registry.ListServicesByOrg(r.Context(), orgID)
	} else {
		services, err = h.registry.ListServices(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, services)
}
