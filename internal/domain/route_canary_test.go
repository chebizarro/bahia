package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func baseTarget() RouteCanaryTarget {
	return RouteCanaryTarget{
		Perspective:       RouteCanaryPerspectivePublicEdge,
		Scheme:            "https",
		Hostname:          "git.example.net",
		Port:              443,
		Path:              "/healthz",
		Method:            "GET",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           5 * time.Second,
	}
}

func verifiedTLS(notAfter time.Time) RouteCanaryTLSObservation {
	return RouteCanaryTLSObservation{
		HandshakeCompleted: true,
		ChainVerified:      true,
		NotAfter:           notAfter,
		Issuer:             "Example CA",
	}
}

// TestClassifyRouteObservationUpstreamErrorIsDistinctFromRouteDown proves the
// central distinction the git.sharegap.net outage required: an edge that is
// reachable over valid TLS but whose origin returns 502/503/504 is reported as
// upstream_error, not as a generic failure.
func TestClassifyRouteObservationUpstreamErrorIsDistinctFromRouteDown(t *testing.T) {
	now := time.Now()
	for _, status := range []int{502, 503, 504} {
		observation := RouteCanaryObservation{
			Target:    baseTarget(),
			Resolved:  true,
			Connected: true,
			TLS:       verifiedTLS(now.Add(90 * 24 * time.Hour)),
			// A cached stale upstream still returns a body; it must not change
			// the classification.
			StatusCode: status,
		}
		if got := ClassifyRouteObservation(observation, now); got != RouteCanaryClassificationUpstreamError {
			t.Fatalf("status %d: got %q, want %q", status, got, RouteCanaryClassificationUpstreamError)
		}
	}
}

func TestClassifyRouteObservationPrecedence(t *testing.T) {
	now := time.Now()
	expiring := verifiedTLS(now.Add(3 * 24 * time.Hour))
	healthyTLS := verifiedTLS(now.Add(90 * 24 * time.Hour))

	bodyTarget := baseTarget()
	bodyTarget.ExpectedBodyContains = "ok"

	expiryTarget := baseTarget()
	expiryTarget.TLSMinDaysRemaining = 30

	tests := []struct {
		name        string
		observation RouteCanaryObservation
		want        RouteCanaryClassification
	}{
		{
			name:        "unresolved outranks everything",
			observation: RouteCanaryObservation{Target: baseTarget(), Resolved: false},
			want:        RouteCanaryClassificationDNSUnresolved,
		},
		{
			name:        "explicit tls error is tls_invalid",
			observation: RouteCanaryObservation{Target: baseTarget(), Resolved: true, TLSError: "unknown authority"},
			want:        RouteCanaryClassificationTLSInvalid,
		},
		{
			name: "handshake without verified chain is tls_invalid",
			observation: RouteCanaryObservation{
				Target: baseTarget(), Resolved: true, Connected: true, StatusCode: 200,
				TLS: RouteCanaryTLSObservation{HandshakeCompleted: true, ChainVerified: false},
			},
			want: RouteCanaryClassificationTLSInvalid,
		},
		{
			name:        "resolved but no response is connect_failed",
			observation: RouteCanaryObservation{Target: baseTarget(), Resolved: true, Connected: false, Error: "connection refused"},
			want:        RouteCanaryClassificationConnectFailed,
		},
		{
			name: "out of range status is status_mismatch",
			observation: RouteCanaryObservation{
				Target: baseTarget(), Resolved: true, Connected: true, TLS: healthyTLS, StatusCode: 404,
			},
			want: RouteCanaryClassificationStatusMismatch,
		},
		{
			name: "missing body marker is body_mismatch",
			observation: RouteCanaryObservation{
				Target: bodyTarget, Resolved: true, Connected: true, TLS: healthyTLS,
				StatusCode: 200, BodyMatched: false,
			},
			want: RouteCanaryClassificationBodyMismatch,
		},
		{
			name: "present body marker passes",
			observation: RouteCanaryObservation{
				Target: bodyTarget, Resolved: true, Connected: true, TLS: healthyTLS,
				StatusCode: 200, BodyMatched: true,
			},
			want: RouteCanaryClassificationRouteOK,
		},
		{
			name: "near expiry is a warning once everything else passes",
			observation: RouteCanaryObservation{
				Target: expiryTarget, Resolved: true, Connected: true, TLS: expiring, StatusCode: 200,
			},
			want: RouteCanaryClassificationTLSExpiring,
		},
		{
			name: "upstream error outranks the expiry warning",
			observation: RouteCanaryObservation{
				Target: expiryTarget, Resolved: true, Connected: true, TLS: expiring, StatusCode: 502,
			},
			want: RouteCanaryClassificationUpstreamError,
		},
		{
			name: "healthy route is route_ok",
			observation: RouteCanaryObservation{
				Target: baseTarget(), Resolved: true, Connected: true, TLS: healthyTLS, StatusCode: 204,
			},
			want: RouteCanaryClassificationRouteOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyRouteObservation(test.observation, now); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

// TestTLSExpiringDoesNotOpenAnOutage pins the deliberate policy that a route
// still serving traffic on a soon-to-expire certificate is a warning, not an
// outage, and therefore must never block or roll back a deployment.
func TestTLSExpiringDoesNotOpenAnOutage(t *testing.T) {
	if RouteCanaryClassificationTLSExpiring.Failing() {
		t.Fatal("tls_expiring must not be a failing classification")
	}
	now := time.Now()
	state := RouteCanaryState{}
	thresholds := RouteCanaryThresholds{FailureThreshold: 1, SuccessThreshold: 1}
	for i := 0; i < 5; i++ {
		var transition RouteCanaryTransition
		state, transition = EvaluateRouteCanary(state, RouteCanaryClassificationTLSExpiring, RouteCanaryClassificationTLSExpiring.Failing(),
			RouteCanaryPerspectivePublicEdge, "expires soon", nil, thresholds, now)
		if state.Open {
			t.Fatalf("tls_expiring opened an outage (transition %q)", transition)
		}
	}
}

// TestReduceRouteObservationsRequiresEveryPerspective proves that a service
// reachable internally but broken at the public edge is still an outage. This is
// the multi-perspective half of the acceptance criterion.
func TestReduceRouteObservationsRequiresEveryPerspective(t *testing.T) {
	now := time.Now()
	healthy := verifiedTLS(now.Add(90 * 24 * time.Hour))

	internalTarget := baseTarget()
	internalTarget.Perspective = RouteCanaryPerspectiveInternalLAN
	internalTarget.ResolveTo = "192.168.40.10"

	observations := []RouteCanaryObservation{
		{Target: internalTarget, Resolved: true, Connected: true, TLS: healthy, StatusCode: 200},
		{Target: baseTarget(), Resolved: true, Connected: true, TLS: healthy, StatusCode: 502},
	}

	reduction, ok := ReduceRouteObservations(observations, now)
	if !ok {
		t.Fatal("expected a reduction")
	}
	if reduction.Classification != RouteCanaryClassificationUpstreamError {
		t.Fatalf("got %q, want %q", reduction.Classification, RouteCanaryClassificationUpstreamError)
	}
	if reduction.Perspective != RouteCanaryPerspectivePublicEdge {
		t.Fatalf("got perspective %q, want %q", reduction.Perspective, RouteCanaryPerspectivePublicEdge)
	}
	if !reduction.Failing {
		t.Fatal("an upstream error must be failing")
	}
}

// TestReduceRouteObservationsDetectsBrokenInternalPathWhileEdgeIsFine is the
// direct git.sharegap.net repro: the public edge answers fine while the internal
// nginx vhost serves 502 from a stale upstream.
func TestReduceRouteObservationsDetectsBrokenInternalPathWhileEdgeIsFine(t *testing.T) {
	now := time.Now()
	healthy := verifiedTLS(now.Add(90 * 24 * time.Hour))

	internalTarget := baseTarget()
	internalTarget.Perspective = RouteCanaryPerspectiveInternalLAN
	internalTarget.ResolveTo = "192.168.40.10"

	observations := []RouteCanaryObservation{
		{Target: baseTarget(), Resolved: true, Connected: true, TLS: healthy, StatusCode: 200},
		{Target: internalTarget, Resolved: true, Connected: true, TLS: healthy, StatusCode: 502},
	}

	reduction, _ := ReduceRouteObservations(observations, now)
	if reduction.Classification != RouteCanaryClassificationUpstreamError {
		t.Fatalf("got %q, want %q", reduction.Classification, RouteCanaryClassificationUpstreamError)
	}
	if reduction.Perspective != RouteCanaryPerspectiveInternalLAN {
		t.Fatalf("got perspective %q, want %q", reduction.Perspective, RouteCanaryPerspectiveInternalLAN)
	}
}

// TestEvaluateRouteCanaryHysteresis pins the open/close thresholds and the
// deliberate decision that a failure streak counts any failing classification,
// so a route flapping between failure classes cannot evade the threshold.
func TestEvaluateRouteCanaryHysteresis(t *testing.T) {
	now := time.Now()
	thresholds := RouteCanaryThresholds{FailureThreshold: 3, SuccessThreshold: 2}
	state := RouteCanaryState{}

	// Two failures of differing class must not open yet.
	state, transition := EvaluateRouteCanary(state, RouteCanaryClassificationUpstreamError, RouteCanaryClassificationUpstreamError.Failing(),
		RouteCanaryPerspectivePublicEdge, "502", nil, thresholds, now)
	if state.Open || transition == RouteCanaryTransitionOpened {
		t.Fatal("opened after one failure")
	}
	state, _ = EvaluateRouteCanary(state, RouteCanaryClassificationDNSUnresolved, RouteCanaryClassificationDNSUnresolved.Failing(),
		RouteCanaryPerspectivePublicEdge, "nxdomain", nil, thresholds, now)
	if state.Open {
		t.Fatal("opened after two failures")
	}
	if state.ConsecutiveFailures != 2 {
		t.Fatalf("mixed failure classes did not accumulate: got %d, want 2", state.ConsecutiveFailures)
	}

	// The third consecutive failure, of a third class, opens the outage.
	state, transition = EvaluateRouteCanary(state, RouteCanaryClassificationConnectFailed, RouteCanaryClassificationConnectFailed.Failing(),
		RouteCanaryPerspectivePublicEdge, "refused", nil, thresholds, now)
	if !state.Open || transition != RouteCanaryTransitionOpened {
		t.Fatalf("expected opened, got open=%v transition=%q", state.Open, transition)
	}
	if state.OpenedAt == nil {
		t.Fatal("OpenedAt not set on open")
	}

	// One success must not clear it.
	state, transition = EvaluateRouteCanary(state, RouteCanaryClassificationRouteOK, RouteCanaryClassificationRouteOK.Failing(),
		RouteCanaryPerspectivePublicEdge, "", nil, thresholds, now)
	if !state.Open {
		t.Fatalf("cleared after one success (transition %q)", transition)
	}

	// The second consecutive success clears it.
	state, transition = EvaluateRouteCanary(state, RouteCanaryClassificationRouteOK, RouteCanaryClassificationRouteOK.Failing(),
		RouteCanaryPerspectivePublicEdge, "", nil, thresholds, now)
	if state.Open || transition != RouteCanaryTransitionRecovered {
		t.Fatalf("expected recovered, got open=%v transition=%q", state.Open, transition)
	}
	if state.LastRecoveredAt == nil {
		t.Fatal("LastRecoveredAt not set on recovery")
	}
	if state.OpenedAt != nil {
		t.Fatal("OpenedAt not cleared on recovery")
	}
	if state.FailureReason != "" {
		t.Fatalf("failure reason not cleared on recovery: %q", state.FailureReason)
	}
}

// TestEvaluateRouteCanaryNormalizesThresholds proves a partially specified
// policy cannot silently disable hysteresis.
func TestEvaluateRouteCanaryNormalizesThresholds(t *testing.T) {
	now := time.Now()
	state := RouteCanaryState{}
	// Zero-valued thresholds must fall back to defaults, not to "open on first".
	state, _ = EvaluateRouteCanary(state, RouteCanaryClassificationUpstreamError, RouteCanaryClassificationUpstreamError.Failing(),
		RouteCanaryPerspectivePublicEdge, "502", nil, RouteCanaryThresholds{}, now)
	if state.Open {
		t.Fatal("zero thresholds opened an outage on the first failure")
	}
}

// TestEvaluateRouteCanarySanitizesFailureReason proves credentials cannot reach
// durable route state.
func TestEvaluateRouteCanarySanitizesFailureReason(t *testing.T) {
	now := time.Now()
	secret := "failed with Authorization: Bearer supersecrettokenvalue"
	state, _ := EvaluateRouteCanary(RouteCanaryState{}, RouteCanaryClassificationConnectFailed, RouteCanaryClassificationConnectFailed.Failing(),
		RouteCanaryPerspectivePublicEdge, secret, nil, RouteCanaryThresholds{}, now)
	if state.FailureReason == secret {
		t.Fatal("failure reason was stored unsanitized")
	}
	if got := state.FailureReason; strings.Contains(got, "supersecrettokenvalue") {
		t.Fatalf("bearer token survived sanitization: %q", got)
	}
}

func TestRouteCanaryKeyCoordinateIsStable(t *testing.T) {
	serviceID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	envID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	unitID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	withUnit := RouteCanaryKey{ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID, Hostname: "git.example.net"}
	withoutUnit := RouteCanaryKey{ServiceID: serviceID, EnvironmentID: envID, Hostname: "git.example.net"}

	if withUnit.Coordinate() == withoutUnit.Coordinate() {
		t.Fatal("keys differing by deployment unit produced the same coordinate")
	}
	if withUnit.Coordinate() != withUnit.Coordinate() {
		t.Fatal("coordinate is not stable")
	}
	if withoutUnit.Coordinate() != "route:"+serviceID.String()+":"+envID.String()+":none:git.example.net" {
		t.Fatalf("unexpected coordinate %q", withoutUnit.Coordinate())
	}
}

func TestRouteCanaryTargetURLOmitsDefaultPort(t *testing.T) {
	target := baseTarget()
	if got := target.URL(); got != "https://git.example.net/healthz" {
		t.Fatalf("got %q", got)
	}
	target.Port = 8443
	if got := target.URL(); got != "https://git.example.net:8443/healthz" {
		t.Fatalf("got %q", got)
	}
}

func TestRouteCanaryTargetValidateRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RouteCanaryTarget)
	}{
		{"unknown perspective", func(target *RouteCanaryTarget) { target.Perspective = "sideways" }},
		{"bad scheme", func(target *RouteCanaryTarget) { target.Scheme = "ftp" }},
		{"empty hostname", func(target *RouteCanaryTarget) { target.Hostname = "" }},
		{"port out of range", func(target *RouteCanaryTarget) { target.Port = 70000 }},
		{"relative path", func(target *RouteCanaryTarget) { target.Path = "healthz" }},
		{"inverted status range", func(target *RouteCanaryTarget) { target.ExpectedStatusMin, target.ExpectedStatusMax = 300, 200 }},
		{"non positive timeout", func(target *RouteCanaryTarget) { target.Timeout = 0 }},
		{"negative tls window", func(target *RouteCanaryTarget) { target.TLSMinDaysRemaining = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := baseTarget()
			test.mutate(&target)
			if err := target.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestDescribeRouteObservationNamesTheStaleUpstream(t *testing.T) {
	now := time.Now()
	observation := RouteCanaryObservation{
		Target: baseTarget(), Resolved: true, Connected: true,
		TLS: verifiedTLS(now.Add(90 * 24 * time.Hour)), StatusCode: 502,
	}
	reason := DescribeRouteObservation(observation, RouteCanaryClassificationUpstreamError, now)
	if reason == "" {
		t.Fatal("expected a reason")
	}
	if !strings.Contains(reason, "upstream returned HTTP 502") {
		t.Fatalf("reason does not name the upstream failure: %q", reason)
	}
	if !strings.Contains(reason, "stale or unreachable") {
		t.Fatalf("reason does not describe the stale-origin condition: %q", reason)
	}
}

func TestDescribeRouteObservationIsEmptyWhenHealthy(t *testing.T) {
	now := time.Now()
	observation := RouteCanaryObservation{
		Target: baseTarget(), Resolved: true, Connected: true,
		TLS: verifiedTLS(now.Add(90 * 24 * time.Hour)), StatusCode: 200,
	}
	if reason := DescribeRouteObservation(observation, RouteCanaryClassificationRouteOK, now); reason != "" {
		t.Fatalf("expected no reason for a healthy route, got %q", reason)
	}
}

// --- Regression: the 2026-09-07 live gate finding ---
//
// A route-only attach was submitted for Astillero with a deliberately wrong
// health path, expecting the gate to block it. The gate allowed the deploy.
// A read-only probe of the live host explained why: Astillero serves a
// single-page-application catch-all, so /health and a garbage path both return
// HTTP 200 with a byte-identical body. The gate was not bypassed - it correctly
// observed a route that was serving. The real defect is that a health path
// which discriminates nothing was indistinguishable from a healthy one.

func catchAllObservation(target RouteCanaryTarget) RouteCanaryObservation {
	// Health path and control path answer identically, as the live host does.
	return RouteCanaryObservation{
		Target: target, Resolved: true, Connected: true, StatusCode: 200,
		BodyFingerprint: "2cb952770280df2c",
		TLS: RouteCanaryTLSObservation{
			HandshakeCompleted: true, ChainVerified: true,
			NotAfter: time.Now().Add(90 * 24 * time.Hour),
		},
		Control: &RouteControlObservation{
			Performed: true, Connected: true, StatusCode: 200,
			BodyFingerprint: "2cb952770280df2c",
		},
	}
}

func TestCatchAllHealthPathIsReportedNotSilentlyPassed(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"

	got := ClassifyRouteObservation(catchAllObservation(target), time.Now())
	if got != RouteCanaryClassificationHealthPathNotDiscriminating {
		t.Fatalf("got %q, want health_path_not_discriminating", got)
	}
}

// A discriminating health path must not be flagged: the control path answering
// differently is exactly what proves the check is meaningful.
func TestDiscriminatingHealthPathIsRouteOK(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"

	observation := catchAllObservation(target)
	observation.Control.StatusCode = 404
	observation.Control.BodyFingerprint = "ffffffffffffffff"

	if got := ClassifyRouteObservation(observation, time.Now()); got != RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}

// Differing bodies alone are enough to prove discrimination, so an application
// that returns 200 for everything but varies content is not flagged.
func TestSameStatusDifferentBodyIsDiscriminating(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"

	observation := catchAllObservation(target)
	observation.Control.BodyFingerprint = "0123456789abcdef"

	if got := ClassifyRouteObservation(observation, time.Now()); got != RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}

// By default a catch-all is a warning, not an outage: serving a shell for every
// path is legitimate, and promoting it to an outage would page operators for
// working routes and roll back healthy deployments.
func TestCatchAllIsWarningByDefault(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"
	observations := []RouteCanaryObservation{catchAllObservation(target)}

	reduction, ok := ReduceRouteObservations(observations, time.Now())
	if !ok {
		t.Fatal("expected a reduction")
	}
	if reduction.Classification != RouteCanaryClassificationHealthPathNotDiscriminating {
		t.Fatalf("got %q", reduction.Classification)
	}
	if reduction.Failing {
		t.Fatal("a catch-all must not fail by default; it would roll back working routes")
	}

	state := RouteCanaryState{}
	for i := 0; i < 5; i++ {
		state, _ = EvaluateRouteCanary(state, reduction.Classification, reduction.Failing,
			reduction.Perspective, "catch-all", nil, RouteCanaryThresholds{FailureThreshold: 1, SuccessThreshold: 1}, time.Now())
		if state.Open {
			t.Fatal("catch-all opened an outage by default")
		}
	}
	if state.FailureReason == "" {
		t.Fatal("the warning must still be recorded so an operator can act on it")
	}
}

// An operator who needs the health check to actually mean something opts in,
// and then the same condition blocks.
func TestCatchAllFailsWhenDiscriminationRequired(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"
	target.RequireDiscriminatingHealthPath = true

	reduction, _ := ReduceRouteObservations([]RouteCanaryObservation{catchAllObservation(target)}, time.Now())
	if !reduction.Failing {
		t.Fatal("require_discriminating_health_path must make a catch-all fail")
	}
}

// An explicit body assertion is real evidence about the application, so it
// outranks the catch-all warning.
func TestExplicitBodyAssertionSuppressesCatchAllWarning(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"
	target.ExpectedBodyContains = "astillero-ok"

	observation := catchAllObservation(target)
	observation.BodyMatched = true

	if got := ClassifyRouteObservation(observation, time.Now()); got != RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}

// A genuinely broken route must still outrank the catch-all warning.
func TestUpstreamErrorOutranksCatchAllWarning(t *testing.T) {
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abcd1234"

	observation := catchAllObservation(target)
	observation.StatusCode = 502
	observation.Control.StatusCode = 502

	if got := ClassifyRouteObservation(observation, time.Now()); got != RouteCanaryClassificationUpstreamError {
		t.Fatalf("got %q, want upstream_error", got)
	}
}

func TestRequireDiscriminationWithoutDetectionIsRejected(t *testing.T) {
	policy := RouteCanaryPolicy{
		Enabled: true, ProbeTimeout: time.Second,
		ExpectedStatusMin: 200, ExpectedStatusMax: 299,
		RequireDiscriminatingHealthPath: true, DetectCatchAll: false,
	}
	if err := policy.Validate(); err == nil {
		t.Fatal("requiring discrimination without performing the control probe must be rejected")
	}
}

func TestDeriveAssignsUniqueControlPaths(t *testing.T) {
	policy := RouteCanaryPolicy{
		Enabled: true, ProbeTimeout: time.Second,
		ExpectedStatusMin: 200, ExpectedStatusMax: 299, DetectCatchAll: true,
	}
	plan := &DesiredPublicRoutePlan{Hostname: "git.example.net", Proxy: DesiredPublicRouteProxy{HealthPath: "/healthz"}}

	first, err := DeriveRouteCanaryTargets(plan, policy)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	second, _ := DeriveRouteCanaryTargets(plan, policy)
	if first[0].ControlPath == "" {
		t.Fatal("detect_catch_all must assign a control path")
	}
	// A fixed control path could be cached or special-cased, silently disabling
	// the negative control.
	if first[0].ControlPath == second[0].ControlPath {
		t.Fatal("control paths must be unpredictable across derivations")
	}
	if first[0].ControlURL() != "https://git.example.net"+first[0].ControlPath {
		t.Fatalf("unexpected control URL %q", first[0].ControlURL())
	}
}
