package domain

import (
	"strings"
	"testing"
)

func TestArtifactDigestDriftStatus(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name           string
		desired        string
		observed       string
		health         HealthStatus
		startingStatus DriftStatus
		want           DriftStatus
	}{
		{name: "caller chooses deploying", desired: digest, observed: digest, health: HealthStatusStarting, startingStatus: DriftStatusDeploying, want: DriftStatusDeploying},
		{name: "caller chooses in sync", desired: digest, observed: digest, health: HealthStatusStarting, startingStatus: DriftStatusInSync, want: DriftStatusInSync},
		{name: "missing stopped container", desired: digest, health: HealthStatusStopped, startingStatus: DriftStatusInSync, want: DriftStatusDrifted},
		{name: "missing unhealthy container", desired: digest, health: HealthStatusUnhealthy, startingStatus: DriftStatusInSync, want: DriftStatusDrifted},
		{name: "healthy observer gap", desired: digest, health: HealthStatusHealthy, startingStatus: DriftStatusInSync, want: DriftStatusUnknown},
		{name: "starting observer gap", desired: digest, health: HealthStatusStarting, startingStatus: DriftStatusInSync, want: DriftStatusUnknown},
		{name: "missing desired digest", observed: digest, health: HealthStatusHealthy, startingStatus: DriftStatusInSync, want: DriftStatusUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ArtifactDigestDriftStatus(test.desired, test.observed, test.health, test.startingStatus); got != test.want {
				t.Fatalf("ArtifactDigestDriftStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeImageDigest(t *testing.T) {
	hex := strings.Repeat("aB", 32)
	want := "sha256:" + strings.ToLower(hex)
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "canonical", value: want, want: want},
		{name: "repository digest", value: "registry.example/team/api@SHA256:" + hex, want: want},
		{name: "docker pullable", value: "docker-pullable://registry.example/team/api@sha256:" + hex, want: want},
		{name: "bare hex", value: hex, want: want},
		{name: "surrounding whitespace", value: "  registry.example/api@sha256:" + hex + "  ", want: want},
		{name: "empty", value: "  ", want: ""},
		{name: "tag only", value: "registry.example/api:latest", want: ""},
		{name: "non sha256", value: "sha512:" + hex, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeImageDigest(test.value); got != test.want {
				t.Fatalf("NormalizeImageDigest(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}
