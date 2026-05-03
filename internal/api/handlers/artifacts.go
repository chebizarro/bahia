package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ArtifactHandler handles HTTP requests for artifacts.
type ArtifactHandler struct {
	registry *service.RegistryService
}

func NewArtifactHandler(registry *service.RegistryService) *ArtifactHandler {
	return &ArtifactHandler{registry: registry}
}

func (h *ArtifactHandler) Register(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteServices) {
		return
	}
	var req dto.RegisterArtifactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredUUID(req.BuildID, "build_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredUUID(req.ServiceID, "service_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredString(req.ImageRepo, "image_repo"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredString(req.ImageTag, "image_tag"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateImageDigest(req.ImageDigest); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateScanStatus(domain.ScanStatus(req.ScanStatus)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	svc, err := h.registry.GetService(r.Context(), req.ServiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	if !serviceInAuthzOrg(w, r, svc.OrgID) {
		return
	}

	a := &domain.Artifact{
		BuildID:           req.BuildID,
		ServiceID:         req.ServiceID,
		ImageRepo:         req.ImageRepo,
		ImageTag:          req.ImageTag,
		ImageDigest:       req.ImageDigest,
		ManifestMediaType: req.ManifestMediaType,
		SizeBytes:         req.SizeBytes,
		SBOMURL:           req.SBOMURL,
		SignatureRef:      req.SignatureRef,
		ScanStatus:        domain.ScanStatus(req.ScanStatus),
		Metadata:          req.Metadata,
	}

	if err := h.registry.RegisterArtifact(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, a)
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
