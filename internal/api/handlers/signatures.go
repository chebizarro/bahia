package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// SignatureVerifier is the interface for signature verification.
type SignatureVerifier interface {
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

// SignatureHandler handles HTTP requests for artifact signature operations.
type SignatureHandler struct {
	signatures repository.ArtifactSignatureRepository
	artifacts  repository.ArtifactRepository
	verifier   SignatureVerifier
}

// NewSignatureHandler creates a new signature handler.
func NewSignatureHandler(
	signatures repository.ArtifactSignatureRepository,
	artifacts repository.ArtifactRepository,
	verifier SignatureVerifier,
) *SignatureHandler {
	return &SignatureHandler{
		signatures: signatures,
		artifacts:  artifacts,
		verifier:   verifier,
	}
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

// Verify triggers signature verification for an artifact and stores any found signatures.
// POST /artifacts/{id}/signatures/verify
func (h *SignatureHandler) Verify(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteServices) {
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	// Look up the artifact.
	artifact, err := h.artifacts.GetByID(r.Context(), artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "artifact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if artifact == nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}

	// Verify signatures.
	sigs, err := h.verifier.VerifySignatures(r.Context(), artifact)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verifying signatures: "+err.Error())
		return
	}

	// Store found signatures and report truthful verification state.
	var stored int
	counts := map[domain.SignatureVerificationStatus]int{
		domain.SignatureStatusVerified:   0,
		domain.SignatureStatusDiscovered: 0,
		domain.SignatureStatusRejected:   0,
		domain.SignatureStatusError:      0,
	}
	for i := range sigs {
		sig := &sigs[i]
		sig.NormalizeVerificationStatus()
		counts[sig.VerificationStatus]++
		if err := h.signatures.Create(r.Context(), sig); err != nil {
			// Log but continue - might be duplicate
			continue
		}
		stored++
	}

	writeData(w, http.StatusOK, map[string]any{
		"found":      len(sigs),
		"stored":     stored,
		"verified":   counts[domain.SignatureStatusVerified],
		"discovered": counts[domain.SignatureStatusDiscovered],
		"rejected":   counts[domain.SignatureStatusRejected],
		"errors":     counts[domain.SignatureStatusError],
		"signatures": sigs,
	})
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
