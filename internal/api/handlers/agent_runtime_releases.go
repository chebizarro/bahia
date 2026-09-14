package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type AgentRuntimeReleaseReader interface {
	ListServiceReleases(context.Context, uuid.UUID, uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error)
	GetRollbackRelease(context.Context, uuid.UUID, string, uuid.UUID, string) (*domain.AgentServiceRuntimeRelease, error)
}

type AgentRuntimeReleaseHandler struct{ reader AgentRuntimeReleaseReader }

func NewAgentRuntimeReleaseHandler(reader AgentRuntimeReleaseReader) *AgentRuntimeReleaseHandler {
	return &AgentRuntimeReleaseHandler{reader: reader}
}

func (h *AgentRuntimeReleaseHandler) ListServiceReleases(w http.ResponseWriter, r *http.Request) {
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	items, err := h.reader.ListServiceReleases(r.Context(), authzOrgID(r), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, items)
}

func (h *AgentRuntimeReleaseHandler) GetRollbackRelease(w http.ResponseWriter, r *http.Request) {
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	channel := strings.TrimSpace(r.URL.Query().Get("release_channel"))
	if agentID == "" || channel == "" {
		writeError(w, http.StatusBadRequest, "agent_id and release_channel are required")
		return
	}
	item, err := h.reader.GetRollbackRelease(r.Context(), authzOrgID(r), agentID, serviceID, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "rollback release not found")
		return
	}
	writeData(w, http.StatusOK, item)
}
