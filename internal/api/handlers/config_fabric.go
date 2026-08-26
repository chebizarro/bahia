package handlers

import (
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type ConfigFabricHandler struct {
	config *service.ConfigFabricService
}

func NewConfigFabricHandler(config *service.ConfigFabricService) *ConfigFabricHandler {
	return &ConfigFabricHandler{config: config}
}

func (h *ConfigFabricHandler) ListDrift(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermReadPolicies) {
		return
	}
	view, err := h.config.ListDrift(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, view)
}

func (h *ConfigFabricHandler) Publish(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWritePolicies) {
		return
	}
	var request service.ConfigPublishRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := h.config.Publish(r.Context(), request)
	if err != nil {
		writeError(w, configFabricErrorStatus(err), err.Error())
		return
	}
	writeData(w, http.StatusCreated, receipt)
}

func (h *ConfigFabricHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWritePolicies) {
		return
	}
	var request service.ConfigRollbackRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := h.config.Rollback(r.Context(), request.EventID)
	if err != nil {
		writeError(w, configFabricErrorStatus(err), err.Error())
		return
	}
	writeData(w, http.StatusCreated, receipt)
}

func configFabricErrorStatus(err error) int {
	message := err.Error()
	switch {
	case strings.Contains(message, "not configured"), strings.Contains(message, "not connected"):
		return http.StatusServiceUnavailable
	case strings.Contains(message, "not found"):
		return http.StatusNotFound
	case strings.Contains(message, "publish config-fabric event"):
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}
