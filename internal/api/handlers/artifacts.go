package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

// ArtifactHandler handles HTTP requests for artifacts.
type ArtifactHandler struct {
	registry *service.RegistryService
}

func NewArtifactHandler(registry *service.RegistryService) *ArtifactHandler {
	return &ArtifactHandler{registry: registry}
}

func (h *ArtifactHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}

	a, err := h.registry.GetArtifact(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a == nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	writeData(w, http.StatusOK, a)
}

func (h *ArtifactHandler) ListByService(w http.ResponseWriter, r *http.Request) {
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

	artifacts, err := h.registry.ListArtifacts(r.Context(), serviceID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: artifacts, Limit: limit, Offset: offset})
}
