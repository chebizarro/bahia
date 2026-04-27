package handlers

import (
	"errors"
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// ServiceHandler handles HTTP requests for services.
type ServiceHandler struct {
	registry *service.RegistryService
}

func NewServiceHandler(registry *service.RegistryService) *ServiceHandler {
	return &ServiceHandler{registry: registry}
}

func (h *ServiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req dto.CreateServiceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := domain.ValidateRequiredString(req.Name, "name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRequiredString(req.ArtifactRepo, "artifact_repo"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := domain.ValidateRuntimeType(domain.RuntimeType(req.RuntimeType)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	svc := &domain.Service{
		Name:          req.Name,
		RepoURL:       req.RepoURL,
		ArtifactRepo:  req.ArtifactRepo,
		DefaultBranch: req.DefaultBranch,
		RuntimeType:   domain.RuntimeType(req.RuntimeType),
	}

	if err := h.registry.CreateService(r.Context(), svc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, svc)
}

func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}

	svc, err := h.registry.GetService(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	writeData(w, http.StatusOK, svc)
}

func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
	services, err := h.registry.ListServices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, services)
}

func (h *ServiceHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}

	svc, err := h.registry.GetService(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}

	var req dto.UpdateServiceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != nil {
		svc.Name = *req.Name
	}
	if req.RepoURL != nil {
		svc.RepoURL = *req.RepoURL
	}
	if req.ArtifactRepo != nil {
		svc.ArtifactRepo = *req.ArtifactRepo
	}
	if req.DefaultBranch != nil {
		svc.DefaultBranch = *req.DefaultBranch
	}
	if req.RuntimeType != nil {
		if err := domain.ValidateRuntimeType(domain.RuntimeType(*req.RuntimeType)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		svc.RuntimeType = domain.RuntimeType(*req.RuntimeType)
	}

	if err := h.registry.UpdateService(r.Context(), svc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, svc)
}

func (h *ServiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := h.registry.DeleteService(r.Context(), id, force); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeMessage(w, http.StatusOK, "service deleted")
}
