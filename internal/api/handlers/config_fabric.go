package handlers

import (
	"net/http"

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
