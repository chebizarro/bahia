package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/domain"
)

type PublicRouteZone struct {
	Name          string
	BackendRef    string
	Provider      string
	ConfigHash    string
	TunnelRef     string
	DNSTarget     string
	CaddyServerID string
	AllowedOrgIDs []uuid.UUID
	Protected     bool
	TTL           int
}

type PublicRouteOrigin struct {
	DeploymentUnitID uuid.UUID
	Host             string
	AllowedPorts     []int
}

type InternalHTTPSPlannerConfig struct {
	Provider   string
	Listen     string
	CertFile   string
	KeyFile    string
	ConfigHash string
	Zones      []string
}

type PublicRoutePlannerConfig struct {
	Provider      string
	TunnelRef     string
	DNSTarget     string
	CaddyServerID string
	Zones         []PublicRouteZone
	Origins       []PublicRouteOrigin
	ConfigHash    string
	InternalHTTPS *InternalHTTPSPlannerConfig
}

type PublicRoutePlanOptions struct {
	// Internal is tri-state: nil or true selects automatic internal HTTPS when
	// configured for the hostname; false explicitly opts out.
	Internal *bool
}

type PublicRoutePlanner struct {
	effectMu       sync.Mutex
	cfg            PublicRoutePlannerConfig
	backends       routing.Resolver
	claimChecker   func(context.Context, *domain.DesiredPublicRoutePlan) error
	releaseChecker func(context.Context, *domain.DesiredPublicRoutePlan) error
}

func NewPublicRoutePlanner(cfg PublicRoutePlannerConfig, backends routing.Resolver) (*PublicRoutePlanner, error) {
	if (cfg.Provider != "cloudflare_tunnel" && cfg.Provider != "caddy") || cfg.ConfigHash == "" || backends == nil {
		return nil, fmt.Errorf("known public routing provider, config hash, and backend resolver are required")
	}
	if cfg.Provider == "cloudflare_tunnel" && (cfg.TunnelRef == "" || cfg.DNSTarget == "") {
		return nil, fmt.Errorf("cloudflare tunnel and DNS target are required")
	}
	if cfg.Provider == "caddy" && cfg.CaddyServerID == "" {
		return nil, fmt.Errorf("caddy server ID is required")
	}
	if len(cfg.Zones) == 0 || len(cfg.Origins) == 0 {
		return nil, fmt.Errorf("public routing zones and origins are required")
	}
	if internal := cfg.InternalHTTPS; internal != nil {
		if internal.Provider != "nginx" || internal.Listen != "443 ssl" || !filepath.IsAbs(internal.CertFile) || !filepath.IsAbs(internal.KeyFile) || strings.TrimSpace(internal.ConfigHash) == "" || len(internal.Zones) == 0 {
			return nil, fmt.Errorf("internal HTTPS routing requires nginx, listen 443 ssl, absolute certificate paths, config hash, and zones")
		}
		for i, zone := range internal.Zones {
			normalized, err := domain.NormalizePublicHostname(zone)
			if err != nil {
				return nil, fmt.Errorf("internal HTTPS zone %q: %w", zone, err)
			}
			internal.Zones[i] = normalized
		}
	}
	return &PublicRoutePlanner{cfg: cfg, backends: backends}, nil
}

func (p *PublicRoutePlanner) Plan(ctx context.Context, svc *domain.Service, env *domain.Environment, desired *domain.DesiredServiceSpec, request domain.PublicRouteRequest) (*domain.DesiredPublicRoutePlan, bool, error) {
	return p.PlanWithOptions(ctx, svc, env, desired, request, PublicRoutePlanOptions{})
}

func (p *PublicRoutePlanner) PlanWithOptions(ctx context.Context, svc *domain.Service, env *domain.Environment, desired *domain.DesiredServiceSpec, request domain.PublicRouteRequest, options PublicRoutePlanOptions) (*domain.DesiredPublicRoutePlan, bool, error) {
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
	originHost := strings.Trim(strings.TrimSpace(origin.Host), "[]")
	canonicalOrigin := url.URL{Scheme: req.UpstreamScheme, Host: net.JoinHostPort(originHost, strconv.Itoa(req.UpstreamPort))}
	originURL := canonicalOrigin.String()
	if _, err := url.ParseRequestURI(originURL); err != nil {
		return nil, false, fmt.Errorf("configured route origin is invalid: %w", err)
	}
	coordinate := domain.PublicRouteCoordinate(svc.ID, env.ID, *desired.DeploymentUnitID)
	provider, configHash, tunnelRef, dnsTarget, caddyServerID := p.zoneProvider(zone)
	plan := &domain.DesiredPublicRoutePlan{
		SchemaVersion: domain.PublicRouteSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: *desired.DeploymentUnitID,
		Hostname: req.Hostname, Zone: zone.Name, BackendRef: zone.BackendRef, Provider: provider, ProviderConfigHash: configHash,
		Proxy: domain.DesiredPublicRouteProxy{HostMatch: req.Hostname, UpstreamScheme: req.UpstreamScheme, UpstreamHost: originHost, UpstreamPort: req.UpstreamPort, HealthPath: req.HealthPath},
	}
	if provider == "cloudflare_tunnel" {
		plan.DNS = domain.DesiredPublicRouteDNS{Type: "CNAME", Name: req.Hostname, Value: dnsTarget, TTL: zone.TTL, Proxied: true, SourceCoordinate: coordinate}
		plan.Tunnel = domain.DesiredPublicRouteTunnel{TunnelRef: tunnelRef, Hostname: req.Hostname, OriginURL: originURL}
		plan.TLS = domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"}
		plan.Operations = []domain.DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply_and_verify", Summary: "apply the signed runtime state and verify container health"}, {Order: 2, Resource: "tunnel_proxy", Action: "upsert", Summary: "route " + req.Hostname + " to " + originURL}, {Order: 3, Resource: "dns", Action: "upsert", Summary: "publish proxied CNAME " + req.Hostname + " -> " + dnsTarget}, {Order: 4, Resource: "https", Action: "verify", Summary: "verify managed TLS and GET " + req.HealthPath}}
		plan.Rollback = []domain.DesiredPublicRouteChange{{Order: 1, Resource: "dns", Action: "restore_or_withdraw", Summary: "restore the prior DNS record or withdraw the new hostname"}, {Order: 2, Resource: "tunnel_proxy", Action: "restore", Summary: "restore the prior remote tunnel ingress configuration"}, {Order: 3, Resource: "application", Action: "restore", Summary: "restore and observe the prior desired runtime state"}}
	} else {
		plan.Caddy = &domain.DesiredPublicRouteCaddy{ServerID: caddyServerID, RouteID: domain.PublicRouteCaddyID(svc.ID, env.ID, *desired.DeploymentUnitID)}
		plan.TLS = domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "caddy"}
		plan.Operations = []domain.DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply_and_verify", Summary: "apply the signed runtime state and verify container health"}, {Order: 2, Resource: "caddy_route", Action: "upsert", Summary: "route " + req.Hostname + " to " + originURL}, {Order: 3, Resource: "caddy_tls", Action: "verify", Summary: "verify managed TLS for " + req.Hostname}}
		plan.Rollback = []domain.DesiredPublicRouteChange{{Order: 1, Resource: "caddy_route", Action: "restore_or_withdraw", Summary: "restore the prior owned Caddy route"}, {Order: 2, Resource: "application", Action: "restore", Summary: "restore and observe the prior desired runtime state"}}
	}
	if p.internalHTTPSAllowed(req.Hostname, options.Internal) {
		internal := p.cfg.InternalHTTPS
		plan.InternalHTTPS = &domain.DesiredInternalHTTPSPlan{
			SchemaVersion: domain.InternalHTTPSPlanSchemaVersion,
			Hostname:      req.Hostname,
			Listen:        internal.Listen,
			UpstreamURL:   originURL,
			CertFile:      internal.CertFile,
			KeyFile:       internal.KeyFile,
			ConfigHash:    internal.ConfigHash,
			Apply:         []domain.DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "upsert_and_reload", Summary: "serve internal HTTPS for " + req.Hostname + " and proxy to " + originURL}},
			Rollback:      []domain.DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "restore_or_remove_and_reload", Summary: "restore the prior Bahia-owned nginx vhost or remove the newly created file"}},
		}
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

// SetClaimChecker binds provider effects to service-signed hostname ownership.
func (p *PublicRoutePlanner) SetClaimChecker(check func(context.Context, *domain.DesiredPublicRoutePlan) error) {
	p.claimChecker = check
}

// SetReleaseChecker binds explicit desired-route removal to a canonical tombstone.
func (p *PublicRoutePlanner) SetReleaseChecker(check func(context.Context, *domain.DesiredPublicRoutePlan) error) {
	p.releaseChecker = check
}

// RequireReleased refuses desired-state removal until the signed hostname release completes.
func (p *PublicRoutePlanner) RequireReleased(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if p.releaseChecker == nil {
		return fmt.Errorf("canonical hostname release checker is not configured")
	}
	return p.releaseChecker(ctx, plan)
}

// RequireClaim checks the canonical reservation before accepting a desired route.
func (p *PublicRoutePlanner) RequireClaim(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	return p.requireClaim(ctx, plan)
}

func (p *PublicRoutePlanner) requireClaim(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if p.claimChecker == nil {
		return fmt.Errorf("canonical hostname claim checker is not configured")
	}
	return p.claimChecker(ctx, plan)
}

// CommitClaim keeps the provider check and canonical claim publication atomic
// with respect to route apply, compensation, and withdrawal in this writer.
func (p *PublicRoutePlanner) CommitClaim(ctx context.Context, plan *domain.DesiredPublicRoutePlan, orgID uuid.UUID, environmentProtected bool, publish func(context.Context) error) error {
	if publish == nil {
		return fmt.Errorf("canonical claim publisher is required")
	}
	p.effectMu.Lock()
	defer p.effectMu.Unlock()
	if err := p.CheckClaim(ctx, plan, orgID, environmentProtected); err != nil {
		return fmt.Errorf("provider collision check: %w", err)
	}
	return publish(ctx)
}

// CheckClaim validates zone ownership and the configured origin before reading
// provider state. A signed intent cannot reserve another organization's zone.
func (p *PublicRoutePlanner) CheckClaim(ctx context.Context, plan *domain.DesiredPublicRoutePlan, orgID uuid.UUID, environmentProtected bool) error {
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	zone, ok := p.resolveZone(plan.Hostname)
	if !ok || zone.Name != plan.Zone || zone.BackendRef != plan.BackendRef || (plan.Provider == "cloudflare_tunnel" && zone.TTL != plan.DNS.TTL) || !uuidAllowed(orgID, zone.AllowedOrgIDs) || (zone.Protected && !environmentProtected) {
		return fmt.Errorf("hostname zone is not configured for organization")
	}
	origin, ok := p.resolveOrigin(plan.DeploymentUnitID)
	provider, _, tunnelRef, dnsTarget, caddyServerID := p.zoneProvider(zone)
	if !ok || !intAllowed(plan.Proxy.UpstreamPort, origin.AllowedPorts) || strings.Trim(strings.TrimSpace(origin.Host), "[]") != plan.Proxy.UpstreamHost || plan.Provider != provider || (provider == "cloudflare_tunnel" && (plan.DNS.Value != dnsTarget || plan.Tunnel.TunnelRef != tunnelRef)) || (provider == "caddy" && (plan.Caddy == nil || plan.Caddy.ServerID != caddyServerID)) {
		return fmt.Errorf("hostname route does not match configured origin or provider")
	}
	return p.Check(ctx, plan)
}

// Check revalidates the signed plan and reads the live provider before a claim.
func (p *PublicRoutePlanner) Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	backend, err := p.resolveValidatedBackend(plan)
	if err != nil {
		return err
	}
	return backend.Check(ctx, plan)
}

// Withdraw removes provider state only while the canonical claim still holds.
func (p *PublicRoutePlanner) Withdraw(ctx context.Context, plan *domain.DesiredPublicRoutePlan, phase func(context.Context, string) error, finalize func(context.Context) error) error {
	p.effectMu.Lock()
	defer p.effectMu.Unlock()
	if err := p.requireClaim(ctx, plan); err != nil {
		return err
	}
	backend, err := p.resolveValidatedBackend(plan)
	if err != nil {
		return err
	}
	withdrawing, ok := backend.(routing.WithdrawingBackend)
	if !ok {
		return fmt.Errorf("public route backend %q cannot withdraw", plan.BackendRef)
	}
	if err := withdrawing.Withdraw(ctx, plan, phase); err != nil {
		return err
	}
	if finalize != nil {
		return finalize(ctx)
	}
	return nil
}

func (p *PublicRoutePlanner) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	p.effectMu.Lock()
	defer p.effectMu.Unlock()
	backend, err := p.resolveValidatedBackend(plan)
	if err != nil {
		return err
	}
	if err := p.requireClaim(ctx, plan); err != nil {
		return err
	}
	return backend.Apply(ctx, plan)
}

// ApplyWithCompensation applies the plan and returns the inverse of a successful
// apply, so an orchestrator can withdraw a route that converged at the providers
// but failed a post-deploy canary gate.
//
// It fails loudly when the resolved backend cannot produce an inverse. Silently
// applying a route that cannot be rolled back would let a gate detect an outage
// it has no way to undo.
func (p *PublicRoutePlanner) ApplyWithCompensation(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (routing.Compensation, error) {
	p.effectMu.Lock()
	defer p.effectMu.Unlock()
	backend, err := p.resolveValidatedBackend(plan)
	if err != nil {
		return nil, err
	}
	if err := p.requireClaim(ctx, plan); err != nil {
		return nil, err
	}
	compensating, ok := backend.(routing.CompensatingBackend)
	if !ok {
		return nil, fmt.Errorf("public route backend %q does not support rollback", plan.BackendRef)
	}
	compensation, err := compensating.ApplyWithCompensation(ctx, plan)
	if err != nil {
		return nil, err
	}
	if compensation == nil {
		return nil, fmt.Errorf("public route backend %q returned no compensation", plan.BackendRef)
	}
	return func(compensationCtx context.Context) error {
		p.effectMu.Lock()
		defer p.effectMu.Unlock()
		if err := p.requireClaim(compensationCtx, plan); err != nil {
			return err
		}
		return compensation(compensationCtx)
	}, nil
}

// resolveValidatedBackend re-validates that the reviewed plan still matches live
// provider configuration, then resolves its backend.
func (p *PublicRoutePlanner) resolveValidatedBackend(plan *domain.DesiredPublicRoutePlan) (routing.Backend, error) {
	if p == nil || plan == nil {
		return nil, fmt.Errorf("public route plan is required")
	}
	zone, ok := p.resolveZone(plan.Hostname)
	if !ok || zone.Name != plan.Zone || zone.BackendRef != plan.BackendRef {
		return nil, fmt.Errorf("public route zone or backend changed after review")
	}
	provider, hash, _, _, _ := p.zoneProvider(zone)
	if plan.Provider != provider || plan.ProviderConfigHash != hash {
		return nil, fmt.Errorf("public route provider configuration changed after review")
	}
	if plan.InternalHTTPS != nil && (p.cfg.InternalHTTPS == nil || plan.InternalHTTPS.ConfigHash != p.cfg.InternalHTTPS.ConfigHash) {
		return nil, fmt.Errorf("internal routing configuration changed after review")
	}
	backend, ok := p.backends.Resolve(plan.BackendRef)
	if !ok {
		return nil, fmt.Errorf("public route backend %q is not configured", plan.BackendRef)
	}
	return backend, nil
}

func (p *PublicRoutePlanner) zoneProvider(zone PublicRouteZone) (provider, hash, tunnelRef, dnsTarget, caddyServerID string) {
	provider, hash, tunnelRef, dnsTarget, caddyServerID = zone.Provider, zone.ConfigHash, zone.TunnelRef, zone.DNSTarget, zone.CaddyServerID
	if provider == "" {
		provider = p.cfg.Provider
	}
	if hash == "" {
		hash = p.cfg.ConfigHash
	}
	if tunnelRef == "" {
		tunnelRef = p.cfg.TunnelRef
	}
	if dnsTarget == "" {
		dnsTarget = p.cfg.DNSTarget
	}
	if caddyServerID == "" {
		caddyServerID = p.cfg.CaddyServerID
	}
	return
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

func (p *PublicRoutePlanner) internalHTTPSAllowed(hostname string, requested *bool) bool {
	if (requested != nil && !*requested) || p.cfg.InternalHTTPS == nil {
		return false
	}
	for _, zone := range p.cfg.InternalHTTPS.Zones {
		if domain.HostnameWithinZone(hostname, zone) {
			return true
		}
	}
	return false
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
	for _, raw := range desired.Ports {
		raw = strings.TrimSpace(strings.SplitN(raw, "/", 2)[0])
		parts := strings.Split(raw, ":")
		var published string
		switch len(parts) {
		case 2: // host:container
			published = parts[0]
		case 3: // ip:host:container
			published = parts[1]
		default:
			// A bare container port has no host-side listener for the edge origin.
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(published))
		if err == nil && port == target {
			return true
		}
	}
	return false
}
