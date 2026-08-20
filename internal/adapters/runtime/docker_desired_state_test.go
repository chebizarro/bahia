package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Fixed UUIDs for deterministic tests
// ---------------------------------------------------------------------------

var (
	testServiceID     = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	testEnvironmentID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	testArtifactID    = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

func testDesiredSpec() *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        testServiceID,
		EnvironmentID:    testEnvironmentID,
		ArtifactID:       testArtifactID,
		StableServiceKey: "my-api",
		ImageRef:         "registry.example/api:v1.2.3",
		Command:          []string{"/app/server", "--port=8080"},
		Entrypoint:       []string{"/docker-entrypoint.sh"},
		WorkDir:          "/app",
		Env: map[string]string{
			"APP_ENV":   "production",
			"LOG_LEVEL": "info",
		},
		SecretRefs: []domain.DesiredSecretRef{
			{
				EnvVar:        "DB_PASSWORD",
				Name:          "DB_PASSWORD",
				SecretID:      uuid.MustParse("44444444-4444-4444-4444-444444444444"),
				RedactedValue: "REDACTED(DB_PASSWORD)",
			},
			{
				EnvVar:        "API_KEY",
				Name:          "API_KEY",
				SecretID:      uuid.MustParse("55555555-5555-5555-5555-555555555555"),
				RedactedValue: "REDACTED(API_KEY)",
			},
		},
		Ports:   []string{"8080:80", "9090:9090"},
		Volumes: []string{"/data/api:/app/data:ro", "/tmp/cache:/cache"},
		Labels: map[string]string{
			"bahia.managed":        "true",
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.artifact_id":    testArtifactID.String(),
			"bahia.desired_hash":   "sha256:abc123",
			"app.team":             "backend",
		},
		Healthcheck: &domain.HealthcheckConfig{
			Test:        []string{"CMD-SHELL", "curl -f http://localhost:8080/health"},
			Interval:    "30s",
			Timeout:     "5s",
			Retries:     3,
			StartPeriod: "10s",
		},
		RestartPolicy:   "unless-stopped",
		NetworkMode:     "my-network",
		PullPolicy:      "if-not-present",
		DockerExtension: &domain.DockerExtension{},
	}
	spec.ComputeDesiredHash()
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash
	return spec
}

// ---------------------------------------------------------------------------
// BahiaContainerName
// ---------------------------------------------------------------------------

func TestBahiaContainerName(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	name := BahiaContainerName(spec)
	expected := "bahia-22222222-my-api"
	if name != expected {
		t.Errorf("BahiaContainerName() = %q, want %q", name, expected)
	}
}

func TestBahiaContainerNameNilSpec(t *testing.T) {
	t.Parallel()
	if name := BahiaContainerName(nil); name != "" {
		t.Errorf("BahiaContainerName(nil) = %q, want empty", name)
	}
}

func TestBahiaContainerNamePreservesAdoptedRuntimeName(t *testing.T) {
	spec := &domain.DesiredServiceSpec{
		EnvironmentID:    uuid.MustParse("12345678-1234-1234-1234-123456789abc"),
		StableServiceKey: "web",
		DockerExtension:  &domain.DockerExtension{ContainerName: "bahia-web"},
	}
	if name := BahiaContainerName(spec); name != "bahia-web" {
		t.Fatalf("BahiaContainerName() = %q, want adopted name", name)
	}
}

// ---------------------------------------------------------------------------
// MapDesiredSpecToContainerConfig — deterministic output
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToContainerConfig_Deterministic(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	secrets := map[string]string{
		"DB_PASSWORD": "s3cret",
		"API_KEY":     "key-123",
	}

	// Run twice and verify identical output.
	cfg1, err := MapDesiredSpecToContainerConfig(spec, secrets)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	cfg2, err := MapDesiredSpecToContainerConfig(spec, secrets)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	json1, _ := json.Marshal(cfg1)
	json2, _ := json.Marshal(cfg2)

	if string(json1) != string(json2) {
		t.Error("MapDesiredSpecToContainerConfig is not deterministic")
		t.Logf("first:  %s", json1)
		t.Logf("second: %s", json2)
	}
}

func TestMapDesiredSpecToContainerConfig_ContainerConfig(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	secrets := map[string]string{
		"DB_PASSWORD": "s3cret",
		"API_KEY":     "key-123",
	}

	cfg, err := MapDesiredSpecToContainerConfig(spec, secrets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cc := cfg.ContainerConfig

	// Image.
	if cc["Image"] != "registry.example/api:v1.2.3" {
		t.Errorf("Image = %v, want registry.example/api:v1.2.3", cc["Image"])
	}

	// Labels include Bahia labels + custom.
	labels, ok := cc["Labels"].(map[string]string)
	if !ok {
		t.Fatalf("Labels type = %T, want map[string]string", cc["Labels"])
	}
	requiredLabels := []string{
		"bahia.managed", "bahia.service_id", "bahia.environment_id",
		"bahia.artifact_id", "bahia.desired_hash", "bahia.service",
	}
	for _, key := range requiredLabels {
		if labels[key] == "" {
			t.Errorf("missing required label %q", key)
		}
	}
	if labels["bahia.managed"] != "true" {
		t.Errorf("bahia.managed = %q, want true", labels["bahia.managed"])
	}
	if labels["bahia.service"] != "my-api" {
		t.Errorf("bahia.service = %q, want my-api", labels["bahia.service"])
	}
	if labels["app.team"] != "backend" {
		t.Errorf("custom label app.team = %q, want backend", labels["app.team"])
	}

	// Env — sorted, includes secrets, no redacted placeholders.
	env, ok := cc["Env"].([]string)
	if !ok {
		t.Fatalf("Env type = %T, want []string", cc["Env"])
	}
	expectedEnv := []string{
		"API_KEY=key-123",
		"APP_ENV=production",
		"DB_PASSWORD=s3cret",
		"LOG_LEVEL=info",
	}
	if len(env) != len(expectedEnv) {
		t.Fatalf("Env length = %d, want %d: %v", len(env), len(expectedEnv), env)
	}
	for i, want := range expectedEnv {
		if env[i] != want {
			t.Errorf("Env[%d] = %q, want %q", i, env[i], want)
		}
	}

	// Cmd and Entrypoint.
	cmd, _ := cc["Cmd"].([]string)
	if len(cmd) != 2 || cmd[0] != "/app/server" {
		t.Errorf("Cmd = %v, want [/app/server --port=8080]", cmd)
	}
	ep, _ := cc["Entrypoint"].([]string)
	if len(ep) != 1 || ep[0] != "/docker-entrypoint.sh" {
		t.Errorf("Entrypoint = %v, want [/docker-entrypoint.sh]", ep)
	}

	// WorkingDir.
	if cc["WorkingDir"] != "/app" {
		t.Errorf("WorkingDir = %v, want /app", cc["WorkingDir"])
	}

	// ExposedPorts.
	exposed, ok := cc["ExposedPorts"].(map[string]struct{})
	if !ok {
		t.Fatalf("ExposedPorts type = %T", cc["ExposedPorts"])
	}
	if _, ok := exposed["80/tcp"]; !ok {
		t.Error("missing exposed port 80/tcp")
	}
	if _, ok := exposed["9090/tcp"]; !ok {
		t.Error("missing exposed port 9090/tcp")
	}

	// Healthcheck.
	hc, ok := cc["Healthcheck"].(map[string]any)
	if !ok {
		t.Fatalf("Healthcheck type = %T", cc["Healthcheck"])
	}
	test, _ := hc["Test"].([]string)
	if len(test) != 2 || test[0] != "CMD-SHELL" {
		t.Errorf("Healthcheck.Test = %v", test)
	}
	if hc["Interval"] != int64(30_000_000_000) {
		t.Errorf("Healthcheck.Interval = %v, want 30s in ns", hc["Interval"])
	}
	if hc["Retries"] != 3 {
		t.Errorf("Healthcheck.Retries = %v, want 3", hc["Retries"])
	}
}

func TestMapDesiredSpecToContainerConfig_HostConfig(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()

	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hc := cfg.HostConfig

	// RestartPolicy.
	restart, ok := hc["RestartPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("RestartPolicy type = %T", hc["RestartPolicy"])
	}
	if restart["Name"] != "unless-stopped" {
		t.Errorf("RestartPolicy.Name = %v, want unless-stopped", restart["Name"])
	}

	// PortBindings.
	bindings, ok := hc["PortBindings"].(map[string][]dockerPortBinding)
	if !ok {
		t.Fatalf("PortBindings type = %T", hc["PortBindings"])
	}
	if _, ok := bindings["80/tcp"]; !ok {
		t.Error("missing port binding for 80/tcp")
	}
	if _, ok := bindings["9090/tcp"]; !ok {
		t.Error("missing port binding for 9090/tcp")
	}

	// Binds (volumes).
	binds, ok := hc["Binds"].([]string)
	if !ok {
		t.Fatalf("Binds type = %T", hc["Binds"])
	}
	if len(binds) != 2 {
		t.Errorf("Binds length = %d, want 2", len(binds))
	}

	// NetworkMode.
	if hc["NetworkMode"] != "my-network" {
		t.Errorf("NetworkMode = %v, want my-network", hc["NetworkMode"])
	}
}

func TestMapDesiredSpecToContainerConfig_NetworkingConfig(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()

	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nc := cfg.NetworkingConfig
	endpoints, ok := nc["EndpointsConfig"].(map[string]any)
	if !ok {
		t.Fatalf("EndpointsConfig type = %T", nc["EndpointsConfig"])
	}

	netCfg, ok := endpoints["my-network"].(map[string]any)
	if !ok {
		t.Fatalf("network config type = %T", endpoints["my-network"])
	}
	aliases, ok := netCfg["Aliases"].([]string)
	if !ok || len(aliases) != 1 || aliases[0] != "my-api" {
		t.Errorf("Aliases = %v, want [my-api]", aliases)
	}
}

func TestMapDesiredSpecToContainerConfig_HostNetworkMode(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	spec.NetworkMode = "host"

	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Host mode should not produce endpoint configs.
	if len(cfg.NetworkingConfig) != 0 {
		t.Errorf("NetworkingConfig should be empty for host mode, got %v", cfg.NetworkingConfig)
	}
}

func TestMapDesiredSpecToContainerConfig_NilSpec(t *testing.T) {
	t.Parallel()
	_, err := MapDesiredSpecToContainerConfig(nil, nil)
	if err == nil {
		t.Error("expected error for nil spec")
	}
}

// ---------------------------------------------------------------------------
// Secret redaction — no plaintext in persisted state
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToContainerConfig_SecretRedaction(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()

	// Call without providing secrets — secret env vars should NOT appear.
	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env, ok := cfg.ContainerConfig["Env"].([]string)
	if !ok {
		t.Fatalf("Env type = %T", cfg.ContainerConfig["Env"])
	}

	for _, entry := range env {
		if entry == "DB_PASSWORD=REDACTED(DB_PASSWORD)" || entry == "API_KEY=REDACTED(API_KEY)" {
			t.Errorf("redacted placeholder leaked into env: %s", entry)
		}
		// Secret vars should not be present at all when no secrets provided.
		key := entry[:len("DB_PASSWORD")]
		if key == "DB_PASSWORD" || key == "API_KEY=key" {
			// More precise check.
			for _, prefix := range []string{"DB_PASSWORD=", "API_KEY="} {
				if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
					t.Errorf("secret env var present without resolved secret: %s", entry)
				}
			}
		}
	}

	// Only literal env vars should be present.
	expectedKeys := []string{"APP_ENV", "LOG_LEVEL"}
	if len(env) != len(expectedKeys) {
		t.Errorf("Env length = %d, want %d (only literals): %v", len(env), len(expectedKeys), env)
	}
}

func TestMapDesiredSpecToContainerConfig_PartialSecrets(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()

	// Only one of two secrets resolved.
	secrets := map[string]string{"DB_PASSWORD": "s3cret"}

	cfg, err := MapDesiredSpecToContainerConfig(spec, secrets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	env, _ := cfg.ContainerConfig["Env"].([]string)
	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitEnvEntry(e)
		envMap[parts[0]] = parts[1]
	}

	if envMap["DB_PASSWORD"] != "s3cret" {
		t.Errorf("DB_PASSWORD = %q, want s3cret", envMap["DB_PASSWORD"])
	}
	if _, ok := envMap["API_KEY"]; ok {
		t.Errorf("API_KEY should not be present when not in secrets map, got %q", envMap["API_KEY"])
	}
}

// ---------------------------------------------------------------------------
// Env sorting determinism
// ---------------------------------------------------------------------------

func TestBuildDockerEnv_Sorted(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		Env: map[string]string{
			"ZEBRA":  "1",
			"APPLE":  "2",
			"MANGO":  "3",
			"BANANA": "4",
		},
	}
	env := buildDockerEnv(spec, nil)
	sorted := make([]string, len(env))
	copy(sorted, env)
	sort.Strings(sorted)

	for i := range env {
		if env[i] != sorted[i] {
			t.Errorf("env not sorted at index %d: got %q, want %q", i, env[i], sorted[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Healthcheck mapping
// ---------------------------------------------------------------------------

func TestBuildDockerHealthcheck_CombinedDuration(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		Healthcheck: &domain.HealthcheckConfig{
			Test:     []string{"CMD", "true"},
			Interval: "1m30s",
			Timeout:  "10s",
			Retries:  5,
		},
	}

	hc := buildDockerHealthcheck(spec)
	if hc == nil {
		t.Fatal("expected healthcheck, got nil")
	}
	if hc["Interval"] != int64(90_000_000_000) {
		t.Errorf("Interval = %v, want 90s in ns", hc["Interval"])
	}
}

func TestBuildDockerHealthcheck_NilHealthcheck(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{}
	if hc := buildDockerHealthcheck(spec); hc != nil {
		t.Errorf("expected nil healthcheck, got %v", hc)
	}
}

func TestBuildDockerHealthcheck_DockerExtensionOverride(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		Healthcheck: &domain.HealthcheckConfig{
			Test: []string{"CMD", "true"},
		},
		DockerExtension: &domain.DockerExtension{
			Healthcheck: map[string]any{
				"Test":     []string{"CMD", "custom-check"},
				"Interval": int64(5_000_000_000),
			},
		},
	}
	hc := buildDockerHealthcheck(spec)
	test, _ := hc["Test"].([]string)
	if len(test) != 2 || test[1] != "custom-check" {
		t.Errorf("expected Docker extension healthcheck override, got %v", hc)
	}
}

// ---------------------------------------------------------------------------
// Pull policy
// ---------------------------------------------------------------------------

func TestShouldPullImage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		pullPolicy   string
		existingHash string
		specHash     string
		want         bool
	}{
		{"always pulls", "always", "sha256:abc", "sha256:abc", true},
		{"never pulls", "never", "", "", false},
		{"default pulls on empty existing", "", "", "sha256:new", true},
		{"default pulls on hash mismatch", "if-not-present", "sha256:old", "sha256:new", true},
		{"default skips on hash match", "if-not-present", "sha256:same", "sha256:same", false},
		{"default skips on hash match (empty policy)", "", "sha256:same", "sha256:same", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := &domain.DesiredServiceSpec{
				PullPolicy:  tt.pullPolicy,
				DesiredHash: tt.specHash,
			}
			got := ShouldPullImage(spec, tt.existingHash)
			if got != tt.want {
				t.Errorf("ShouldPullImage() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Desired hash comparison
// ---------------------------------------------------------------------------

func TestContainerDesiredHashMatches(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()

	matching := &DockerContainer{
		Labels: map[string]string{"bahia.desired_hash": spec.DesiredHash},
	}
	if !ContainerDesiredHashMatches(matching, spec) {
		t.Error("expected hash match")
	}

	mismatched := &DockerContainer{
		Labels: map[string]string{"bahia.desired_hash": "sha256:different"},
	}
	if ContainerDesiredHashMatches(mismatched, spec) {
		t.Error("expected hash mismatch")
	}

	noLabel := &DockerContainer{Labels: map[string]string{}}
	if ContainerDesiredHashMatches(noLabel, spec) {
		t.Error("expected no match when label missing")
	}

	if ContainerDesiredHashMatches(nil, spec) {
		t.Error("expected no match for nil container")
	}
}

// ---------------------------------------------------------------------------
// Labels include all required Bahia labels
// ---------------------------------------------------------------------------

func TestBuildDockerLabels_AllRequired(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	labels := buildDockerLabels(spec)

	required := map[string]string{
		"bahia.managed":        "true",
		"bahia.service_id":     testServiceID.String(),
		"bahia.environment_id": testEnvironmentID.String(),
		"bahia.artifact_id":    testArtifactID.String(),
		"bahia.desired_hash":   spec.DesiredHash,
		"bahia.service":        "my-api",
	}

	for key, want := range required {
		got := labels[key]
		if got != want {
			t.Errorf("label %q = %q, want %q", key, got, want)
		}
	}
}

func TestBuildDockerLabels_PreservesCustomLabels(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	labels := buildDockerLabels(spec)
	if labels["app.team"] != "backend" {
		t.Errorf("custom label app.team = %q, want backend", labels["app.team"])
	}
}

func TestBuildDockerLabels_LegacyServiceLabel(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	// If bahia.service is already set by the caller, it should not be overwritten.
	spec.Labels["bahia.service"] = "custom-name"
	labels := buildDockerLabels(spec)
	if labels["bahia.service"] != "custom-name" {
		t.Errorf("bahia.service = %q, want custom-name (should not overwrite)", labels["bahia.service"])
	}
}

// ---------------------------------------------------------------------------
// Volatile fields excluded from hash
// ---------------------------------------------------------------------------

func TestVolatileFieldsExcludedFromHash(t *testing.T) {
	t.Parallel()
	spec1 := testDesiredSpec()
	spec2 := testDesiredSpec()

	// Modify volatile/extension fields that should NOT affect the hash.
	spec2.DockerExtension = &domain.DockerExtension{
		HostConfig:       map[string]any{"Memory": 1024},
		NetworkingConfig: map[string]any{"custom": true},
	}

	hash1 := spec1.ComputeDesiredHash()
	hash2 := spec2.ComputeDesiredHash()

	if hash1 != hash2 {
		t.Errorf("volatile fields affected hash: %q != %q", hash1, hash2)
	}
}

// ---------------------------------------------------------------------------
// FindBahiaManagedContainer — label-preferred lookup
// ---------------------------------------------------------------------------

func TestFindBahiaManagedContainer_PrefersLabels(t *testing.T) {
	t.Parallel()

	labelContainer := DockerContainer{
		ID:    "label-match-id",
		Names: []string{"/some-other-name"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
		},
	}
	nameContainer := DockerContainer{
		ID:    "name-match-id",
		Names: []string{"/bahia-22222222-my-api"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters := r.URL.Query().Get("filters")
		w.Header().Set("Content-Type", "application/json")
		if filters != "" {
			// Label-based query — return the label-matched container.
			json.NewEncoder(w).Encode([]DockerContainer{labelContainer})
		} else {
			// All containers query — return the name-matched container.
			json.NewEncoder(w).Encode([]DockerContainer{nameContainer})
		}
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	spec := testDesiredSpec()
	found, err := FindBahiaManagedContainer(context.Background(), observer, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected container, got nil")
	}
	// Should prefer label match over name match.
	if found.ID != "label-match-id" {
		t.Errorf("expected label-matched container, got ID %q", found.ID)
	}
}

func TestFindBahiaManagedContainer_FallsBackToName(t *testing.T) {
	t.Parallel()

	nameContainer := DockerContainer{
		ID:    "name-match-id",
		Names: []string{"/bahia-22222222-my-api"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		filters := r.URL.Query().Get("filters")
		if filters != "" {
			// Label query returns empty.
			json.NewEncoder(w).Encode([]DockerContainer{})
		} else {
			json.NewEncoder(w).Encode([]DockerContainer{nameContainer})
		}
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	spec := testDesiredSpec()
	found, err := FindBahiaManagedContainer(context.Background(), observer, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected container from name fallback, got nil")
	}
	if found.ID != "name-match-id" {
		t.Errorf("expected name-matched container, got ID %q", found.ID)
	}
}

func TestFindBahiaManagedContainer_NoMatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]DockerContainer{})
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	spec := testDesiredSpec()
	found, err := FindBahiaManagedContainer(context.Background(), observer, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil, got container %+v", found)
	}
}

func TestFindBahiaManagedContainer_NilInputs(t *testing.T) {
	t.Parallel()
	found, err := FindBahiaManagedContainer(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil for nil inputs, got %+v", found)
	}
}

// ---------------------------------------------------------------------------
// Duration parsing
// ---------------------------------------------------------------------------

func TestParseDurationToNanoseconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int64
	}{
		{"30s", 30_000_000_000},
		{"5m", 300_000_000_000},
		{"1m30s", 90_000_000_000},
		{"10", 10_000_000_000},
		{"0s", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := parseDurationToNanoseconds(tt.input)
			if err != nil {
				t.Fatalf("parseDurationToNanoseconds(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseDurationToNanoseconds(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDurationToNanoseconds_Invalid(t *testing.T) {
	t.Parallel()
	invalids := []string{"", "abc", "m30s"}
	for _, input := range invalids {
		t.Run(fmt.Sprintf("invalid_%s", input), func(t *testing.T) {
			t.Parallel()
			_, err := parseDurationToNanoseconds(input)
			if err == nil {
				t.Errorf("expected error for input %q", input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Minimal spec (no optional fields)
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToContainerConfig_MinimalSpec(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        testServiceID,
		EnvironmentID:    testEnvironmentID,
		ArtifactID:       testArtifactID,
		StableServiceKey: "minimal-svc",
		ImageRef:         "alpine:latest",
		Labels: map[string]string{
			"bahia.managed":        "true",
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.artifact_id":    testArtifactID.String(),
		},
	}
	spec.ComputeDesiredHash()
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash

	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have image and labels at minimum.
	if cfg.ContainerConfig["Image"] != "alpine:latest" {
		t.Errorf("Image = %v, want alpine:latest", cfg.ContainerConfig["Image"])
	}

	// No Env, Cmd, Entrypoint, ExposedPorts, Healthcheck, Binds, PortBindings.
	for _, key := range []string{"Env", "Cmd", "Entrypoint", "ExposedPorts", "Healthcheck", "WorkingDir"} {
		if cfg.ContainerConfig[key] != nil {
			t.Errorf("ContainerConfig[%q] should be nil for minimal spec, got %v", key, cfg.ContainerConfig[key])
		}
	}
	for _, key := range []string{"Binds", "PortBindings"} {
		if cfg.HostConfig[key] != nil {
			t.Errorf("HostConfig[%q] should be nil for minimal spec, got %v", key, cfg.HostConfig[key])
		}
	}
}

// ---------------------------------------------------------------------------
// Docker extension host config merge
// ---------------------------------------------------------------------------

func TestMapDesiredSpecToContainerConfig_DockerExtensionHostConfig(t *testing.T) {
	t.Parallel()
	spec := testDesiredSpec()
	spec.DockerExtension = &domain.DockerExtension{
		HostConfig: map[string]any{
			"Memory":      int64(536870912), // 512MB
			"NetworkMode": "should-not-overwrite",
		},
	}

	cfg, err := MapDesiredSpecToContainerConfig(spec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Extension's Memory should be present.
	if cfg.HostConfig["Memory"] != int64(536870912) {
		t.Errorf("Memory = %v, want 536870912", cfg.HostConfig["Memory"])
	}

	// Extension should NOT overwrite core NetworkMode.
	if cfg.HostConfig["NetworkMode"] != "my-network" {
		t.Errorf("NetworkMode = %v, want my-network (should not be overwritten)", cfg.HostConfig["NetworkMode"])
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func splitEnvEntry(entry string) [2]string {
	idx := len(entry)
	for i, ch := range entry {
		if ch == '=' {
			idx = i
			break
		}
	}
	if idx >= len(entry) {
		return [2]string{entry, ""}
	}
	return [2]string{entry[:idx], entry[idx+1:]}
}
