package nostr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSecurityCanonicalDBLessLocalStoreSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	first := startLocalHistoryDaemon(t, dir, script)
	firstView := NewSecurityCanonicalPublisher(first.projector, &mockConfidentialEncryptor{}, zap.NewNop())
	firstRepo := service.NewCanonicalSecurityRepository(nil, firstView, zap.NewNop())

	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	stored, err := firstRepo.UpsertSecurityTarget(ctx, &target)
	require.NoError(t, err)
	schedule := domain.SecurityScanSchedule{PolicyID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Enabled: true, IntervalSeconds: 3600, NextDueAt: time.Now().UTC().Add(time.Hour)}
	require.NoError(t, firstRepo.UpsertSecurityScanSchedule(ctx, &schedule))
	run := domain.SecurityScanRun{ID: uuid.New(), TargetID: stored.ID, TargetKeyHash: stored.TargetKeyHash, Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerScheduled}
	require.NoError(t, firstRepo.CreateSecurityScanRun(ctx, &run))
	finding := domain.SecurityOSVFinding{RunID: run.ID, TargetKeyHash: stored.TargetKeyHash, FindingKey: "lodash:GHSA-1", FindingKeyHash: "finding-hash", OSVID: "GHSA-1", Details: strings.Repeat("x", detailChunkSize+100)}
	require.NoError(t, firstRepo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding}))
	finding.Details = "replacement detail"
	require.NoError(t, firstRepo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding}))
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	restartedView := NewSecurityCanonicalPublisher(restarted.projector, &mockConfidentialEncryptor{}, zap.NewNop())
	restartedRepo := service.NewCanonicalSecurityRepository(nil, restartedView, zap.NewNop())
	targets, err := restartedRepo.ListSecurityTargets(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	runs, err := restartedRepo.ListSecurityScanRuns(ctx, stored.TargetKeyHash, 0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	schedules, err := restartedRepo.ListSecurityScanSchedulesFiltered(ctx, repository.SecurityScheduleFilter{EnabledOnly: true})
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	findings, err := restartedRepo.ListSecurityFindings(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "replacement detail", findings[0].Details, "base manifest must hide stale multipart coordinates")
}
