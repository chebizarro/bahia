package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	defaultInstanceHealthListLimit    = 50
	maxInstanceHealthListLimit        = 500
	defaultInstanceHealthHistoryLimit = 50
	maxInstanceHealthHistoryLimit     = 500
)

// InstanceMaintenanceOperator is the mutating subset exposed by the supervisor.
type InstanceMaintenanceOperator interface {
	SetMaintenanceOverride(context.Context, domain.ManagedInstanceKey, string, string, *time.Time) (*domain.MaintenanceOverride, error)
	ClearMaintenanceOverride(context.Context, domain.ManagedInstanceKey, string) error
}

// InstanceHealthHandler serves tenant-scoped managed-instance health and maintenance APIs.
type InstanceHealthHandler struct {
	health   repository.ManagedInstanceHealthRepository
	operator InstanceMaintenanceOperator
	now      func() time.Time
}

func NewInstanceHealthHandler(health repository.ManagedInstanceHealthRepository, _ repository.ServiceRepository, _ repository.EnvironmentRepository, operator InstanceMaintenanceOperator) *InstanceHealthHandler {
	return &InstanceHealthHandler{health: health, operator: operator, now: func() time.Time { return time.Now().UTC() }}
}

type instanceHealthSummary struct {
	domain.ManagedInstanceHealth
	MaintenanceOverride *domain.MaintenanceOverride `json:"maintenance_override,omitempty"`
}

type instanceHealthDetail struct {
	Health              domain.ManagedInstanceHealth `json:"health"`
	MaintenanceOverride *domain.MaintenanceOverride  `json:"maintenance_override,omitempty"`
}

type setMaintenanceRequest struct {
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (h *InstanceHealthHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	serviceID, err := optionalUUIDQuery(r, "service_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service_id")
		return
	}
	environmentID, err := optionalUUIDQuery(r, "environment_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment_id")
		return
	}
	unhealthy := false
	if raw := strings.TrimSpace(r.URL.Query().Get("unhealthy")); raw != "" {
		unhealthy, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid unhealthy filter")
			return
		}
	}

	limit := instanceHealthListLimit(r)
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	items, err := h.health.ListHealth(r.Context(), repository.ManagedInstanceHealthListOptions{
		OrgID:         authzOrgID(r),
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		UnhealthyOnly: unhealthy,
		ActiveAt:      h.now(),
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summaries := make([]instanceHealthSummary, 0, len(items))
	for i := range items {
		sanitizeHealth(&items[i].Health)
		sanitizeOverride(items[i].MaintenanceOverride)
		summaries = append(summaries, instanceHealthSummary{
			ManagedInstanceHealth: items[i].Health,
			MaintenanceOverride:   items[i].MaintenanceOverride,
		})
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: summaries, Limit: limit, Offset: offset})
}

func (h *InstanceHealthHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	key, ok := instanceKeyFromRoute(w, r)
	if !ok {
		return
	}
	health, err := h.health.GetHealth(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if health == nil {
		writeError(w, http.StatusNotFound, "managed instance health not found")
		return
	}
	override, err := h.health.GetActiveMaintenanceOverride(r.Context(), key, h.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sanitizeHealth(health)
	sanitizeOverride(override)
	writeData(w, http.StatusOK, instanceHealthDetail{Health: *health, MaintenanceOverride: override})
}

func (h *InstanceHealthHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	key, ok := instanceKeyFromRoute(w, r)
	if !ok {
		return
	}
	events, err := h.health.ListRecentHealthEvents(r.Context(), key, historyLimit(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range events {
		events[i].Reason = domain.SanitizeEvidence(events[i].Reason)
		events[i].Evidence = domain.SanitizeEvidence(events[i].Evidence)
	}
	writeData(w, http.StatusOK, events)
}

func (h *InstanceHealthHandler) ListRecoveryAttempts(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	key, ok := instanceKeyFromRoute(w, r)
	if !ok {
		return
	}
	attempts, err := h.health.ListRecentRecoveryAttempts(r.Context(), key, historyLimit(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range attempts {
		attempts[i].Evidence = domain.SanitizeEvidence(attempts[i].Evidence)
	}
	writeData(w, http.StatusOK, attempts)
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

func optionalUUIDQuery(r *http.Request, name string) (uuid.UUID, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(value)
}

func instanceHealthListLimit(r *http.Request) int {
	limit := queryInt(r, "limit", defaultInstanceHealthListLimit)
	if limit < 1 {
		return 1
	}
	if limit > maxInstanceHealthListLimit {
		return maxInstanceHealthListLimit
	}
	return limit
}

func historyLimit(r *http.Request) int {
	limit := queryInt(r, "limit", defaultInstanceHealthHistoryLimit)
	if limit < 1 {
		return 1
	}
	if limit > maxInstanceHealthHistoryLimit {
		return maxInstanceHealthHistoryLimit
	}
	return limit
}

func sanitizeHealth(health *domain.ManagedInstanceHealth) {
	if health == nil {
		return
	}
	health.FailureReason = domain.SanitizeEvidence(health.FailureReason)
	if health.LastRecoveryAttempt != nil {
		health.LastRecoveryAttempt.Evidence = domain.SanitizeEvidence(health.LastRecoveryAttempt.Evidence)
	}
}

func sanitizeOverride(override *domain.MaintenanceOverride) {
	if override == nil {
		return
	}
	override.Actor = domain.SanitizeEvidence(override.Actor)
	override.Reason = domain.SanitizeEvidence(override.Reason)
}
