package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/domain"
)

type PublicRouteZone struct {
	Name          string
	BackendRef    string
	AllowedOrgIDs []uuid.UUID
	Protected     bool
	TTL           int
}

type PublicRouteOrigin struct {
	DeploymentUnitID uuid.UUID
	Host             string
	AllowedPorts     []int
}

type PublicRoutePlannerConfig struct {
	Provider   string
	TunnelRef  string
	DNSTarget  string
	Zones      []PublicRouteZone
	Origins    []PublicRouteOrigin
	ConfigHash string
}

type PublicRoutePlanner struct {
	cfg      PublicRoutePlannerConfig
	backends routing.Resolver
}

func NewPublicRoutePlanner(cfg PublicRoutePlannerConfig, backends routing.Resolver) (*PublicRoutePlanner, error) {
	if cfg.Provider == "" || cfg.TunnelRef == "" || cfg.DNSTarget == "" || cfg.ConfigHash == "" || backends == nil {
		return nil, fmt.Errorf("public routing provider, tunnel, DNS target, config hash, and backend resolver are required")
	}
	if len(cfg.Zones) == 0 || len(cfg.Origins) == 0 {
		return nil, fmt.Errorf("public routing zones and origins are required")
	}
	return &PublicRoutePlanner{cfg: cfg, backends: backends}, nil
}

func (p *PublicRoutePlanner) Plan(ctx context.Context, svc *domain.Service, env *domain.Environment, desired *domain.DesiredServiceSpec, request domain.PublicRouteRequest) (*domain.DesiredPublicRoutePlan, bool, error) {
	if p == nil {
		return nil, false, fmt.Errorf("public route provisioning is not configured")
	}
	if svc == nil || env == nil || desired == nil || desired.DeploymentUnitID == nil {
		return nil, false, fmt.Errorf("service, environment, and explicit deployment unit are required for public routing")
	}
	req, err := domain.NormalizePublicRouteRequest(request)
	if err != nil {
		return nil, false, err
	}
	zone, ok := p.resolveZone(req.Hostname)
	if !ok {
		return nil, false, fmt.Errorf("hostname %s is outside Bahia-managed public zones", req.Hostname)
	}
	if !uuidAllowed(svc.OrgID, zone.AllowedOrgIDs) {
		return nil, false, fmt.Errorf("organization does not own public zone %s", zone.Name)
	}
	if zone.Protected && !env.Protected {
		return nil, false, fmt.Errorf("protected zone %s requires a protected environment", zone.Name)
	}
	origin, ok := p.resolveOrigin(*desired.DeploymentUnitID)
	if !ok {
		return nil, false, fmt.Errorf("deployment unit %s has no configured public-route origin", desired.DeploymentUnitID)
	}
	if !intAllowed(req.UpstreamPort, origin.AllowedPorts) {
		return nil, false, fmt.Errorf("upstream port %d is not allowed for the deployment unit origin", req.UpstreamPort)
	}
	if !desiredExposesPort(desired, req.UpstreamPort) {
		return nil, false, fmt.Errorf("upstream port %d is not exposed by the signed managed runtime configuration", req.UpstreamPort)
	}
	originURL := fmt.Sprintf("%s://%s", req.UpstreamScheme, net.JoinHostPort(strings.TrimSpace(origin.Host), strconv.Itoa(req.UpstreamPort)))
	if _, err := url.ParseRequestURI(originURL); err != nil {
		return nil, false, fmt.Errorf("configured route origin is invalid: %w", err)
	}
	coordinate := domain.PublicRouteCoordinate(svc.ID, env.ID, *desired.DeploymentUnitID)
	plan := &domain.DesiredPublicRoutePlan{
		SchemaVersion: domain.PublicRouteSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: *desired.DeploymentUnitID,
		Hostname: req.Hostname, Zone: zone.Name, BackendRef: zone.BackendRef, Provider: p.cfg.Provider, ProviderConfigHash: p.cfg.ConfigHash,
		DNS:        domain.DesiredPublicRouteDNS{Type: "CNAME", Name: req.Hostname, Value: p.cfg.DNSTarget, TTL: zone.TTL, Proxied: true, SourceCoordinate: coordinate},
		Tunnel:     domain.DesiredPublicRouteTunnel{TunnelRef: p.cfg.TunnelRef, Hostname: req.Hostname, OriginURL: originURL},
		Proxy:      domain.DesiredPublicRouteProxy{HostMatch: req.Hostname, UpstreamScheme: req.UpstreamScheme, UpstreamHost: origin.Host, UpstreamPort: req.UpstreamPort, HealthPath: req.HealthPath},
		TLS:        domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"},
		Operations: []domain.DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply_and_verify", Summary: "apply the signed runtime state and verify container health"}, {Order: 2, Resource: "tunnel_proxy", Action: "upsert", Summary: "route " + req.Hostname + " to " + originURL}, {Order: 3, Resource: "dns", Action: "upsert", Summary: "publish proxied CNAME " + req.Hostname + " -> " + p.cfg.DNSTarget}, {Order: 4, Resource: "https", Action: "verify", Summary: "verify managed TLS and GET " + req.HealthPath}},
		Rollback:   []domain.DesiredPublicRouteChange{{Order: 1, Resource: "dns", Action: "restore_or_withdraw", Summary: "restore the prior DNS record or withdraw the new hostname"}, {Order: 2, Resource: "tunnel_proxy", Action: "restore", Summary: "restore the prior remote tunnel ingress configuration"}, {Order: 3, Resource: "application", Action: "restore", Summary: "restore and observe the prior desired runtime state"}},
	}
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return nil, false, err
	}
	backend, ok := p.backends.Resolve(plan.BackendRef)
	if !ok {
		return nil, false, fmt.Errorf("public route backend %q is not configured", plan.BackendRef)
	}
	if err := backend.Check(ctx, plan); err != nil {
		return nil, false, err
	}
	return plan, zone.Protected, nil
}

func (p *PublicRoutePlanner) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if p == nil || plan == nil {
		return fmt.Errorf("public route plan is required")
	}
	if plan.ProviderConfigHash != p.cfg.ConfigHash {
		return fmt.Errorf("public route provider configuration changed after review")
	}
	backend, ok := p.backends.Resolve(plan.BackendRef)
	if !ok {
		return fmt.Errorf("public route backend %q is not configured", plan.BackendRef)
	}
	return backend.Apply(ctx, plan)
}

func (p *PublicRoutePlanner) resolveZone(host string) (PublicRouteZone, bool) {
	zones := append([]PublicRouteZone(nil), p.cfg.Zones...)
	sort.Slice(zones, func(i, j int) bool { return len(zones[i].Name) > len(zones[j].Name) })
	for _, zone := range zones {
		if domain.HostnameWithinZone(host, zone.Name) {
			return zone, true
		}
	}
	return PublicRouteZone{}, false
}

func (p *PublicRoutePlanner) resolveOrigin(id uuid.UUID) (PublicRouteOrigin, bool) {
	for _, o := range p.cfg.Origins {
		if o.DeploymentUnitID == id {
			return o, true
		}
	}
	return PublicRouteOrigin{}, false
}
func uuidAllowed(id uuid.UUID, ids []uuid.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
func intAllowed(port int, ports []int) bool {
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

func desiredExposesPort(desired *domain.DesiredServiceSpec, target int) bool {
	if desired.Healthcheck != nil && desired.Healthcheck.Port == target {
		return true
	}
	for _, raw := range desired.Ports {
		raw = strings.TrimSpace(strings.SplitN(raw, "/", 2)[0])
		parts := strings.Split(raw, ":")
		candidate := parts[len(parts)-1]
		if len(parts) >= 2 && net.ParseIP(strings.Trim(parts[0], "[]")) != nil {
			candidate = parts[len(parts)-1]
		}
		port, err := strconv.Atoi(candidate)
		if err == nil && port == target {
			return true
		}
	}
	return false
}
