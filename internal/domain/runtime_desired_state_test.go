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

func TestDesiredServiceSpec_JSONRoundTrip_KubernetesExtension(t *testing.T) {
	replicas := int32(3)
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		EnvironmentID:    uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		ArtifactID:       uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		StableServiceKey: "api-server",
		ImageRef:         "ghcr.io/org/api:v2.0.0",
		KubernetesExtension: &KubernetesExtension{
			Namespace:   "production",
			Replicas:    &replicas,
			ServiceType: "ClusterIP",
			ServicePorts: []K8sServicePort{
				{Name: "http", Port: 80, TargetPort: 8080, Protocol: "TCP"},
				{Name: "grpc", Port: 9090, TargetPort: 9090, Protocol: "TCP"},
			},
			ResourceLimits:   &K8sResources{CPU: "500m", Memory: "256Mi"},
			ResourceRequests: &K8sResources{CPU: "100m", Memory: "128Mi"},
			Annotations:      map[string]string{"prometheus.io/scrape": "true"},
			NodeSelector:     map[string]string{"kubernetes.io/os": "linux"},
			Tolerations: []K8sToleration{
				{Key: "node-role", Operator: "Equal", Value: "worker", Effect: "NoSchedule"},
			},
			LivenessProbe: &K8sProbe{
				HTTPGet:             &K8sHTTPGet{Path: "/healthz", Port: 8080, Scheme: "HTTP"},
				InitialDelaySeconds: 10,
				PeriodSeconds:       30,
				TimeoutSeconds:      5,
				FailureThreshold:    3,
			},
			ReadinessProbe: &K8sProbe{
				Exec:                []string{"cat", "/tmp/ready"},
				InitialDelaySeconds: 5,
				PeriodSeconds:       10,
			},
			ImagePullSecrets: []string{"registry-creds"},
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

	// Re-compute hash on decoded — must match.
	decoded.ComputeDesiredHash()
	if decoded.DesiredHash != spec.DesiredHash {
		t.Errorf("hash mismatch after round-trip:\n  original: %s\n  decoded:  %s", spec.DesiredHash, decoded.DesiredHash)
	}

	k := decoded.KubernetesExtension
	if k == nil {
		t.Fatal("KubernetesExtension is nil after round-trip")
	}
	if k.Namespace != "production" {
		t.Errorf("Namespace = %q, want %q", k.Namespace, "production")
	}
	if k.Replicas == nil || *k.Replicas != 3 {
		t.Errorf("Replicas = %v, want 3", k.Replicas)
	}
	if k.ServiceType != "ClusterIP" {
		t.Errorf("ServiceType = %q, want %q", k.ServiceType, "ClusterIP")
	}
	if len(k.ServicePorts) != 2 {
		t.Fatalf("ServicePorts len = %d, want 2", len(k.ServicePorts))
	}
	if k.ServicePorts[0].Name != "http" || k.ServicePorts[0].Port != 80 || k.ServicePorts[0].TargetPort != 8080 {
		t.Errorf("ServicePorts[0] = %+v, unexpected", k.ServicePorts[0])
	}
	if k.ResourceLimits == nil || k.ResourceLimits.CPU != "500m" || k.ResourceLimits.Memory != "256Mi" {
		t.Errorf("ResourceLimits = %+v, unexpected", k.ResourceLimits)
	}
	if k.ResourceRequests == nil || k.ResourceRequests.CPU != "100m" {
		t.Errorf("ResourceRequests = %+v, unexpected", k.ResourceRequests)
	}
	if k.Annotations["prometheus.io/scrape"] != "true" {
		t.Errorf("Annotations = %v, unexpected", k.Annotations)
	}
	if k.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Errorf("NodeSelector = %v, unexpected", k.NodeSelector)
	}
	if len(k.Tolerations) != 1 || k.Tolerations[0].Key != "node-role" {
		t.Errorf("Tolerations = %v, unexpected", k.Tolerations)
	}
	if k.LivenessProbe == nil || k.LivenessProbe.HTTPGet == nil || k.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("LivenessProbe = %+v, unexpected", k.LivenessProbe)
	}
	if k.ReadinessProbe == nil || len(k.ReadinessProbe.Exec) != 2 {
		t.Errorf("ReadinessProbe = %+v, unexpected", k.ReadinessProbe)
	}
	if len(k.ImagePullSecrets) != 1 || k.ImagePullSecrets[0] != "registry-creds" {
		t.Errorf("ImagePullSecrets = %v, unexpected", k.ImagePullSecrets)
	}
}

func TestDesiredServiceSpec_KubernetesExtension_HashIgnored(t *testing.T) {
	// KubernetesExtension content must NOT affect the desired hash — extensions
	// are excluded from hashing per the hashInput struct design.
	base := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		EnvironmentID:    uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		ArtifactID:       uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		StableServiceKey: "api-server",
		ImageRef:         "ghcr.io/org/api:v2.0.0",
	}
	h1 := base.ComputeDesiredHash()

	replicas := int32(5)
	base.KubernetesExtension = &KubernetesExtension{
		Namespace:        "staging",
		Replicas:         &replicas,
		ServiceType:      "LoadBalancer",
		ImagePullSecrets: []string{"my-secret"},
	}
	h2 := base.ComputeDesiredHash()

	if h1 != h2 {
		t.Errorf("KubernetesExtension should not affect desired hash:\n  without: %s\n  with:    %s", h1, h2)
	}
}

func TestDesiredServiceSpec_KubernetesExtension_HashStability(t *testing.T) {
	// Two identical specs with KubernetesExtension must produce the same desired hash.
	replicas := int32(2)
	makeSpec := func() DesiredServiceSpec {
		r := replicas
		return DesiredServiceSpec{
			SchemaVersion:    DesiredStateSchemaVersion,
			ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
			ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
			StableServiceKey: "worker",
			ImageRef:         "worker:v1",
			RestartPolicy:    "Always",
			KubernetesExtension: &KubernetesExtension{
				Namespace: "default",
				Replicas:  &r,
				ResourceLimits: &K8sResources{
					CPU:    "250m",
					Memory: "128Mi",
				},
			},
		}
	}

	s1 := makeSpec()
	s2 := makeSpec()
	h1 := s1.ComputeDesiredHash()
	h2 := s2.ComputeDesiredHash()

	if h1 != h2 {
		t.Errorf("identical specs produced different hashes:\n  h1: %s\n  h2: %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("hash missing sha256: prefix: %q", h1)
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

// ---------------------------------------------------------------------------
// Golden hash fixtures — these lock hash stability across serialization changes.
// If any of these tests fail, it means the canonical serialization has changed
// and hash continuity is broken. This is a breaking change for drift detection
// and no-op apply logic.
// ---------------------------------------------------------------------------

func TestDesiredServiceSpec_GoldenHash(t *testing.T) {
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		StableServiceKey: "my-app",
		ImageRef:         "ghcr.io/org/my-app:v1.2.3",
		Command:          []string{"/bin/app", "serve"},
		Entrypoint:       []string{"/entrypoint.sh"},
		WorkDir:          "/app",
		Env:              map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
		SecretRefs: []DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-password", SecretID: uuid.MustParse("44444444-4444-4444-4444-444444444444"), RedactedValue: "REDACTED(db-password)"},
		},
		Ports:         []string{"8080:8080", "9090:9090"},
		Volumes:       []string{"/data:/app/data"},
		Labels:        map[string]string{"bahia.managed": "true"},
		Healthcheck:   &HealthcheckConfig{Test: []string{"CMD", "curl", "-f", "http://localhost:8080/health"}, Interval: "30s", Retries: 3},
		DependsOn:     []string{"postgres"},
		NetworkMode:   "bridge",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
	}

	got := spec.ComputeDesiredHash()

	// This is the golden hash. If this changes, canonical serialization has broken.
	// To update: run the test, get the new hash, verify the serialization change
	// is intentional, bump DesiredStateSchemaVersion, and update this value.
	want := "sha256:8ca65902196c5983f49c14be60d8368f8a92d239bd0abc76b267e46619e55e80"
	if got != want {
		t.Fatalf("golden desired hash changed — canonical serialization broken:\n  got:  %s\n  want: %s", got, want)
	}

	// Verify determinism: compute again and compare.
	spec2 := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ArtifactID:       uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		StableServiceKey: "my-app",
		ImageRef:         "ghcr.io/org/my-app:v1.2.3",
		Command:          []string{"/bin/app", "serve"},
		Entrypoint:       []string{"/entrypoint.sh"},
		WorkDir:          "/app",
		Env:              map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
		SecretRefs: []DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-password", SecretID: uuid.MustParse("44444444-4444-4444-4444-444444444444"), RedactedValue: "REDACTED(db-password)"},
		},
		Ports:         []string{"8080:8080", "9090:9090"},
		Volumes:       []string{"/data:/app/data"},
		Labels:        map[string]string{"bahia.managed": "true"},
		Healthcheck:   &HealthcheckConfig{Test: []string{"CMD", "curl", "-f", "http://localhost:8080/health"}, Interval: "30s", Retries: 3},
		DependsOn:     []string{"postgres"},
		NetworkMode:   "bridge",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
	}
	got2 := spec2.ComputeDesiredHash()

	if got != got2 {
		t.Fatalf("golden hash not deterministic:\n  first:  %s\n  second: %s", got, got2)
	}

	t.Logf("Golden desired hash: %s", got)
}

func TestDesiredServiceSpec_GoldenHash_Minimal(t *testing.T) {
	// Minimal spec — verifies hash stability for services with no optional fields.
	spec := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		EnvironmentID:    uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		ArtifactID:       uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		StableServiceKey: "nginx",
		ImageRef:         "nginx:1.25",
	}

	got := spec.ComputeDesiredHash()
	want := "sha256:ef26e7506549ae9258556d0f97a75ce74bfb62fad60865bad5c76fc2eaca0691"
	if got != want {
		t.Fatalf("minimal golden desired hash changed — canonical serialization broken:\n  got:  %s\n  want: %s", got, want)
	}

	// Rebuild from scratch and verify determinism.
	spec2 := DesiredServiceSpec{
		SchemaVersion:    DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		EnvironmentID:    uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		ArtifactID:       uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		StableServiceKey: "nginx",
		ImageRef:         "nginx:1.25",
	}
	got2 := spec2.ComputeDesiredHash()

	if got != got2 {
		t.Fatalf("minimal golden hash not deterministic:\n  first:  %s\n  second: %s", got, got2)
	}

	t.Logf("Golden minimal desired hash: %s", got)
}

func TestNormalizedObservation_GoldenHash(t *testing.T) {
	obs := NormalizedObservation{
		SchemaVersion:      "1",
		ImageRef:           "ghcr.io/org/my-app:v1.2.3",
		ImageDigest:        "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Command:            []string{"/bin/app", "serve"},
		Entrypoint:         []string{"/entrypoint.sh"},
		WorkDir:            "/app",
		Env:                map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
		SecretEnvKeys:      []string{"DB_PASSWORD"},
		Ports:              []string{"8080:8080", "9090:9090"},
		Volumes:            []string{"/data:/app/data"},
		RestartPolicy:      "unless-stopped",
		NetworkAttachments: []string{"bahia-net", "default"},
		BahiaLabels:        map[string]string{"bahia.managed": "true", "bahia.service_id": "11111111-1111-1111-1111-111111111111"},
	}

	got := obs.ComputeObservationHash()
	want := "sha256:a43e0096d1b415177274da66dff0f90d42f0dfd570f9e6118b78e698648d0a34"
	if got != want {
		t.Fatalf("golden observation hash changed — canonical serialization broken:\n  got:  %s\n  want: %s", got, want)
	}

	// Rebuild from scratch and verify determinism.
	obs2 := NormalizedObservation{
		SchemaVersion:      "1",
		ImageRef:           "ghcr.io/org/my-app:v1.2.3",
		ImageDigest:        "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Command:            []string{"/bin/app", "serve"},
		Entrypoint:         []string{"/entrypoint.sh"},
		WorkDir:            "/app",
		Env:                map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
		SecretEnvKeys:      []string{"DB_PASSWORD"},
		Ports:              []string{"8080:8080", "9090:9090"},
		Volumes:            []string{"/data:/app/data"},
		RestartPolicy:      "unless-stopped",
		NetworkAttachments: []string{"bahia-net", "default"},
		BahiaLabels:        map[string]string{"bahia.managed": "true", "bahia.service_id": "11111111-1111-1111-1111-111111111111"},
	}
	got2 := obs2.ComputeObservationHash()

	if got != got2 {
		t.Fatalf("golden observation hash not deterministic:\n  first:  %s\n  second: %s", got, got2)
	}

	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("observation hash missing sha256: prefix, got %q", got)
	}

	t.Logf("Golden observation hash: %s", got)
}

func TestNormalizedObservation_GoldenHash_Minimal(t *testing.T) {
	obs := NormalizedObservation{
		SchemaVersion: "1",
		ImageRef:      "nginx:1.25",
	}

	got := obs.ComputeObservationHash()
	want := "sha256:ee47aea3161532209ef9d4022324903a5bd414941f3a40d8d60fe7ea5256ec3a"
	if got != want {
		t.Fatalf("minimal observation golden hash changed — canonical serialization broken:\n  got:  %s\n  want: %s", got, want)
	}

	obs2 := NormalizedObservation{
		SchemaVersion: "1",
		ImageRef:      "nginx:1.25",
	}
	got2 := obs2.ComputeObservationHash()

	if got != got2 {
		t.Fatalf("minimal observation golden hash not deterministic:\n  first:  %s\n  second: %s", got, got2)
	}

	t.Logf("Golden minimal observation hash: %s", got)
}

// ---------------------------------------------------------------------------
// Observation hash behavior tests
// ---------------------------------------------------------------------------

func TestNormalizedObservation_HashChangesOnImageChange(t *testing.T) {
	obs := NormalizedObservation{
		SchemaVersion: "1",
		ImageRef:      "app:v1",
	}
	h1 := obs.ComputeObservationHash()

	obs.ImageRef = "app:v2"
	h2 := obs.ComputeObservationHash()

	if h1 == h2 {
		t.Error("observation hash should change when ImageRef changes")
	}
}

func TestNormalizedObservation_HashIgnoresOrder(t *testing.T) {
	obs1 := NormalizedObservation{
		SchemaVersion:      "1",
		ImageRef:           "app:v1",
		Ports:              []string{"9090:9090", "8080:8080"},
		NetworkAttachments: []string{"net-b", "net-a"},
		Volumes:            []string{"/z:/z", "/a:/a"},
	}
	h1 := obs1.ComputeObservationHash()

	obs2 := NormalizedObservation{
		SchemaVersion:      "1",
		ImageRef:           "app:v1",
		Ports:              []string{"8080:8080", "9090:9090"},
		NetworkAttachments: []string{"net-a", "net-b"},
		Volumes:            []string{"/a:/a", "/z:/z"},
	}
	h2 := obs2.ComputeObservationHash()

	if h1 != h2 {
		t.Errorf("observation hash should be order-independent for slices:\n  h1: %s\n  h2: %s", h1, h2)
	}
}

func TestNormalizedObservation_HashExcludesVolatileFields(t *testing.T) {
	obs := NormalizedObservation{
		SchemaVersion: "1",
		ImageRef:      "app:v1",
	}
	h1 := obs.ComputeObservationHash()

	// ObservationHash itself should not affect the next computation.
	obs.ObservationHash = "sha256:something-old"
	h2 := obs.ComputeObservationHash()

	if h1 != h2 {
		t.Error("observation hash should not be affected by the ObservationHash field itself")
	}
}

func TestNormalizedObservation_SecretKeysAffectHash(t *testing.T) {
	obs := NormalizedObservation{
		SchemaVersion: "1",
		ImageRef:      "app:v1",
	}
	h1 := obs.ComputeObservationHash()

	obs.SecretEnvKeys = []string{"API_KEY"}
	h2 := obs.ComputeObservationHash()

	if h1 == h2 {
		t.Error("observation hash should change when secret env keys are added")
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestFilterBahiaLabels(t *testing.T) {
	labels := map[string]string{
		"bahia.managed":                   "true",
		"bahia.service_id":                "abc",
		"com.docker.compose.project":      "myproject",
		"com.docker.compose.service":      "web",
		"org.opencontainers.image.source": "https://github.com/org/repo",
	}

	got := FilterBahiaLabels(labels)

	if len(got) != 2 {
		t.Fatalf("expected 2 bahia labels, got %d", len(got))
	}
	if got["bahia.managed"] != "true" {
		t.Error("missing bahia.managed")
	}
	if got["bahia.service_id"] != "abc" {
		t.Error("missing bahia.service_id")
	}
}

func TestFilterBahiaLabels_Empty(t *testing.T) {
	got := FilterBahiaLabels(nil)
	if len(got) != 0 {
		t.Errorf("expected empty map, got %d entries", len(got))
	}
}

func TestFilterNonSecretEnv(t *testing.T) {
	env := map[string]string{
		"PORT":        "8080",
		"LOG_LEVEL":   "info",
		"DB_PASSWORD": "secret123",
		"API_KEY":     "key456",
	}
	secretNames := map[string]bool{
		"DB_PASSWORD": true,
		"API_KEY":     true,
	}

	nonSecret, secretKeys := FilterNonSecretEnv(env, secretNames)

	if len(nonSecret) != 2 {
		t.Fatalf("expected 2 non-secret env vars, got %d", len(nonSecret))
	}
	if nonSecret["PORT"] != "8080" {
		t.Error("missing PORT")
	}
	if nonSecret["LOG_LEVEL"] != "info" {
		t.Error("missing LOG_LEVEL")
	}
	if _, ok := nonSecret["DB_PASSWORD"]; ok {
		t.Error("DB_PASSWORD should not be in non-secret map")
	}

	if len(secretKeys) != 2 {
		t.Fatalf("expected 2 secret keys, got %d", len(secretKeys))
	}
	// Should be sorted.
	if secretKeys[0] != "API_KEY" || secretKeys[1] != "DB_PASSWORD" {
		t.Errorf("secret keys not sorted: %v", secretKeys)
	}
}

func TestFilterNonSecretEnv_NoSecrets(t *testing.T) {
	env := map[string]string{"PORT": "8080"}
	nonSecret, secretKeys := FilterNonSecretEnv(env, nil)

	if len(nonSecret) != 1 || nonSecret["PORT"] != "8080" {
		t.Error("non-secret env should pass through")
	}
	if len(secretKeys) != 0 {
		t.Errorf("expected no secret keys, got %v", secretKeys)
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

func TestKubernetesExtensionFromDeploymentUnit_NilUnit(t *testing.T) {
	ext := KubernetesExtensionFromDeploymentUnit(nil)
	if ext == nil {
		t.Fatal("expected non-nil extension for nil unit")
	}
	if ext.Namespace != "" || ext.Replicas != nil || ext.ServiceType != "" {
		t.Errorf("expected empty extension, got %#v", ext)
	}
}

func TestKubernetesExtensionFromDeploymentUnit_PopulatesFromRuntimeConfig(t *testing.T) {
	unit := &DeploymentUnit{
		Namespace: "prod",
		RuntimeConfig: map[string]any{
			"replicas":      float64(4), // JSON-decoded numbers arrive as float64
			"service_type":  "NodePort",
			"node_selector": map[string]any{"zone": "us-east"},
			"annotations":   map[string]any{"owner": "platform"},
		},
	}
	ext := KubernetesExtensionFromDeploymentUnit(unit)
	if ext.Namespace != "prod" {
		t.Errorf("namespace = %q, want prod", ext.Namespace)
	}
	if ext.Replicas == nil || *ext.Replicas != 4 {
		t.Errorf("replicas = %v, want 4", ext.Replicas)
	}
	if ext.ServiceType != "NodePort" {
		t.Errorf("service_type = %q, want NodePort", ext.ServiceType)
	}
	if ext.NodeSelector["zone"] != "us-east" {
		t.Errorf("node_selector[zone] = %q, want us-east", ext.NodeSelector["zone"])
	}
	if ext.Annotations["owner"] != "platform" {
		t.Errorf("annotations[owner] = %q, want platform", ext.Annotations["owner"])
	}
}

func TestKubernetesExtensionFromDeploymentUnit_IgnoresBadReplicas(t *testing.T) {
	unit := &DeploymentUnit{RuntimeConfig: map[string]any{"replicas": "not-a-number"}}
	ext := KubernetesExtensionFromDeploymentUnit(unit)
	if ext.Replicas != nil {
		t.Errorf("replicas should be nil for non-numeric config, got %v", *ext.Replicas)
	}
}

func TestKubernetesExtensionFromDeploymentUnit_ParsesRichRuntimeConfigFields(t *testing.T) {
	tests := []struct {
		name          string
		runtimeConfig map[string]any
		assert        func(t *testing.T, ext *KubernetesExtension)
	}{
		{
			name: "service ports",
			runtimeConfig: map[string]any{
				"service_ports": []any{
					map[string]any{"name": "http", "port": float64(80), "target_port": float64(8080), "protocol": "TCP", "node_port": float64(30080)},
				},
			},
			assert: func(t *testing.T, ext *KubernetesExtension) {
				t.Helper()
				if len(ext.ServicePorts) != 1 {
					t.Fatalf("ServicePorts len = %d, want 1", len(ext.ServicePorts))
				}
				port := ext.ServicePorts[0]
				if port.Name != "http" || port.Port != 80 || port.TargetPort != 8080 || port.Protocol != "TCP" || port.NodePort != 30080 {
					t.Errorf("ServicePorts[0] = %+v, unexpected", port)
				}
			},
		},
		{
			name: "resources",
			runtimeConfig: map[string]any{
				"resource_limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
				"resource_requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			},
			assert: func(t *testing.T, ext *KubernetesExtension) {
				t.Helper()
				if ext.ResourceLimits == nil || ext.ResourceLimits.CPU != "500m" || ext.ResourceLimits.Memory != "512Mi" {
					t.Errorf("ResourceLimits = %+v, unexpected", ext.ResourceLimits)
				}
				if ext.ResourceRequests == nil || ext.ResourceRequests.CPU != "100m" || ext.ResourceRequests.Memory != "128Mi" {
					t.Errorf("ResourceRequests = %+v, unexpected", ext.ResourceRequests)
				}
			},
		},
		{
			name: "probes",
			runtimeConfig: map[string]any{
				"liveness_probe": map[string]any{
					"http_get":              map[string]any{"path": "/healthz", "port": float64(8080), "scheme": "HTTP"},
					"initial_delay_seconds": float64(5),
					"period_seconds":        float64(10),
				},
				"readiness_probe": map[string]any{
					"exec":              []any{"cat", "/tmp/ready"},
					"timeout_seconds":   float64(2),
					"failure_threshold": float64(3),
				},
			},
			assert: func(t *testing.T, ext *KubernetesExtension) {
				t.Helper()
				if ext.LivenessProbe == nil || ext.LivenessProbe.HTTPGet == nil {
					t.Fatalf("LivenessProbe = %+v, want http_get", ext.LivenessProbe)
				}
				if ext.LivenessProbe.HTTPGet.Path != "/healthz" || ext.LivenessProbe.HTTPGet.Port != 8080 || ext.LivenessProbe.HTTPGet.Scheme != "HTTP" {
					t.Errorf("LivenessProbe.HTTPGet = %+v, unexpected", ext.LivenessProbe.HTTPGet)
				}
				if ext.LivenessProbe.InitialDelaySeconds != 5 || ext.LivenessProbe.PeriodSeconds != 10 {
					t.Errorf("LivenessProbe timings = %+v, unexpected", ext.LivenessProbe)
				}
				if ext.ReadinessProbe == nil || len(ext.ReadinessProbe.Exec) != 2 || ext.ReadinessProbe.Exec[0] != "cat" || ext.ReadinessProbe.TimeoutSeconds != 2 || ext.ReadinessProbe.FailureThreshold != 3 {
					t.Errorf("ReadinessProbe = %+v, unexpected", ext.ReadinessProbe)
				}
			},
		},
		{
			name: "tolerations",
			runtimeConfig: map[string]any{
				"tolerations": []any{
					map[string]any{"key": "dedicated", "operator": "Equal", "value": "payments", "effect": "NoSchedule"},
				},
			},
			assert: func(t *testing.T, ext *KubernetesExtension) {
				t.Helper()
				if len(ext.Tolerations) != 1 {
					t.Fatalf("Tolerations len = %d, want 1", len(ext.Tolerations))
				}
				tol := ext.Tolerations[0]
				if tol.Key != "dedicated" || tol.Operator != "Equal" || tol.Value != "payments" || tol.Effect != "NoSchedule" {
					t.Errorf("Tolerations[0] = %+v, unexpected", tol)
				}
			},
		},
		{
			name: "image pull secrets",
			runtimeConfig: map[string]any{
				"image_pull_secrets": []any{" regcred ", "backup-regcred"},
			},
			assert: func(t *testing.T, ext *KubernetesExtension) {
				t.Helper()
				if len(ext.ImagePullSecrets) != 2 || ext.ImagePullSecrets[0] != "regcred" || ext.ImagePullSecrets[1] != "backup-regcred" {
					t.Errorf("ImagePullSecrets = %v, unexpected", ext.ImagePullSecrets)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := KubernetesExtensionFromDeploymentUnit(&DeploymentUnit{RuntimeConfig: tt.runtimeConfig})
			tt.assert(t, ext)
		})
	}
}

func TestKubernetesExtensionFromDeploymentUnit_IgnoresMalformedRichRuntimeConfig(t *testing.T) {
	unit := &DeploymentUnit{
		RuntimeConfig: map[string]any{
			"service_ports":      []any{"not-a-map", map[string]any{"name": "missing-port"}, map[string]any{"port": "eighty"}},
			"resource_limits":    "not-a-map",
			"resource_requests":  map[string]any{"cpu": float64(1)},
			"liveness_probe":     map[string]any{"http_get": "not-a-map"},
			"readiness_probe":    map[string]any{"exec": "not-a-list"},
			"tolerations":        []any{"not-a-map", map[string]any{"key": float64(1)}},
			"image_pull_secrets": []any{float64(123), "  "},
		},
	}
	ext := KubernetesExtensionFromDeploymentUnit(unit)
	if len(ext.ServicePorts) != 0 {
		t.Errorf("ServicePorts = %+v, want empty", ext.ServicePorts)
	}
	if ext.ResourceLimits != nil || ext.ResourceRequests != nil {
		t.Errorf("resources = limits:%+v requests:%+v, want nil", ext.ResourceLimits, ext.ResourceRequests)
	}
	if ext.LivenessProbe != nil || ext.ReadinessProbe != nil {
		t.Errorf("probes = liveness:%+v readiness:%+v, want nil", ext.LivenessProbe, ext.ReadinessProbe)
	}
	if len(ext.Tolerations) != 0 {
		t.Errorf("Tolerations = %+v, want empty", ext.Tolerations)
	}
	if len(ext.ImagePullSecrets) != 0 {
		t.Errorf("ImagePullSecrets = %v, want empty", ext.ImagePullSecrets)
	}
}
