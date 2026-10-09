package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRuntimeObservationMaterialComparisonIgnoresSamplingAndDiagnostics(t *testing.T) {
	previous := &RuntimeObservation{ID: uuid.New(), ObservedAt: time.Now(), Metadata: map[string]any{"probe": "old"}, ObservedImageDigest: "sha256:ABC", ObservedImageRepo: "repo", HealthStatus: HealthStatusHealthy}
	current := *previous
	current.ID = uuid.New()
	current.ObservedAt = previous.ObservedAt.Add(time.Hour)
	current.Metadata = map[string]any{"probe": "new"}
	current.ObservedImageDigest = "sha256:abc"
	require.False(t, RuntimeObservationMateriallyChanged(previous, &current))
	current.ObservedImageRepo = "other"
	require.True(t, RuntimeObservationMateriallyChanged(previous, &current))
	require.True(t, RuntimeObservationMateriallyChanged(nil, previous))
}
