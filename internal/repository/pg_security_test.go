package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgSecurityRepositoryUpsertTargetReturnsStoredRow(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	target.ID = uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO security_scan_targets").
		WithArgs(target.ID, target.Type, target.TargetKey, target.TargetKeyHash, target.Display, pgxmock.AnyArg(), pgxmock.AnyArg(), nil, nil, nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityTargetColumns)).
			AddRow(target.ID, target.Type, target.TargetKey, target.TargetKeyHash, target.Display, nil, []byte(`{"ecosystem":"npm","name":"lodash","version":"4.17.21"}`), nil, nil, nil, []byte(`{}`), now, now))

	stored, err := repo.UpsertSecurityTarget(ctx, &target)

	require.NoError(t, err)
	require.Equal(t, target.TargetKeyHash, stored.TargetKeyHash)
	require.Equal(t, "lodash", stored.Package.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryUpsertTargetReturnsExistingRowOnDuplicateHash(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	target, err := domain.NewPackageSecurityTarget("npm", "lodash", "4.17.21")
	require.NoError(t, err)
	target.ID = uuid.New()
	existingID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO security_scan_targets").
		WithArgs(target.ID, target.Type, target.TargetKey, target.TargetKeyHash, target.Display, pgxmock.AnyArg(), pgxmock.AnyArg(), nil, nil, nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityTargetColumns)).
			AddRow(existingID, target.Type, target.TargetKey, target.TargetKeyHash, target.Display, nil, []byte(`{"ecosystem":"npm","name":"lodash","version":"4.17.21"}`), nil, nil, nil, []byte(`{}`), now, now))

	stored, err := repo.UpsertSecurityTarget(ctx, &target)

	require.NoError(t, err)
	require.Equal(t, existingID, stored.ID)
	require.Equal(t, existingID, target.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryCreateGetListRunAndUpsertLatest(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: uuid.New(), TargetKeyHash: "target-hash", Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerManual, PublishState: domain.SecurityPublicationPending, SeverityCounts: domain.SecuritySeverityCounts{High: 1}, UnsupportedReasons: map[string]int{"unsupported_coordinate": 2}, Metadata: map[string]any{"source": "test"}}
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectExec("INSERT INTO security_scan_runs").
		WithArgs(run.ID, run.TargetID, run.TargetKeyHash, run.Status, run.Trigger, run.RequestedBy, run.RequestEventID, run.RequestDTag, run.OSVQueryCount, run.FindingCount, run.SeverityCounts.Critical, run.SeverityCounts.High, run.SeverityCounts.Moderate, run.SeverityCounts.Low, run.SeverityCounts.Unknown, run.UnsupportedCount, pgxmock.AnyArg(), run.PublishState, nil, pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.CreateSecurityScanRun(ctx, run))

	row := []any{run.ID, run.TargetID, run.TargetKeyHash, run.Status, run.Trigger, "", "", "", 3, 1, 0, 1, 0, 0, 0, 2, []byte(`{"unsupported_coordinate":2}`), run.PublishState, "", []byte(`{"source":"test"}`), nil, nil, now, now}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + securityRunColumns + " FROM security_scan_runs WHERE id = $1")).
		WithArgs(run.ID).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityRunColumns)).AddRow(row...))
	stored, err := repo.GetSecurityScanRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, stored.SeverityCounts.High)
	require.Equal(t, 2, stored.UnsupportedReasons["unsupported_coordinate"])

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+securityRunColumns+" FROM security_scan_runs WHERE target_key_hash = $1 ORDER BY created_at DESC LIMIT $2")).
		WithArgs("target-hash", 25).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityRunColumns)).AddRow(row...))
	runs, err := repo.ListSecurityScanRuns(ctx, "target-hash", 25)
	require.NoError(t, err)
	require.Len(t, runs, 1)

	latest := &domain.SecurityTargetLatest{TargetID: run.TargetID, TargetKeyHash: run.TargetKeyHash, RunID: run.ID, Status: domain.SecurityScanCompleted, FindingCount: 1, SeverityCounts: domain.SecuritySeverityCounts{High: 1}, ScannedAt: now}
	mock.ExpectExec("INSERT INTO security_target_latest").
		WithArgs(latest.TargetKeyHash, latest.TargetID, latest.RunID, latest.Status, latest.FindingCount, latest.SeverityCounts.Critical, latest.SeverityCounts.High, latest.SeverityCounts.Moderate, latest.SeverityCounts.Low, latest.SeverityCounts.Unknown, latest.ScannedAt, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.UpsertSecurityTargetLatest(ctx, latest))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryCreateRunPropagatesActiveDuplicateError(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	run := &domain.SecurityScanRun{ID: uuid.New(), TargetID: uuid.New(), TargetKeyHash: "target-hash", Status: domain.SecurityScanAccepted, Trigger: domain.SecurityTriggerManual}

	mock.ExpectExec("INSERT INTO security_scan_runs").
		WithArgs(run.ID, run.TargetID, run.TargetKeyHash, run.Status, run.Trigger, run.RequestedBy, run.RequestEventID, run.RequestDTag, run.OSVQueryCount, run.FindingCount, run.SeverityCounts.Critical, run.SeverityCounts.High, run.SeverityCounts.Moderate, run.SeverityCounts.Low, run.SeverityCounts.Unknown, run.UnsupportedCount, pgxmock.AnyArg(), domain.SecurityPublicationPending, nil, pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_security_scan_runs_active_target"})

	err = repo.CreateSecurityScanRun(ctx, run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "creating security scan run")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryUpdateScanRunStatusRefusesTerminalTransition(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	runID := uuid.New()
	targetID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectExec("UPDATE security_scan_runs").
		WithArgs(runID, domain.SecurityScanRunning, nil, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + securityRunColumns + " FROM security_scan_runs WHERE id = $1")).
		WithArgs(runID).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityRunColumns)).
			AddRow(runID, targetID, "hash", domain.SecurityScanCompleted, domain.SecurityTriggerManual, "", "", "", 1, 1, 0, 1, 0, 0, 0, 0, []byte(`{}`), domain.SecurityPublicationPublished, "", []byte(`{}`), nil, now, now, now))

	err = repo.UpdateSecurityScanRunStatus(ctx, runID, domain.SecurityScanRunning, "", nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "terminal")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryUpsertFindingsDedupesByRunAndFindingHash(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	finding := domain.SecurityOSVFinding{
		ID:             uuid.New(),
		RunID:          uuid.New(),
		TargetKeyHash:  "target-hash",
		FindingKey:     "target-hash:GHSA-1:pkg:npm/lodash@4.17.21",
		FindingKeyHash: domain.CanonicalTargetHash("finding"),
		OSVID:          "GHSA-1",
		Severity:       domain.SecuritySeverityHigh,
		Package:        domain.SecurityPackage{Ecosystem: "npm", Name: "lodash", Version: "4.17.21"},
	}

	mock.ExpectExec("INSERT INTO security_findings").
		WithArgs(finding.ID, finding.RunID, finding.TargetKeyHash, finding.FindingKey, finding.FindingKeyHash, finding.OSVID, nil, nil, nil, finding.Severity, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, repo.UpsertSecurityFindings(ctx, []domain.SecurityOSVFinding{finding}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryClaimDueSchedulesUsesAtomicLeaseQuery(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	now := time.Now().UTC().Truncate(time.Second)
	leaseUntil := now.Add(5 * time.Minute)
	scheduleID := uuid.New()
	policyID := uuid.New()
	targetID := uuid.New()

	mock.ExpectQuery("UPDATE security_scan_schedules").
		WithArgs(now, 10, leaseUntil, "worker-1").
		WillReturnRows(pgxmock.NewRows(splitColumns(securityScheduleColumns)).
			AddRow(scheduleID, policyID, targetID, "target-hash", true, 3600, now, leaseUntil, "worker-1", nil, nil, []byte(`{"source":"policy"}`), now, now))

	schedules, err := repo.ClaimDueSecurityScanSchedules(ctx, now, 10, "worker-1", leaseUntil)

	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, "worker-1", schedules[0].LeasedBy)
	require.Equal(t, leaseUntil, *schedules[0].LeaseUntil)
	require.Equal(t, "policy", schedules[0].Metadata["source"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryBreachLifecycleNewUnchangedChangedResolved(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	policyID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	newBreach := &domain.SecurityPolicyBreach{ID: uuid.New(), PolicyID: policyID, TargetKeyHash: "target-hash", Fingerprint: "fp1", Enforcement: "block", ViolatedRules: []string{"max_high_vulns"}, OSVIDs: []string{"GHSA-1"}, NotificationStatus: domain.SecurityBreachNotificationPending, FirstSeenAt: now, LastSeenAt: now}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+securityBreachColumns+" FROM security_policy_breaches WHERE policy_id = $1 AND target_key_hash = $2 AND resolved_at IS NULL")).
		WithArgs(policyID, "target-hash").
		WillReturnError(ErrNotFound)
	mock.ExpectExec("INSERT INTO security_policy_breaches").
		WithArgs(newBreach.ID, policyID, "target-hash", "fp1", nil, "block", pgxmock.AnyArg(), 0, 0, 0, 0, 0, pgxmock.AnyArg(), domain.SecurityBreachNotificationPending, now, now, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	result, err := repo.RecordSecurityPolicyBreach(ctx, newBreach)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityBreachRecordNew, result)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+securityBreachColumns+" FROM security_policy_breaches WHERE policy_id = $1 AND target_key_hash = $2 AND resolved_at IS NULL")).
		WithArgs(policyID, "target-hash").
		WillReturnRows(pgxmock.NewRows(splitColumns(securityBreachColumns)).
			AddRow(newBreach.ID, policyID, "target-hash", "fp1", nil, "block", []byte(`["max_high_vulns"]`), 0, 0, 0, 0, 0, []byte(`["GHSA-1"]`), domain.SecurityBreachNotificationDispatched, now, now, nil, []byte(`{}`), now, now))
	mock.ExpectExec("UPDATE security_policy_breaches").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result, err = repo.RecordSecurityPolicyBreach(ctx, newBreach)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityBreachRecordUnchanged, result)
	require.Equal(t, domain.SecurityBreachNotificationDispatched, newBreach.NotificationStatus)

	changed := *newBreach
	changed.Fingerprint = "fp2"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+securityBreachColumns+" FROM security_policy_breaches WHERE policy_id = $1 AND target_key_hash = $2 AND resolved_at IS NULL")).
		WithArgs(policyID, "target-hash").
		WillReturnRows(pgxmock.NewRows(splitColumns(securityBreachColumns)).
			AddRow(newBreach.ID, policyID, "target-hash", "fp1", nil, "block", []byte(`["max_high_vulns"]`), 0, 0, 0, 0, 0, []byte(`["GHSA-1"]`), domain.SecurityBreachNotificationPending, now, now, nil, []byte(`{}`), now, now))
	mock.ExpectExec("UPDATE security_policy_breaches").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result, err = repo.RecordSecurityPolicyBreach(ctx, &changed)
	require.NoError(t, err)
	require.Equal(t, domain.SecurityBreachRecordChanged, result)
	require.Equal(t, "fp1", changed.PreviousFingerprint)
	require.Equal(t, domain.SecurityBreachNotificationPending, changed.NotificationStatus)

	mock.ExpectExec("UPDATE security_policy_breaches").
		WithArgs(policyID, "target-hash", now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.ResolveSecurityPolicyBreach(ctx, policyID, "target-hash", now))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryUpsertAndDispatchSchedule(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(time.Hour)
	runID := uuid.New()
	schedule := &domain.SecurityScanSchedule{ID: uuid.New(), PolicyID: uuid.New(), TargetID: uuid.New(), TargetKeyHash: "target-hash", Enabled: true, IntervalSeconds: 3600, NextDueAt: now, Metadata: map[string]any{"source": "policy"}}

	mock.ExpectExec("INSERT INTO security_scan_schedules").
		WithArgs(schedule.ID, schedule.PolicyID, schedule.TargetID, schedule.TargetKeyHash, schedule.Enabled, schedule.IntervalSeconds, schedule.NextDueAt, schedule.LeaseUntil, nil, schedule.LastDispatchedAt, schedule.LastRunID, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.UpsertSecurityScanSchedule(ctx, schedule))

	mock.ExpectExec("UPDATE security_scan_schedules").
		WithArgs(schedule.ID, now, runID, next).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.MarkSecurityScheduleDispatched(ctx, schedule.ID, runID, now, next))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryUpsertAndUpdatePublicationState(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	now := time.Now().UTC().Truncate(time.Second)
	nextRetry := now.Add(time.Minute)
	publication := &domain.SecurityObservablePublication{ID: uuid.New(), ObservableType: "scan_status", TargetKeyHash: "target-hash", EventKind: 30315, DTag: "security:scan:run", Schema: "bahia.status.security-scan.v1", PublishState: domain.SecurityPublicationPending}

	mock.ExpectExec("INSERT INTO security_observable_publications").
		WithArgs(publication.ID, publication.ObservableType, publication.RunID, publication.TargetKeyHash, publication.FindingID, publication.BreachID, publication.EventKind, publication.DTag, publication.Schema, publication.PublishState, nil, publication.AttemptCount, nil, publication.NextRetryAt, publication.PublishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, repo.UpsertSecurityPublication(ctx, publication))

	mock.ExpectExec("UPDATE security_observable_publications").
		WithArgs(publication.ID, domain.SecurityPublicationFailedRetryable, nil, "relay closed", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.UpdateSecurityPublicationState(ctx, publication.ID, domain.SecurityPublicationFailedRetryable, "", "relay closed", &nextRetry, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryLatestForArtifactAndDisableSchedules(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	artifactID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	targetID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT l\\.target_key_hash").
		WithArgs(artifactID.String()).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityLatestColumns)).
			AddRow("target-hash", targetID, runID, domain.SecurityScanCompleted, 2, 1, 1, 0, 0, 0, now, now))
	latest, err := repo.GetLatestSecurityTargetLatestForArtifact(ctx, artifactID)
	require.NoError(t, err)
	require.Equal(t, 2, latest.FindingCount)

	mock.ExpectExec("UPDATE security_scan_schedules").
		WithArgs(uuid.Nil, now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.DisableSecurityScanSchedulesForPolicy(ctx, uuid.Nil, now))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgSecurityRepositoryCacheRetentionAndPublicationRetry(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgSecurityRepositoryWithDB(mock)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+securityCacheColumns+" FROM security_osv_vulnerability_cache WHERE osv_id = $1 AND expires_at > $2")).
		WithArgs("GHSA-1", now).
		WillReturnError(ErrNotFound)
	_, err = repo.GetOSVVulnerabilityCache(ctx, "GHSA-1", now)
	require.ErrorIs(t, err, ErrNotFound)

	mock.ExpectExec("DELETE FROM security_osv_vulnerability_cache").
		WithArgs(now).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	deleted, err := repo.PruneExpiredOSVVulnerabilityCache(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)

	pubID := uuid.New()
	nextRetry := now.Add(-time.Minute)
	mock.ExpectQuery("SELECT "+regexp.QuoteMeta(securityPublicationColumns)).
		WithArgs(now, 50).
		WillReturnRows(pgxmock.NewRows(splitColumns(securityPublicationColumns)).
			AddRow(pubID, "scan_status", nil, "target-hash", nil, nil, 30315, "security:scan:run", "bahia.status.security-scan.v1", domain.SecurityPublicationFailedRetryable, "", 1, "relay closed", nextRetry, nil, now, now))
	pubs, err := repo.ListRetryableSecurityPublications(ctx, now, 50)
	require.NoError(t, err)
	require.Len(t, pubs, 1)
	require.Equal(t, domain.SecurityPublicationFailedRetryable, pubs[0].PublishState)
	require.NoError(t, mock.ExpectationsWereMet())
}
