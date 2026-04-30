package handlers

import (
	"errors"
	"net/http"
	"strings"

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
	if err := validateCreateServiceRepository(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	svc := &domain.Service{
		Name:          req.Name,
		RepoURL:       strings.TrimSpace(req.RepoURL),
		Repository:    mapRepositoryRefRequest(req.Repository),
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
	if err := validateUpdateServiceRepository(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Name != nil {
		svc.Name = *req.Name
	}
	if req.RepoURL != nil {
		svc.RepoURL = strings.TrimSpace(*req.RepoURL)
		svc.Repository = nil
	}
	if req.Repository != nil {
		svc.Repository = mapRepositoryRefRequest(req.Repository)
		svc.RepoURL = strings.TrimSpace(req.Repository.CloneURL)
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

func validateCreateServiceRepository(req *dto.CreateServiceRequest) error {
	if req == nil {
		return nil
	}
	return validateServiceRepositoryRequest(strings.TrimSpace(req.RepoURL), req.Repository, false)
}

func validateUpdateServiceRepository(req *dto.UpdateServiceRequest) error {
	if req == nil {
		return nil
	}
	if req.RepoURL == nil {
		return validateServiceRepositoryRequest("", req.Repository, false)
	}
	return validateServiceRepositoryRequest(strings.TrimSpace(*req.RepoURL), req.Repository, true)
}

func validateServiceRepositoryRequest(repoURL string, repo *dto.RepositoryRefRequest, repoURLProvided bool) error {
	if repo == nil {
		return nil
	}

	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		return errors.New("repository.clone_url is required when repository is provided")
	}
	if repoURL != "" && repoURL != cloneURL {
		return errors.New("repo_url must match repository.clone_url when both are provided")
	}
	if repoURLProvided && repoURL == "" {
		return errors.New("repo_url cannot be empty when repository is provided")
	}

	source := strings.TrimSpace(repo.Source)
	if source != "" && source != "manual" && source != "nip34" {
		return errors.New("repository.source must be one of: manual, nip34")
	}
	if source == "nip34" && strings.TrimSpace(repo.RepoCoordinate) == "" {
		return errors.New("repository.repo_coordinate is required when repository.source is nip34")
	}

	if repo.CI != nil {
		provider := strings.TrimSpace(repo.CI.Provider)
		if provider != "" && provider != "hiveci" {
			return errors.New("repository.ci.provider must be hiveci when provided")
		}
	}

	return nil
}

func mapRepositoryRefRequest(repo *dto.RepositoryRefRequest) *domain.RepositoryRef {
	if repo == nil {
		return nil
	}

	mapped := &domain.RepositoryRef{
		Source:         strings.TrimSpace(repo.Source),
		RepoCoordinate: strings.TrimSpace(repo.RepoCoordinate),
		CloneURL:       strings.TrimSpace(repo.CloneURL),
		WebURL:         strings.TrimSpace(repo.WebURL),
		RelayURLs:      trimDedupeStrings(repo.RelayURLs),
	}
	if repo.CI != nil {
		mapped.CI = &domain.ServiceCIConfig{
			Provider:     strings.TrimSpace(repo.CI.Provider),
			WorkflowPath: strings.TrimSpace(repo.CI.WorkflowPath),
		}
	}
	return mapped
}

func trimDedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	trimmedValues := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		trimmedValues = append(trimmedValues, trimmed)
	}
	return trimmedValues
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
