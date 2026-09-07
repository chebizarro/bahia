package domain

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RouteCanaryPerspective identifies the vantage point a route observation was
// taken from. A managed route can be healthy from one perspective and broken
// from another, so every observation is attributed to exactly one.
type RouteCanaryPerspective string

const (
	// RouteCanaryPerspectivePublicEdge observes the route the way an internet
	// user reaches it: public DNS resolution followed by HTTPS through the
	// public edge provider.
	RouteCanaryPerspectivePublicEdge RouteCanaryPerspective = "public_edge"
	// RouteCanaryPerspectiveInternalLAN observes the route the way a LAN client
	// reaches it under split DNS: the hostname is pinned to a configured
	// internal address while TLS server name and Host header stay canonical.
	RouteCanaryPerspectiveInternalLAN RouteCanaryPerspective = "internal_lan"
)

// Valid reports whether the perspective is one of the known vantage points.
func (p RouteCanaryPerspective) Valid() bool {
	switch p {
	case RouteCanaryPerspectivePublicEdge, RouteCanaryPerspectiveInternalLAN:
		return true
	default:
		return false
	}
}

// RouteCanaryClassification is the operator-facing reason a route observation
// passed or failed. Classifications are ordered by diagnostic precedence: the
// first failing layer wins so a 502 behind valid TLS is reported as an upstream
// error rather than a compound failure.
type RouteCanaryClassification string

const (
	// RouteCanaryClassificationRouteOK means every configured assertion held.
	RouteCanaryClassificationRouteOK RouteCanaryClassification = "route_ok"
	// RouteCanaryClassificationDNSUnresolved means the hostname did not resolve
	// from this perspective.
	RouteCanaryClassificationDNSUnresolved RouteCanaryClassification = "dns_unresolved"
	// RouteCanaryClassificationConnectFailed means the address resolved but the
	// TCP connection or the HTTP exchange never completed.
	RouteCanaryClassificationConnectFailed RouteCanaryClassification = "connect_failed"
	// RouteCanaryClassificationTLSInvalid means the TLS handshake completed with
	// a certificate that does not verify for the requested hostname, or the
	// handshake failed for certificate reasons.
	RouteCanaryClassificationTLSInvalid RouteCanaryClassification = "tls_invalid"
	// RouteCanaryClassificationTLSExpiring means TLS is currently valid but the
	// leaf certificate expires within the configured warning window. It is a
	// warning classification and never opens an outage on its own.
	RouteCanaryClassificationTLSExpiring RouteCanaryClassification = "tls_expiring"
	// RouteCanaryClassificationUpstreamError means the edge answered but the
	// origin behind it did not: HTTP 502, 503 or 504. This is the signature of a
	// stale or unreachable upstream while the route itself is still published.
	RouteCanaryClassificationUpstreamError RouteCanaryClassification = "upstream_error"
	// RouteCanaryClassificationStatusMismatch means the response status fell
	// outside the configured expected range for a non-upstream reason.
	RouteCanaryClassificationStatusMismatch RouteCanaryClassification = "status_mismatch"
	// RouteCanaryClassificationBodyMismatch means status assertions held but the
	// configured expected body substring was absent.
	RouteCanaryClassificationBodyMismatch RouteCanaryClassification = "body_mismatch"
	// RouteCanaryClassificationHealthPathNotDiscriminating means the health path
	// returned the same response as a deliberately bogus control path, so the
	// check proves only that something answered, not that the intended
	// application answered.
	//
	// This is the catch-all case: a single-page application that serves its shell
	// with HTTP 200 for every path makes a health-path assertion vacuous. The
	// route may well be fine, which is why this is a warning by default, but the
	// canary cannot vouch for it and must say so rather than imply confidence it
	// does not have.
	RouteCanaryClassificationHealthPathNotDiscriminating RouteCanaryClassification = "health_path_not_discriminating"
)

// Failing reports whether the classification represents a failed route
// assertion. RouteCanaryClassificationTLSExpiring is explicitly not failing: the
// route still serves traffic correctly, so it is surfaced as a warning without
// opening an outage or blocking a deployment.
func (c RouteCanaryClassification) Failing() bool {
	switch c {
	case RouteCanaryClassificationRouteOK,
		RouteCanaryClassificationTLSExpiring,
		RouteCanaryClassificationHealthPathNotDiscriminating:
		return false
	default:
		return true
	}
}

// FailingForTarget reports whether the classification should fail this specific
// target. It differs from Failing only for warning classifications that a target
// has been configured to treat as errors.
//
// A non-discriminating health path is a warning by default because serving a
// catch-all is legitimate for many applications. An operator who needs the check
// to actually mean something sets RequireDiscriminatingHealthPath, which
// promotes it to a failure.
func (c RouteCanaryClassification) FailingForTarget(target RouteCanaryTarget) bool {
	if c == RouteCanaryClassificationHealthPathNotDiscriminating {
		return target.RequireDiscriminatingHealthPath
	}
	return c.Failing()
}

// Valid reports whether the classification is a known value.
func (c RouteCanaryClassification) Valid() bool {
	switch c {
	case RouteCanaryClassificationRouteOK,
		RouteCanaryClassificationDNSUnresolved,
		RouteCanaryClassificationConnectFailed,
		RouteCanaryClassificationTLSInvalid,
		RouteCanaryClassificationTLSExpiring,
		RouteCanaryClassificationUpstreamError,
		RouteCanaryClassificationStatusMismatch,
		RouteCanaryClassificationBodyMismatch,
		RouteCanaryClassificationHealthPathNotDiscriminating:
		return true
	default:
		return false
	}
}

// RouteCanaryTarget is one fully resolved, provider-neutral check. Targets are
// derived from a DesiredPublicRoutePlan plus control-plane configuration; they
// never carry credentials.
type RouteCanaryTarget struct {
	Perspective          RouteCanaryPerspective `json:"perspective"`
	Scheme               string                 `json:"scheme"`
	Hostname             string                 `json:"hostname"`
	Port                 int                    `json:"port"`
	Path                 string                 `json:"path"`
	Method               string                 `json:"method"`
	HostHeader           string                 `json:"host_header,omitempty"`
	ResolverAddr         string                 `json:"resolver_addr,omitempty"`
	ResolveTo            string                 `json:"resolve_to,omitempty"`
	ExpectedStatusMin    int                    `json:"expected_status_min"`
	ExpectedStatusMax    int                    `json:"expected_status_max"`
	ExpectedBodyContains string                 `json:"expected_body_contains,omitempty"`
	TLSMinDaysRemaining  int                    `json:"tls_min_days_remaining,omitempty"`
	Timeout              time.Duration          `json:"timeout"`
	// ControlPath is a deliberately bogus path used as a negative control. When
	// set, the probe also requests it; if the health path is indistinguishable
	// from it, the health assertion is proving nothing.
	ControlPath string `json:"control_path,omitempty"`
	// RequireDiscriminatingHealthPath promotes a non-discriminating health path
	// from a warning to a failure for this target.
	RequireDiscriminatingHealthPath bool `json:"require_discriminating_health_path,omitempty"`
}

// ControlURL renders the absolute URL of the negative-control request.
func (t RouteCanaryTarget) ControlURL() string {
	if t.ControlPath == "" {
		return ""
	}
	control := t
	control.Path = t.ControlPath
	return control.URL()
}

// URL renders the absolute request URL for the target. The port is omitted when
// it is the scheme default so that the Host header and TLS server name stay
// canonical for the hostname.
func (t RouteCanaryTarget) URL() string {
	host := t.Hostname
	if t.Port != 0 && !t.isDefaultPort() {
		host = fmt.Sprintf("%s:%d", t.Hostname, t.Port)
	}
	return t.Scheme + "://" + host + t.Path
}

func (t RouteCanaryTarget) isDefaultPort() bool {
	return (t.Scheme == "https" && t.Port == 443) || (t.Scheme == "http" && t.Port == 80)
}

// Describe renders a stable, secret-free label used in evidence and lineage.
func (t RouteCanaryTarget) Describe() string {
	return string(t.Perspective) + " " + t.Method + " " + t.URL()
}

// Validate enforces the invariants a target must hold before it is probed.
func (t RouteCanaryTarget) Validate() error {
	if !t.Perspective.Valid() {
		return fmt.Errorf("route canary target: unknown perspective %q", t.Perspective)
	}
	if t.Scheme != "https" && t.Scheme != "http" {
		return fmt.Errorf("route canary target: scheme must be http or https, got %q", t.Scheme)
	}
	if strings.TrimSpace(t.Hostname) == "" {
		return fmt.Errorf("route canary target: hostname is required")
	}
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("route canary target: port %d out of range", t.Port)
	}
	if !strings.HasPrefix(t.Path, "/") {
		return fmt.Errorf("route canary target: path must be absolute, got %q", t.Path)
	}
	if t.Method == "" {
		return fmt.Errorf("route canary target: method is required")
	}
	if t.ExpectedStatusMin < 100 || t.ExpectedStatusMax > 599 || t.ExpectedStatusMin > t.ExpectedStatusMax {
		return fmt.Errorf("route canary target: invalid expected status range %d..%d", t.ExpectedStatusMin, t.ExpectedStatusMax)
	}
	if t.Timeout <= 0 {
		return fmt.Errorf("route canary target: timeout must be positive")
	}
	if t.TLSMinDaysRemaining < 0 {
		return fmt.Errorf("route canary target: tls_min_days_remaining must not be negative")
	}
	return nil
}

// RouteCanaryTLSObservation records what the TLS handshake actually presented.
type RouteCanaryTLSObservation struct {
	// HandshakeCompleted reports whether a TLS handshake finished at all.
	HandshakeCompleted bool `json:"handshake_completed"`
	// ChainVerified reports whether the presented chain verified against the
	// host trust store for the requested server name. Verification is never
	// disabled; a false value means the route is genuinely untrusted.
	ChainVerified bool `json:"chain_verified"`
	// NotAfter is the leaf certificate expiry.
	NotAfter time.Time `json:"not_after,omitempty"`
	// Issuer is the leaf certificate issuer common name.
	Issuer string `json:"issuer,omitempty"`
}

// DaysRemaining reports whole days until leaf expiry relative to now. It returns
// a negative number for an already-expired certificate.
func (o RouteCanaryTLSObservation) DaysRemaining(now time.Time) int {
	if o.NotAfter.IsZero() {
		return 0
	}
	return int(o.NotAfter.Sub(now) / (24 * time.Hour))
}

// RouteCanaryObservation is the raw, provider-neutral outcome of probing one
// target once. It is produced by the probe adapter and consumed by the pure
// classifier, which keeps classification independent of transport details.
type RouteCanaryObservation struct {
	Target RouteCanaryTarget `json:"target"`
	// Resolved reports whether the hostname resolved from this perspective. A
	// target with a static dial override is always considered resolved.
	Resolved bool `json:"resolved"`
	// Connected reports whether an HTTP response was received.
	Connected bool `json:"connected"`
	// TLSError is set when the handshake failed for certificate reasons.
	TLSError string `json:"tls_error,omitempty"`
	// TLS carries what the handshake presented when it completed.
	TLS RouteCanaryTLSObservation `json:"tls"`
	// StatusCode is the HTTP status when Connected is true.
	StatusCode int `json:"status_code,omitempty"`
	// Body is the bounded, sanitized response prefix.
	Body string `json:"body,omitempty"`
	// BodyMatched reports whether the configured expected substring was found.
	// It is evaluated by the probe against the unsanitized, bounded body so that
	// redaction cannot change match semantics.
	BodyMatched bool `json:"body_matched"`
	// Error is the sanitized transport-level error, when any.
	Error string `json:"error,omitempty"`
	// Duration is how long the probe took.
	Duration time.Duration `json:"duration"`
	// ObservedAt is when the probe started.
	ObservedAt time.Time `json:"observed_at"`
	// BodyFingerprint is a hash of the bounded body, used to compare the health
	// response against the negative control without storing two bodies.
	BodyFingerprint string `json:"body_fingerprint,omitempty"`
	// Control is the negative-control observation, when one was requested.
	Control *RouteControlObservation `json:"control,omitempty"`
}

// RouteControlObservation is the result of requesting a deliberately bogus path.
//
// Its only purpose is to establish whether the health path discriminates. If a
// path that should not exist answers identically to the health path, the health
// path is not evidence about the application behind the route.
type RouteControlObservation struct {
	Performed       bool   `json:"performed"`
	Connected       bool   `json:"connected"`
	StatusCode      int    `json:"status_code,omitempty"`
	BodyFingerprint string `json:"body_fingerprint,omitempty"`
}

// Indistinguishable reports whether the health response and the control response
// cannot be told apart.
//
// Both status and body must match. Comparing bodies avoids false positives on
// applications that legitimately return the same status for everything but
// different content, and any difference at all is treated as discriminating so
// that dynamic content never produces a spurious warning.
func (o RouteCanaryObservation) Indistinguishable() bool {
	if o.Control == nil || !o.Control.Performed || !o.Control.Connected || !o.Connected {
		return false
	}
	if o.Control.StatusCode != o.StatusCode {
		return false
	}
	return o.BodyFingerprint != "" && o.BodyFingerprint == o.Control.BodyFingerprint
}

// ClassifyRouteObservation reduces a raw observation to a single classification.
//
// Precedence is diagnostic, outermost failure first: resolution, then TLS trust,
// then connectivity, then upstream status, then status range, then body. This
// guarantees that an origin returning 502 behind a valid certificate is reported
// as upstream_error rather than being masked by a lower-layer symptom, which is
// the distinction the git.sharegap.net outage required.
func ClassifyRouteObservation(observation RouteCanaryObservation, now time.Time) RouteCanaryClassification {
	if !observation.Resolved {
		return RouteCanaryClassificationDNSUnresolved
	}
	if observation.TLSError != "" {
		return RouteCanaryClassificationTLSInvalid
	}
	if observation.Target.Scheme == "https" && observation.TLS.HandshakeCompleted && !observation.TLS.ChainVerified {
		return RouteCanaryClassificationTLSInvalid
	}
	if !observation.Connected {
		return RouteCanaryClassificationConnectFailed
	}
	switch observation.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return RouteCanaryClassificationUpstreamError
	}
	if observation.StatusCode < observation.Target.ExpectedStatusMin || observation.StatusCode > observation.Target.ExpectedStatusMax {
		return RouteCanaryClassificationStatusMismatch
	}
	if observation.Target.ExpectedBodyContains != "" && !observation.BodyMatched {
		return RouteCanaryClassificationBodyMismatch
	}
	// An explicit body assertion that held is real evidence about the
	// application, so it outranks the catch-all warning: the operator has
	// already proven the response is the intended one.
	if observation.Target.ExpectedBodyContains == "" && observation.Indistinguishable() {
		return RouteCanaryClassificationHealthPathNotDiscriminating
	}
	if observation.Target.TLSMinDaysRemaining > 0 &&
		observation.TLS.HandshakeCompleted &&
		!observation.TLS.NotAfter.IsZero() &&
		observation.TLS.DaysRemaining(now) < observation.Target.TLSMinDaysRemaining {
		return RouteCanaryClassificationTLSExpiring
	}
	return RouteCanaryClassificationRouteOK
}

// ReduceRouteObservations collapses every perspective's observation for one
// route into the single classification that should drive route state.
//
// A route is only healthy when every configured perspective is healthy: an
// internally reachable service whose public edge is broken is an outage, and a
// publicly reachable service whose LAN split-DNS path is broken is an outage
// too. The first failing observation in precedence order is reported so that
// operators see the most actionable cause. A warning classification is reported
// only when nothing is failing.
func ReduceRouteObservations(observations []RouteCanaryObservation, now time.Time) (RouteCanaryReduction, bool) {
	if len(observations) == 0 {
		return RouteCanaryReduction{Classification: RouteCanaryClassificationRouteOK}, false
	}
	worst := RouteCanaryClassificationRouteOK
	worstObservation := observations[0]
	worstRank := -1
	for _, observation := range observations {
		classification := ClassifyRouteObservation(observation, now)
		rank := routeClassificationRank(classification)
		if rank > worstRank {
			worst = classification
			worstObservation = observation
			worstRank = rank
		}
	}
	return RouteCanaryReduction{
		Classification: worst,
		Perspective:    worstObservation.Target.Perspective,
		// Failing is target-aware so a warning the operator has configured as
		// mandatory is treated as a failure for that route only.
		Failing:     worst.FailingForTarget(worstObservation.Target),
		Observation: worstObservation,
	}, true
}

// RouteCanaryReduction is the single verdict derived from every perspective.
type RouteCanaryReduction struct {
	Classification RouteCanaryClassification
	Perspective    RouteCanaryPerspective
	Failing        bool
	Observation    RouteCanaryObservation
}

// routeClassificationRank orders classifications by severity so a reduction over
// multiple perspectives reports the most actionable failure. route_ok is lowest,
// the tls_expiring warning sits above it, and hard failures rank above both.
func routeClassificationRank(classification RouteCanaryClassification) int {
	switch classification {
	case RouteCanaryClassificationRouteOK:
		return 0
	case RouteCanaryClassificationTLSExpiring:
		return 1
	case RouteCanaryClassificationHealthPathNotDiscriminating:
		return 2
	case RouteCanaryClassificationBodyMismatch:
		return 3
	case RouteCanaryClassificationStatusMismatch:
		return 4
	case RouteCanaryClassificationUpstreamError:
		return 5
	case RouteCanaryClassificationConnectFailed:
		return 6
	case RouteCanaryClassificationTLSInvalid:
		return 7
	case RouteCanaryClassificationDNSUnresolved:
		return 8
	default:
		return 9
	}
}

// RouteCanaryThresholds is the hysteresis policy that converts a stream of
// classifications into a stable open/closed outage state.
type RouteCanaryThresholds struct {
	// FailureThreshold is the number of consecutive failing observations
	// required to open an outage.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive non-failing observations
	// required to clear an open outage.
	SuccessThreshold int
}

// Normalized returns the thresholds with non-positive values replaced by safe
// defaults so a partially specified policy can never disable hysteresis.
func (t RouteCanaryThresholds) Normalized() RouteCanaryThresholds {
	normalized := t
	if normalized.FailureThreshold < 1 {
		normalized.FailureThreshold = defaultRouteCanaryFailureThreshold
	}
	if normalized.SuccessThreshold < 1 {
		normalized.SuccessThreshold = defaultRouteCanarySuccessThreshold
	}
	return normalized
}

const (
	defaultRouteCanaryFailureThreshold = 3
	defaultRouteCanarySuccessThreshold = 2
)

// RouteCanaryKey addresses one managed route. Route canary state is keyed by the
// same service/environment/deployment-unit coordinate as managed instance
// health, which is what lets the read model contrast a healthy container with a
// broken route.
type RouteCanaryKey struct {
	ServiceID        uuid.UUID  `json:"service_id"`
	EnvironmentID    uuid.UUID  `json:"environment_id"`
	DeploymentUnitID *uuid.UUID `json:"deployment_unit_id,omitempty"`
	Hostname         string     `json:"hostname"`
}

// Coordinate renders the stable string identity used as the persistence primary
// key and in operator-facing lineage.
func (k RouteCanaryKey) Coordinate() string {
	unit := "none"
	if k.DeploymentUnitID != nil {
		unit = k.DeploymentUnitID.String()
	}
	return fmt.Sprintf("route:%s:%s:%s:%s", k.ServiceID, k.EnvironmentID, unit, k.Hostname)
}

// RouteCanaryState is the durable outage state for one managed route.
type RouteCanaryState struct {
	RouteCanaryKey
	// Open reports whether a route outage is currently declared.
	Open bool `json:"open"`
	// Classification is the latest observed classification.
	Classification RouteCanaryClassification `json:"classification"`
	// Perspective is the vantage point that produced the latest classification.
	Perspective RouteCanaryPerspective `json:"perspective,omitempty"`
	// ConsecutiveFailures counts consecutive failing observations. The streak
	// counts any failing classification, not a single class: a route flapping
	// between dns_unresolved and upstream_error is still down, and per-class
	// streaks would let it evade the threshold indefinitely.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// ConsecutiveSuccesses counts consecutive non-failing observations.
	ConsecutiveSuccesses int `json:"consecutive_successes"`
	// FailureReason is the sanitized, operator-facing cause.
	FailureReason string `json:"failure_reason,omitempty"`
	// TLSNotAfter is the last observed leaf expiry, when TLS was observed.
	TLSNotAfter *time.Time `json:"tls_not_after,omitempty"`
	// LastObservedAt is when the latest observation was taken.
	LastObservedAt time.Time `json:"last_observed_at"`
	// OpenedAt is when the current outage was declared, if open.
	OpenedAt *time.Time `json:"opened_at,omitempty"`
	// LastRecoveredAt is when the most recent outage cleared.
	LastRecoveredAt *time.Time `json:"last_recovered_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// RouteCanaryTransition names the durable state change produced by an
// evaluation, which is what drives events and notifications.
type RouteCanaryTransition string

const (
	// RouteCanaryTransitionNone means nothing operator-visible changed.
	RouteCanaryTransitionNone RouteCanaryTransition = "none"
	// RouteCanaryTransitionOpened means an outage was declared.
	RouteCanaryTransitionOpened RouteCanaryTransition = "opened"
	// RouteCanaryTransitionRecovered means an open outage cleared.
	RouteCanaryTransitionRecovered RouteCanaryTransition = "recovered"
	// RouteCanaryTransitionClassificationChanged means the route is still
	// failing but for a different reason, or a warning appeared or cleared.
	RouteCanaryTransitionClassificationChanged RouteCanaryTransition = "classification_changed"
)

// EvaluateRouteCanary folds one classification into the durable route state and
// reports the resulting transition.
//
// It is pure: it takes the previous state, the new classification and the
// evaluation instant, and returns the next state. It never consults container
// health, so route verdicts stay independent of supervision timing; the contrast
// between a healthy container and a broken route is joined at the read model.
func EvaluateRouteCanary(
	previous RouteCanaryState,
	classification RouteCanaryClassification,
	failing bool,
	perspective RouteCanaryPerspective,
	reason string,
	tlsNotAfter *time.Time,
	thresholds RouteCanaryThresholds,
	now time.Time,
) (RouteCanaryState, RouteCanaryTransition) {
	policy := thresholds.Normalized()
	next := previous
	next.Classification = classification
	next.Perspective = perspective
	next.LastObservedAt = now
	next.UpdatedAt = now
	if tlsNotAfter != nil {
		next.TLSNotAfter = tlsNotAfter
	}

	if failing {
		next.ConsecutiveFailures = previous.ConsecutiveFailures + 1
		next.ConsecutiveSuccesses = 0
		next.FailureReason = SanitizeEvidence(reason)
	} else {
		next.ConsecutiveSuccesses = previous.ConsecutiveSuccesses + 1
		next.ConsecutiveFailures = 0
		switch classification {
		case RouteCanaryClassificationTLSExpiring, RouteCanaryClassificationHealthPathNotDiscriminating:
			next.FailureReason = SanitizeEvidence(reason)
		default:
			next.FailureReason = ""
		}
	}

	switch {
	case !previous.Open && failing && next.ConsecutiveFailures >= policy.FailureThreshold:
		next.Open = true
		opened := now
		next.OpenedAt = &opened
		return next, RouteCanaryTransitionOpened
	case previous.Open && !failing && next.ConsecutiveSuccesses >= policy.SuccessThreshold:
		next.Open = false
		next.OpenedAt = nil
		recovered := now
		next.LastRecoveredAt = &recovered
		return next, RouteCanaryTransitionRecovered
	case previous.Classification != classification:
		return next, RouteCanaryTransitionClassificationChanged
	default:
		return next, RouteCanaryTransitionNone
	}
}

// RouteCanaryEvent is one append-only lineage record for a managed route.
type RouteCanaryEvent struct {
	ID uuid.UUID `json:"id"`
	RouteCanaryKey
	Transition             RouteCanaryTransition     `json:"transition"`
	PreviousClassification RouteCanaryClassification `json:"previous_classification,omitempty"`
	Classification         RouteCanaryClassification `json:"classification"`
	Perspective            RouteCanaryPerspective    `json:"perspective,omitempty"`
	Reason                 string                    `json:"reason,omitempty"`
	Evidence               string                    `json:"evidence,omitempty"`
	ObservedInstanceStatus InstanceHealthStatus      `json:"observed_instance_status,omitempty"`
	ObservedAt             time.Time                 `json:"observed_at"`
}

// DescribeRouteObservation renders a bounded, sanitized, operator-facing reason
// string for a classified observation. It never includes request headers or
// credentials; the response body is already bounded and sanitized upstream.
func DescribeRouteObservation(observation RouteCanaryObservation, classification RouteCanaryClassification, now time.Time) string {
	target := observation.Target
	switch classification {
	case RouteCanaryClassificationRouteOK:
		return ""
	case RouteCanaryClassificationDNSUnresolved:
		resolver := target.ResolverAddr
		if resolver == "" {
			resolver = "system"
		}
		return SanitizeEvidence(fmt.Sprintf("%s: hostname %s did not resolve (resolver %s): %s",
			target.Describe(), target.Hostname, resolver, observation.Error))
	case RouteCanaryClassificationTLSInvalid:
		detail := observation.TLSError
		if detail == "" {
			detail = "presented certificate chain did not verify for the requested hostname"
		}
		return SanitizeEvidence(fmt.Sprintf("%s: TLS verification failed: %s", target.Describe(), detail))
	case RouteCanaryClassificationTLSExpiring:
		return SanitizeEvidence(fmt.Sprintf("%s: TLS certificate expires in %d days (minimum %d), issuer %s",
			target.Describe(), observation.TLS.DaysRemaining(now), target.TLSMinDaysRemaining, observation.TLS.Issuer))
	case RouteCanaryClassificationConnectFailed:
		return SanitizeEvidence(fmt.Sprintf("%s: no HTTP response: %s", target.Describe(), observation.Error))
	case RouteCanaryClassificationUpstreamError:
		return SanitizeEvidence(fmt.Sprintf("%s: route published and edge reachable but upstream returned HTTP %d; origin is stale or unreachable",
			target.Describe(), observation.StatusCode))
	case RouteCanaryClassificationStatusMismatch:
		return SanitizeEvidence(fmt.Sprintf("%s: HTTP %d outside expected range %d..%d",
			target.Describe(), observation.StatusCode, target.ExpectedStatusMin, target.ExpectedStatusMax))
	case RouteCanaryClassificationBodyMismatch:
		return SanitizeEvidence(fmt.Sprintf("%s: HTTP %d but response body did not contain the expected marker",
			target.Describe(), observation.StatusCode))
	case RouteCanaryClassificationHealthPathNotDiscriminating:
		return SanitizeEvidence(fmt.Sprintf(
			"%s: health path returned HTTP %d with the same body as control path %s, so it does not discriminate; "+
				"this route serves a catch-all and the health path proves only that something answered. "+
				"Configure a real health endpoint or expected_body_contains to make this check meaningful",
			target.Describe(), observation.StatusCode, target.ControlPath))
	default:
		return SanitizeEvidence(fmt.Sprintf("%s: unclassified route failure", target.Describe()))
	}
}
