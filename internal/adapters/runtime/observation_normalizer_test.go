package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test: Deterministic hashing — same input always produces the same hash
// ---------------------------------------------------------------------------

func TestNormalizeDockerContainer_DeterministicHash(t *testing.T) {
	t.Parallel()
	inspected := sampleInspectData()
	secrets := map[string]bool{"DB_PASSWORD": true}

	obs1 := NormalizeDockerContainer(inspected, secrets)
	obs1.ComputeObservationHash()

	obs2 := NormalizeDockerContainer(inspected, secrets)
	obs2.ComputeObservationHash()

	if obs1.ObservationHash == "" {
		t.Fatal("expected non-empty hash")
	}
	if obs1.ObservationHash != obs2.ObservationHash {
		t.Errorf("hashes differ: %s vs %s", obs1.ObservationHash, obs2.ObservationHash)
	}
}

// ---------------------------------------------------------------------------
// Test: Compose/Docker parity — same container data produces same hash
// ---------------------------------------------------------------------------

func TestNormalize_ComposeDockerParity(t *testing.T) {
	t.Parallel()
	inspected := sampleInspectData()
	secrets := map[string]bool{"DB_PASSWORD": true}

	dockerObs := NormalizeDockerContainer(inspected, secrets)
	dockerObs.ComputeObservationHash()

	composeObs := NormalizeComposeService(inspected, secrets)
	composeObs.ComputeObservationHash()

	if dockerObs.ObservationHash != composeObs.ObservationHash {
		t.Errorf("Docker and Compose observations produce different hashes:\n  Docker:  %s\n  Compose: %s",
			dockerObs.ObservationHash, composeObs.ObservationHash)
	}

	// Verify JSON serialization matches too.
	dockerJSON, _ := json.Marshal(dockerObs)
	composeJSON, _ := json.Marshal(composeObs)
	if string(dockerJSON) != string(composeJSON) {
		t.Errorf("Docker and Compose observation JSON differ")
	}
}

// ---------------------------------------------------------------------------
// Test: Secret redaction
// ---------------------------------------------------------------------------

func TestNormalize_SecretRedaction(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
			Env: []string{
				"APP_NAME=myapp",
				"DB_PASSWORD=super-secret-value",
				"API_KEY=another-secret",
				"LOG_LEVEL=debug",
			},
		},
		HostConfig: dockerContainerHostConfig{
			RestartPolicy: dockerContainerRestartRule{Name: "always"},
		},
	}
	secrets := map[string]bool{
		"DB_PASSWORD": true,
		"API_KEY":     true,
	}

	obs := NormalizeDockerContainer(inspected, secrets)

	// Non-secret env vars should be present with values.
	if obs.Env["APP_NAME"] != "myapp" {
		t.Errorf("expected APP_NAME=myapp, got %q", obs.Env["APP_NAME"])
	}
	if obs.Env["LOG_LEVEL"] != "debug" {
		t.Errorf("expected LOG_LEVEL=debug, got %q", obs.Env["LOG_LEVEL"])
	}

	// Secret values must NOT appear in env.
	if _, ok := obs.Env["DB_PASSWORD"]; ok {
		t.Error("DB_PASSWORD should not be in non-secret env")
	}
	if _, ok := obs.Env["API_KEY"]; ok {
		t.Error("API_KEY should not be in non-secret env")
	}

	// Secret keys should appear in SecretEnvKeys (sorted).
	if len(obs.SecretEnvKeys) != 2 {
		t.Fatalf("expected 2 secret keys, got %d", len(obs.SecretEnvKeys))
	}
	if obs.SecretEnvKeys[0] != "API_KEY" || obs.SecretEnvKeys[1] != "DB_PASSWORD" {
		t.Errorf("expected sorted secret keys [API_KEY, DB_PASSWORD], got %v", obs.SecretEnvKeys)
	}

	// Verify no secret plaintext in JSON serialization.
	jsonBytes, _ := json.Marshal(obs)
	jsonStr := string(jsonBytes)
	if strings.Contains(jsonStr, "super-secret-value") {
		t.Error("secret plaintext 'super-secret-value' found in JSON")
	}
	if strings.Contains(jsonStr, "another-secret") {
		t.Error("secret plaintext 'another-secret' found in JSON")
	}
}

// ---------------------------------------------------------------------------
// Test: Volatile field exclusion
// ---------------------------------------------------------------------------

func TestNormalize_ExcludesVolatileFields(t *testing.T) {
	t.Parallel()

	// Two containers with same semantic config but different volatile fields.
	inspected1 := sampleInspectData()
	inspected1.ID = "container-aaa111"
	inspected1.Name = "/container-name-1"
	inspected1.State = dockerContainerState{Status: "running"}

	inspected2 := sampleInspectData()
	inspected2.ID = "container-bbb222" // Different container ID
	inspected2.Name = "/container-name-2" // Different name
	inspected2.State = dockerContainerState{Status: "running"}

	secrets := map[string]bool{}

	obs1 := NormalizeDockerContainer(inspected1, secrets)
	obs1.ComputeObservationHash()

	obs2 := NormalizeDockerContainer(inspected2, secrets)
	obs2.ComputeObservationHash()

	// Hashes should be identical — container ID and name are volatile.
	if obs1.ObservationHash != obs2.ObservationHash {
		t.Errorf("expected same hash despite different container IDs, got:\n  %s\n  %s",
			obs1.ObservationHash, obs2.ObservationHash)
	}
}

// ---------------------------------------------------------------------------
// Test: System env vars excluded
// ---------------------------------------------------------------------------

func TestNormalize_ExcludesSystemEnvVars(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
			Env: []string{
				"APP_NAME=myapp",
				"PATH=/usr/bin:/bin",
				"HOSTNAME=abc123def",
				"HOME=/root",
			},
		},
		HostConfig: dockerContainerHostConfig{
			RestartPolicy: dockerContainerRestartRule{Name: "always"},
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	if _, ok := obs.Env["PATH"]; ok {
		t.Error("PATH should be excluded as system env var")
	}
	if _, ok := obs.Env["HOSTNAME"]; ok {
		t.Error("HOSTNAME should be excluded as system env var")
	}
	if _, ok := obs.Env["HOME"]; ok {
		t.Error("HOME should be excluded as system env var")
	}
	if obs.Env["APP_NAME"] != "myapp" {
		t.Errorf("expected APP_NAME=myapp, got %q", obs.Env["APP_NAME"])
	}
}

// ---------------------------------------------------------------------------
// Test: Bahia labels only — non-Bahia and Compose labels excluded
// ---------------------------------------------------------------------------

func TestNormalize_OnlyBahiaLabels(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
			Labels: map[string]string{
				"bahia.service":                "web",
				"bahia.environment_id":         "env-123",
				"bahia.desired_hash":           "sha256:deadbeef",
				"com.docker.compose.project":   "myproject",
				"com.docker.compose.service":   "web",
				"com.docker.compose.version":   "2.24.0",
				"org.opencontainers.image.ref": "registry.example/app:v1",
				"maintainer":                   "team@example.com",
			},
		},
		HostConfig: dockerContainerHostConfig{},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	// Only bahia.* labels should be included.
	if len(obs.BahiaLabels) != 3 {
		t.Fatalf("expected 3 Bahia labels, got %d: %v", len(obs.BahiaLabels), obs.BahiaLabels)
	}
	if obs.BahiaLabels["bahia.service"] != "web" {
		t.Error("missing bahia.service label")
	}
	if obs.BahiaLabels["bahia.environment_id"] != "env-123" {
		t.Error("missing bahia.environment_id label")
	}
	if obs.BahiaLabels["bahia.desired_hash"] != "sha256:deadbeef" {
		t.Error("missing bahia.desired_hash label")
	}

	// Compose and OCI labels should NOT be present.
	if _, ok := obs.BahiaLabels["com.docker.compose.project"]; ok {
		t.Error("Compose label should be excluded")
	}
	if _, ok := obs.BahiaLabels["maintainer"]; ok {
		t.Error("non-Bahia label should be excluded")
	}
}

// ---------------------------------------------------------------------------
// Test: Port normalization
// ---------------------------------------------------------------------------

func TestNormalize_PortNormalization(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
		},
		HostConfig: dockerContainerHostConfig{},
		NetworkSettings: dockerContainerNetworkConfig{
			Ports: map[string][]dockerPortPublish{
				"80/tcp": {
					{HostIP: "0.0.0.0", HostPort: "8080"},
				},
				"443/tcp": {
					{HostIP: "::", HostPort: "8443"},
				},
				"9090/tcp": {}, // Exposed but not bound.
			},
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	// Ports should be sorted and exclude host IPs.
	if len(obs.Ports) != 3 {
		t.Fatalf("expected 3 ports, got %d: %v", len(obs.Ports), obs.Ports)
	}
	expected := []string{"8080:80/tcp", "8443:443/tcp", "9090/tcp"}
	for i, want := range expected {
		if obs.Ports[i] != want {
			t.Errorf("port[%d]: expected %q, got %q", i, want, obs.Ports[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Volume normalization
// ---------------------------------------------------------------------------

func TestNormalize_VolumeNormalization(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
		},
		HostConfig: dockerContainerHostConfig{
			Binds: []string{
				"/host/data:/container/data",
				"/host/config:/container/config:ro",
			},
		},
		Mounts: []dockerContainerMount{
			{Type: "volume", Name: "mydata", Destination: "/var/lib/data", RW: true},
			{Type: "bind", Source: "/host/data", Destination: "/container/data", RW: true}, // Duplicate of bind.
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	// Should have 3 unique volumes (binds + named volume), sorted.
	if len(obs.Volumes) != 3 {
		t.Fatalf("expected 3 volumes, got %d: %v", len(obs.Volumes), obs.Volumes)
	}

	expectedVols := []string{
		"/host/config:/container/config:ro",
		"/host/data:/container/data",
		"mydata:/var/lib/data",
	}
	for i, want := range expectedVols {
		if obs.Volumes[i] != want {
			t.Errorf("volume[%d]: expected %q, got %q", i, want, obs.Volumes[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Network normalization — bridge excluded
// ---------------------------------------------------------------------------

func TestNormalize_NetworkNormalization(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
		},
		HostConfig: dockerContainerHostConfig{},
		NetworkSettings: dockerContainerNetworkConfig{
			Networks: map[string]dockerContainerAttachment{
				"bridge":  {Aliases: []string{"abc123"}},
				"backend": {Aliases: []string{"web"}},
				"monitor": {Aliases: []string{"web"}},
			},
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	// Bridge should be excluded; remaining sorted.
	if len(obs.NetworkAttachments) != 2 {
		t.Fatalf("expected 2 network attachments, got %d: %v", len(obs.NetworkAttachments), obs.NetworkAttachments)
	}
	if obs.NetworkAttachments[0] != "backend" || obs.NetworkAttachments[1] != "monitor" {
		t.Errorf("expected [backend, monitor], got %v", obs.NetworkAttachments)
	}
}

// ---------------------------------------------------------------------------
// Test: Nil input
// ---------------------------------------------------------------------------

func TestNormalizeDockerContainer_NilInput(t *testing.T) {
	t.Parallel()
	if obs := NormalizeDockerContainer(nil, nil); obs != nil {
		t.Error("expected nil for nil input")
	}
}

func TestNormalizeComposeService_NilInput(t *testing.T) {
	t.Parallel()
	if obs := NormalizeComposeService(nil, nil); obs != nil {
		t.Error("expected nil for nil input")
	}
}

// ---------------------------------------------------------------------------
// Test: Schema version included
// ---------------------------------------------------------------------------

func TestNormalize_SchemaVersionIncluded(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc123",
		Config: dockerContainerConfig{
			Image: "registry.example/app:v1",
		},
		HostConfig: dockerContainerHostConfig{},
	}

	obs := NormalizeDockerContainer(inspected, nil)
	if obs.SchemaVersion != domain.DesiredStateSchemaVersion {
		t.Errorf("expected schema version %q, got %q", domain.DesiredStateSchemaVersion, obs.SchemaVersion)
	}
}

// ---------------------------------------------------------------------------
// Test: Full field extraction
// ---------------------------------------------------------------------------

func TestNormalize_AllSemanticFields(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		ID:    "container-abc123",
		Name:  "/my-container",
		Image: "sha256:deadbeef1234567890",
		Config: dockerContainerConfig{
			Image:      "registry.example/app:v2",
			Cmd:        []string{"./server", "--port=8080"},
			Entrypoint: []string{"/usr/bin/dumb-init", "--"},
			WorkingDir: "/app",
			Env: []string{
				"APP_PORT=8080",
				"NODE_ENV=production",
				"PATH=/usr/bin:/bin",
			},
			Labels: map[string]string{
				"bahia.service":      "web",
				"bahia.desired_hash": "sha256:aaa",
				"maintainer":         "team",
			},
		},
		State: dockerContainerState{Status: "running"},
		HostConfig: dockerContainerHostConfig{
			Binds:         []string{"/data:/app/data"},
			NetworkMode:   "backend",
			RestartPolicy: dockerContainerRestartRule{Name: "unless-stopped"},
		},
		NetworkSettings: dockerContainerNetworkConfig{
			Ports: map[string][]dockerPortPublish{
				"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
			},
			Networks: map[string]dockerContainerAttachment{
				"backend": {Aliases: []string{"web"}},
			},
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	// Image.
	if obs.ImageRef != "registry.example/app:v2" {
		t.Errorf("ImageRef: %q", obs.ImageRef)
	}

	// Process.
	if len(obs.Command) != 2 || obs.Command[0] != "./server" {
		t.Errorf("Command: %v", obs.Command)
	}
	if len(obs.Entrypoint) != 2 || obs.Entrypoint[0] != "/usr/bin/dumb-init" {
		t.Errorf("Entrypoint: %v", obs.Entrypoint)
	}
	if obs.WorkDir != "/app" {
		t.Errorf("WorkDir: %q", obs.WorkDir)
	}

	// Env (PATH excluded as system var).
	if obs.Env["APP_PORT"] != "8080" || obs.Env["NODE_ENV"] != "production" {
		t.Errorf("Env: %v", obs.Env)
	}
	if _, ok := obs.Env["PATH"]; ok {
		t.Error("PATH should be excluded")
	}

	// Restart.
	if obs.RestartPolicy != "unless-stopped" {
		t.Errorf("RestartPolicy: %q", obs.RestartPolicy)
	}

	// Labels — only bahia.*.
	if len(obs.BahiaLabels) != 2 {
		t.Errorf("expected 2 Bahia labels, got %d", len(obs.BahiaLabels))
	}

	// Networks.
	if len(obs.NetworkAttachments) != 1 || obs.NetworkAttachments[0] != "backend" {
		t.Errorf("NetworkAttachments: %v", obs.NetworkAttachments)
	}

	// Ports.
	if len(obs.Ports) != 1 || obs.Ports[0] != "8080:8080/tcp" {
		t.Errorf("Ports: %v", obs.Ports)
	}

	// Volumes.
	if len(obs.Volumes) != 1 || obs.Volumes[0] != "/data:/app/data" {
		t.Errorf("Volumes: %v", obs.Volumes)
	}
}

// ---------------------------------------------------------------------------
// Test: Hash changes when semantic fields change
// ---------------------------------------------------------------------------

func TestNormalize_HashChangesOnSemanticDrift(t *testing.T) {
	t.Parallel()
	base := sampleInspectData()
	secrets := map[string]bool{}

	baseObs := NormalizeDockerContainer(base, secrets)
	baseObs.ComputeObservationHash()
	baseHash := baseObs.ObservationHash

	// Change image → hash should change.
	drifted := sampleInspectData()
	drifted.Config.Image = "registry.example/app:v3"
	driftedObs := NormalizeDockerContainer(drifted, secrets)
	driftedObs.ComputeObservationHash()
	if driftedObs.ObservationHash == baseHash {
		t.Error("hash should change when image changes")
	}

	// Change restart policy → hash should change.
	drifted2 := sampleInspectData()
	drifted2.HostConfig.RestartPolicy.Name = "on-failure"
	drifted2Obs := NormalizeDockerContainer(drifted2, secrets)
	drifted2Obs.ComputeObservationHash()
	if drifted2Obs.ObservationHash == baseHash {
		t.Error("hash should change when restart policy changes")
	}

	// Change env value → hash should change.
	drifted3 := sampleInspectData()
	drifted3.Config.Env = []string{"APP_PORT=9090", "NODE_ENV=staging"}
	drifted3Obs := NormalizeDockerContainer(drifted3, secrets)
	drifted3Obs.ComputeObservationHash()
	if drifted3Obs.ObservationHash == baseHash {
		t.Error("hash should change when env values change")
	}
}

// ---------------------------------------------------------------------------
// Test: Compose labels excluded from hash
// ---------------------------------------------------------------------------

func TestNormalize_ComposeLabelsExcludedFromHash(t *testing.T) {
	t.Parallel()

	// Same semantic config, different Compose labels.
	inspected1 := sampleInspectData()
	inspected1.Config.Labels = map[string]string{
		"bahia.service":              "web",
		"com.docker.compose.project": "proj-a",
	}

	inspected2 := sampleInspectData()
	inspected2.Config.Labels = map[string]string{
		"bahia.service":              "web",
		"com.docker.compose.project": "proj-b", // Different compose project.
	}

	obs1 := NormalizeDockerContainer(inspected1, nil)
	obs1.ComputeObservationHash()

	obs2 := NormalizeDockerContainer(inspected2, nil)
	obs2.ComputeObservationHash()

	if obs1.ObservationHash != obs2.ObservationHash {
		t.Error("Compose-generated labels should not affect hash")
	}
}

// ---------------------------------------------------------------------------
// Test: IP addresses in networks excluded
// ---------------------------------------------------------------------------

func TestNormalize_EphemeralIPsExcluded(t *testing.T) {
	t.Parallel()

	// Network attachments don't include IPs — just names.
	// The dockerContainerAttachment struct only has Aliases, not IPs.
	// This test verifies that different alias values don't leak through.
	inspected1 := sampleInspectData()
	inspected1.NetworkSettings.Networks = map[string]dockerContainerAttachment{
		"backend": {Aliases: []string{"web", "172.18.0.5"}},
	}

	inspected2 := sampleInspectData()
	inspected2.NetworkSettings.Networks = map[string]dockerContainerAttachment{
		"backend": {Aliases: []string{"web", "172.18.0.99"}}, // Different IP alias.
	}

	obs1 := NormalizeDockerContainer(inspected1, nil)
	obs1.ComputeObservationHash()

	obs2 := NormalizeDockerContainer(inspected2, nil)
	obs2.ComputeObservationHash()

	// Network attachments only record names, not aliases/IPs.
	if obs1.ObservationHash != obs2.ObservationHash {
		t.Error("different network aliases should not affect hash (only network names matter)")
	}
}

// ---------------------------------------------------------------------------
// Test: InspectAndNormalize integration via mock HTTP server
// ---------------------------------------------------------------------------

func TestInspectAndNormalizeDocker_Integration(t *testing.T) {
	t.Parallel()
	inspectResp := sampleInspectData()
	inspectJSON, _ := json.Marshal(inspectResp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(inspectJSON)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	obs, err := InspectAndNormalizeDocker(context.Background(), observer, "test-container-id", map[string]bool{"SECRET_VAR": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.ObservationHash == "" {
		t.Error("expected non-empty observation hash")
	}
	if obs.ImageRef != "registry.example/app:v2" {
		t.Errorf("unexpected image ref: %s", obs.ImageRef)
	}
}

func TestInspectAndNormalizeCompose_Integration(t *testing.T) {
	t.Parallel()
	inspectResp := sampleInspectData()
	inspectJSON, _ := json.Marshal(inspectResp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(inspectJSON)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	obs, err := InspectAndNormalizeCompose(context.Background(), observer, "test-container-id", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.ObservationHash == "" {
		t.Error("expected non-empty observation hash")
	}
}

// ---------------------------------------------------------------------------
// Test: Docker/Compose integration parity via mock HTTP server
// ---------------------------------------------------------------------------

func TestInspectAndNormalize_ParityViaHTTP(t *testing.T) {
	t.Parallel()
	inspectResp := sampleInspectData()
	inspectJSON, _ := json.Marshal(inspectResp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(inspectJSON)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	secrets := map[string]bool{"SECRET_VAR": true}

	dockerObs, err := InspectAndNormalizeDocker(context.Background(), observer, "container-1", secrets)
	if err != nil {
		t.Fatalf("Docker normalize failed: %v", err)
	}

	composeObs, err := InspectAndNormalizeCompose(context.Background(), observer, "container-1", secrets)
	if err != nil {
		t.Fatalf("Compose normalize failed: %v", err)
	}

	if dockerObs.ObservationHash != composeObs.ObservationHash {
		t.Errorf("parity violation: Docker hash %s != Compose hash %s",
			dockerObs.ObservationHash, composeObs.ObservationHash)
	}
}

// ---------------------------------------------------------------------------
// Test: Empty container (minimal config)
// ---------------------------------------------------------------------------

func TestNormalize_EmptyContainer(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Config:     dockerContainerConfig{Image: "alpine:latest"},
		HostConfig: dockerContainerHostConfig{},
	}

	obs := NormalizeDockerContainer(inspected, nil)
	obs.ComputeObservationHash()

	if obs.ImageRef != "alpine:latest" {
		t.Errorf("expected alpine:latest, got %q", obs.ImageRef)
	}
	if obs.ObservationHash == "" {
		t.Error("expected non-empty hash even for minimal container")
	}
	if len(obs.Env) != 0 {
		t.Errorf("expected no env, got %v", obs.Env)
	}
	if len(obs.Ports) != 0 {
		t.Errorf("expected no ports, got %v", obs.Ports)
	}
}

// ---------------------------------------------------------------------------
// Test: Port ordering is deterministic
// ---------------------------------------------------------------------------

func TestNormalize_PortOrdering(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc",
		Config: dockerContainerConfig{
			Image: "app:v1",
		},
		HostConfig: dockerContainerHostConfig{},
		NetworkSettings: dockerContainerNetworkConfig{
			Ports: map[string][]dockerPortPublish{
				"443/tcp":  {{HostIP: "0.0.0.0", HostPort: "8443"}},
				"80/tcp":   {{HostIP: "0.0.0.0", HostPort: "8080"}},
				"9090/tcp": {{HostIP: "0.0.0.0", HostPort: "9090"}},
			},
		},
	}

	// Run multiple times to verify determinism.
	var lastHash string
	for i := 0; i < 5; i++ {
		obs := NormalizeDockerContainer(inspected, nil)
		obs.ComputeObservationHash()
		if lastHash != "" && obs.ObservationHash != lastHash {
			t.Fatalf("iteration %d: hash changed from %s to %s", i, lastHash, obs.ObservationHash)
		}
		lastHash = obs.ObservationHash
	}
}

// ---------------------------------------------------------------------------
// Test: Inspect API error handling
// ---------------------------------------------------------------------------

func TestInspectAndNormalizeDocker_APIError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"container not found"}`)
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	_, err := InspectAndNormalizeDocker(context.Background(), observer, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing container")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Volume deduplication
// ---------------------------------------------------------------------------

func TestNormalize_VolumeDeduplication(t *testing.T) {
	t.Parallel()
	inspected := &dockerContainerInspect{
		Image: "sha256:abc",
		Config: dockerContainerConfig{
			Image: "app:v1",
		},
		HostConfig: dockerContainerHostConfig{
			Binds: []string{
				"/data:/app/data",
				"/data:/app/data", // Exact duplicate.
			},
		},
		Mounts: []dockerContainerMount{
			{Type: "bind", Source: "/data", Destination: "/app/data", RW: true}, // Also a duplicate.
		},
	}

	obs := NormalizeDockerContainer(inspected, nil)

	if len(obs.Volumes) != 1 {
		t.Errorf("expected 1 deduplicated volume, got %d: %v", len(obs.Volumes), obs.Volumes)
	}
}

// ---------------------------------------------------------------------------
// Kubernetes pod normalization tests
// ---------------------------------------------------------------------------

func TestNormalizeKubernetesPod_Nil(t *testing.T) {
	t.Parallel()
	if obs := NormalizeKubernetesPod(nil, nil); obs != nil {
		t.Error("expected nil for nil pod input")
	}
}

func TestNormalizeKubernetesPod_Basic(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()

	obs := NormalizeKubernetesPod(pod, nil)

	if obs == nil {
		t.Fatal("expected non-nil observation")
	}
	if obs.ImageRef != "ghcr.io/org/api:v1.0.0" {
		t.Errorf("ImageRef = %q, want %q", obs.ImageRef, "ghcr.io/org/api:v1.0.0")
	}
	if obs.SchemaVersion == "" {
		t.Error("SchemaVersion should be set")
	}
}

func TestNormalizeKubernetesPod_Command(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	pod.Spec.Containers[0].Command = []string{"/app/server", "--port=8080"}

	obs := NormalizeKubernetesPod(pod, nil)

	if len(obs.Command) != 2 || obs.Command[0] != "/app/server" {
		t.Errorf("Command = %v, unexpected", obs.Command)
	}
}

func TestNormalizeKubernetesPod_Ports(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()

	obs := NormalizeKubernetesPod(pod, nil)

	if obs == nil {
		t.Fatal("expected non-nil observation")
	}
	// Ports should be sorted and lowercase protocol
	if len(obs.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d: %v", len(obs.Ports), obs.Ports)
	}
	// sorted: 8080/tcp, 9090/tcp
	if obs.Ports[0] != "8080/tcp" {
		t.Errorf("Ports[0] = %q, want %q", obs.Ports[0], "8080/tcp")
	}
	if obs.Ports[1] != "9090/tcp" {
		t.Errorf("Ports[1] = %q, want %q", obs.Ports[1], "9090/tcp")
	}
}

func TestNormalizeKubernetesPod_SecretRedaction(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	pod.Spec.Containers[0].Env = []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{
		{Name: "APP_NAME", Value: "api"},
		{Name: "DB_PASSWORD", Value: "super-secret"},
		{Name: "API_KEY", Value: "another-secret"},
		{Name: "LOG_LEVEL", Value: "info"},
	}
	secretNames := map[string]bool{"DB_PASSWORD": true, "API_KEY": true}

	obs := NormalizeKubernetesPod(pod, secretNames)

	// Non-secret env vars should be present.
	if obs.Env["APP_NAME"] != "api" {
		t.Errorf("expected APP_NAME=api, got %q", obs.Env["APP_NAME"])
	}
	if obs.Env["LOG_LEVEL"] != "info" {
		t.Errorf("expected LOG_LEVEL=info, got %q", obs.Env["LOG_LEVEL"])
	}

	// Secret values must NOT appear in env.
	if _, ok := obs.Env["DB_PASSWORD"]; ok {
		t.Error("DB_PASSWORD should not be in non-secret env")
	}
	if _, ok := obs.Env["API_KEY"]; ok {
		t.Error("API_KEY should not be in non-secret env")
	}

	// Secret keys should appear in SecretEnvKeys (sorted).
	if len(obs.SecretEnvKeys) != 2 {
		t.Fatalf("expected 2 secret keys, got %d", len(obs.SecretEnvKeys))
	}
	if obs.SecretEnvKeys[0] != "API_KEY" || obs.SecretEnvKeys[1] != "DB_PASSWORD" {
		t.Errorf("expected sorted secret keys [API_KEY, DB_PASSWORD], got %v", obs.SecretEnvKeys)
	}
}

func TestNormalizeKubernetesPod_BahiaLabelsOnly(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	pod.Metadata.Labels = map[string]string{
		"bahia.service":          "api",
		"bahia.environment_id":   "env-123",
		"app":                    "my-app",
		"k8s-app":                "api",
		"app.kubernetes.io/name": "api",
	}

	obs := NormalizeKubernetesPod(pod, nil)

	if len(obs.BahiaLabels) != 2 {
		t.Fatalf("expected 2 Bahia labels, got %d: %v", len(obs.BahiaLabels), obs.BahiaLabels)
	}
	if obs.BahiaLabels["bahia.service"] != "api" {
		t.Error("missing bahia.service label")
	}
	if obs.BahiaLabels["bahia.environment_id"] != "env-123" {
		t.Error("missing bahia.environment_id label")
	}
	if _, ok := obs.BahiaLabels["app"]; ok {
		t.Error("non-Bahia label 'app' should be excluded")
	}
}

func TestNormalizeKubernetesPod_RestartPolicy(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	pod.Spec.RestartPolicy = "Always"

	obs := NormalizeKubernetesPod(pod, nil)

	if obs.RestartPolicy != "Always" {
		t.Errorf("RestartPolicy = %q, want %q", obs.RestartPolicy, "Always")
	}
}

func TestNormalizeKubernetesPod_ImageDigestFromStatus(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	pod.Status.ContainerStatuses[0].ImageID = "docker-pullable://ghcr.io/org/api@sha256:abcdef1234567890"

	obs := NormalizeKubernetesPod(pod, nil)

	if obs.ImageDigest != "sha256:abcdef1234567890" {
		t.Errorf("ImageDigest = %q, want %q", obs.ImageDigest, "sha256:abcdef1234567890")
	}
}

func TestNormalizeKubernetesPod_HashStability(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	secrets := map[string]bool{"SECRET_KEY": true}

	obs1 := NormalizeKubernetesPod(pod, secrets)
	obs2 := NormalizeKubernetesPod(pod, secrets)

	if obs1.ObservationHash == "" {
		t.Fatal("expected non-empty hash")
	}
	if obs1.ObservationHash != obs2.ObservationHash {
		t.Errorf("hash not stable: %s vs %s", obs1.ObservationHash, obs2.ObservationHash)
	}
}

func TestNormalizeKubernetesPod_HashChangesOnDrift(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	obs1 := NormalizeKubernetesPod(pod, nil)

	// Change image → hash must change.
	pod2 := sampleKubePod()
	pod2.Spec.Containers[0].Image = "ghcr.io/org/api:v2.0.0"
	obs2 := NormalizeKubernetesPod(pod2, nil)

	if obs1.ObservationHash == obs2.ObservationHash {
		t.Error("hash should change when image changes")
	}

	// Change restart policy → hash must change.
	pod3 := sampleKubePod()
	pod3.Spec.RestartPolicy = "Never"
	obs3 := NormalizeKubernetesPod(pod3, nil)

	if obs1.ObservationHash == obs3.ObservationHash {
		t.Error("hash should change when restart policy changes")
	}
}

func TestNormalizeKubernetesPod_PortProtocolDefaultsTCP(t *testing.T) {
	t.Parallel()
	pod := sampleKubePod()
	// Override ports with one that has no protocol set.
	pod.Spec.Containers[0].Ports = []struct {
		ContainerPort int32  `json:"containerPort"`
		Protocol      string `json:"protocol,omitempty"`
	}{
		{ContainerPort: 5432, Protocol: ""},
	}

	obs := NormalizeKubernetesPod(pod, nil)

	if len(obs.Ports) != 1 || obs.Ports[0] != "5432/tcp" {
		t.Errorf("Ports = %v, want [5432/tcp]", obs.Ports)
	}
}

func TestNormalizeKubernetesPod_EmptyPodNoContainers(t *testing.T) {
	t.Parallel()
	pod := &kubePod{}
	pod.Metadata.Labels = map[string]string{"bahia.service": "empty"}

	obs := NormalizeKubernetesPod(pod, nil)

	if obs == nil {
		t.Fatal("expected non-nil observation even for empty pod")
	}
	if obs.ImageRef != "" {
		t.Errorf("expected empty ImageRef, got %q", obs.ImageRef)
	}
	if obs.ObservationHash == "" {
		t.Error("expected non-empty hash even for empty pod")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func sampleKubePod() *kubePod {
	pod := &kubePod{}
	pod.Metadata.Name = "api-7d8f9b-xyz"
	pod.Metadata.Labels = map[string]string{
		"bahia.service":        "api",
		"bahia.environment_id": "env-abc",
		"app":                  "api",
	}
	pod.Spec.Containers = []struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Command []string `json:"command,omitempty"`
		Env     []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"env,omitempty"`
		Ports []struct {
			ContainerPort int32  `json:"containerPort"`
			Protocol      string `json:"protocol,omitempty"`
		} `json:"ports,omitempty"`
	}{
		{
			Name:  "api",
			Image: "ghcr.io/org/api:v1.0.0",
			Env: []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}{
				{Name: "APP_PORT", Value: "8080"},
				{Name: "LOG_LEVEL", Value: "info"},
			},
			Ports: []struct {
				ContainerPort int32  `json:"containerPort"`
				Protocol      string `json:"protocol,omitempty"`
			}{
				{ContainerPort: 9090, Protocol: "TCP"},
				{ContainerPort: 8080, Protocol: "TCP"},
			},
		},
	}
	pod.Spec.RestartPolicy = "Always"
	pod.Status.Phase = "Running"
	pod.Status.ContainerStatuses = []struct {
		ContainerID string `json:"containerID"`
		Image       string `json:"image"`
		ImageID     string `json:"imageID"`
		Ready       bool   `json:"ready"`
	}{
		{
			ContainerID: "containerd://abc123",
			Image:       "ghcr.io/org/api:v1.0.0",
			ImageID:     "ghcr.io/org/api@sha256:deadbeef12345678",
			Ready:       true,
		},
	}
	return pod
}

func sampleInspectData() *dockerContainerInspect {
	return &dockerContainerInspect{
		ID:    "container-test-123",
		Name:  "/bahia-test-web",
		Image: "sha256:deadbeef1234567890abcdef",
		Config: dockerContainerConfig{
			Image:      "registry.example/app:v2",
			Cmd:        []string{"./server"},
			Entrypoint: []string{"/entrypoint.sh"},
			WorkingDir: "/app",
			Env: []string{
				"APP_PORT=8080",
				"NODE_ENV=production",
				"SECRET_VAR=secret-value",
			},
			Labels: map[string]string{
				"bahia.service":              "web",
				"bahia.environment_id":       "env-test-456",
				"com.docker.compose.project": "test-project",
			},
		},
		State: dockerContainerState{Status: "running"},
		HostConfig: dockerContainerHostConfig{
			Binds:         []string{"/data:/app/data"},
			NetworkMode:   "backend",
			RestartPolicy: dockerContainerRestartRule{Name: "unless-stopped"},
		},
		Mounts: []dockerContainerMount{
			{Type: "bind", Source: "/data", Destination: "/app/data", RW: true},
		},
		NetworkSettings: dockerContainerNetworkConfig{
			Ports: map[string][]dockerPortPublish{
				"8080/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
			},
			Networks: map[string]dockerContainerAttachment{
				"backend": {Aliases: []string{"web"}},
			},
		},
	}
}
