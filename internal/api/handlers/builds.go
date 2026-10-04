package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

// BuildHandler handles HTTP requests for builds.
type BuildHandler struct {
	registry *service.RegistryService
}

func NewBuildHandler(registry *service.RegistryService) *BuildHandler {
	return &BuildHandler{registry: registry}
}

func (h *BuildHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid build id")
		return
	}

	b, err := h.registry.GetBuild(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	writeData(w, http.StatusOK, b)
}

func (h *BuildHandler) ListByService(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	builds, err := h.registry.ListBuilds(r.Context(), serviceID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: builds, Limit: limit, Offset: offset})
}
