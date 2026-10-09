package nostr

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRuntimeStateRecordRedactsUntrustedDiagnosticMetadata(t *testing.T) {
	state := &domain.EnvironmentServiceState{
		ServiceID: uuid.New(), EnvironmentID: uuid.New(), UpdatedAt: time.Now().UTC(),
		ReconcileFailureMetadata: map[string]any{
			"starting_since":     "2026-10-09T01:00:00Z",
			"observed_health":    "starting",
			"unhealthy_evidence": "container did not become healthy within 10m0s; last observed health starting",
			"reason":             "auto_apply_failed",
			"message":            "credential-like runtime diagnostic",
			"docker_status":      "token=super-secret-value",
			"docker_state":       "credential-like runtime diagnostic",
			"failed_at":          "2026-10-09T01:10:00Z",
			"backoff":            "2m0s",
			"failure_count":      2,
			"unknown_field":      "credential-like runtime diagnostic",
		},
	}
	_, content := RuntimeStateRecord(state, nil)
	require.NotContains(t, content, "credential-like runtime diagnostic")
	require.NotContains(t, content, "super-secret-value")
	require.NotContains(t, content, "docker_status")
	var decoded struct {
		Metadata map[string]any `json:"reconcile_failure_metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(content), &decoded))
	require.Equal(t, "2026-10-09T01:00:00Z", decoded.Metadata["starting_since"])
	require.Equal(t, "container did not become healthy within 10m0s; last observed health starting", decoded.Metadata["unhealthy_evidence"])
	require.Equal(t, "automatic desired-state application failed", decoded.Metadata["message"])
	require.Equal(t, float64(2), decoded.Metadata["failure_count"])
	require.NotContains(t, decoded.Metadata, "unknown_field")
}

func TestRuntimeStateRecordRejectsForgedEvidenceAndMarkers(t *testing.T) {
	state := &domain.EnvironmentServiceState{ServiceID: uuid.New(), EnvironmentID: uuid.New(), ReconcileFailureMetadata: map[string]any{
		"starting_since":     "yesterday; token=secret",
		"observed_health":    "starting token=secret",
		"unhealthy_evidence": "container did not become healthy within 10m0s; last observed health token=secret",
		"reason":             "token_secret",
		"message":            "token=secret",
	}}
	_, content := RuntimeStateRecord(state, nil)
	require.NotContains(t, content, "secret")
	require.NotContains(t, content, "starting_since")
	require.NotContains(t, content, "unhealthy_evidence")
}

func TestRuntimeStateRecordStopsAndStripsCredentialBearingDockerEndpoint(t *testing.T) {
	state := &domain.EnvironmentServiceState{ServiceID: uuid.New(), EnvironmentID: uuid.New(), ReconcileFailureMetadata: map[string]any{
		"observed_health":    "stopped",
		"unhealthy_evidence": "desired configuration is applied but the runtime is stopped; the deployment is not healthy",
	}}
	observation := &domain.RuntimeObservation{
		ObservedHost: "https://docker-user:super-secret-value@docker.example:2376/private?token=other-secret#fragment",
		HealthStatus: domain.HealthStatusStopped,
	}
	_, content := RuntimeStateRecord(state, observation)
	for _, secret := range []string{"docker-user", "super-secret-value", "other-secret", "/private", "fragment"} {
		require.NotContains(t, content, secret)
	}
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &decoded))
	require.Equal(t, "https://docker.example:2376", decoded["observed_host"])
	require.Equal(t, "stopped", decoded["health_status"])
	metadata := decoded["reconcile_failure_metadata"].(map[string]any)
	require.Equal(t, "stopped", metadata["observed_health"])
	require.Contains(t, metadata["unhealthy_evidence"], "runtime is stopped")
}

func TestRuntimeStateRecordInvalidObservedHealthIsUnknown(t *testing.T) {
	state := &domain.EnvironmentServiceState{ServiceID: uuid.New(), EnvironmentID: uuid.New()}
	obs := &domain.RuntimeObservation{HealthStatus: domain.HealthStatus("token=super-secret-value"), ObservedHost: "user:pass@host"}
	_, content := RuntimeStateRecord(state, obs)
	require.NotContains(t, content, "super-secret-value")
	require.NotContains(t, content, "user:pass")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &decoded))
	require.Equal(t, "unknown", decoded["health_status"])
	require.NotContains(t, decoded, "observed_host")
}

func TestPublicObservedHostRejectsMalformedAuthorityPort(t *testing.T) {
	for _, raw := range []string{"https://token:secret", "tcp://host:credential", "https://host:99999", "https://host:", "API_KEY:mysecret", "token:secret"} {
		require.Empty(t, publicObservedHost(raw), raw)
	}
	require.Equal(t, "docker.example:2376", publicObservedHost("docker.example:2376"))
	require.Equal(t, "https://docker.example:2376", publicObservedHost("https://user:secret@docker.example:2376/private?token=other"))
	require.Equal(t, "https://[::1]:2376", publicObservedHost("https://user:secret@[::1]:2376/private"))
}
