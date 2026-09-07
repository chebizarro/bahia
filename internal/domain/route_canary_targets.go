package domain

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RouteCanaryPolicy is the control-plane configuration that turns a signed route
// plan into concrete checks. It is deliberately not part of the signed desired
// state: probe expectations are operational policy, so changing them must not
// invalidate a deployed plan hash.
type RouteCanaryPolicy struct {
	// Enabled turns route canary derivation on. When false no targets are
	// derived and neither the periodic supervisor nor the post-deploy gate runs.
	Enabled bool
	// ProbeTimeout bounds a single probe.
	ProbeTimeout time.Duration
	// ExpectedStatusMin and ExpectedStatusMax bound an acceptable response.
	ExpectedStatusMin int
	ExpectedStatusMax int
	// ExpectedBodyContains, when set, must appear in the bounded response body.
	ExpectedBodyContains string
	// TLSMinDaysRemaining raises the tls_expiring warning below this many days.
	// Zero disables expiry warning while still requiring a verifiable chain.
	TLSMinDaysRemaining int
	// PublicResolverAddr is the DNS server used for the public_edge perspective,
	// so an edge check cannot be satisfied by split-horizon LAN DNS. Empty means
	// the system resolver.
	PublicResolverAddr string
	// InternalDialAddresses maps a DNS zone to the internal address that serves
	// its hostnames on the LAN. A hostname in a mapped zone gets an internal_lan
	// target that dials this address while keeping the canonical hostname as TLS
	// server name and Host header, which is exactly what split DNS does.
	InternalDialAddresses map[string]string
	// Thresholds is the open/close hysteresis policy.
	Thresholds RouteCanaryThresholds
}

// Validate enforces that an enabled policy is fully specified. A policy that is
// parsed but cannot produce a usable target is a configuration error, not a
// silent no-op.
func (p RouteCanaryPolicy) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.ProbeTimeout <= 0 {
		return fmt.Errorf("route canary policy: probe_timeout must be positive")
	}
	if p.ExpectedStatusMin < 100 || p.ExpectedStatusMax > 599 || p.ExpectedStatusMin > p.ExpectedStatusMax {
		return fmt.Errorf("route canary policy: invalid expected status range %d..%d", p.ExpectedStatusMin, p.ExpectedStatusMax)
	}
	if p.TLSMinDaysRemaining < 0 {
		return fmt.Errorf("route canary policy: tls_min_days_remaining must not be negative")
	}
	if p.PublicResolverAddr != "" && !strings.EqualFold(p.PublicResolverAddr, "system") {
		if _, _, err := net.SplitHostPort(p.PublicResolverAddr); err != nil {
			return fmt.Errorf("route canary policy: public_resolver_addr must be host:port or \"system\": %w", err)
		}
	}
	for zone, address := range p.InternalDialAddresses {
		if strings.TrimSpace(zone) == "" {
			return fmt.Errorf("route canary policy: internal_dial_addresses has an empty zone key")
		}
		if net.ParseIP(strings.TrimSpace(address)) == nil {
			return fmt.Errorf("route canary policy: internal_dial_addresses[%s] must be an IP address, got %q", zone, address)
		}
	}
	if p.Thresholds.FailureThreshold < 0 || p.Thresholds.SuccessThreshold < 0 {
		return fmt.Errorf("route canary policy: thresholds must not be negative")
	}
	return nil
}

// internalDialAddressFor resolves the configured LAN address for a hostname by
// matching its zone. Matching is case-insensitive and accepts the zone itself or
// any subdomain of it.
func (p RouteCanaryPolicy) internalDialAddressFor(hostname string) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	for zone, address := range p.InternalDialAddresses {
		normalizedZone := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
		if normalizedZone == "" {
			continue
		}
		if host == normalizedZone || strings.HasSuffix(host, "."+normalizedZone) {
			return strings.TrimSpace(address)
		}
	}
	return ""
}

// DeriveRouteCanaryTargets builds the checks for one managed route plan.
//
// A public_edge target is always derived: it resolves the hostname through the
// configured public resolver and requires managed TLS to verify, which is how an
// operator learns the route is broken for real users.
//
// An internal_lan target is derived when the plan carries an internal HTTPS
// vhost and the hostname's zone has a configured LAN address. It pins the dial
// address the way split DNS would while keeping the canonical hostname for TLS
// and Host, so it detects a stale nginx upstream even when the public edge is
// still serving a cached good response.
func DeriveRouteCanaryTargets(plan *DesiredPublicRoutePlan, policy RouteCanaryPolicy) ([]RouteCanaryTarget, error) {
	if plan == nil {
		return nil, fmt.Errorf("route canary: plan is required")
	}
	if !policy.Enabled {
		return nil, nil
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	hostname := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(plan.Hostname), "."))
	if hostname == "" {
		return nil, fmt.Errorf("route canary: plan hostname is required")
	}
	path := plan.Proxy.HealthPath
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("route canary: plan health path %q must be absolute", path)
	}

	base := RouteCanaryTarget{
		Scheme:               "https",
		Hostname:             hostname,
		Port:                 443,
		Path:                 path,
		Method:               "GET",
		ExpectedStatusMin:    policy.ExpectedStatusMin,
		ExpectedStatusMax:    policy.ExpectedStatusMax,
		ExpectedBodyContains: policy.ExpectedBodyContains,
		TLSMinDaysRemaining:  policy.TLSMinDaysRemaining,
		Timeout:              policy.ProbeTimeout,
	}

	publicTarget := base
	publicTarget.Perspective = RouteCanaryPerspectivePublicEdge
	publicTarget.ResolverAddr = policy.PublicResolverAddr
	if err := publicTarget.Validate(); err != nil {
		return nil, err
	}
	targets := []RouteCanaryTarget{publicTarget}

	if plan.InternalHTTPS != nil {
		dialAddress := policy.internalDialAddressFor(hostname)
		if dialAddress != "" {
			internalTarget := base
			internalTarget.Perspective = RouteCanaryPerspectiveInternalLAN
			internalTarget.ResolveTo = dialAddress
			port, err := internalListenPort(plan.InternalHTTPS.Listen)
			if err != nil {
				return nil, err
			}
			internalTarget.Port = port
			if err := internalTarget.Validate(); err != nil {
				return nil, err
			}
			targets = append(targets, internalTarget)
		}
	}
	return targets, nil
}

// internalListenPort extracts the TCP port from an nginx listen directive such
// as "443 ssl" or "8443 ssl http2".
func internalListenPort(listen string) (int, error) {
	fields := strings.Fields(strings.TrimSpace(listen))
	if len(fields) == 0 {
		return 0, fmt.Errorf("route canary: internal listen directive is empty")
	}
	candidate := fields[0]
	if index := strings.LastIndex(candidate, ":"); index >= 0 {
		candidate = candidate[index+1:]
	}
	port, err := strconv.Atoi(candidate)
	if err != nil {
		return 0, fmt.Errorf("route canary: cannot parse port from internal listen directive %q", listen)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("route canary: internal listen port %d out of range", port)
	}
	return port, nil
}

// RouteCanaryKeyForPlan builds the durable key for a route plan.
func RouteCanaryKeyForPlan(plan *DesiredPublicRoutePlan) RouteCanaryKey {
	key := RouteCanaryKey{
		ServiceID:     plan.ServiceID,
		EnvironmentID: plan.EnvironmentID,
		Hostname:      strings.ToLower(strings.TrimSuffix(strings.TrimSpace(plan.Hostname), ".")),
	}
	if plan.DeploymentUnitID != uuid.Nil {
		unit := plan.DeploymentUnitID
		key.DeploymentUnitID = &unit
	}
	return key
}
