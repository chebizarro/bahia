package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Golden hash fixture tests — lock SHA-256 stability across code changes.
//
// These tests use deterministic JSON inputs with fixed UUIDs and field values.
// If any golden hash changes, it means canonical serialization has changed,
// which is a breaking change for drift detection and no-op apply logic.
//
// To update after an intentional serialization change:
//   1. Run the test to get the new hash.
//   2. Verify the serialization change is intentional.
//   3. Bump DesiredStateSchemaVersion.
//   4. Update the golden value.
// ---------------------------------------------------------------------------

// Deterministic UUIDs used across all golden fixtures.
var (
	goldenSvcID  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	goldenEnvID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	goldenArtID  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	goldenSecID  = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	goldenSvcID2 = uuid.MustParse("55555555-5555-5555-5555-555555555555")
	goldenArtID2 = uuid.MustParse("66666666-6666-6666-6666-666666666666")
)

// ---------------------------------------------------------------------------
// TestGoldenDesiredServiceHash — canonical service spec → expected hash
// ---------------------------------------------------------------------------

func TestGoldenDesiredServiceHash(t *testing.T) {
	t.Run("full spec with all hash-relevant fields", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "my-app",
			ImageRef:         "ghcr.io/org/my-app:v1.2.3",
			Command:          []string{"/bin/app", "serve"},
			Entrypoint:       []string{"/entrypoint.sh"},
			WorkDir:          "/app",
			Env:              map[string]string{"LOG_LEVEL": "info", "PORT": "8080"},
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASSWORD", Name: "db-password", SecretID: goldenSecID, RedactedValue: "REDACTED(db-password)"},
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
		want := "sha256:a783ee5860f8bf9d07e89d1dae4111b6c84d6b591c7b8ca210adc963b48d92e8"
		if got != want {
			t.Fatalf("golden desired hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("minimal spec with nil/empty fields normalized", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "nginx",
			ImageRef:         "nginx:1.25",
			// All optional fields are nil/zero — they must normalize to empty.
		}

		got := spec.ComputeDesiredHash()
		want := "sha256:86eb3e4a2b5055f3890829466dec533b8a21104006634cd8eb41f61c9d8b6689"
		if got != want {
			t.Fatalf("golden minimal desired hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("empty env map and nil env produce same hash", func(t *testing.T) {
		base := func() DesiredServiceSpec {
			return DesiredServiceSpec{
				SchemaVersion:    "1",
				ServiceID:        goldenSvcID,
				EnvironmentID:    goldenEnvID,
				ArtifactID:       goldenArtID,
				StableServiceKey: "svc",
				ImageRef:         "img:v1",
			}
		}

		specNil := base()
		specNil.Env = nil
		hNil := specNil.ComputeDesiredHash()

		specEmpty := base()
		specEmpty.Env = map[string]string{}
		hEmpty := specEmpty.ComputeDesiredHash()

		if hNil != hEmpty {
			t.Fatalf("nil env and empty env produced different hashes:\n  nil:   %s\n  empty: %s", hNil, hEmpty)
		}
	})

	t.Run("nil slices and empty slices produce same hash", func(t *testing.T) {
		base := func() DesiredServiceSpec {
			return DesiredServiceSpec{
				SchemaVersion:    "1",
				ServiceID:        goldenSvcID,
				EnvironmentID:    goldenEnvID,
				ArtifactID:       goldenArtID,
				StableServiceKey: "svc",
				ImageRef:         "img:v1",
			}
		}

		specNil := base()
		// Command, Entrypoint, Ports, Volumes, DependsOn all nil

		specEmpty := base()
		specEmpty.Command = []string{}
		specEmpty.Entrypoint = []string{}
		specEmpty.Ports = []string{}
		specEmpty.Volumes = []string{}
		specEmpty.DependsOn = []string{}
		specEmpty.Labels = map[string]string{}

		hNil := specNil.ComputeDesiredHash()
		hEmpty := specEmpty.ComputeDesiredHash()

		if hNil != hEmpty {
			t.Fatalf("nil slices and empty slices produced different hashes:\n  nil:   %s\n  empty: %s", hNil, hEmpty)
		}
	})

	t.Run("secret refs included as keys only", func(t *testing.T) {
		// Two specs with same secret env var names but different secret IDs
		// and different redacted values should produce the same hash,
		// because only the EnvVar key list is hashed.
		spec1 := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "svc",
			ImageRef:         "img:v1",
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: goldenSecID, RedactedValue: "REDACTED(db-pass)"},
			},
		}

		spec2 := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "svc",
			ImageRef:         "img:v1",
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "other-name", SecretID: goldenArtID2, RedactedValue: "REDACTED(other-name)"},
			},
		}

		h1 := spec1.ComputeDesiredHash()
		h2 := spec2.ComputeDesiredHash()

		if h1 != h2 {
			t.Fatalf("secret refs with same EnvVar but different metadata produced different hashes:\n  h1: %s\n  h2: %s\nOnly EnvVar keys should be included in hash", h1, h2)
		}
	})

	t.Run("extension data excluded from hash", func(t *testing.T) {
		base := func() DesiredServiceSpec {
			return DesiredServiceSpec{
				SchemaVersion:    "1",
				ServiceID:        goldenSvcID,
				EnvironmentID:    goldenEnvID,
				ArtifactID:       goldenArtID,
				StableServiceKey: "svc",
				ImageRef:         "img:v1",
			}
		}

		specPlain := base()
		hPlain := specPlain.ComputeDesiredHash()

		specCompose := base()
		specCompose.ComposeExtension = &ComposeExtension{
			ProjectName:      "my-project",
			Networks:         []string{"net1"},
			VolumeDeclarations: []string{"vol1"},
			DependsOn: map[string]ComposeDependency{
				"db": {Condition: "service_healthy"},
			},
		}
		hCompose := specCompose.ComputeDesiredHash()

		specDocker := base()
		specDocker.DockerExtension = &DockerExtension{
			HostConfig:       map[string]any{"Memory": 536870912},
			NetworkingConfig: map[string]any{"EndpointsConfig": map[string]any{}},
		}
		hDocker := specDocker.ComputeDesiredHash()

		specK8s := base()
		specK8s.KubernetesExtension = &KubernetesExtension{}
		hK8s := specK8s.ComputeDesiredHash()

		specPodman := base()
		specPodman.PodmanExtension = &PodmanExtension{}
		hPodman := specPodman.ComputeDesiredHash()

		for _, tc := range []struct {
			name string
			hash string
		}{
			{"compose", hCompose},
			{"docker", hDocker},
			{"kubernetes", hK8s},
			{"podman", hPodman},
		} {
			if tc.hash != hPlain {
				t.Errorf("%s extension changed hash:\n  plain: %s\n  with:  %s", tc.name, hPlain, tc.hash)
			}
		}
	})

	t.Run("volatile DesiredHash field excluded from hash", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "svc",
			ImageRef:         "img:v1",
		}
		h1 := spec.ComputeDesiredHash()

		// Set DesiredHash to something else and recompute.
		spec.DesiredHash = "sha256:stale-old-value"
		h2 := spec.ComputeDesiredHash()

		if h1 != h2 {
			t.Fatalf("DesiredHash field affected hash computation:\n  h1: %s\n  h2: %s", h1, h2)
		}
	})
}

// ---------------------------------------------------------------------------
// TestGoldenObservationHash — normalized observation → expected hash
// ---------------------------------------------------------------------------

func TestGoldenObservationHash(t *testing.T) {
	t.Run("full observation with all fields", func(t *testing.T) {
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
			t.Fatalf("golden observation hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("minimal observation with nil/empty normalization", func(t *testing.T) {
		obs := NormalizedObservation{
			SchemaVersion: "1",
			ImageRef:      "nginx:1.25",
		}

		got := obs.ComputeObservationHash()
		want := "sha256:ee47aea3161532209ef9d4022324903a5bd414941f3a40d8d60fe7ea5256ec3a"
		if got != want {
			t.Fatalf("golden minimal observation hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("nil slices and empty slices produce same hash", func(t *testing.T) {
		obsNil := NormalizedObservation{
			SchemaVersion: "1",
			ImageRef:      "app:v1",
		}
		hNil := obsNil.ComputeObservationHash()

		obsEmpty := NormalizedObservation{
			SchemaVersion:      "1",
			ImageRef:           "app:v1",
			Command:            []string{},
			Entrypoint:         []string{},
			Env:                map[string]string{},
			SecretEnvKeys:      []string{},
			Ports:              []string{},
			Volumes:            []string{},
			NetworkAttachments: []string{},
			BahiaLabels:        map[string]string{},
		}
		hEmpty := obsEmpty.ComputeObservationHash()

		if hNil != hEmpty {
			t.Fatalf("nil and empty observation fields produced different hashes:\n  nil:   %s\n  empty: %s", hNil, hEmpty)
		}
	})

	t.Run("slice ordering is normalized", func(t *testing.T) {
		obs1 := NormalizedObservation{
			SchemaVersion:      "1",
			ImageRef:           "app:v1",
			Ports:              []string{"9090:9090", "8080:8080"},
			Volumes:            []string{"/z:/z", "/a:/a"},
			SecretEnvKeys:      []string{"Z_KEY", "A_KEY"},
			NetworkAttachments: []string{"net-z", "net-a"},
		}
		h1 := obs1.ComputeObservationHash()

		obs2 := NormalizedObservation{
			SchemaVersion:      "1",
			ImageRef:           "app:v1",
			Ports:              []string{"8080:8080", "9090:9090"},
			Volumes:            []string{"/a:/a", "/z:/z"},
			SecretEnvKeys:      []string{"A_KEY", "Z_KEY"},
			NetworkAttachments: []string{"net-a", "net-z"},
		}
		h2 := obs2.ComputeObservationHash()

		if h1 != h2 {
			t.Fatalf("differently ordered slices produced different hashes:\n  h1: %s\n  h2: %s", h1, h2)
		}
	})

	t.Run("ObservationHash field excluded from computation", func(t *testing.T) {
		obs := NormalizedObservation{
			SchemaVersion: "1",
			ImageRef:      "app:v1",
		}
		h1 := obs.ComputeObservationHash()

		obs.ObservationHash = "sha256:stale-value"
		h2 := obs.ComputeObservationHash()

		if h1 != h2 {
			t.Fatalf("ObservationHash field affected computation:\n  h1: %s\n  h2: %s", h1, h2)
		}
	})
}

// ---------------------------------------------------------------------------
// TestGoldenEnvironmentRevisionHash — environment plan → expected hash
// ---------------------------------------------------------------------------

func TestGoldenEnvironmentRevisionHash(t *testing.T) {
	t.Run("two-service plan with deterministic hashes", func(t *testing.T) {
		svcA := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "api",
			ImageRef:         "api:v1",
		}
		svcA.ComputeDesiredHash()

		svcB := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID2,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID2,
			StableServiceKey: "web",
			ImageRef:         "web:v2",
		}
		svcB.ComputeDesiredHash()

		plan := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{svcA, svcB},
		}

		got := plan.ComputeRevisionHash()
		// Lock the golden revision hash.
		want := "sha256:ded6d951b3102510cda47519ebd07c72d4f161dcd741bf3f9eb8286cce51c23a"
		if got != want {
			t.Fatalf("golden environment revision hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("service order does not affect revision hash", func(t *testing.T) {
		svcA := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "api",
			ImageRef:         "api:v1",
		}
		svcA.ComputeDesiredHash()

		svcB := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID2,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID2,
			StableServiceKey: "web",
			ImageRef:         "web:v2",
		}
		svcB.ComputeDesiredHash()

		plan1 := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{svcA, svcB},
		}
		h1 := plan1.ComputeRevisionHash()

		plan2 := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{svcB, svcA}, // reversed
		}
		h2 := plan2.ComputeRevisionHash()

		if h1 != h2 {
			t.Fatalf("service order affected revision hash:\n  A,B: %s\n  B,A: %s", h1, h2)
		}
	})

	t.Run("single-service plan", func(t *testing.T) {
		svc := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "solo",
			ImageRef:         "solo:v1",
		}
		svc.ComputeDesiredHash()

		plan := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{svc},
		}

		got := plan.ComputeRevisionHash()
		want := "sha256:4b48e8c4792b9d3fa90edb55a487534e418c8ece5a51ae1e6b857dee1a65eff6"
		if got != want {
			t.Fatalf("golden single-service revision hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})

	t.Run("empty plan", func(t *testing.T) {
		plan := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{},
		}

		got := plan.ComputeRevisionHash()
		want := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		if got != want {
			t.Fatalf("golden empty plan revision hash changed:\n  got:  %s\n  want: %s", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Redaction tests — verify ContainsPlaintextSecret catches various patterns
// ---------------------------------------------------------------------------

func TestGoldenRedaction_ContainsPlaintextSecret(t *testing.T) {
	tests := []struct {
		name     string
		refs     []DesiredSecretRef
		wantLeak bool
	}{
		{
			name:     "no secret refs",
			refs:     nil,
			wantLeak: false,
		},
		{
			name: "properly redacted single ref",
			refs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: goldenSecID, RedactedValue: "REDACTED(db-pass)"},
			},
			wantLeak: false,
		},
		{
			name: "properly redacted multiple refs",
			refs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: goldenSecID, RedactedValue: "REDACTED(db-pass)"},
				{EnvVar: "API_KEY", Name: "api-key", SecretID: goldenSecID, RedactedValue: "REDACTED(api-key)"},
				{EnvVar: "TOKEN", Name: "token", SecretID: goldenSecID, RedactedValue: "REDACTED(token)"},
			},
			wantLeak: false,
		},
		{
			name: "empty redacted value is safe",
			refs: []DesiredSecretRef{
				{EnvVar: "TOKEN", Name: "token", SecretID: goldenSecID, RedactedValue: ""},
			},
			wantLeak: false,
		},
		{
			name: "plaintext password leaked",
			refs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: goldenSecID, RedactedValue: "s3cret-p@ssw0rd"},
			},
			wantLeak: true,
		},
		{
			name: "base64-encoded secret leaked",
			refs: []DesiredSecretRef{
				{EnvVar: "API_KEY", Name: "api-key", SecretID: goldenSecID, RedactedValue: "dGhpcyBpcyBhIHNlY3JldA=="},
			},
			wantLeak: true,
		},
		{
			name: "UUID-like token leaked",
			refs: []DesiredSecretRef{
				{EnvVar: "TOKEN", Name: "token", SecretID: goldenSecID, RedactedValue: "a1b2c3d4-e5f6-7890-abcd-ef1234567890"},
			},
			wantLeak: true,
		},
		{
			name: "mixed: one redacted, one leaked",
			refs: []DesiredSecretRef{
				{EnvVar: "DB_PASS", Name: "db-pass", SecretID: goldenSecID, RedactedValue: "REDACTED(db-pass)"},
				{EnvVar: "API_KEY", Name: "api-key", SecretID: goldenSecID, RedactedValue: "leaked-key-value"},
			},
			wantLeak: true,
		},
		{
			name: "partial REDACTED prefix is not valid",
			refs: []DesiredSecretRef{
				{EnvVar: "KEY", Name: "key", SecretID: goldenSecID, RedactedValue: "REDACT(key)"},
			},
			wantLeak: true,
		},
		{
			name: "case-sensitive REDACTED check",
			refs: []DesiredSecretRef{
				{EnvVar: "KEY", Name: "key", SecretID: goldenSecID, RedactedValue: "redacted(key)"},
			},
			wantLeak: true,
		},
		{
			name: "whitespace value is not redacted",
			refs: []DesiredSecretRef{
				{EnvVar: "KEY", Name: "key", SecretID: goldenSecID, RedactedValue: "  "},
			},
			wantLeak: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := DesiredServiceSpec{SecretRefs: tt.refs}
			got := spec.ContainsPlaintextSecret()
			if got != tt.wantLeak {
				t.Errorf("ContainsPlaintextSecret() = %v, want %v", got, tt.wantLeak)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Redaction serialization tests — serialized JSON must never contain plaintext
// ---------------------------------------------------------------------------

func TestGoldenRedaction_SerializedJSONNeverContainsPlaintext(t *testing.T) {
	t.Run("secret ref values are redacted in JSON", func(t *testing.T) {
		spec := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "app",
			ImageRef:         "app:latest",
			Env:              map[string]string{"PORT": "8080", "LOG_LEVEL": "debug"},
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "DB_PASSWORD", Name: "db-password", SecretID: goldenSecID, RedactedValue: "REDACTED(db-password)"},
				{EnvVar: "API_KEY", Name: "api-key", SecretID: goldenSecID, RedactedValue: "REDACTED(api-key)"},
			},
		}
		spec.ComputeDesiredHash()

		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(data)

		// Must contain redacted placeholders.
		for _, placeholder := range []string{"REDACTED(db-password)", "REDACTED(api-key)"} {
			if !strings.Contains(s, placeholder) {
				t.Errorf("expected %q in JSON output", placeholder)
			}
		}

		// Must contain literal env values (they are not secrets).
		if !strings.Contains(s, `"8080"`) {
			t.Error("expected literal PORT value in JSON")
		}
		if !strings.Contains(s, `"debug"`) {
			t.Error("expected literal LOG_LEVEL value in JSON")
		}

		// DesiredSecretRef does not have a plaintext field, but verify
		// the JSON keys are as expected.
		if !strings.Contains(s, `"env_var"`) {
			t.Error("expected env_var key in JSON")
		}
		if !strings.Contains(s, `"redacted_value"`) {
			t.Error("expected redacted_value key in JSON")
		}
	})

	t.Run("hash input excludes secret values", func(t *testing.T) {
		// Two specs with same secret key but different redacted values
		// should hash identically — proving the hash excludes secret metadata.
		spec1 := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "app",
			ImageRef:         "app:v1",
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "SECRET_A", Name: "secret-a", SecretID: goldenSecID, RedactedValue: "REDACTED(secret-a)"},
			},
		}
		h1 := spec1.ComputeDesiredHash()

		spec2 := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "app",
			ImageRef:         "app:v1",
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "SECRET_A", Name: "different-name", SecretID: goldenArtID2, RedactedValue: "REDACTED(different-name)"},
			},
		}
		h2 := spec2.ComputeDesiredHash()

		if h1 != h2 {
			t.Fatalf("secret metadata affected hash (only keys should matter):\n  h1: %s\n  h2: %s", h1, h2)
		}
	})

	t.Run("environment plan JSON contains only redacted secrets", func(t *testing.T) {
		svc := DesiredServiceSpec{
			SchemaVersion:    "1",
			ServiceID:        goldenSvcID,
			EnvironmentID:    goldenEnvID,
			ArtifactID:       goldenArtID,
			StableServiceKey: "app",
			ImageRef:         "app:v1",
			SecretRefs: []DesiredSecretRef{
				{EnvVar: "TOKEN", Name: "token", SecretID: goldenSecID, RedactedValue: "REDACTED(token)"},
			},
		}
		svc.ComputeDesiredHash()

		plan := DesiredEnvironmentPlan{
			EnvironmentID: goldenEnvID,
			Services:      []DesiredServiceSpec{svc},
		}
		plan.ComputeRevisionHash()

		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(data)

		if !strings.Contains(s, "REDACTED(token)") {
			t.Error("expected REDACTED placeholder in plan JSON")
		}

		// Verify no plaintext-like patterns appear.
		// The JSON should not contain any "value" field with non-redacted content
		// for secret refs.
		if strings.Contains(s, `"plaintext"`) {
			t.Error("unexpected plaintext field in plan JSON")
		}
	})
}
