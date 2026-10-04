package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// InstanceMaintenanceOperator is the mutating subset exposed by the supervisor.
type InstanceMaintenanceOperator interface {
	SetMaintenanceOverride(context.Context, domain.ManagedInstanceKey, string, string, *time.Time) (*domain.MaintenanceOverride, error)
	ClearMaintenanceOverride(context.Context, domain.ManagedInstanceKey, string) error
}

// InstanceHealthHandler serves tenant-scoped managed-instance health and maintenance APIs.
type InstanceHealthHandler struct {
	operator InstanceMaintenanceOperator
	now      func() time.Time
}

func NewInstanceHealthHandler(operator InstanceMaintenanceOperator) *InstanceHealthHandler {
	return &InstanceHealthHandler{operator: operator, now: func() time.Time { return time.Now().UTC() }}
}

type setMaintenanceRequest struct {
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (h *InstanceHealthHandler) SetMaintenance(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteServices) {
		return
	}
	actor, authenticated := authenticatedSubject(r)
	if !authenticated {
		writeError(w, http.StatusUnauthorized, "authenticated operator required")
		return
	}
	if h.operator == nil {
		writeError(w, http.StatusServiceUnavailable, "managed instance supervision is unavailable")
		return
	}
	key, ok := instanceKeyFromRoute(w, r)
	if !ok {
		return
	}
	var request setMaintenanceRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		writeError(w, http.StatusBadRequest, "maintenance reason is required")
		return
	}
	if request.ExpiresAt != nil && !request.ExpiresAt.After(h.now()) {
		writeError(w, http.StatusBadRequest, "maintenance expiry must be in the future")
		return
	}
	override, err := h.operator.SetMaintenanceOverride(r.Context(), key, actor, request.Reason, request.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sanitizeOverride(override)
	writeData(w, http.StatusOK, override)
}

func (h *InstanceHealthHandler) ClearMaintenance(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteServices) {
		return
	}
	actor, authenticated := authenticatedSubject(r)
	if !authenticated {
		writeError(w, http.StatusUnauthorized, "authenticated operator required")
		return
	}
	if h.operator == nil {
		writeError(w, http.StatusServiceUnavailable, "managed instance supervision is unavailable")
		return
	}
	key, ok := instanceKeyFromRoute(w, r)
	if !ok {
		return
	}
	if err := h.operator.ClearMaintenanceOverride(r.Context(), key, actor); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"cleared": true})
}

func instanceKeyFromRoute(w http.ResponseWriter, r *http.Request) (domain.ManagedInstanceKey, bool) {
	serviceID, err := uuidParam(r, "serviceId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return domain.ManagedInstanceKey{}, false
	}
	environmentID, err := uuidParam(r, "envId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return domain.ManagedInstanceKey{}, false
	}
	deploymentUnitID, err := uuidParam(r, "deploymentUnitId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment unit id")
		return domain.ManagedInstanceKey{}, false
	}
	target := strings.TrimSpace(r.URL.Query().Get("runtime_target_name"))
	if target == "" {
		writeError(w, http.StatusBadRequest, "runtime_target_name is required")
		return domain.ManagedInstanceKey{}, false
	}
	return domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: deploymentUnitID, RuntimeTargetName: target}, true
}

func sanitizeOverride(override *domain.MaintenanceOverride) {
	if override == nil {
		return
	}
	override.Actor = domain.SanitizeEvidence(override.Actor)
	override.Reason = domain.SanitizeEvidence(override.Reason)
}
