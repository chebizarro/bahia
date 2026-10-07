package domain

import (
	"strings"
	"testing"
	"time"
)

func canaryOverrideInt(value int) *int          { return &value }
func canaryOverrideString(value string) *string { return &value }

const jsonStatusOKRegex = `(?s).*"status"\s*:\s*"ok".*`

// TestCompileRouteCanaryBodyRegexAnchorsTheWholeBody pins the anchoring
// contract: a pattern must match the entire bounded body, so it can never pass
// by matching an incidental fragment.
func TestCompileRouteCanaryBodyRegexAnchorsTheWholeBody(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		body    string
		want    bool
	}{
		{"bare pattern matches only the whole body", `ok`, "ok", true},
		{"bare pattern does not match a fragment", `ok`, "not ok", false},
		{"bare pattern does not match a prefix", `ok`, "ok, mostly", false},
		{"explicit wildcards opt into partial matching", `.*ok.*`, "service ok today", true},
		{"(?m) cannot weaken the anchors into line anchors", `(?m)^ok$`, "broken\nok\nbroken", false},
		{"alternation stays inside the anchors", `ok|ready`, "not ready", false},
		{"json field in declared order", jsonStatusOKRegex, `{"status":"ok","version":"1.2"}`, true},
		{"json field in a different order", jsonStatusOKRegex, `{"version":"1.2","status":"ok"}`, true},
		{"pretty-printed json", jsonStatusOKRegex, "{\n  \"version\": \"1.2\",\n  \"status\" : \"ok\"\n}\n", true},
		{"json field with a different value", jsonStatusOKRegex, `{"status":"degraded"}`, false},
		{"json field only mentioned in a different key", jsonStatusOKRegex, `{"last_status":"error","note":"ok"}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern, err := CompileRouteCanaryBodyRegex(test.pattern)
			if err != nil {
				t.Fatalf("compile %q: %v", test.pattern, err)
			}
			if got := pattern.MatchString(test.body); got != test.want {
				t.Fatalf("pattern %q against %q: got %v, want %v", test.pattern, test.body, got, test.want)
			}
		})
	}
}

// TestCompileRouteCanaryBodyRegexRejectsUnsafePatterns proves the pattern is
// bounded and validated before it can reach a probe.
func TestCompileRouteCanaryBodyRegexRejectsUnsafePatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{"empty", "", "empty"},
		{"too long", strings.Repeat("a", MaxRouteCanaryBodyRegexLength+1), "byte limit"},
		{"invalid syntax", `(`, "not a valid RE2 pattern"},
		{"backreference is not RE2", `(a)\1`, "not a valid RE2 pattern"},
		{"lookahead is not RE2", `(?=ok)`, "not a valid RE2 pattern"},
		// Wrapping this in the anchoring group would otherwise yield
		// \A(?:a)|(b)\z, which matches any body that ends in b.
		{"unbalanced group cannot escape the anchors", `a)|(b`, "not a valid RE2 pattern"},
		{"unterminated quote cannot swallow the anchors", `\Qok`, "cannot be anchored"},
		{"compiled program too large", `.{1000}.{1000}.{1000}`, "instruction limit"},
		{"matches everything", `(?s).*`, "matches an empty body"},
		{"matches an empty body", `(ok)?`, "matches an empty body"},
		{"invalid utf-8", "ok\xff", "UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileRouteCanaryBodyRegex(test.pattern)
			if err == nil {
				t.Fatalf("pattern %q was accepted", test.pattern)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("pattern %q: error %q does not mention %q", test.pattern, err, test.want)
			}
		})
	}
}

// TestCompileRouteCanaryBodyRegexIsLinearTime documents why no backtracking
// guard is needed: the textbook catastrophic pattern is accepted and evaluated
// by RE2 in linear time against a maximal adversarial body.
func TestCompileRouteCanaryBodyRegexIsLinearTime(t *testing.T) {
	pattern, err := CompileRouteCanaryBodyRegex(`(a+)+b`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if pattern.MatchString(strings.Repeat("a", 512)) {
		t.Fatal("pattern unexpectedly matched")
	}
}

func TestMatchBodyRequiresEveryConfiguredAssertion(t *testing.T) {
	body := []byte(`{"status":"ok","service":"gitea"}`)
	tests := []struct {
		name     string
		contains string
		regex    string
		want     bool
	}{
		{"no assertion", "", "", true},
		{"substring holds", "gitea", "", true},
		{"substring fails", "grafana", "", false},
		{"regex holds", "", jsonStatusOKRegex, true},
		{"regex fails", "", `(?s).*"status"\s*:\s*"down".*`, false},
		{"both hold", "gitea", jsonStatusOKRegex, true},
		{"substring holds but regex fails", "gitea", `(?s).*"status"\s*:\s*"down".*`, false},
		{"regex holds but substring fails", "grafana", jsonStatusOKRegex, false},
		{"invalid regex fails closed", "", `(`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := baseTarget()
			target.ExpectedBodyContains = test.contains
			target.ExpectedBodyRegex = test.regex
			if got := target.MatchBody(body); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestRouteCanaryTargetValidateRejectsInvalidBodyRegex(t *testing.T) {
	target := baseTarget()
	target.ExpectedBodyRegex = `(?s).*`
	if err := target.Validate(); err == nil || !strings.Contains(err.Error(), "expected_body_regex") {
		t.Fatalf("expected an expected_body_regex validation error, got %v", err)
	}
}

// TestClassifyRegexMismatchReusesBodyMismatch proves a regex failure shares the
// existing body_mismatch classification, so no new database constraint value is
// needed, and that its reason names the regex rather than a marker.
func TestClassifyRegexMismatchReusesBodyMismatch(t *testing.T) {
	now := time.Now()
	target := baseTarget()
	target.ExpectedBodyRegex = jsonStatusOKRegex
	observation := RouteCanaryObservation{
		Target: target, Resolved: true, Connected: true, StatusCode: 200,
		TLS:         verifiedTLS(now.Add(90 * 24 * time.Hour)),
		BodyMatched: false,
	}
	classification := ClassifyRouteObservation(observation, now)
	if classification != RouteCanaryClassificationBodyMismatch {
		t.Fatalf("got %q, want body_mismatch", classification)
	}
	reason := DescribeRouteObservation(observation, classification, now)
	if !strings.Contains(reason, "expected_body_regex") {
		t.Fatalf("reason does not name the regex assertion: %q", reason)
	}

	observation.BodyMatched = true
	if got := ClassifyRouteObservation(observation, now); got != RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q for a held regex, want route_ok", got)
	}
}

// TestClassifyHeldRegexOutranksCatchAllWarning proves a regex assertion is real
// evidence about the application, exactly like the substring form.
func TestClassifyHeldRegexOutranksCatchAllWarning(t *testing.T) {
	now := time.Now()
	target := baseTarget()
	target.ControlPath = "/.bahia-route-canary-control/abc"
	observation := RouteCanaryObservation{
		Target: target, Resolved: true, Connected: true, StatusCode: 200,
		TLS:             verifiedTLS(now.Add(90 * 24 * time.Hour)),
		BodyFingerprint: "same",
		Control:         &RouteControlObservation{Performed: true, Connected: true, StatusCode: 200, BodyFingerprint: "same"},
	}
	if got := ClassifyRouteObservation(observation, now); got != RouteCanaryClassificationHealthPathNotDiscriminating {
		t.Fatalf("precondition: got %q, want health_path_not_discriminating", got)
	}
	observation.Target.ExpectedBodyRegex = jsonStatusOKRegex
	observation.BodyMatched = true
	if got := ClassifyRouteObservation(observation, now); got != RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok once a regex assertion held", got)
	}
}

func overriddenPolicy() RouteCanaryPolicy {
	policy := enabledPolicy()
	policy.ExpectedBodyContains = "fleet-marker"
	policy.Overrides = map[string]RouteCanaryOverride{
		"git.sharegap.net": {
			Interval:             15 * time.Second,
			ProbeTimeout:         45 * time.Second,
			ExpectedStatusMin:    canaryOverrideInt(401),
			ExpectedStatusMax:    canaryOverrideInt(401),
			ExpectedBodyContains: canaryOverrideString(""),
			ExpectedBodyRegex:    canaryOverrideString(jsonStatusOKRegex),
			TLSMinDaysRemaining:  canaryOverrideInt(0),
		},
	}
	return policy
}

// TestDeriveRouteCanaryTargetsAppliesOverrideOnlyToItsRoute is the bahia-6xztt
// acceptance criterion at the domain layer: one route's override changes that
// route's targets, from every perspective, and no other route's.
func TestDeriveRouteCanaryTargetsAppliesOverrideOnlyToItsRoute(t *testing.T) {
	policy := overriddenPolicy()

	plan := planWithInternal(true)
	// The override key is canonical; the plan hostname need not be.
	plan.Hostname = "Git.ShareGap.net."
	targets, err := DeriveRouteCanaryTargets(plan, policy)
	if err != nil {
		t.Fatalf("derive overridden route: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected public and internal targets, got %d", len(targets))
	}
	for _, target := range targets {
		if target.ExpectedStatusMin != 401 || target.ExpectedStatusMax != 401 {
			t.Fatalf("%s: status range %d..%d, want 401..401", target.Perspective, target.ExpectedStatusMin, target.ExpectedStatusMax)
		}
		if target.ExpectedBodyContains != "" {
			t.Fatalf("%s: explicit empty override did not clear the fleet marker, got %q", target.Perspective, target.ExpectedBodyContains)
		}
		if target.ExpectedBodyRegex != jsonStatusOKRegex {
			t.Fatalf("%s: regex %q not applied", target.Perspective, target.ExpectedBodyRegex)
		}
		if target.TLSMinDaysRemaining != 0 {
			t.Fatalf("%s: explicit zero did not disable the expiry window, got %d", target.Perspective, target.TLSMinDaysRemaining)
		}
		if target.Timeout != 45*time.Second {
			t.Fatalf("%s: timeout %s, want 45s", target.Perspective, target.Timeout)
		}
	}

	other := planWithInternal(false)
	other.Hostname = "arcana.sharegap.net"
	otherTargets, err := DeriveRouteCanaryTargets(other, policy)
	if err != nil {
		t.Fatalf("derive other route: %v", err)
	}
	fleet := otherTargets[0]
	if fleet.ExpectedStatusMin != 200 || fleet.ExpectedStatusMax != 299 ||
		fleet.ExpectedBodyContains != "fleet-marker" || fleet.ExpectedBodyRegex != "" ||
		fleet.TLSMinDaysRemaining != 14 || fleet.Timeout != 10*time.Second {
		t.Fatalf("override leaked to another route: %+v", fleet)
	}
}

func TestForHostnameInheritsUnsetFields(t *testing.T) {
	policy := enabledPolicy()
	policy.ExpectedBodyContains = "fleet-marker"
	policy.Overrides = map[string]RouteCanaryOverride{
		"git.sharegap.net": {ExpectedStatusMax: canaryOverrideInt(399)},
	}
	effective := policy.ForHostname("git.sharegap.net")
	if effective.ExpectedStatusMin != 200 || effective.ExpectedStatusMax != 399 {
		t.Fatalf("got range %d..%d, want 200..399", effective.ExpectedStatusMin, effective.ExpectedStatusMax)
	}
	if effective.ExpectedBodyContains != "fleet-marker" || effective.TLSMinDaysRemaining != 14 || effective.ProbeTimeout != 10*time.Second {
		t.Fatalf("unset override fields did not inherit: %+v", effective)
	}
	if effective.Overrides != nil {
		t.Fatal("an effective policy must not carry overrides of its own")
	}
}

func TestProbeIntervalFor(t *testing.T) {
	policy := overriddenPolicy()
	if interval, ok := policy.ProbeIntervalFor("GIT.sharegap.net."); !ok || interval != 15*time.Second {
		t.Fatalf("got %s, %v; want 15s, true", interval, ok)
	}
	if _, ok := policy.ProbeIntervalFor("arcana.sharegap.net"); ok {
		t.Fatal("a route without an override must inherit the fleet-wide interval")
	}
	policy.Overrides["arcana.sharegap.net"] = RouteCanaryOverride{ExpectedStatusMax: canaryOverrideInt(399)}
	if _, ok := policy.ProbeIntervalFor("arcana.sharegap.net"); ok {
		t.Fatal("an override without an interval must inherit the fleet-wide interval")
	}
}

// TestRouteCanaryPolicyValidateRejectsUnusableOverrides proves an override that
// cannot take effect fails validation instead of silently doing nothing.
func TestRouteCanaryPolicyValidateRejectsUnusableOverrides(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		override RouteCanaryOverride
		fleet    func(*RouteCanaryPolicy)
		want     string
	}{
		{name: "empty override", hostname: "git.sharegap.net", override: RouteCanaryOverride{}, want: "sets nothing"},
		{name: "non-canonical key", hostname: "Git.sharegap.net", override: RouteCanaryOverride{Interval: time.Minute}, want: "normalized"},
		{name: "wildcard key", hostname: "*.sharegap.net", override: RouteCanaryOverride{Interval: time.Minute}, want: "bare hostname"},
		{name: "url key", hostname: "https://git.sharegap.net", override: RouteCanaryOverride{Interval: time.Minute}, want: "bare hostname"},
		{name: "empty label", hostname: "git..sharegap.net", override: RouteCanaryOverride{Interval: time.Minute}, want: "empty DNS label"},
		{name: "interval below minimum", hostname: "git.sharegap.net", override: RouteCanaryOverride{Interval: time.Second}, want: "minimum"},
		{name: "negative interval", hostname: "git.sharegap.net", override: RouteCanaryOverride{Interval: -time.Minute}, want: "negative"},
		{name: "negative timeout", hostname: "git.sharegap.net", override: RouteCanaryOverride{ProbeTimeout: -time.Second}, want: "negative"},
		{name: "min above inherited max", hostname: "git.sharegap.net", override: RouteCanaryOverride{ExpectedStatusMin: canaryOverrideInt(401)}, want: "status range"},
		{name: "status out of range", hostname: "git.sharegap.net", override: RouteCanaryOverride{ExpectedStatusMax: canaryOverrideInt(600)}, want: "status range"},
		{name: "negative tls window", hostname: "git.sharegap.net", override: RouteCanaryOverride{TLSMinDaysRemaining: canaryOverrideInt(-1)}, want: "tls_min_days_remaining"},
		{name: "invalid regex", hostname: "git.sharegap.net", override: RouteCanaryOverride{ExpectedBodyRegex: canaryOverrideString(`(`)}, want: "expected_body_regex"},
		{name: "vacuous regex", hostname: "git.sharegap.net", override: RouteCanaryOverride{ExpectedBodyRegex: canaryOverrideString(`(?s).*`)}, want: "empty body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := enabledPolicy()
			policy.Overrides = map[string]RouteCanaryOverride{test.hostname: test.override}
			err := policy.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

func TestRouteCanaryPolicyValidateRejectsInvalidFleetRegex(t *testing.T) {
	policy := enabledPolicy()
	policy.ExpectedBodyRegex = `a)|(b`
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "expected_body_regex") {
		t.Fatalf("expected an expected_body_regex error, got %v", err)
	}
}

// TestOverrideCanRemoveFleetRegexForItsRoute proves an explicit empty regex
// removes a fleet-wide pattern for that route and no other.
func TestOverrideCanRemoveFleetRegexForItsRoute(t *testing.T) {
	policy := enabledPolicy()
	policy.ExpectedBodyRegex = jsonStatusOKRegex
	policy.Overrides = map[string]RouteCanaryOverride{
		"git.sharegap.net": {ExpectedBodyRegex: canaryOverrideString("")},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := policy.ForHostname("git.sharegap.net").ExpectedBodyRegex; got != "" {
		t.Fatalf("explicit empty override did not remove the fleet regex, got %q", got)
	}
	if got := policy.ForHostname("arcana.sharegap.net").ExpectedBodyRegex; got != jsonStatusOKRegex {
		t.Fatalf("fleet regex lost for a route without an override, got %q", got)
	}
}
