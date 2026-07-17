package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// SBOMHandler handles HTTP requests for SBOM operations.
type SBOMHandler struct {
	sboms     repository.SBOMRepository
	artifacts repository.ArtifactRepository
	importer  SBOMImportService
}

type SBOMImportService interface {
	Import(context.Context, service.SBOMImportRequest) (*service.SBOMRunResult, error)
}

const maxSBOMIngestBytes = 10 * 1024 * 1024

// NewSBOMReadHandler creates a read-only SBOM handler for REST compatibility reads.
func NewSBOMReadHandler(sboms repository.SBOMRepository, artifacts repository.ArtifactRepository) *SBOMHandler {
	return &SBOMHandler{sboms: sboms, artifacts: artifacts}
}

// NewSBOMHandler creates an SBOM handler with the required import service for REST compatibility writes.
func NewSBOMHandler(sboms repository.SBOMRepository, artifacts repository.ArtifactRepository, importer SBOMImportService) *SBOMHandler {
	return &SBOMHandler{sboms: sboms, artifacts: artifacts, importer: importer}
}

// GetSBOM returns the SBOM for an artifact.
// GET /artifacts/{id}/sbom
func (h *SBOMHandler) GetSBOM(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
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
	if !requireMember(w, r) {
		return
	}
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

// IngestSBOM accepts an SBOM document through the REST compatibility path.
// POST /artifacts/{id}/sbom
func (h *SBOMHandler) IngestSBOM(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if h.importer == nil {
		writeError(w, http.StatusServiceUnavailable, "SBOM import service is not configured")
		return
	}
	artifactID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact ID")
		return
	}

	// Verify artifact exists and resolve the canonical SBOM subject.
	artifact, err := h.artifacts.GetByID(r.Context(), artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "artifact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(artifact.ImageDigest) == "" {
		writeError(w, http.StatusBadRequest, "artifact image digest is required for SBOM import")
		return
	}

	// Read body with an explicit hard limit so oversized SBOMs are rejected instead of truncated.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSBOMIngestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading SBOM body: "+err.Error())
		return
	}
	defer r.Body.Close()
	if len(body) > maxSBOMIngestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "SBOM payload exceeds 10 MiB limit")
		return
	}

	// Check idempotency via hash.
	hash := sha256.Sum256(body)
	rawHash := hex.EncodeToString(hash[:])
	if existing, err := h.sboms.GetSBOMByHash(r.Context(), rawHash); err == nil && existing != nil {
		writeData(w, http.StatusOK, existing) // already ingested
		return
	}

	_, err = h.importer.Import(r.Context(), service.SBOMImportRequest{
		IDempotencyKey: "rest-artifact-sbom:" + artifactID.String() + ":" + rawHash,
		Subject: domain.SBOMSubject{
			Type:        domain.SBOMSubjectArtifact,
			ID:          artifactID.String(),
			DisplayName: strings.Trim(artifact.ImageRepo+":"+artifact.ImageTag, ":"),
			Digest:      artifact.ImageDigest,
		},
		Payload:   body,
		Storage:   domain.SBOMStorageBlossom,
		Generator: domain.SBOMGenerator{ID: "rest-import"},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "importing SBOM: "+err.Error())
		return
	}

	imported, err := h.sboms.GetSBOMByHash(r.Context(), rawHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "loading imported SBOM projection: "+err.Error())
		return
	}
	writeData(w, http.StatusCreated, imported)
}
