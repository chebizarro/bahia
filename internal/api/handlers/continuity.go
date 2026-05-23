package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/service"
)

// ContinuityGraphReader exposes continuity graph assessment operations used by HTTP handlers.
type ContinuityGraphReader interface {
	AssessAll() []service.ContinuityAssessment
	SimulateFailure(workerPubKey string) []service.ContinuityAssessment
}

// ContinuityHandler serves query endpoints for the continuity status read model and graph assessments.
type ContinuityHandler struct {
	statuses service.ContinuityStatusReader
	graph    ContinuityGraphReader
}

// NewContinuityHandler creates a continuity status HTTP handler.
func NewContinuityHandler(statuses service.ContinuityStatusReader, graph ContinuityGraphReader) *ContinuityHandler {
	return &ContinuityHandler{statuses: statuses, graph: graph}
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

// Topology returns continuity graph assessments for all managed services.
func (h *ContinuityHandler) Topology(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if h == nil || h.graph == nil {
		writeError(w, http.StatusServiceUnavailable, "continuity graph unavailable")
		return
	}

	assessments := h.graph.AssessAll()
	if assessments == nil {
		assessments = []service.ContinuityAssessment{}
	}
	writeData(w, http.StatusOK, dto.ContinuityAssessmentDTOsFromService(assessments))
}

// Simulate evaluates continuity graph survivability after a worker failure.
func (h *ContinuityHandler) Simulate(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if h == nil || h.graph == nil {
		writeError(w, http.StatusServiceUnavailable, "continuity graph unavailable")
		return
	}

	var req dto.SimulateFailureRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid simulation request")
		return
	}
	if req.WorkerPubKey == "" {
		writeError(w, http.StatusBadRequest, "worker_pubkey is required")
		return
	}

	assessments := h.graph.SimulateFailure(req.WorkerPubKey)
	if assessments == nil {
		assessments = []service.ContinuityAssessment{}
	}
	writeData(w, http.StatusOK, dto.ContinuityAssessmentDTOsFromService(assessments))
}
