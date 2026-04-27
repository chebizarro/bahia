package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/repository"
)

// WorkerHandler handles HTTP requests for the Loom worker catalog.
type WorkerHandler struct {
	repo repository.WorkerRepository
}

// NewWorkerHandler creates a new WorkerHandler.
func NewWorkerHandler(repo repository.WorkerRepository) *WorkerHandler {
	return &WorkerHandler{repo: repo}
}

// List returns all workers, optionally filtered by status query param.
func (h *WorkerHandler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	workers, err := h.repo.List(r.Context(), status, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, workers)
}

// Get returns a single worker by pubkey.
func (h *WorkerHandler) Get(w http.ResponseWriter, r *http.Request) {
	pubkey := chi.URLParam(r, "pubkey")
	if pubkey == "" {
		writeError(w, http.StatusBadRequest, "pubkey is required")
		return
	}

	worker, err := h.repo.GetByPubKey(r.Context(), pubkey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if worker == nil {
		writeError(w, http.StatusNotFound, "worker not found")
		return
	}
	writeData(w, http.StatusOK, worker)
}

// Pricing returns just the pricing info for a worker.
func (h *WorkerHandler) Pricing(w http.ResponseWriter, r *http.Request) {
	pubkey := chi.URLParam(r, "pubkey")
	if pubkey == "" {
		writeError(w, http.StatusBadRequest, "pubkey is required")
		return
	}

	worker, err := h.repo.GetByPubKey(r.Context(), pubkey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if worker == nil {
		writeError(w, http.StatusNotFound, "worker not found")
		return
	}
	writeData(w, http.StatusOK, worker.Pricing)
}
