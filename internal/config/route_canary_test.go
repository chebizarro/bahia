package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRouteCanaryConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func routeCanaryInt(value int) *int          { return &value }
func routeCanaryString(value string) *string { return &value }

// TestLoadDecodesRouteCanaryOverridesFromRepoConfig proves per-route overrides
// are repo-configured: they decode from YAML keyed by hostname, dotted hostnames
// survive the koanf key delimiter, and an explicit empty or zero value is kept
// distinct from an unset field.
func TestLoadDecodesRouteCanaryOverridesFromRepoConfig(t *testing.T) {
	// Canaries stay disabled so this test exercises decoding without needing a
	// complete edge routing configuration; validation is covered separately.
	path := writeRouteCanaryConfig(t, `route_canaries:
  enabled: false
  expected_body_contains: "fleet-marker"
  expected_body_regex: '(?s).*"ok".*'
  overrides:
    Git.ShareGap.net.:
      interval: 15s
      probe_timeout: 45s
      expected_status_min: 401
      expected_status_max: 401
      expected_body_contains: ""
      expected_body_regex: '(?s).*"status"\s*:\s*"ok".*'
      tls_min_days_remaining: 0
    arcana.sharegap.net:
      expected_status_max: 399
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	policy := cfg.RouteCanaries.Policy()
	if policy.ExpectedBodyRegex != `(?s).*"ok".*` {
		t.Fatalf("fleet-wide regex not decoded, got %q", policy.ExpectedBodyRegex)
	}

	git, ok := policy.Overrides["git.sharegap.net"]
	if !ok {
		t.Fatalf("override key was not canonicalized; got keys %v", keysOf(policy.Overrides))
	}
	if git.Interval != 15*time.Second || git.ProbeTimeout != 45*time.Second {
		t.Fatalf("durations not decoded: %+v", git)
	}
	if git.ExpectedStatusMin == nil || *git.ExpectedStatusMin != 401 || git.ExpectedStatusMax == nil || *git.ExpectedStatusMax != 401 {
		t.Fatalf("status range not decoded: %+v", git)
	}
	if git.ExpectedBodyContains == nil || *git.ExpectedBodyContains != "" {
		t.Fatal("an explicit empty expected_body_contains must be kept, not treated as unset")
	}
	if git.ExpectedBodyRegex == nil || *git.ExpectedBodyRegex != `(?s).*"status"\s*:\s*"ok".*` {
		t.Fatalf("override regex not decoded: %v", git.ExpectedBodyRegex)
	}
	if git.TLSMinDaysRemaining == nil || *git.TLSMinDaysRemaining != 0 {
		t.Fatal("an explicit zero tls_min_days_remaining must be kept, not treated as unset")
	}

	arcana := policy.Overrides["arcana.sharegap.net"]
	if arcana.Interval != 0 || arcana.ExpectedStatusMin != nil || arcana.ExpectedBodyContains != nil || arcana.TLSMinDaysRemaining != nil {
		t.Fatalf("unset override fields must stay unset so they inherit: %+v", arcana)
	}
	if effective := policy.ForHostname("arcana.sharegap.net"); effective.ExpectedStatusMin != 200 || effective.ExpectedStatusMax != 399 {
		t.Fatalf("effective range %d..%d, want 200..399", effective.ExpectedStatusMin, effective.ExpectedStatusMax)
	}
	if effective := policy.ForHostname("other.sharegap.net"); effective.ExpectedStatusMax != 299 || effective.ExpectedBodyContains != "fleet-marker" {
		t.Fatalf("a route without an override did not get fleet-wide policy: %+v", effective)
	}
}

func keysOf[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// TestLoadRejectsUnknownRouteCanaryOverrideKey proves a misspelled override
// field fails startup instead of leaving the route silently on fleet policy.
func TestLoadRejectsUnknownRouteCanaryOverrideKey(t *testing.T) {
	path := writeRouteCanaryConfig(t, `route_canaries:
  overrides:
    git.sharegap.net:
      expected_status: 401
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown override key")
	}
	want := `unknown route_canaries.overrides[git.sharegap.net] key "expected_status"`
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("Load error = %q, want %q with a hint", err, want)
	}
}

func TestLoadRejectsMalformedRouteCanaryOverrides(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "list instead of map",
			content: "route_canaries:\n  overrides:\n    - hostname: git.sharegap.net\n      interval: 15s\n",
			want:    "must be a map of route hostname",
		},
		{
			name:    "scalar entry",
			content: "route_canaries:\n  overrides:\n    git.sharegap.net: 15s\n",
			want:    "must be a map of override fields",
		},
		{
			name:    "null entry",
			content: "route_canaries:\n  overrides:\n    git.sharegap.net:\n",
			want:    "is empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeRouteCanaryConfig(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRouteCanaryOverrideFieldsCoverEveryDecodedField(t *testing.T) {
	fields := routeCanaryOverrideFields()
	for _, want := range []string{
		"interval", "probe_timeout", "expected_status_min", "expected_status_max",
		"expected_body_contains", "expected_body_regex", "tls_min_days_remaining",
	} {
		if _, ok := fields[want]; !ok {
			t.Fatalf("override field %q is not accepted", want)
		}
	}
}

func enabledRouteCanaryConfig() Config {
	cfg := validEdgeRoutingConfig()
	cfg.RouteCanaries = RouteCanaryConfig{Enabled: true}
	return cfg
}

func TestValidateRouteCanariesAcceptsOverrides(t *testing.T) {
	cfg := enabledRouteCanaryConfig()
	cfg.RouteCanaries.ExpectedBodyRegex = `(?s).*"ok".*`
	cfg.RouteCanaries.Overrides = map[string]RouteCanaryOverrideConfig{
		"git.sharegap.net": {
			Interval:          15 * time.Second,
			ExpectedStatusMin: routeCanaryInt(401),
			ExpectedStatusMax: routeCanaryInt(401),
			ExpectedBodyRegex: routeCanaryString(""),
		},
	}
	if err := cfg.validateRouteCanaries(); err != nil {
		t.Fatalf("validateRouteCanaries: %v", err)
	}
}

// TestValidateRouteCanariesFailsClosedOnUnusableOverrides proves an enabled
// canary configuration whose overrides or regex cannot take effect fails at
// startup rather than silently probing with different expectations.
func TestValidateRouteCanariesFailsClosedOnUnusableOverrides(t *testing.T) {
	tests := []struct {
		name string
		edit func(*RouteCanaryConfig)
		want string
	}{
		{
			name: "invalid fleet-wide regex",
			edit: func(c *RouteCanaryConfig) { c.ExpectedBodyRegex = `(` },
			want: "expected_body_regex",
		},
		{
			name: "overlong fleet-wide regex",
			edit: func(c *RouteCanaryConfig) { c.ExpectedBodyRegex = strings.Repeat("a", 300) },
			want: "byte limit",
		},
		{
			name: "duplicate hostnames after canonicalization",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{
					"git.sharegap.net":  {Interval: time.Minute},
					"GIT.sharegap.net.": {Interval: 2 * time.Minute},
				}
			},
			want: "duplicate entries",
		},
		{
			name: "empty override",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{"git.sharegap.net": {}}
			},
			want: "sets nothing",
		},
		{
			name: "interval below minimum",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{"git.sharegap.net": {Interval: time.Second}}
			},
			want: "minimum",
		},
		{
			name: "override breaks inherited status range",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{"git.sharegap.net": {ExpectedStatusMin: routeCanaryInt(401)}}
			},
			want: "status range",
		},
		{
			name: "override regex matches everything",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{"git.sharegap.net": {ExpectedBodyRegex: routeCanaryString(`(?s).*`)}}
			},
			want: "empty body",
		},
		{
			name: "wildcard hostname",
			edit: func(c *RouteCanaryConfig) {
				c.Overrides = map[string]RouteCanaryOverrideConfig{"*.sharegap.net": {Interval: time.Minute}}
			},
			want: "bare hostname",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := enabledRouteCanaryConfig()
			test.edit(&cfg.RouteCanaries)
			err := cfg.validateRouteCanaries()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRouteCanaries error = %v, want %q", err, test.want)
			}
		})
	}
}
