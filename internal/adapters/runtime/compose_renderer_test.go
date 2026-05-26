package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fixedUUID returns a deterministic UUID from a short seed for test stability.
func fixedUUID(seed string) uuid.UUID {
	// Pad seed to 16 bytes and use as UUID bytes.
	padded := seed + strings.Repeat("\x00", 16-len(seed))
	id, _ := uuid.FromBytes([]byte(padded[:16]))
	return id
}

// testPlan builds a canonical test plan with two services, networks, volumes,
// healthchecks, depends_on, labels, ports, env, secrets, and all fields
// exercised.
func testPlan() *domain.DesiredEnvironmentPlan {
	envID := fixedUUID("env-test-001\x00")
	svcA := fixedUUID("svc-api-00001")
	svcB := fixedUUID("svc-web-00001")
	artA := fixedUUID("art-api-00001")
	artB := fixedUUID("art-web-00001")

	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        svcB,
				EnvironmentID:    envID,
				ArtifactID:       artB,
				StableServiceKey: "web-frontend",
				ImageRef:         "registry.example.com/web:v2.1.0",
				Command:          []string{"nginx", "-g", "daemon off;"},
				WorkDir:          "/usr/share/nginx",
				Env: map[string]string{
					"NODE_ENV":  "production",
					"API_URL":   "http://api:8080",
					"LOG_LEVEL": "info",
				},
				SecretRefs: []domain.DesiredSecretRef{
					{
						EnvVar:        "SESSION_SECRET",
						Name:          "session-secret",
						SecretID:      fixedUUID("sec-sess-0001"),
						RedactedValue: domain.RedactedPlaceholder("session-secret"),
					},
				},
				Ports:         []string{"443:443", "80:80"},
				Volumes:       []string{"/data/web/static:/usr/share/nginx/html:ro", "/data/web/certs:/etc/nginx/certs:ro"},
				Labels: map[string]string{
					"bahia.managed":      "true",
					"bahia.service_id":   svcB.String(),
					"bahia.environment":  "production",
				},
				Healthcheck: &domain.HealthcheckConfig{
					Test:        []string{"CMD", "curl", "-f", "http://localhost:80/health"},
					Interval:    "30s",
					Timeout:     "5s",
					Retries:     3,
					StartPeriod: "10s",
				},
				DependsOn:     []string{"api-server"},
				RestartPolicy: "unless-stopped",
				PullPolicy:    "if-not-present",
				ComposeExtension: &domain.ComposeExtension{
					ProjectName: "bahia-production",
					DependsOn: map[string]domain.ComposeDependency{
						"api-server": {Condition: "service_healthy"},
					},
					EnvFile:            ".bahia/env/web-frontend.env",
					Networks:           []string{"frontend", "backend"},
					VolumeDeclarations: []string{"web-static"},
				},
			},
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        svcA,
				EnvironmentID:    envID,
				ArtifactID:       artA,
				StableServiceKey: "api-server",
				ImageRef:         "registry.example.com/api:v3.0.1",
				Command:          []string{"./api-server", "--port=8080"},
				Entrypoint:       []string{"/entrypoint.sh"},
				WorkDir:          "/app",
				Env: map[string]string{
					"PORT":      "8080",
					"LOG_LEVEL": "debug",
				},
				SecretRefs: []domain.DesiredSecretRef{
					{
						EnvVar:        "DB_PASSWORD",
						Name:          "db-password",
						SecretID:      fixedUUID("sec-dbpw-0001"),
						RedactedValue: domain.RedactedPlaceholder("db-password"),
					},
					{
						EnvVar:        "API_KEY",
						Name:          "api-key",
						SecretID:      fixedUUID("sec-akey-0001"),
						RedactedValue: domain.RedactedPlaceholder("api-key"),
					},
				},
				Ports:   []string{"8080:8080"},
				Volumes: []string{"api-data:/app/data"},
				Labels: map[string]string{
					"bahia.managed":      "true",
					"bahia.service_id":   svcA.String(),
					"bahia.environment":  "production",
					"bahia.artifact_id":  artA.String(),
				},
				Healthcheck: &domain.HealthcheckConfig{
					Test:     []string{"CMD-SHELL", "curl -f http://localhost:8080/healthz || exit 1"},
					Interval: "15s",
					Timeout:  "3s",
					Retries:  5,
				},
				RestartPolicy: "always",
				ComposeExtension: &domain.ComposeExtension{
					Networks:           []string{"backend"},
					VolumeDeclarations: []string{"api-data"},
				},
			},
		},
	}

	// Compute hashes for deterministic output.
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	return plan
}

// ---------------------------------------------------------------------------
// Golden file helpers
// ---------------------------------------------------------------------------

const goldenDir = "testdata/golden"

func goldenPath(name string) string {
	return filepath.Join(goldenDir, name)
}

// updateGolden writes golden file content. Set UPDATE_GOLDEN=1 to regenerate.
func shouldUpdateGolden() bool {
	return os.Getenv("UPDATE_GOLDEN") == "1"
}

func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	path := goldenPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write golden file %s: %v", name, err)
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("read golden file %s: %v (run with UPDATE_GOLDEN=1 to generate)", name, err)
	}
	return data
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	if shouldUpdateGolden() {
		writeGolden(t, name, got)
		t.Logf("updated golden file: %s", name)
		return
	}
	want := readGolden(t, name)
	if string(got) != string(want) {
		t.Errorf("output does not match golden file %s\n--- want ---\n%s\n--- got ---\n%s",
			name, string(want), string(got))
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestComposeRenderer_RenderEnvironmentPlan_DeterministicOutput(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result1, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}

	// Render again — output must be identical.
	result2, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}

	if string(result1.ComposeYAML) != string(result2.ComposeYAML) {
		t.Error("ComposeYAML is not deterministic across renders")
		t.Logf("render1:\n%s", result1.ComposeYAML)
		t.Logf("render2:\n%s", result2.ComposeYAML)
	}

	if result1.Metadata.ContentHash != result2.Metadata.ContentHash {
		t.Errorf("ContentHash differs: %s vs %s", result1.Metadata.ContentHash, result2.Metadata.ContentHash)
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_GoldenYAML(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	assertGolden(t, "full_project_compose.yml", result.ComposeYAML)
}

func TestComposeRenderer_RenderEnvironmentPlan_GoldenEnvMaterial(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Check env material for api-server.
	apiEnv, ok := result.EnvMaterial["api-server"]
	if !ok {
		t.Fatal("missing env material for api-server")
	}
	assertGolden(t, "api-server.env", []byte(apiEnv))

	// Check env material for web-frontend.
	webEnv, ok := result.EnvMaterial["web-frontend"]
	if !ok {
		t.Fatal("missing env material for web-frontend")
	}
	assertGolden(t, "web-frontend.env", []byte(webEnv))
}

func TestComposeRenderer_RenderEnvironmentPlan_ExplicitProjectName(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if result.Metadata.ProjectName != "bahia-production" {
		t.Errorf("expected project name 'bahia-production', got %q", result.Metadata.ProjectName)
	}

	// Verify it appears in the YAML.
	if !strings.Contains(string(result.ComposeYAML), "name: bahia-production") {
		t.Error("project name not found in rendered YAML")
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_ProjectNameFallback(t *testing.T) {
	renderer := NewComposeRenderer()
	envID := fixedUUID("env-fallback\x00")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-minimal01"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-minimal01"),
				StableServiceKey: "minimal",
				ImageRef:         "alpine:latest",
				RestartPolicy:    "no",
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Should fall back to bahia-<short-env-id>.
	if !strings.HasPrefix(result.Metadata.ProjectName, "bahia-") {
		t.Errorf("expected project name to start with 'bahia-', got %q", result.Metadata.ProjectName)
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_ServiceOrdering(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// api-server should come before web-frontend in sorted output.
	yaml := string(result.ComposeYAML)
	apiIdx := strings.Index(yaml, "api-server:")
	webIdx := strings.Index(yaml, "web-frontend:")
	if apiIdx < 0 || webIdx < 0 {
		t.Fatal("service keys not found in YAML")
	}
	if apiIdx >= webIdx {
		t.Error("api-server should appear before web-frontend in sorted output")
	}

	// Metadata service keys should be sorted.
	if len(result.Metadata.ServiceKeys) != 2 {
		t.Fatalf("expected 2 service keys, got %d", len(result.Metadata.ServiceKeys))
	}
	if result.Metadata.ServiceKeys[0] != "api-server" || result.Metadata.ServiceKeys[1] != "web-frontend" {
		t.Errorf("service keys not sorted: %v", result.Metadata.ServiceKeys)
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_SecretRedaction(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)

	// Secrets should NOT appear as plaintext in the YAML.
	// (They're only in env material for file writing.)
	if strings.Contains(yaml, "DB_PASSWORD") {
		t.Error("DB_PASSWORD should not appear in Compose YAML (it's a secret ref)")
	}
	if strings.Contains(yaml, "API_KEY") {
		t.Error("API_KEY should not appear in Compose YAML (it's a secret ref)")
	}
	if strings.Contains(yaml, "SESSION_SECRET") {
		t.Error("SESSION_SECRET should not appear in Compose YAML (it's a secret ref)")
	}

	// Env material should contain redacted placeholders for secrets.
	apiEnv := result.EnvMaterial["api-server"]
	if !strings.Contains(apiEnv, "API_KEY=REDACTED(api-key)") {
		t.Error("api-server env should contain redacted API_KEY")
	}
	if !strings.Contains(apiEnv, "DB_PASSWORD=REDACTED(db-password)") {
		t.Error("api-server env should contain redacted DB_PASSWORD")
	}

	webEnv := result.EnvMaterial["web-frontend"]
	if !strings.Contains(webEnv, "SESSION_SECRET=REDACTED(session-secret)") {
		t.Error("web-frontend env should contain redacted SESSION_SECRET")
	}

	// Metadata should not contain any secret values.
	metadataJSON, err := result.Metadata.MetadataJSON()
	if err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	metaStr := string(metadataJSON)
	for _, secret := range []string{"DB_PASSWORD", "API_KEY", "SESSION_SECRET", "REDACTED"} {
		if strings.Contains(metaStr, secret) {
			t.Errorf("metadata should not contain %q", secret)
		}
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_AllFieldsRendered(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)

	// Check all expected fields appear in the rendered YAML.
	checks := []struct {
		field string
		want  string
	}{
		{"image (api)", "registry.example.com/api:v3.0.1"},
		{"image (web)", "registry.example.com/web:v2.1.0"},
		{"command (api)", "./api-server"},
		{"command (web)", "nginx"},
		{"entrypoint", "/entrypoint.sh"},
		{"working_dir (api)", "/app"},
		{"working_dir (web)", "/usr/share/nginx"},
		{"port (api)", "8080:8080"},
		{"port (web 80)", "80:80"},
		{"port (web 443)", "443:443"},
		{"volume (api)", "api-data:/app/data"},
		{"label", "bahia.managed"},
		{"healthcheck test", "curl"},
		{"healthcheck interval", "interval"},
		{"healthcheck timeout", "timeout"},
		{"healthcheck retries", "retries"},
		{"depends_on", "depends_on"},
		{"restart (api)", "always"},
		{"restart (web)", "unless-stopped"},
		{"pull_policy", "if-not-present"},
		{"networks section", "networks:"},
		{"volumes section", "volumes:"},
		{"env_file", ".bahia/env/web-frontend.env"},
		{"environment PORT", "PORT"},
		{"container_name", "container_name"},
		{"network frontend", "frontend"},
		{"network backend", "backend"},
		{"condition service_healthy", "service_healthy"},
	}

	for _, c := range checks {
		if !strings.Contains(yaml, c.want) {
			t.Errorf("field %s: expected %q in rendered YAML", c.field, c.want)
		}
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_Metadata(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	m := result.Metadata

	if m.SchemaVersion != 1 {
		t.Errorf("expected schema version 1, got %d", m.SchemaVersion)
	}
	if m.Renderer != "compose" {
		t.Errorf("expected renderer 'compose', got %q", m.Renderer)
	}
	if m.RenderedAt.IsZero() {
		t.Error("RenderedAt should not be zero")
	}
	if time.Since(m.RenderedAt) > time.Minute {
		t.Error("RenderedAt should be recent")
	}
	if m.EnvironmentID != plan.EnvironmentID.String() {
		t.Errorf("expected environment ID %s, got %s", plan.EnvironmentID, m.EnvironmentID)
	}
	if m.RevisionHash != plan.RevisionHash {
		t.Errorf("expected revision hash %s, got %s", plan.RevisionHash, m.RevisionHash)
	}
	if m.ServiceCount != 2 {
		t.Errorf("expected 2 services, got %d", m.ServiceCount)
	}
	if m.ContentHash == "" {
		t.Error("ContentHash should not be empty")
	}
	if !strings.HasPrefix(m.ContentHash, "sha256:") {
		t.Errorf("ContentHash should start with 'sha256:', got %q", m.ContentHash)
	}

	// Networks and volumes should be sorted.
	if len(m.NetworksDeclared) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(m.NetworksDeclared))
	}
	if m.NetworksDeclared[0] != "backend" || m.NetworksDeclared[1] != "frontend" {
		t.Errorf("networks not sorted: %v", m.NetworksDeclared)
	}
	if len(m.VolumesDeclared) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(m.VolumesDeclared))
	}
	if m.VolumesDeclared[0] != "api-data" || m.VolumesDeclared[1] != "web-static" {
		t.Errorf("volumes not sorted: %v", m.VolumesDeclared)
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_NilPlan(t *testing.T) {
	renderer := NewComposeRenderer()
	_, err := renderer.RenderEnvironmentPlan(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_EmptyServices(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: fixedUUID("env-empty-001"),
		Services:      []domain.DesiredServiceSpec{},
	}
	_, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error for empty services")
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_MinimalService(t *testing.T) {
	renderer := NewComposeRenderer()
	envID := fixedUUID("env-minimal01")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-minimal01"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-minimal01"),
				StableServiceKey: "bare-service",
				ImageRef:         "busybox:latest",
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	assertGolden(t, "minimal_service_compose.yml", result.ComposeYAML)

	// Env material should be empty for a service with no env/secrets.
	if len(result.EnvMaterial) != 0 {
		t.Errorf("expected no env material, got %d entries", len(result.EnvMaterial))
	}
}

func TestComposeRenderer_RenderEnvironmentPlan_NetworkModeService(t *testing.T) {
	renderer := NewComposeRenderer()
	envID := fixedUUID("env-netmode01")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-netmode01"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-netmode01"),
				StableServiceKey: "host-network-svc",
				ImageRef:         "nginx:latest",
				NetworkMode:      "host",
				RestartPolicy:    "always",
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)
	if !strings.Contains(yaml, "network_mode: host") {
		t.Error("expected network_mode: host in YAML")
	}
}

func TestComposeRenderer_MetadataJSON(t *testing.T) {
	m := RenderMetadata{
		SchemaVersion: 1,
		Renderer:      "compose",
		RenderedAt:    time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		EnvironmentID: "test-env-id",
		RevisionHash:  "sha256:abc123",
		ProjectName:   "test-project",
		ServiceCount:  1,
		ServiceKeys:   []string{"my-svc"},
		ContentHash:   "sha256:def456",
	}

	data, err := m.MetadataJSON()
	if err != nil {
		t.Fatalf("MetadataJSON: %v", err)
	}

	assertGolden(t, "render_metadata.json", data)
}

func TestComposeRenderer_EnvMaterialOrdering(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	// Render multiple times and verify env material is stable.
	var prevAPI, prevWeb string
	for i := 0; i < 5; i++ {
		result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		apiEnv := result.EnvMaterial["api-server"]
		webEnv := result.EnvMaterial["web-frontend"]
		if i > 0 {
			if apiEnv != prevAPI {
				t.Errorf("api-server env material changed on iteration %d", i)
			}
			if webEnv != prevWeb {
				t.Errorf("web-frontend env material changed on iteration %d", i)
			}
		}
		prevAPI = apiEnv
		prevWeb = webEnv
	}
}

func TestComposeRenderer_HealthcheckOverride(t *testing.T) {
	renderer := NewComposeRenderer()
	envID := fixedUUID("env-hcoverri")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-hcoverri"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-hcoverri"),
				StableServiceKey: "hc-override-svc",
				ImageRef:         "myapp:latest",
				Healthcheck: &domain.HealthcheckConfig{
					Test:     []string{"CMD", "true"},
					Interval: "10s",
				},
				ComposeExtension: &domain.ComposeExtension{
					HealthcheckOverride: &domain.HealthcheckConfig{
						Test:     []string{"CMD-SHELL", "wget -q --spider http://localhost:9090/health"},
						Interval: "20s",
						Timeout:  "10s",
						Retries:  10,
					},
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)
	// The override healthcheck should be used, not the portable one.
	if !strings.Contains(yaml, "wget") {
		t.Error("expected Compose healthcheck override (wget) in YAML")
	}
	if strings.Contains(yaml, "CMD\", \"true") {
		t.Error("portable healthcheck should be overridden by Compose extension")
	}
}
