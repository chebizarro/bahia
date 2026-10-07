package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Per target, only terminal runs beyond the newest securityRunsRetainedPerTarget
// are retired; active runs and targets within the cap are untouched, and a
// repeated prune retires nothing more.
func TestPruneSecurityScanRunsRetiresTerminalRunsBeyondPerTargetRetention(t *testing.T) {
	ctx := context.Background()
	canonical := newMemoryCanonicalSecurityStore()
	repo := NewCanonicalSecurityRepository(nil, canonical, zap.NewNop())
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	var oldest []uuid.UUID
	for i := 0; i < securityRunsRetainedPerTarget+3; i++ {
		finished := base.Add(time.Duration(i) * time.Hour)
		run := domain.SecurityScanRun{ID: uuid.New(), TargetKeyHash: "busy", Status: domain.SecurityScanCompleted, CreatedAt: finished.Add(-time.Minute), FinishedAt: &finished}
		require.NoError(t, canonical.PublishRun(ctx, &run))
		if i < 3 {
			oldest = append(oldest, run.ID)
		}
	}
	active := domain.SecurityScanRun{ID: uuid.New(), TargetKeyHash: "busy", Status: domain.SecurityScanRunning, CreatedAt: base.Add(-24 * time.Hour)}
	require.NoError(t, canonical.PublishRun(ctx, &active))
	quiet := domain.SecurityScanRun{ID: uuid.New(), TargetKeyHash: "quiet", Status: domain.SecurityScanFailed, CreatedAt: base.Add(-48 * time.Hour)}
	require.NoError(t, canonical.PublishRun(ctx, &quiet))

	retired, err := repo.PruneSecurityScanRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, retired)
	remaining, err := canonical.ListSecurityRuns(ctx, uuid.Nil, "busy")
	require.NoError(t, err)
	require.Len(t, remaining, securityRunsRetainedPerTarget+1, "the cap of terminal runs plus the active run")
	for _, run := range remaining {
		require.NotContains(t, oldest, run.ID, "the oldest terminal runs were retired")
	}
	_, err = repo.GetSecurityScanRun(ctx, active.ID)
	require.NoError(t, err, "an active run is never retired, however old")
	_, err = repo.GetSecurityScanRun(ctx, quiet.ID)
	require.NoError(t, err, "a target within the cap keeps its runs")

	retired, err = repo.PruneSecurityScanRuns(ctx)
	require.NoError(t, err)
	require.Zero(t, retired, "retention is level triggered")
}
