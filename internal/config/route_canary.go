package config

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultRouteCanaryInterval          = 60 * time.Second
	defaultRouteCanaryProbeTimeout      = 15 * time.Second
	defaultRouteCanaryGateTimeout       = 90 * time.Second
	defaultRouteCanaryGateRetryInterval = 3 * time.Second
	defaultRouteCanaryFailureThreshold  = 3
	defaultRouteCanarySuccessThreshold  = 2
	defaultRouteCanaryExpectedStatusMin = 200
	defaultRouteCanaryExpectedStatusMax = 299
)

// Normalized returns the configuration with unset values replaced by defaults.
//
// Defaults are applied here rather than at use sites so an operator reading the
// effective configuration sees the same values the probes actually use.
func (c RouteCanaryConfig) Normalized() RouteCanaryConfig {
	normalized := c
	if normalized.Interval <= 0 {
		normalized.Interval = defaultRouteCanaryInterval
	}
	if normalized.ProbeTimeout <= 0 {
		normalized.ProbeTimeout = defaultRouteCanaryProbeTimeout
	}
	if normalized.GateTimeout <= 0 {
		normalized.GateTimeout = defaultRouteCanaryGateTimeout
	}
	if normalized.GateRetryInterval <= 0 {
		normalized.GateRetryInterval = defaultRouteCanaryGateRetryInterval
	}
	if normalized.FailureThreshold <= 0 {
		normalized.FailureThreshold = defaultRouteCanaryFailureThreshold
	}
	if normalized.SuccessThreshold <= 0 {
		normalized.SuccessThreshold = defaultRouteCanarySuccessThreshold
	}
	if normalized.ExpectedStatusMin == 0 && normalized.ExpectedStatusMax == 0 {
		normalized.ExpectedStatusMin = defaultRouteCanaryExpectedStatusMin
		normalized.ExpectedStatusMax = defaultRouteCanaryExpectedStatusMax
	}
	normalized.PublicResolver = strings.TrimSpace(normalized.PublicResolver)
	return normalized
}

// Policy converts the configuration into the domain policy used to derive and
// evaluate checks.
func (c RouteCanaryConfig) Policy() domain.RouteCanaryPolicy {
	normalized := c.Normalized()
	dialAddresses := make(map[string]string, len(normalized.InternalDialAddresses))
	for zone, address := range normalized.InternalDialAddresses {
		dialAddresses[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))] = strings.TrimSpace(address)
	}
	resolver := normalized.PublicResolver
	if strings.EqualFold(resolver, "system") {
		resolver = ""
	}
	return domain.RouteCanaryPolicy{
		Enabled:              normalized.Enabled,
		ProbeTimeout:         normalized.ProbeTimeout,
		ExpectedStatusMin:    normalized.ExpectedStatusMin,
		ExpectedStatusMax:    normalized.ExpectedStatusMax,
		ExpectedBodyContains: normalized.ExpectedBodyContains,
		TLSMinDaysRemaining:  normalized.TLSMinDaysRemaining,
		DetectCatchAll:       normalized.DetectCatchAll,

		RequireDiscriminatingHealthPath: normalized.RequireDiscriminatingHealthPath,
		PublicResolverAddr:              resolver,
		InternalDialAddresses:           dialAddresses,
		Thresholds: domain.RouteCanaryThresholds{
			FailureThreshold: normalized.FailureThreshold,
			SuccessThreshold: normalized.SuccessThreshold,
		},
	}
}

// validateRouteCanaries rejects a route canary configuration that is enabled but
// cannot produce a usable check. A section that parses but silently probes
// nothing would give operators false confidence that routes are being watched.
func (c *Config) validateRouteCanaries() error {
	canaries := c.RouteCanaries
	if !canaries.Enabled {
		return nil
	}
	if !c.EdgeRouting.Enabled {
		return fmt.Errorf("config validation failed: route_canaries.enabled=true requires edge_routing.enabled=true")
	}
	normalized := canaries.Normalized()
	if normalized.ExpectedStatusMin < 100 || normalized.ExpectedStatusMax > 599 || normalized.ExpectedStatusMin > normalized.ExpectedStatusMax {
		return fmt.Errorf("config validation failed: route_canaries expected status range %d..%d is invalid",
			normalized.ExpectedStatusMin, normalized.ExpectedStatusMax)
	}
	if normalized.TLSMinDaysRemaining < 0 {
		return fmt.Errorf("config validation failed: route_canaries.tls_min_days_remaining must not be negative")
	}
	if normalized.RequireDiscriminatingHealthPath && !normalized.DetectCatchAll {
		return fmt.Errorf("config validation failed: route_canaries.require_discriminating_health_path requires detect_catch_all=true")
	}
	if resolver := normalized.PublicResolver; resolver != "" && !strings.EqualFold(resolver, "system") {
		host, port, err := net.SplitHostPort(resolver)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("config validation failed: route_canaries.public_resolver must be system or host:port")
		}
	}
	for zone, address := range normalized.InternalDialAddresses {
		if strings.TrimSpace(zone) == "" {
			return fmt.Errorf("config validation failed: route_canaries.internal_dial_addresses has an empty zone key")
		}
		if net.ParseIP(strings.TrimSpace(address)) == nil {
			return fmt.Errorf("config validation failed: route_canaries.internal_dial_addresses[%s] must be an IP address", zone)
		}
	}
	// An internal perspective can only be derived for a zone that internal
	// routing actually serves; mapping an unserved zone would probe a host that
	// never carries the vhost.
	if len(normalized.InternalDialAddresses) > 0 && !c.InternalRouting.Enabled {
		return fmt.Errorf("config validation failed: route_canaries.internal_dial_addresses requires internal_routing.enabled=true")
	}
	if err := normalized.Policy().Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}
	return nil
}
