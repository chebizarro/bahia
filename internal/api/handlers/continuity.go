package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

// ContinuityHandler serves query endpoints for the continuity status read model.
type ContinuityHandler struct {
	statuses service.ContinuityStatusReader
}

// NewContinuityHandler creates a continuity status HTTP handler.
func NewContinuityHandler(statuses service.ContinuityStatusReader) *ContinuityHandler {
	return &ContinuityHandler{statuses: statuses}
}

// Status returns the latest continuity status for all services with projected state.
func (h *ContinuityHandler) Status(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if h == nil || h.statuses == nil {
		writeError(w, http.StatusServiceUnavailable, "continuity status store unavailable")
		return
	}

	statuses := h.statuses.ListAllStatuses()
	if statuses == nil {
		statuses = []service.ContinuityStatus{}
	}
	writeData(w, http.StatusOK, dto.ContinuityServiceStatusDTOsFromService(statuses))
}
