package handlers

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ServiceCreateCommandPublisher publishes signer-first service creation commands.
type ServiceCreateCommandPublisher interface {
	PublishServiceCreateRequest(ctx context.Context, cmd controlplane.ServiceCreateCommand) (*controlplane.ServiceCommandReceipt, error)
}

// ServiceMutationCommandPublisher publishes signer-first service registry and deployment commands.
type ServiceMutationCommandPublisher interface {
	ServiceCreateCommandPublisher
	DeploymentIntentCommandPublisher
}

// ServiceHandler handles HTTP requests for services.
type ServiceHandler struct {
	registry *service.RegistryService
	commands ServiceCreateCommandPublisher
}

func NewServiceHandler(registry *service.RegistryService) *ServiceHandler {
	return &ServiceHandler{registry: registry}
}

func NewServiceHandlerWithCommands(registry *service.RegistryService, commands ServiceCreateCommandPublisher) *ServiceHandler {
	return &ServiceHandler{registry: registry, commands: commands}
}

func (h *ServiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteServices) {
		return
	}
	if h.commands == nil {
		writeError(w, http.StatusServiceUnavailable, "service command publisher is not configured")
		return
	}
	var req dto.CreateServiceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := h.commands.PublishServiceCreateRequest(r.Context(), controlplane.ServiceCreateCommand{
		Name:           req.Name,
		RepoURL:        req.RepoURL,
		Repository:     req.Repository,
		ArtifactRepo:   req.ArtifactRepo,
		DefaultBranch:  req.DefaultBranch,
		RuntimeType:    req.RuntimeType,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeCommandPublishError(w, err)
		return
	}
	writeAcceptedCommandReceipt(w, commandReceiptFromService(receipt))
}

func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
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
	if !requireMember(w, r) {
		return
	}
	var services []domain.Service
	var err error
	if orgID := authzOrgID(r); orgID != uuid.Nil {
		services, err = h.registry.ListServicesByOrg(r.Context(), orgID)
	} else {
		services, err = h.registry.ListServices(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, services)
}
