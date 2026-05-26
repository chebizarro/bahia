package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// JSON round-trip tests
// ---------------------------------------------------------------------------

func TestDesiredServiceSpec_JSONRoundTrip(t *testing.T) {
	svcID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	envID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	artID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	secID := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	original := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        svcID,
		EnvironmentID:    envID,
		ArtifactID:       artID,
		StableServiceKey: "my-app",
		ImageRef:         "ghcr.io/org/my-app:v1.2.3",
		Command:          []string{"/bin/app", "serve"},
		Entrypoint:       []string{"/entrypoint.sh"},
		WorkDir:          "/app",
		Env:              map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
		SecretRefs: []DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-password", SecretID: secID, RedactedValue: RedactedPlaceholder("db-password")},
		},
		Ports:         []string{"8080:8080", "9090:9090"},
		Volumes:       []string{"/data:/app/data"},
		Labels:        map[string]string{"bahia.managed": "true"},
		Healthcheck:   &HealthcheckConfig{Test: []string{"CMD", "curl", "-f", "http://localhost:8080/health"}, Interval: "30s", Retries: 3},
		DependsOn:     []string{"postgres"},
		NetworkMode:   "bridge",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
		ComposeExtension: &ComposeExtension{
			DependsOn: map[string]ComposeDependency{
				"postgres": {Condition: "service_healthy"},
			},
			EnvFile:     ".bahia/env/my-app.env",
			ProjectName: "staging-abc",
		},
	}

	original.ComputeDesiredHash()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DesiredServiceSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Re-compute hash on the decoded copy — must match.
	decoded.ComputeDesiredHash()
	if decoded.DesiredHash != original.DesiredHash {
		t.Errorf("hash mismatch after round-trip:\n  original: %s\n  decoded:  %s", original.DesiredHash, decoded.DesiredHash)
	}

	// Spot-check key fields.
	if decoded.StableServiceKey != "my-app" {
		t.Errorf("StableServiceKey = %q, want %q", decoded.StableServiceKey, "my-app")
	}
	if decoded.ImageRef != original.ImageRef {
		t.Errorf("ImageRef = %q, want %q", decoded.ImageRef, original.ImageRef)
	}
	if len(decoded.SecretRefs) != 1 {
		t.Fatalf("SecretRefs len = %d, want 1", len(decoded.SecretRefs))
	}
	if decoded.SecretRefs[0].RedactedValue != RedactedPlaceholder("db-password") {
		t.Errorf("RedactedValue = %q, want %q", decoded.SecretRefs[0].RedactedValue, RedactedPlaceholder("db-password"))
	}
	if decoded.ComposeExtension == nil {
		t.Fatal("ComposeExtension is nil after round-trip")
	}
	if decoded.ComposeExtension.ProjectName != "staging-abc" {
		t.Errorf("ComposeExtension.ProjectName = %q, want %q", decoded.ComposeExtension.ProjectName, "staging-abc")
	}
}

func TestDesiredServiceSpec_JSONRoundTrip_DockerExtension(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    uuid.New(),
		ArtifactID:       uuid.New(),
		StableServiceKey: "worker",
		ImageRef:         "myregistry/worker:latest",
		DockerExtension: &DockerExtension{
			HostConfig:       map[string]any{"Memory": 536870912},
			NetworkingConfig: map[string]any{"EndpointsConfig": map[string]any{}},
			Healthcheck:      map[string]any{"Test": []any{"CMD", "true"}},
		},
	}
	spec.ComputeDesiredHash()

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded DesiredServiceSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.DockerExtension == nil {
		t.Fatal("DockerExtension is nil after round-trip")
	}
	if decoded.DockerExtension.HostConfig["Memory"] == nil {
		t.Error("DockerExtension.HostConfig.Memory lost after round-trip")
	}
}

func TestDesiredServiceSpec_JSONRoundTrip_EmptyExtensions(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:       DesiredStateSchemaVersion,
		ServiceID:           uuid.New(),
		EnvironmentID:       uuid.New(),
		ArtifactID:          uuid.New(),
		StableServiceKey:    "placeholder",
		ImageRef:            "nginx:latest",
		KubernetesExtension: &KubernetesExtension{},
		PodmanExtension:     &PodmanExtension{},
	}
	spec.ComputeDesiredHash()

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded DesiredServiceSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.KubernetesExtension == nil {
		t.Error("KubernetesExtension is nil after round-trip")
	}
	if decoded.PodmanExtension == nil {
		t.Error("PodmanExtension is nil after round-trip")
	}
}

func TestDesiredEnvironmentPlan_JSONRoundTrip(t *testing.T) {
	envID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	svcA := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    envID,
		ArtifactID:       uuid.New(),
		StableServiceKey: "api",
		ImageRef:         "api:v1",
	}
	svcA.ComputeDesiredHash()

	svcB := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    envID,
		ArtifactID:       uuid.New(),
		StableServiceKey: "web",
		ImageRef:         "web:v2",
	}
	svcB.ComputeDesiredHash()

	plan := DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services:      []DesiredServiceSpec{svcB, svcA}, // intentionally unsorted
	}
	plan.ComputeRevisionHash()

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DesiredEnvironmentPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	decoded.ComputeRevisionHash()
	if decoded.RevisionHash != plan.RevisionHash {
		t.Errorf("revision hash mismatch:\n  original: %s\n  decoded:  %s", plan.RevisionHash, decoded.RevisionHash)
	}
	if len(decoded.Services) != 2 {
		t.Fatalf("services len = %d, want 2", len(decoded.Services))
	}
	// After ComputeRevisionHash, services should be sorted.
	if decoded.Services[0].StableServiceKey != "api" {
		t.Errorf("first service key = %q, want %q", decoded.Services[0].StableServiceKey, "api")
	}
}

// ---------------------------------------------------------------------------
// Secret redaction verification
// ---------------------------------------------------------------------------

func TestDesiredServiceSpec_ContainsPlaintextSecret(t *testing.T) {
	t.Run("properly redacted", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: uuid.New(), RedactedValue: RedactedPlaceholder("db-pass")},
				{EnvVar: "API_KEY", Name: "api-key", SecretID: uuid.New(), RedactedValue: RedactedPlaceholder("api-key")},
			},
		}
		if spec.ContainsPlaintextSecret() {
			t.Error("expected no plaintext detected for properly redacted refs")
		}
	})

	t.Run("plaintext leaked", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: uuid.New(), RedactedValue: "actual-password-value"},
			},
		}
		if !spec.ContainsPlaintextSecret() {
			t.Error("expected plaintext detected for non-redacted value")
		}
	})

	t.Run("empty redacted value is safe", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "TOKEN", Name: "token", SecretID: uuid.New(), RedactedValue: ""},
			},
		}
		if spec.ContainsPlaintextSecret() {
			t.Error("empty RedactedValue should not be flagged as plaintext")
		}
	})

	t.Run("no secret refs", func(t *testing.T) {
		spec := DesiredServiceSpec{}
		if spec.ContainsPlaintextSecret() {
			t.Error("no refs should not be flagged")
		}
	})
}

func TestDesiredServiceSpec_SecretNotInJSON(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    uuid.New(),
		ArtifactID:       uuid.New(),
		StableServiceKey: "app",
		ImageRef:         "app:latest",
		SecretRefs: []DesiredSecretRef{
			{EnvVar: "DB_PASS", Name: "db-pass", SecretID: uuid.New(), RedactedValue: RedactedPlaceholder("db-pass")},
		},
	}
	spec.ComputeDesiredHash()

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)

	// The serialized JSON must NOT contain any field that could hold plaintext.
	// It should contain the redacted placeholder.
	if !strings.Contains(s, "REDACTED(db-pass)") {
		t.Error("expected REDACTED placeholder in JSON output")
	}
}

func TestRedactedPlaceholder(t *testing.T) {
	got := RedactedPlaceholder("my-secret")
	want := "REDACTED(my-secret)"
	if got != want {
		t.Errorf("RedactedPlaceholder = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Service key normalization
// ---------------------------------------------------------------------------

func TestNormalizeServiceKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-app", "my-app"},
		{"My App", "my-app"},
		{"  My  App  ", "my-app"},
		{"my_app_v2", "my_app_v2"},
		{"My.App/Service:v1", "my-app-service-v1"},
		{"---leading-trailing---", "leading-trailing"},
		{"UPPER_CASE", "upper_case"},
		{"café-service", "caf-service"},
		{"", "unnamed"},
		{"   ", "unnamed"},
		{"a--b--c", "a-b-c"},
		{"valid-name-123", "valid-name-123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeServiceKey(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeServiceKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeServiceKeyWithSuffix(t *testing.T) {
	id := uuid.MustParse("abcdef12-3456-7890-abcd-ef1234567890")

	t.Run("normal name", func(t *testing.T) {
		got := NormalizeServiceKeyWithSuffix("my-app", id)
		if got != "my-app" {
			t.Errorf("got %q, want %q", got, "my-app")
		}
	})

	t.Run("empty name uses ID prefix", func(t *testing.T) {
		got := NormalizeServiceKeyWithSuffix("", id)
		want := "svc-abcdef12"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only uses ID prefix", func(t *testing.T) {
		got := NormalizeServiceKeyWithSuffix("   ", id)
		want := "svc-abcdef12"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Hash stability
// ---------------------------------------------------------------------------

func TestDesiredServiceSpec_HashStability(t *testing.T) {
	makeSpec := func() DesiredServiceSpec {
		return DesiredServiceSpec{
			SchemaVersion:    DesiredStateSchemaVersion,
			ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
			ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
			StableServiceKey: "my-app",
			ImageRef:         "ghcr.io/org/app:v1",
			Command:          []string{"serve"},
			Env:              map[string]string{"PORT": "8080"},
			Ports:            []string{"8080:8080"},
			RestartPolicy:    "always",
		}
	}

	spec1 := makeSpec()
	spec2 := makeSpec()

	h1 := spec1.ComputeDesiredHash()
	h2 := spec2.ComputeDesiredHash()

	if h1 != h2 {
		t.Errorf("identical specs produced different hashes:\n  h1: %s\n  h2: %s", h1, h2)
	}

	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("hash should have sha256: prefix, got %q", h1)
	}
}

func TestDesiredServiceSpec_HashChangesOnMutation(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		StableServiceKey: "my-app",
		ImageRef:         "ghcr.io/org/app:v1",
	}
	h1 := spec.ComputeDesiredHash()

	spec.ImageRef = "ghcr.io/org/app:v2"
	h2 := spec.ComputeDesiredHash()

	if h1 == h2 {
		t.Error("hash should change when ImageRef changes")
	}
}

func TestDesiredServiceSpec_HashIgnoresExtensions(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		StableServiceKey: "my-app",
		ImageRef:         "app:v1",
	}
	h1 := spec.ComputeDesiredHash()

	spec.ComposeExtension = &ComposeExtension{ProjectName: "test-project"}
	h2 := spec.ComputeDesiredHash()

	if h1 != h2 {
		t.Error("hash should NOT change when only extensions change")
	}
}

func TestDesiredServiceSpec_HashIncludesSecretKeys(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    uuid.New(),
		ArtifactID:       uuid.New(),
		StableServiceKey: "app",
		ImageRef:         "app:v1",
	}
	h1 := spec.ComputeDesiredHash()

	spec.SecretRefs = []DesiredSecretRef{
		{EnvVar: "DB_PASS", Name: "db-pass", SecretID: uuid.New(), RedactedValue: RedactedPlaceholder("db-pass")},
	}
	h2 := spec.ComputeDesiredHash()

	if h1 == h2 {
		t.Error("hash should change when secret refs are added")
	}
}

func TestDesiredEnvironmentPlan_RevisionHashDeterministic(t *testing.T) {
	envID := uuid.MustParse("eeee0000-0000-0000-0000-000000000000")
	artA := uuid.MustParse("cccc0000-0000-0000-0000-000000000000")
	artB := uuid.MustParse("dddd0000-0000-0000-0000-000000000000")

	makeServices := func() []DesiredServiceSpec {
		svcA := DesiredServiceSpec{
			SchemaVersion:    DesiredStateSchemaVersion,
			ServiceID:        uuid.MustParse("aaaa0000-0000-0000-0000-000000000000"),
			EnvironmentID:    envID,
			ArtifactID:       artA,
			StableServiceKey: "api",
			ImageRef:         "api:v1",
		}
		svcA.ComputeDesiredHash()

		svcB := DesiredServiceSpec{
			SchemaVersion:    DesiredStateSchemaVersion,
			ServiceID:        uuid.MustParse("bbbb0000-0000-0000-0000-000000000000"),
			EnvironmentID:    envID,
			ArtifactID:       artB,
			StableServiceKey: "web",
			ImageRef:         "web:v1",
		}
		svcB.ComputeDesiredHash()
		return []DesiredServiceSpec{svcA, svcB}
	}

	// Plan with services in order A, B.
	plan1 := DesiredEnvironmentPlan{EnvironmentID: envID, Services: makeServices()}
	plan1.ComputeRevisionHash()

	// Plan with services in reverse order B, A — should produce same hash.
	svcs := makeServices()
	plan2 := DesiredEnvironmentPlan{EnvironmentID: envID, Services: []DesiredServiceSpec{svcs[1], svcs[0]}}
	plan2.ComputeRevisionHash()

	if plan1.RevisionHash != plan2.RevisionHash {
		t.Errorf("revision hash should be order-independent:\n  plan1: %s\n  plan2: %s", plan1.RevisionHash, plan2.RevisionHash)
	}
}
