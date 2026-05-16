package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/service"
)

// PaymentHandler handles HTTP requests for Cashu payment operations.
type PaymentHandler struct {
	payments *service.PaymentService
}

// NewPaymentHandler creates a new payment handler.
func NewPaymentHandler(payments *service.PaymentService) *PaymentHandler {
	return &PaymentHandler{payments: payments}
}

func (h *PaymentHandler) requirePayments(w http.ResponseWriter) bool {
	if h.payments != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "payment service is not configured")
	return false
}

// EstimateCost returns a cost estimate for a deployment run.
// POST /payments/estimate
func (h *PaymentHandler) EstimateCost(w http.ResponseWriter, r *http.Request) {
	if !h.requirePayments(w) {
		return
	}
	var req struct {
		RunID             string `json:"run_id"`
		EstimatedDuration int    `json:"estimated_duration_secs"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runID, err := uuid.Parse(req.RunID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run_id")
		return
	}

	estimate, err := h.payments.EstimateCost(r.Context(), runID, req.EstimatedDuration)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	writeData(w, http.StatusOK, estimate)
}

// GetRunCost returns payment records and cost summary for a deployment run.
// GET /deployments/runs/{id}/cost
func (h *PaymentHandler) GetRunCost(w http.ResponseWriter, r *http.Request) {
	if !h.requirePayments(w) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}

	summary, err := h.payments.GetRunCostSummary(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	payments, err := h.payments.GetRunPayments(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]any{
		"summary":  summary,
		"payments": payments,
	})
}

// GetPaymentHistory returns payment history, optionally filtered by worker.
// GET /payments/history?worker=<pubkey>&limit=50
func (h *PaymentHandler) GetPaymentHistory(w http.ResponseWriter, r *http.Request) {
	if !h.requirePayments(w) {
		return
	}
	workerPubkey := r.URL.Query().Get("worker")
	if workerPubkey == "" {
		writeError(w, http.StatusBadRequest, "worker query parameter is required")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	records, err := h.payments.GetPaymentHistory(r.Context(), workerPubkey, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, records)
}
