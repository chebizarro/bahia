package config

import (
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/strutil"
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
	if len(c.Overrides) > 0 {
		// Canonicalize override keys the same way route canary keys are, so an
		// override written with different case or a trailing dot still applies.
		// Keys that collide once canonicalized are rejected by validation.
		overrides := make(map[string]RouteCanaryOverrideConfig, len(c.Overrides))
		for hostname, override := range c.Overrides {
			overrides[domain.NormalizeRouteCanaryHostname(hostname)] = override
		}
		normalized.Overrides = overrides
	}
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
		ExpectedBodyRegex:    normalized.ExpectedBodyRegex,
		TLSMinDaysRemaining:  normalized.TLSMinDaysRemaining,
		DetectCatchAll:       normalized.DetectCatchAll,
		Overrides:            normalized.domainOverrides(),

		RequireDiscriminatingHealthPath: normalized.RequireDiscriminatingHealthPath,
		PublicResolverAddr:              resolver,
		InternalDialAddresses:           dialAddresses,
		Thresholds: domain.RouteCanaryThresholds{
			FailureThreshold: normalized.FailureThreshold,
			SuccessThreshold: normalized.SuccessThreshold,
		},
	}
}

// domainOverrides converts the configured per-route overrides into domain
// policy. It returns nil when no route is overridden.
func (c RouteCanaryConfig) domainOverrides() map[string]domain.RouteCanaryOverride {
	if len(c.Overrides) == 0 {
		return nil
	}
	overrides := make(map[string]domain.RouteCanaryOverride, len(c.Overrides))
	for hostname, override := range c.Overrides {
		overrides[hostname] = domain.RouteCanaryOverride{
			Interval:             override.Interval,
			ProbeTimeout:         override.ProbeTimeout,
			ExpectedStatusMin:    cloneOptional(override.ExpectedStatusMin),
			ExpectedStatusMax:    cloneOptional(override.ExpectedStatusMax),
			ExpectedBodyContains: cloneOptional(override.ExpectedBodyContains),
			ExpectedBodyRegex:    cloneOptional(override.ExpectedBodyRegex),
			TLSMinDaysRemaining:  cloneOptional(override.TLSMinDaysRemaining),
		}
	}
	return overrides
}

// cloneOptional copies an optional value so the domain policy never aliases
// the configuration it was built from.
func cloneOptional[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
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
	if err := validateRouteCanaryOverrideKeys(canaries.Overrides); err != nil {
		return err
	}
	if err := normalized.Policy().Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}
	return nil
}

// validateRouteCanaryOverrideKeys rejects override hostnames that collide once
// canonicalized. Two entries for the same route would otherwise be merged in an
// unspecified order, so which expectations applied would depend on map order.
func validateRouteCanaryOverrideKeys(overrides map[string]RouteCanaryOverrideConfig) error {
	hostnames := make([]string, 0, len(overrides))
	for hostname := range overrides {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	seen := make(map[string]string, len(hostnames))
	for _, hostname := range hostnames {
		normalized := domain.NormalizeRouteCanaryHostname(hostname)
		if previous, ok := seen[normalized]; ok {
			return fmt.Errorf("config validation failed: route_canaries.overrides has duplicate entries %q and %q for route %q",
				previous, hostname, normalized)
		}
		seen[normalized] = hostname
	}
	return nil
}

// routeCanaryOverrideFields is the set of keys an override entry accepts,
// derived from the struct tags so it cannot drift from the decoded type.
func routeCanaryOverrideFields() map[string]struct{} {
	fields := map[string]struct{}{}
	overrideType := reflect.TypeOf(RouteCanaryOverrideConfig{})
	for i := 0; i < overrideType.NumField(); i++ {
		if tag := overrideType.Field(i).Tag.Get("koanf"); tag != "" {
			fields[tag] = struct{}{}
		}
	}
	return fields
}

// rejectUnknownRouteCanaryOverrideKeys fails loading when a route override
// carries a key the decoder would silently drop.
//
// Unknown keys are otherwise ignored during decoding, so a misspelled field such
// as expected_status (for expected_status_min) would leave the route on
// fleet-wide policy while the operator believes it has been tuned. That is an
// enabled-but-unusable configuration, which must fail at startup.
//
// Hostnames contain the key delimiter, so this walks the nested map rather than
// the flattened key list, where a hostname and a field name are ambiguous.
func rejectUnknownRouteCanaryOverrideKeys(k *koanf.Koanf) error {
	const path = "route_canaries.overrides"
	if !k.Exists(path) {
		return nil
	}
	raw := k.Get(path)
	if raw == nil {
		return nil
	}
	overrides, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a map of route hostname to override, got %T", path, raw)
	}
	known := routeCanaryOverrideFields()
	hostnames := make([]string, 0, len(overrides))
	for hostname := range overrides {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	for _, hostname := range hostnames {
		entry := overrides[hostname]
		if entry == nil {
			return fmt.Errorf("%s[%s] is empty; set at least one field or remove the entry", path, hostname)
		}
		fields, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%s] must be a map of override fields, got %T", path, hostname, entry)
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, ok := known[name]; ok {
				continue
			}
			hint := ""
			bestDistance := -1
			for candidate := range known {
				distance := strutil.LevenshteinDistance(name, candidate)
				if bestDistance == -1 || distance < bestDistance || distance == bestDistance && candidate < hint {
					bestDistance = distance
					hint = candidate
				}
			}
			return fmt.Errorf("unknown %s[%s] key %q (did you mean %q?)", path, hostname, name, hint)
		}
	}
	return nil
}
