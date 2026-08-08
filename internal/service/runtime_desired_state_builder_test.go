package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// testIDs returns deterministic UUIDs for stable test output.
func testIDs() (serviceID, envID, artifactID, buildID, secretID uuid.UUID) {
	serviceID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	envID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	artifactID = uuid.MustParse("00000000-0000-0000-0000-000000000003")
	buildID = uuid.MustParse("00000000-0000-0000-0000-000000000004")
	secretID = uuid.MustParse("00000000-0000-0000-0000-000000000005")
	return
}

func makeTestInput() BuildInput {
	serviceID, envID, artifactID, buildID, secretID := testIDs()

	return BuildInput{
		Service: &domain.Service{
			ID:          serviceID,
			Name:        "my-app",
			RuntimeType: domain.RuntimeTypeCompose,
			RuntimeConfig: &domain.ServiceRuntimeConfig{
				Adopted: &domain.AdoptedRuntimeConfig{
					TargetName:  "My App Container",
					Environment: map[string]string{"APP_ENV": "production", "DB_PASSWORD": "should-be-removed"},
					Ports:       []string{"8080:80", "443:443"},
					Volumes:     []string{"/data:/app/data"},
					Restart:     "unless-stopped",
					Command:     []string{"serve", "--port=80"},
					Entrypoint:  []string{"/entrypoint.sh"},
					WorkingDir:  "/app",
					NetworkMode: "bridge",
					Labels:      map[string]string{"app.version": "1.0"},
					Compose: &domain.ComposeMetadata{
						ProjectName: "myproject",
						ServiceName: "my-app",
					},
				},
			},
		},
		Environment: &domain.Environment{
			ID:   envID,
			Name: "production",
		},
		Artifact: &domain.Artifact{
			ID:          artifactID,
			BuildID:     buildID,
			ServiceID:   serviceID,
			ImageRepo:   "ghcr.io/org/my-app",
			ImageTag:    "v1.2.3",
			ImageDigest: "sha256:abc123",
		},
		RuntimeConfig: &domain.ServiceRuntimeConfig{
			Adopted: &domain.AdoptedRuntimeConfig{
				TargetName:  "My App Container",
				Environment: map[string]string{"APP_ENV": "production", "DB_PASSWORD": "should-be-removed"},
				Ports:       []string{"8080:80", "443:443"},
				Volumes:     []string{"/data:/app/data"},
				Restart:     "unless-stopped",
				Command:     []string{"serve", "--port=80"},
				Entrypoint:  []string{"/entrypoint.sh"},
				WorkingDir:  "/app",
				NetworkMode: "bridge",
				Labels:      map[string]string{"app.version": "1.0"},
				Compose: &domain.ComposeMetadata{
					ProjectName: "myproject",
					ServiceName: "my-app",
				},
			},
		},
		Secrets: []domain.ServiceSecret{
			{
				ID:               secretID,
				ServiceID:        serviceID,
				Name:             "DB_PASSWORD",
				EncryptedValue:   []byte("encrypted-value"),
				EncryptionMethod: domain.EncryptionAES256,
				Version:          1,
				CreatedAt:        time.Now(),
				UpdatedAt:        time.Now(),
			},
		},
	}
}

func TestDesiredStateBuilder_Build(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Verify identity fields.
	serviceID, envID, artifactID, _, _ := testIDs()
	if spec.ServiceID != serviceID {
		t.Errorf("ServiceID = %v, want %v", spec.ServiceID, serviceID)
	}
	if spec.EnvironmentID != envID {
		t.Errorf("EnvironmentID = %v, want %v", spec.EnvironmentID, envID)
	}
	if spec.ArtifactID != artifactID {
		t.Errorf("ArtifactID = %v, want %v", spec.ArtifactID, artifactID)
	}
	if spec.DeploymentUnitKey != domain.DefaultDeploymentUnitKey {
		t.Errorf("DeploymentUnitKey = %q, want %q", spec.DeploymentUnitKey, domain.DefaultDeploymentUnitKey)
	}
	if spec.UnitRuntimeType != domain.RuntimeTypeCompose {
		t.Errorf("UnitRuntimeType = %q, want %q", spec.UnitRuntimeType, domain.RuntimeTypeCompose)
	}

	// Verify stable service key is normalized.
	if spec.StableServiceKey != "my-app-container" {
		t.Errorf("StableServiceKey = %q, want %q", spec.StableServiceKey, "my-app-container")
	}

	// Verify image ref uses digest when available.
	wantImage := "ghcr.io/org/my-app@sha256:abc123"
	if spec.ImageRef != wantImage {
		t.Errorf("ImageRef = %q, want %q", spec.ImageRef, wantImage)
	}

	// Verify process fields.
	if len(spec.Command) != 2 || spec.Command[0] != "serve" {
		t.Errorf("Command = %v, want [serve --port=80]", spec.Command)
	}
	if len(spec.Entrypoint) != 1 || spec.Entrypoint[0] != "/entrypoint.sh" {
		t.Errorf("Entrypoint = %v, want [/entrypoint.sh]", spec.Entrypoint)
	}
	if spec.WorkDir != "/app" {
		t.Errorf("WorkDir = %q, want %q", spec.WorkDir, "/app")
	}
	if spec.RestartPolicy != "unless-stopped" {
		t.Errorf("RestartPolicy = %q, want %q", spec.RestartPolicy, "unless-stopped")
	}

	// Verify schema version.
	if spec.SchemaVersion != domain.DesiredStateSchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", spec.SchemaVersion, domain.DesiredStateSchemaVersion)
	}

	// Verify Compose extension is populated.
	if spec.ComposeExtension == nil {
		t.Fatal("ComposeExtension is nil, want non-nil for Compose runtime")
	}
	if spec.ComposeExtension.ProjectName != "myproject" {
		t.Errorf("ComposeExtension.ProjectName = %q, want %q", spec.ComposeExtension.ProjectName, "myproject")
	}

	// Verify desired hash is set.
	if spec.DesiredHash == "" {
		t.Fatal("DesiredHash is empty")
	}
}

func TestDesiredStateBuilder_BahiaLabelsAlwaysInjected(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Verify all required Bahia labels.
	requiredLabels := map[string]string{
		"bahia.managed":             "true",
		"bahia.service_id":          input.Service.ID.String(),
		"bahia.environment_id":      input.Environment.ID.String(),
		"bahia.deployment_unit_key": domain.DefaultDeploymentUnitKey,
		"bahia.artifact_id":         input.Artifact.ID.String(),
	}

	for label, want := range requiredLabels {
		got, ok := spec.Labels[label]
		if !ok {
			t.Errorf("missing required label %q", label)
			continue
		}
		if got != want {
			t.Errorf("label %q = %q, want %q", label, got, want)
		}
	}

	// bahia.desired_hash must equal DesiredHash.
	if got := spec.Labels["bahia.desired_hash"]; got != spec.DesiredHash {
		t.Errorf("bahia.desired_hash label = %q, want %q", got, spec.DesiredHash)
	}

	// Original user labels should be preserved alongside Bahia labels.
	if got := spec.Labels["app.version"]; got != "1.0" {
		t.Errorf("user label app.version = %q, want %q", got, "1.0")
	}
}

func TestDesiredStateBuilder_UsesExplicitDeploymentUnitIdentity(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()
	unitID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	input.DeploymentUnit = &domain.DeploymentUnit{
		ID:            unitID,
		EnvironmentID: input.Environment.ID,
		Key:           "edge",
		RuntimeType:   domain.RuntimeTypeDocker,
	}

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if spec.DeploymentUnitID == nil || *spec.DeploymentUnitID != unitID {
		t.Fatalf("DeploymentUnitID = %v, want %s", spec.DeploymentUnitID, unitID)
	}
	if spec.DeploymentUnitKey != "edge" {
		t.Errorf("DeploymentUnitKey = %q, want edge", spec.DeploymentUnitKey)
	}
	if spec.UnitRuntimeType != domain.RuntimeTypeDocker {
		t.Errorf("UnitRuntimeType = %q, want docker", spec.UnitRuntimeType)
	}
	if spec.Labels["bahia.deployment_unit_id"] != unitID.String() {
		t.Errorf("deployment unit id label missing or wrong")
	}
}

func TestDesiredStateBuilder_SecretValuesNeverIncluded(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// DB_PASSWORD should NOT be in literal env.
	if v, ok := spec.Env["DB_PASSWORD"]; ok {
		t.Errorf("secret DB_PASSWORD found in literal env with value %q — must not be included", v)
	}

	// DB_PASSWORD should be in SecretRefs with redacted value.
	found := false
	for _, ref := range spec.SecretRefs {
		if ref.EnvVar == "DB_PASSWORD" {
			found = true
			if ref.RedactedValue != "REDACTED(DB_PASSWORD)" {
				t.Errorf("SecretRef RedactedValue = %q, want %q", ref.RedactedValue, "REDACTED(DB_PASSWORD)")
			}
			if ref.SecretID == uuid.Nil {
				t.Error("SecretRef SecretID is nil")
			}
			break
		}
	}
	if !found {
		t.Error("DB_PASSWORD not found in SecretRefs")
	}

	// Non-secret env should be preserved.
	if v, ok := spec.Env["APP_ENV"]; !ok || v != "production" {
		t.Errorf("non-secret env APP_ENV = %q, want %q", v, "production")
	}

	// Verify the safety check passes.
	if spec.ContainsPlaintextSecret() {
		t.Error("ContainsPlaintextSecret() = true, want false")
	}
}

func TestDesiredStateBuilder_HashStability(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()

	spec1, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() first call error: %v", err)
	}

	spec2, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() second call error: %v", err)
	}

	if spec1.DesiredHash != spec2.DesiredHash {
		t.Errorf("Hash is not stable: %q != %q", spec1.DesiredHash, spec2.DesiredHash)
	}

	// Changing a field should change the hash.
	input.Artifact.ImageTag = "v2.0.0"
	input.Artifact.ImageDigest = "sha256:def456"
	spec3, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() changed input error: %v", err)
	}
	if spec3.DesiredHash == spec1.DesiredHash {
		t.Error("Hash should change when artifact changes, but got same hash")
	}
}

func TestDesiredStateBuilder_ValidationErrors(t *testing.T) {
	builder := NewDesiredStateBuilder()

	tests := []struct {
		name  string
		input BuildInput
	}{
		{
			name:  "nil service",
			input: BuildInput{Environment: &domain.Environment{}, Artifact: &domain.Artifact{}},
		},
		{
			name:  "nil environment",
			input: BuildInput{Service: &domain.Service{}, Artifact: &domain.Artifact{}},
		},
		{
			name:  "nil artifact",
			input: BuildInput{Service: &domain.Service{}, Environment: &domain.Environment{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := builder.Build(tt.input)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDesiredStateBuilder_NoRuntimeConfig(t *testing.T) {
	builder := NewDesiredStateBuilder()
	serviceID, envID, artifactID, buildID, _ := testIDs()

	input := BuildInput{
		Service: &domain.Service{
			ID:          serviceID,
			Name:        "bare-service",
			RuntimeType: domain.RuntimeTypeDocker,
		},
		Environment: &domain.Environment{
			ID:   envID,
			Name: "staging",
		},
		Artifact: &domain.Artifact{
			ID:        artifactID,
			BuildID:   buildID,
			ServiceID: serviceID,
			ImageRepo: "ghcr.io/org/bare",
			ImageTag:  "latest",
		},
	}

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should still have Bahia labels.
	if spec.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label")
	}

	// StableServiceKey should fall back to service name.
	if spec.StableServiceKey != "bare-service" {
		t.Errorf("StableServiceKey = %q, want %q", spec.StableServiceKey, "bare-service")
	}

	// Docker extension should be set.
	if spec.DockerExtension == nil {
		t.Error("DockerExtension is nil for Docker runtime type")
	}

	// Hash should still be computed.
	if spec.DesiredHash == "" {
		t.Error("DesiredHash is empty")
	}
}

func TestDesiredStateBuilder_DockerRuntime(t *testing.T) {
	builder := NewDesiredStateBuilder()
	serviceID, envID, artifactID, buildID, _ := testIDs()

	input := BuildInput{
		Service: &domain.Service{
			ID:          serviceID,
			Name:        "docker-svc",
			RuntimeType: domain.RuntimeTypeDocker,
		},
		Environment: &domain.Environment{ID: envID, Name: "prod"},
		Artifact:    &domain.Artifact{ID: artifactID, BuildID: buildID, ServiceID: serviceID, ImageRepo: "img", ImageTag: "v1"},
	}

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if spec.DockerExtension == nil {
		t.Error("expected DockerExtension, got nil")
	}
	if spec.ComposeExtension != nil {
		t.Error("expected nil ComposeExtension for Docker runtime")
	}
}

func TestDesiredStateBuilder_DockerRuntimePreservesAdoptedTargetName(t *testing.T) {
	builder := NewDesiredStateBuilder()
	serviceID, envID, artifactID, buildID, _ := testIDs()
	adopted := &domain.AdoptedRuntimeConfig{TargetName: "bahia-web"}
	input := BuildInput{
		Service: &domain.Service{
			ID: serviceID, Name: "web", RuntimeType: domain.RuntimeTypeDocker,
			RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adopted},
		},
		Environment:   &domain.Environment{ID: envID, Name: "prod"},
		Artifact:      &domain.Artifact{ID: artifactID, BuildID: buildID, ServiceID: serviceID, ImageRepo: "img", ImageTag: "v1"},
		RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adopted},
	}

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if spec.DockerExtension == nil || spec.DockerExtension.ContainerName != "bahia-web" {
		t.Fatalf("DockerExtension = %#v, want adopted container name", spec.DockerExtension)
	}
}

func TestValidateSpec(t *testing.T) {
	builder := NewDesiredStateBuilder()
	input := makeTestInput()

	spec, err := builder.Build(input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Valid spec should pass.
	if err := ValidateSpec(spec); err != nil {
		t.Errorf("ValidateSpec() error on valid spec: %v", err)
	}

	// Missing label should fail.
	delete(spec.Labels, "bahia.managed")
	if err := ValidateSpec(spec); err == nil {
		t.Error("ValidateSpec() should fail with missing bahia.managed label")
	}
}

func TestDesiredStateBuilderBuildsManagedComposeDefinition(t *testing.T) {
	input := makeTestInput()
	input.Artifact.ImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secretID := input.Secrets[0].ID
	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "arcana-web",
		Ports:         []string{"8080:8080"},
		Command:       []string{"nginx", "-g", "daemon off;"},
		Environment:   map[string]string{"PUBLIC_MODE": "production"},
		SecretRefs:    []domain.ManagedSecretReference{{EnvVar: "API_TOKEN", SecretID: secretID}},
		Healthcheck: &domain.ManagedHTTPHealthcheck{
			Protocol: "http", Method: "GET", Path: "/healthz", Port: 8080,
			Interval: "30s", Timeout: "5s", Retries: 3,
		},
		RestartPolicy:  "unless-stopped",
		ResourceLimits: &domain.RuntimeResourceLimits{CPUMillis: 500, MemoryBytes: 268435456},
		PullPolicy:     "always",
	}
	input.Service.RuntimeConfig = &domain.ServiceRuntimeConfig{Managed: managed}
	input.RuntimeConfig = input.Service.RuntimeConfig

	spec, err := NewDesiredStateBuilder().Build(input)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if spec.StableServiceKey != "arcana-web" || spec.ImageRef != "ghcr.io/org/my-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("managed identity or immutable image mismatch: %#v", spec)
	}
	if spec.Healthcheck == nil || spec.Healthcheck.Method != "GET" || spec.Healthcheck.Path != "/healthz" || spec.Healthcheck.Port != 8080 {
		t.Fatalf("managed healthcheck mismatch: %#v", spec.Healthcheck)
	}
	if len(spec.SecretRefs) != 1 || spec.SecretRefs[0].EnvVar != "API_TOKEN" || spec.SecretRefs[0].RedactedValue != "REDACTED(API_TOKEN)" {
		t.Fatalf("secret references mismatch: %#v", spec.SecretRefs)
	}
	if spec.ResourceLimits == nil || spec.ResourceLimits.CPUMillis != 500 || spec.ResourceLimits.MemoryBytes != 268435456 {
		t.Fatalf("resource limits mismatch: %#v", spec.ResourceLimits)
	}
	if spec.Env["PUBLIC_MODE"] != "production" || spec.DesiredHash == "" {
		t.Fatalf("managed literals or desired hash missing: %#v", spec)
	}
}

func TestDesiredStateBuilderRejectsUnavailableManagedSecretReference(t *testing.T) {
	input := makeTestInput()
	input.Artifact.ImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	input.RuntimeConfig = &domain.ServiceRuntimeConfig{Managed: &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "web",
		SecretRefs:    []domain.ManagedSecretReference{{EnvVar: "TOKEN", SecretID: uuid.New()}},
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
	}}
	input.Service.RuntimeConfig = input.RuntimeConfig
	if _, err := NewDesiredStateBuilder().Build(input); err == nil {
		t.Fatal("expected unavailable secret reference to fail")
	}
}

func TestDesiredStateBuilderRejectsMutableManagedArtifact(t *testing.T) {
	input := makeTestInput()
	input.RuntimeConfig = &domain.ServiceRuntimeConfig{Managed: &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "web",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
	}}
	input.Service.RuntimeConfig = input.RuntimeConfig
	if _, err := NewDesiredStateBuilder().Build(input); err == nil {
		t.Fatal("expected mutable managed artifact to fail")
	}
}

func TestDesiredStateBuilder_NormalizeServiceKey(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		expected string
	}{
		{"simple", "my-app", "my-app"},
		{"spaces", "My App Container", "my-app-container"},
		{"special chars", "app@v2.0!beta", "app-v2-0-beta"},
		{"leading trailing", "---app---", "app"},
		{"empty uses service name", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.NormalizeServiceKey(tt.target)
			if got != tt.expected && tt.expected != "" {
				t.Errorf("NormalizeServiceKey(%q) = %q, want %q", tt.target, got, tt.expected)
			}
		})
	}
}
