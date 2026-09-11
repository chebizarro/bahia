package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MinRouteCanaryOverrideInterval is the shortest per-route probe interval an
// override may request. Every probe is a real HTTPS request against a
// production route, so an interval tuned for one noisy route must not be able
// to turn the canary into load.
const MinRouteCanaryOverrideInterval = 5 * time.Second

// RouteCanaryOverride tunes the canary for one managed route.
//
// Overrides are control-plane policy keyed by hostname, never part of the
// signed route plan: tuning one route's expectations must not invalidate the
// deployed plan hash of that route or any other.
//
// Every field is optional and overrides exactly that field of the fleet-wide
// policy; an unset field inherits. Pointer fields distinguish "unset" from an
// explicit zero value, which is what lets a route clear a fleet-wide body
// marker (ExpectedBodyContains set to "") or switch off the expiry warning
// (TLSMinDaysRemaining set to 0) without affecting any other route.
type RouteCanaryOverride struct {
	// Interval replaces the fleet-wide periodic probe interval for this route.
	// Zero inherits. It does not affect the post-deploy gate, which retries on
	// its own schedule until its deadline.
	Interval time.Duration
	// ProbeTimeout replaces the per-probe timeout, for a slow origin. Zero
	// inherits.
	ProbeTimeout time.Duration
	// ExpectedStatusMin and ExpectedStatusMax replace either bound of the
	// accepted status range. The resulting range must still be valid.
	ExpectedStatusMin *int
	ExpectedStatusMax *int
	// ExpectedBodyContains replaces the substring assertion. An explicit empty
	// string removes the fleet-wide marker for this route.
	ExpectedBodyContains *string
	// ExpectedBodyRegex replaces the anchored regex assertion. An explicit empty
	// string removes the fleet-wide pattern for this route.
	ExpectedBodyRegex *string
	// TLSMinDaysRemaining replaces the expiry warning window. An explicit zero
	// disables the warning for this route; chain validity is still required.
	TLSMinDaysRemaining *int
}

// IsEmpty reports whether the override changes nothing. An empty override is
// almost always a misspelled key, so validation rejects it rather than letting
// it silently do nothing.
func (o RouteCanaryOverride) IsEmpty() bool {
	return o.Interval == 0 &&
		o.ProbeTimeout == 0 &&
		o.ExpectedStatusMin == nil &&
		o.ExpectedStatusMax == nil &&
		o.ExpectedBodyContains == nil &&
		o.ExpectedBodyRegex == nil &&
		o.TLSMinDaysRemaining == nil
}

// NormalizeRouteCanaryHostname canonicalizes a hostname the same way route
// canary keys are canonicalized, so an override written as "Git.Example.net."
// addresses the route whose key is "git.example.net".
func NormalizeRouteCanaryHostname(hostname string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
}

// validateRouteCanaryOverrideHostname rejects override keys that can never
// match a managed route hostname. An override that cannot match is a
// configuration error, not a silent no-op.
func validateRouteCanaryOverrideHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("override hostname is empty")
	}
	if strings.ContainsAny(hostname, "/:*@ \t") {
		return fmt.Errorf("override key %q must be a bare hostname, without scheme, port, path or wildcard", hostname)
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" {
			return fmt.Errorf("override key %q has an empty DNS label", hostname)
		}
	}
	return nil
}

// ForHostname resolves the effective policy for one managed route by applying
// its override, if any, on top of the fleet-wide policy.
//
// The result carries no overrides of its own, so it can be validated and used
// without re-resolving. Routes without an override get the fleet-wide policy
// unchanged, which is what guarantees an override cannot leak to other routes.
func (p RouteCanaryPolicy) ForHostname(hostname string) RouteCanaryPolicy {
	effective := p
	effective.Overrides = nil
	override, ok := p.Overrides[NormalizeRouteCanaryHostname(hostname)]
	if !ok {
		return effective
	}
	if override.ProbeTimeout > 0 {
		effective.ProbeTimeout = override.ProbeTimeout
	}
	if override.ExpectedStatusMin != nil {
		effective.ExpectedStatusMin = *override.ExpectedStatusMin
	}
	if override.ExpectedStatusMax != nil {
		effective.ExpectedStatusMax = *override.ExpectedStatusMax
	}
	if override.ExpectedBodyContains != nil {
		effective.ExpectedBodyContains = *override.ExpectedBodyContains
	}
	if override.ExpectedBodyRegex != nil {
		effective.ExpectedBodyRegex = *override.ExpectedBodyRegex
	}
	if override.TLSMinDaysRemaining != nil {
		effective.TLSMinDaysRemaining = *override.TLSMinDaysRemaining
	}
	return effective
}

// ProbeIntervalFor reports the per-route periodic probe interval, when the
// route has an override that sets one. ok is false when the route inherits the
// supervisor's fleet-wide interval.
func (p RouteCanaryPolicy) ProbeIntervalFor(hostname string) (time.Duration, bool) {
	override, ok := p.Overrides[NormalizeRouteCanaryHostname(hostname)]
	if !ok || override.Interval <= 0 {
		return 0, false
	}
	return override.Interval, true
}

// validateOverrides checks every override in isolation and in combination with
// the fleet-wide policy it modifies.
func (p RouteCanaryPolicy) validateOverrides() error {
	hostnames := make([]string, 0, len(p.Overrides))
	for hostname := range p.Overrides {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)
	for _, hostname := range hostnames {
		override := p.Overrides[hostname]
		if normalized := NormalizeRouteCanaryHostname(hostname); normalized != hostname {
			// A key that is not already canonical would never be found by
			// ForHostname, so it would silently do nothing.
			return fmt.Errorf("route canary policy: override key %q must be normalized as %q", hostname, normalized)
		}
		if err := validateRouteCanaryOverrideHostname(hostname); err != nil {
			return fmt.Errorf("route canary policy: %w", err)
		}
		if override.IsEmpty() {
			return fmt.Errorf("route canary policy: overrides[%s] sets nothing; check for a misspelled key", hostname)
		}
		if override.Interval < 0 {
			return fmt.Errorf("route canary policy: overrides[%s].interval must not be negative", hostname)
		}
		if override.Interval > 0 && override.Interval < MinRouteCanaryOverrideInterval {
			return fmt.Errorf("route canary policy: overrides[%s].interval %s is below the %s minimum",
				hostname, override.Interval, MinRouteCanaryOverrideInterval)
		}
		if override.ProbeTimeout < 0 {
			return fmt.Errorf("route canary policy: overrides[%s].probe_timeout must not be negative", hostname)
		}
		// Validate the policy the route will actually be probed with, so an
		// override that breaks the fleet-wide range (for example a minimum above
		// the inherited maximum) fails at startup.
		if err := p.ForHostname(hostname).validateExpectations(); err != nil {
			return fmt.Errorf("route canary policy: overrides[%s]: %w", hostname, err)
		}
	}
	return nil
}
