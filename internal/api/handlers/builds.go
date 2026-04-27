package handlers

import (
	"errors"
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// BuildHandler handles HTTP requests for builds.
type BuildHandler struct {
	registry *service.RegistryService
}

func NewBuildHandler(registry *service.RegistryService) *BuildHandler {
	return &BuildHandler{registry: registry}
}

func (h *BuildHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req dto.RegisterBuildRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredUUID(req.ServiceID, "service_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateGitSHA(req.GitSHA); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredString(req.GitRef, "git_ref"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredString(req.CIRunID, "ci_run_id"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateBuildStatus(domain.BuildStatus(req.Status)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	b := &domain.Build{
		ServiceID:     req.ServiceID,
		GitSHA:        req.GitSHA,
		GitRef:        req.GitRef,
		CISystem:      req.CISystem,
		CIRunID:       req.CIRunID,
		LoomJobID:     req.LoomJobID,
		Status:        domain.BuildStatus(req.Status),
		SourceEventID: req.SourceEventID,
		Metadata:      req.Metadata,
	}
	if b.CISystem == "" {
		b.CISystem = "hive-ci"
	}

	if err := h.registry.RegisterBuild(r.Context(), b); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, b)
}

func (h *BuildHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid build id")
		return
	}

	b, err := h.registry.GetBuild(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	writeData(w, http.StatusOK, b)
}

func (h *BuildHandler) ListByService(w http.ResponseWriter, r *http.Request) {
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	builds, err := h.registry.ListBuilds(r.Context(), serviceID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: builds, Limit: limit, Offset: offset})
}

func (h *BuildHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid build id")
		return
	}

	var req dto.UpdateBuildStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateBuildStatus(domain.BuildStatus(req.Status)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "status is required")
		return
	}

	if err := h.registry.UpdateBuildStatus(r.Context(), id, domain.BuildStatus(req.Status)); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "build not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "build status updated")
}
