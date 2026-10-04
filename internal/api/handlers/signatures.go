package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

// SignatureHandler handles HTTP requests for artifact signature operations.
type SignatureHandler struct {
	signatures repository.ArtifactSignatureRepository
}

// NewSignatureHandler creates a new signature handler.
func NewSignatureHandler(signatures repository.ArtifactSignatureRepository) *SignatureHandler {
	return &SignatureHandler{signatures: signatures}
}

// List returns all signatures for an artifact.
// GET /artifacts/{id}/signatures
func (h *SignatureHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	sigs, err := h.signatures.ListByArtifact(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, sigs)
}

// ListVerified returns only verified signatures for an artifact.
// GET /artifacts/{id}/signatures/verified
func (h *SignatureHandler) ListVerified(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	sigs, err := h.signatures.ListVerifiedByArtifact(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, sigs)
}

// HasVerified checks if an artifact has at least one verified signature.
// GET /artifacts/{id}/signatures/check
func (h *SignatureHandler) HasVerified(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	hasVerified, err := h.signatures.HasVerifiedSignature(r.Context(), artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, map[string]bool{"has_verified_signature": hasVerified})
}

// Get returns a single signature by ID.
// GET /signatures/{id}
func (h *SignatureHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	sigID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid signature ID")
		return
	}

	sig, err := h.signatures.GetByID(r.Context(), sigID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "signature not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, sig)
}
