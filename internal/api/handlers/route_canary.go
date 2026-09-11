package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultRouteCanaryHistoryLimit = 50
	maxRouteCanaryHistoryLimit     = 500
)

// RouteCanaryReader is the read surface of the route canary store.
type RouteCanaryReader interface {
	ListState(ctx context.Context) ([]domain.RouteCanaryState, error)
	// ListStateByEnvironment backs detail lookups that omit deployment_unit_id,
	// which must resolve the route's deployment unit from stored state.
	ListStateByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.RouteCanaryState, error)
	GetState(ctx context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error)
	ListRecentEvents(ctx context.Context, key domain.RouteCanaryKey, limit int) ([]domain.RouteCanaryEvent, error)
}

// RouteInstanceHealthReader supplies the container-level status behind a route.
type RouteInstanceHealthReader interface {
	InstanceStatusForRoute(ctx context.Context, key domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool)
}

// RouteCanaryHandler serves managed-route outage state and failure lineage.
type RouteCanaryHandler struct {
	canaries RouteCanaryReader
	health   RouteInstanceHealthReader
}

// NewRouteCanaryHandler builds the route canary read handler.
func NewRouteCanaryHandler(canaries RouteCanaryReader, health RouteInstanceHealthReader) *RouteCanaryHandler {
	return &RouteCanaryHandler{canaries: canaries, health: health}
}

// routeCanarySummary pairs route outage state with the container-level status of
// the deployment unit behind it.
//
// The pairing is the point: it makes "the service is healthy but its route is
// broken" a single readable fact rather than something an operator has to infer
// by correlating two separate screens during an incident.
type routeCanarySummary struct {
	domain.RouteCanaryState
	ObservedInstanceStatus domain.InstanceHealthStatus `json:"observed_instance_status,omitempty"`
	// ServiceHealthyRouteBroken flags the specific contradiction that has no
	// other signal in the system.
	ServiceHealthyRouteBroken bool `json:"service_healthy_route_broken"`
}

// List returns managed-route canary state, optionally filtered.
func (h *RouteCanaryHandler) List(w http.ResponseWriter, r *http.Request) {
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
	openOnly := false
	if raw := strings.TrimSpace(r.URL.Query().Get("open")); raw != "" {
		parsed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid open filter")
			return
		}
		openOnly = parsed
	}

	states, err := h.canaries.ListState(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summaries := make([]routeCanarySummary, 0, len(states))
	for _, state := range states {
		if serviceID != uuid.Nil && state.ServiceID != serviceID {
			continue
		}
		if environmentID != uuid.Nil && state.EnvironmentID != environmentID {
			continue
		}
		if openOnly && !state.Open {
			continue
		}
		summaries = append(summaries, h.summarize(r.Context(), state))
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: summaries, Total: len(summaries), Limit: len(summaries), Offset: 0})
}

// Get returns state for one managed route, wrapped in the {data: ...} detail
// envelope every other single-resource endpoint uses.
func (h *RouteCanaryHandler) Get(w http.ResponseWriter, r *http.Request) {
	key, ok := h.routeKeyFromRequest(w, r)
	if !ok {
		return
	}
	route, ok := h.resolveRoute(w, r, key)
	if !ok {
		return
	}
	state := route.state
	if state == nil {
		var err error
		state, err = h.canaries.GetState(r.Context(), route.key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if state == nil {
			writeError(w, http.StatusNotFound, "route canary state not found")
			return
		}
	}
	writeData(w, http.StatusOK, h.summarize(r.Context(), *state))
}

// ListEvents returns append-only failure lineage for one managed route, newest
// first, so an operator can see how an outage developed and cleared.
//
// Lineage is a collection, so it uses the same {data, total, limit, offset}
// list envelope as List; data is always a JSON array.
func (h *RouteCanaryHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	key, ok := h.routeKeyFromRequest(w, r)
	if !ok {
		return
	}
	limit := defaultRouteCanaryHistoryLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > maxRouteCanaryHistoryLimit {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	route, ok := h.resolveRoute(w, r, key)
	if !ok {
		return
	}
	events, err := h.canaries.ListRecentEvents(r.Context(), route.key, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []domain.RouteCanaryEvent{}
	}
	writeJSON(w, http.StatusOK, dto.ListResponse{Data: events, Total: len(events), Limit: limit, Offset: 0})
}

// resolvedRoute is the managed route a detail request names. state is set when
// resolution already loaded it.
type resolvedRoute struct {
	key   domain.RouteCanaryKey
	state *domain.RouteCanaryState
}

// resolveRoute resolves the managed route a detail request names.
//
// Every valid route plan carries a deployment unit, so every stored route is
// keyed by one. An explicit deployment_unit_id addresses that key directly.
// Without it, the route is resolved from stored state for (service,
// environment, hostname): a unique match is used, no match is 404, and more
// than one match is 409 naming the candidate units, because silently picking
// one would show an operator the wrong route's outage.
func (h *RouteCanaryHandler) resolveRoute(w http.ResponseWriter, r *http.Request, key domain.RouteCanaryKey) (resolvedRoute, bool) {
	if key.DeploymentUnitID != nil {
		return resolvedRoute{key: key}, true
	}
	states, err := h.canaries.ListStateByEnvironment(r.Context(), key.EnvironmentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return resolvedRoute{}, false
	}
	var matches []domain.RouteCanaryState
	for _, state := range states {
		if state.ServiceID == key.ServiceID && state.EnvironmentID == key.EnvironmentID &&
			domain.NormalizeRouteCanaryHostname(state.Hostname) == key.Hostname {
			matches = append(matches, state)
		}
	}
	switch len(matches) {
	case 0:
		writeError(w, http.StatusNotFound, "route canary state not found")
		return resolvedRoute{}, false
	case 1:
		state := matches[0]
		return resolvedRoute{key: state.RouteCanaryKey, state: &state}, true
	default:
		units := make([]string, 0, len(matches))
		for _, state := range matches {
			if state.DeploymentUnitID == nil {
				units = append(units, "none")
				continue
			}
			units = append(units, state.DeploymentUnitID.String())
		}
		sort.Strings(units)
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"route %s matches %d deployment units; pass deployment_unit_id (one of: %s)",
			key.Hostname, len(matches), strings.Join(units, ", ")))
		return resolvedRoute{}, false
	}
}

// routeKeyFromRequest builds the route key from path and query parameters.
func (h *RouteCanaryHandler) routeKeyFromRequest(w http.ResponseWriter, r *http.Request) (domain.RouteCanaryKey, bool) {
	if !requireMember(w, r) {
		return domain.RouteCanaryKey{}, false
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return domain.RouteCanaryKey{}, false
	}
	environmentID, err := uuid.Parse(chi.URLParam(r, "envId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment id")
		return domain.RouteCanaryKey{}, false
	}
	hostname := domain.NormalizeRouteCanaryHostname(chi.URLParam(r, "hostname"))
	if hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname is required")
		return domain.RouteCanaryKey{}, false
	}
	key := domain.RouteCanaryKey{ServiceID: serviceID, EnvironmentID: environmentID, Hostname: hostname}
	if raw := strings.TrimSpace(r.URL.Query().Get("deployment_unit_id")); raw != "" {
		unitID, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid deployment_unit_id")
			return domain.RouteCanaryKey{}, false
		}
		key.DeploymentUnitID = &unitID
	}
	return key, true
}

func (h *RouteCanaryHandler) summarize(ctx context.Context, state domain.RouteCanaryState) routeCanarySummary {
	summary := routeCanarySummary{RouteCanaryState: state}
	if h.health == nil {
		return summary
	}
	status, ok := h.health.InstanceStatusForRoute(ctx, state.RouteCanaryKey)
	if !ok {
		return summary
	}
	summary.ObservedInstanceStatus = status
	summary.ServiceHealthyRouteBroken = state.Open &&
		(status == domain.InstanceHealthStatusHealthy || status == domain.InstanceHealthStatusRunning)
	return summary
}
