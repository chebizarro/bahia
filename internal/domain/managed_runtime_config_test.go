package domain

import (
	"testing"

	"github.com/google/uuid"
)

func validManagedRuntimeConfig() *ManagedRuntimeConfig {
	return &ManagedRuntimeConfig{
		ServiceName:    "arcana-web",
		Ports:          []string{"8080:8080"},
		Environment:    map[string]string{"PUBLIC_MODE": "production"},
		SecretRefs:     []ManagedSecretReference{{EnvVar: "API_TOKEN", SecretID: uuid.MustParse("00000000-0000-0000-0000-000000000111")}},
		Healthcheck:    &ManagedHTTPHealthcheck{Protocol: "HTTP", Method: "get", Path: "/healthz", Port: 8080, Interval: "30s", Timeout: "5s", Retries: 3},
		RestartPolicy:  "UNLESS-STOPPED",
		ResourceLimits: &RuntimeResourceLimits{CPUMillis: 500, MemoryBytes: 268435456},
	}
}

func TestNormalizeAndValidateManagedRuntimeConfig(t *testing.T) {
	config := NormalizeManagedRuntimeConfig(validManagedRuntimeConfig())
	if config.SchemaVersion != ManagedRuntimeConfigSchemaVersion || config.Healthcheck.Protocol != "http" || config.Healthcheck.Method != "GET" {
		t.Fatalf("configuration was not normalized: %#v", config)
	}
	if config.PullPolicy != "always" || config.RestartPolicy != "unless-stopped" {
		t.Fatalf("defaults were not normalized: %#v", config)
	}
	if err := ValidateManagedRuntimeConfig(config); err != nil {
		t.Fatalf("ValidateManagedRuntimeConfig: %v", err)
	}
}

func TestManagedRuntimeConfigRejectsSecretLiteralCollision(t *testing.T) {
	config := NormalizeManagedRuntimeConfig(validManagedRuntimeConfig())
	config.Environment["API_TOKEN"] = "plaintext"
	if err := ValidateManagedRuntimeConfig(config); err == nil {
		t.Fatal("expected literal and secret reference collision to fail")
	}
}

func TestManagedRuntimeConfigRejectsDuplicateSecretID(t *testing.T) {
	config := NormalizeManagedRuntimeConfig(validManagedRuntimeConfig())
	config.SecretRefs = append(config.SecretRefs, ManagedSecretReference{EnvVar: "SECOND_TOKEN", SecretID: config.SecretRefs[0].SecretID})
	if err := ValidateManagedRuntimeConfig(config); err == nil {
		t.Fatal("expected one secret ID bound to multiple environment variables to fail")
	}
}

func TestManagedRuntimeConfigRejectsNonGETHealthcheck(t *testing.T) {
	config := NormalizeManagedRuntimeConfig(validManagedRuntimeConfig())
	config.Healthcheck.Method = "POST"
	if err := ValidateManagedRuntimeConfig(config); err == nil {
		t.Fatal("expected non-GET healthcheck to fail")
	}
}

func TestDesiredHashIncludesResourceLimitsAndSemanticHealthcheck(t *testing.T) {
	base := DesiredServiceSpec{
		SchemaVersion:     DesiredStateSchemaVersion,
		ServiceID:         uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EnvironmentID:     uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		DeploymentUnitKey: DefaultDeploymentUnitKey,
		ArtifactID:        uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		StableServiceKey:  "arcana-web",
		ImageRef:          "registry.example/arcana@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Healthcheck: &HealthcheckConfig{
			Test:     []string{"CMD", "wget", "-q", "--spider", "http://localhost:8080/healthz"},
			Protocol: "http", Method: "GET", Path: "/healthz", Port: 8080,
		},
		ResourceLimits: &RuntimeResourceLimits{CPUMillis: 500, MemoryBytes: 268435456},
	}
	first := base.ComputeDesiredHash()
	changedResources := base
	changedResources.ResourceLimits = &RuntimeResourceLimits{CPUMillis: 750, MemoryBytes: 268435456}
	if first == changedResources.ComputeDesiredHash() {
		t.Fatal("CPU limit change must change desired hash")
	}
	changedHealth := base
	health := *base.Healthcheck
	health.Path = "/ready"
	changedHealth.Healthcheck = &health
	if first == changedHealth.ComputeDesiredHash() {
		t.Fatal("healthcheck path change must change desired hash")
	}
}
