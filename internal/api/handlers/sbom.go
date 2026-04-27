package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/repository"
)

// SBOMHandler handles HTTP requests for SBOM operations.
type SBOMHandler struct {
	sboms     repository.SBOMRepository
	artifacts repository.ArtifactRepository
}

// NewSBOMHandler creates a new SBOM handler.
func NewSBOMHandler(sboms repository.SBOMRepository, artifacts repository.ArtifactRepository) *SBOMHandler {
	return &SBOMHandler{sboms: sboms, artifacts: artifacts}
}

// GetSBOM returns the SBOM for an artifact.
// GET /artifacts/{id}/sbom
func (h *SBOMHandler) GetSBOM(w http.ResponseWriter, r *http.Request) {
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	s, err := h.sboms.GetSBOMByArtifact(r.Context(), artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "SBOM not found for this artifact")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, s)
}

// GetSBOMPackages returns packages for an artifact's SBOM.
// GET /artifacts/{id}/sbom/packages
func (h *SBOMHandler) GetSBOMPackages(w http.ResponseWriter, r *http.Request) {
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	s, err := h.sboms.GetSBOMByArtifact(r.Context(), artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "SBOM not found for this artifact")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	pkgs, err := h.sboms.ListPackagesBySBOM(r.Context(), s.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, pkgs)
}

// SearchPackages searches for packages across all SBOMs.
// GET /sbom/search?package=log4j&limit=100
func (h *SBOMHandler) SearchPackages(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("package")
	if name == "" {
		writeError(w, http.StatusBadRequest, "package query parameter is required")
		return
	}

	limit := queryInt(r, "limit", 100)

	pkgs, err := h.sboms.SearchPackagesByName(r.Context(), name, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, pkgs)
}

// IngestSBOM accepts an SBOM document, parses it, and stores it.
// POST /artifacts/{id}/sbom
func (h *SBOMHandler) IngestSBOM(w http.ResponseWriter, r *http.Request) {
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	// Verify artifact exists.
	if _, err := h.artifacts.GetByID(r.Context(), artifactID); err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "artifact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Read body (limit to 10MB).
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading SBOM body: "+err.Error())
		return
	}
	defer r.Body.Close()

	// Check idempotency via hash.
	hash := sha256.Sum256(body)
	rawHash := hex.EncodeToString(hash[:])
	if existing, err := h.sboms.GetSBOMByHash(r.Context(), rawHash); err == nil && existing != nil {
		writeData(w, http.StatusOK, existing) // already ingested
		return
	}

	// Parse the SBOM.
	result, err := sbom.Parse(body, artifactID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parsing SBOM: "+err.Error())
		return
	}

	// Store.
	if err := h.sboms.CreateSBOM(r.Context(), &result.SBOM); err != nil {
		writeError(w, http.StatusInternalServerError, "storing SBOM: "+err.Error())
		return
	}
	if len(result.Packages) > 0 {
		if err := h.sboms.CreatePackages(r.Context(), result.Packages); err != nil {
			writeError(w, http.StatusInternalServerError, "storing packages: "+err.Error())
			return
		}
	}

	writeData(w, http.StatusCreated, result.SBOM)
}
