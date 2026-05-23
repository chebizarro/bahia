package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
)

// DNSCatalogProvider exposes the projected DNS endpoint read model used by catalog endpoints.
type DNSCatalogProvider interface {
	ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
}

// DNSCatalogHandler serves DNS service catalog read endpoints.
type DNSCatalogHandler struct {
	provider DNSCatalogProvider
}

// NewDNSCatalogHandler creates a DNS catalog HTTP handler.
func NewDNSCatalogHandler(provider DNSCatalogProvider) *DNSCatalogHandler {
	return &DNSCatalogHandler{provider: provider}
}

// ListCatalog returns DNS endpoints filtered by catalog attributes with offset/limit pagination.
func (h *DNSCatalogHandler) ListCatalog(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if !h.requireProvider(w) {
		return
	}

	endpoints, err := h.provider.ListDNSEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filtered := filterDNSEndpoints(endpoints, r)
	limit, offset := pagination(r)
	page := pageDNSEndpoints(filtered, limit, offset)
	writeData(w, http.StatusOK, dto.ListResponse{Data: page, Total: len(filtered), Limit: limit, Offset: offset})
}

// GetCatalogEndpoint returns one DNS endpoint by FQDN.
func (h *DNSCatalogHandler) GetCatalogEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if !h.requireProvider(w) {
		return
	}

	fqdn := strings.TrimSpace(chi.URLParam(r, "fqdn"))
	if fqdn == "" {
		writeError(w, http.StatusBadRequest, "fqdn is required")
		return
	}
	endpoints, err := h.provider.ListDNSEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, endpoint := range endpoints {
		if endpoint.Health == domain.HealthStatusHealthy && strings.EqualFold(endpoint.FQDN, fqdn) {
			writeData(w, http.StatusOK, endpoint)
			return
		}
	}
	writeError(w, http.StatusNotFound, "DNS endpoint not found")
}

// ListZones returns configured DNS zones inferred from projected endpoint state.
func (h *DNSCatalogHandler) ListZones(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if !h.requireProvider(w) {
		return
	}

	endpoints, err := h.provider.ListDNSEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zones := dnsZonesFromEndpoints(endpoints)
	writeData(w, http.StatusOK, zones)
}

// ListDrift returns recent DNS endpoints whose projected DNS state is not in sync.
func (h *DNSCatalogHandler) ListDrift(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	if !h.requireProvider(w) {
		return
	}

	endpoints, err := h.provider.ListDNSEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	drifted := make([]domain.DNSEndpoint, 0)
	for _, endpoint := range endpoints {
		if endpoint.DriftStatus != "" && endpoint.DriftStatus != domain.DriftStatusInSync {
			drifted = append(drifted, endpoint)
		}
	}
	sort.SliceStable(drifted, func(i, j int) bool {
		return drifted[i].MaterializedAt.After(drifted[j].MaterializedAt)
	})
	limit, offset := pagination(r)
	page := pageDNSEndpoints(drifted, limit, offset)
	writeData(w, http.StatusOK, dto.ListResponse{Data: page, Total: len(drifted), Limit: limit, Offset: offset})
}

func (h *DNSCatalogHandler) requireProvider(w http.ResponseWriter) bool {
	if h == nil || h.provider == nil {
		writeError(w, http.StatusServiceUnavailable, "DNS catalog provider unavailable")
		return false
	}
	return true
}

type dnsZoneStatus struct {
	Name          string              `json:"name"`
	Health        domain.HealthStatus `json:"health"`
	EndpointCount int                 `json:"endpoint_count"`
}

func dnsZonesFromEndpoints(endpoints []domain.DNSEndpoint) []dnsZoneStatus {
	byName := map[string]*dnsZoneStatus{}
	for _, endpoint := range endpoints {
		zoneName := strings.TrimSpace(endpoint.Zone)
		if zoneName == "" {
			continue
		}
		zone := byName[zoneName]
		if zone == nil {
			zone = &dnsZoneStatus{Name: zoneName, Health: domain.HealthStatusHealthy}
			byName[zoneName] = zone
		}
		zone.EndpointCount++
		zone.Health = worseDNSHealth(zone.Health, endpoint.Health)
	}
	zones := make([]dnsZoneStatus, 0, len(byName))
	for _, zone := range byName {
		zones = append(zones, *zone)
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	return zones
}

func worseDNSHealth(current, candidate domain.HealthStatus) domain.HealthStatus {
	if dnsHealthRank(candidate) > dnsHealthRank(current) {
		return candidate
	}
	return current
}

func dnsHealthRank(status domain.HealthStatus) int {
	switch status {
	case domain.HealthStatusUnhealthy:
		return 5
	case domain.HealthStatusStopped:
		return 4
	case domain.HealthStatusStarting:
		return 3
	case domain.HealthStatusUnknown, "":
		return 2
	case domain.HealthStatusHealthy:
		return 1
	default:
		return 2
	}
}

func filterDNSEndpoints(endpoints []domain.DNSEndpoint, r *http.Request) []domain.DNSEndpoint {
	query := r.URL.Query()
	health := strings.TrimSpace(query.Get("health"))
	if health == "" {
		health = string(domain.HealthStatusHealthy)
	}
	filtered := make([]domain.DNSEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if !matchesDNSFilter(string(endpoint.Health), health) {
			continue
		}
		if !matchesDNSFilter(endpoint.Zone, query.Get("zone")) {
			continue
		}
		if !matchesDNSFilter(endpoint.Environment, query.Get("environment")) {
			continue
		}
		if !matchesDNSFilter(endpoint.Runtime, query.Get("runtime")) {
			continue
		}
		if !matchesDNSFilter(endpoint.Hardware, query.Get("hardware")) {
			continue
		}
		if capability := strings.TrimSpace(query.Get("capability")); capability != "" && !hasDNSCapability(endpoint.Capabilities, capability) {
			continue
		}
		filtered = append(filtered, endpoint)
	}
	return filtered
}

func matchesDNSFilter(value, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(value), filter)
}

func hasDNSCapability(capabilities []string, capability string) bool {
	for _, value := range capabilities {
		if strings.EqualFold(strings.TrimSpace(value), capability) {
			return true
		}
	}
	return false
}

func pagination(r *http.Request) (int, int) {
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func pageDNSEndpoints(endpoints []domain.DNSEndpoint, limit, offset int) []domain.DNSEndpoint {
	if offset >= len(endpoints) {
		return []domain.DNSEndpoint{}
	}
	end := offset + limit
	if limit == 0 || end > len(endpoints) {
		end = len(endpoints)
	}
	page := endpoints[offset:end]
	if page == nil {
		return []domain.DNSEndpoint{}
	}
	return page
}
