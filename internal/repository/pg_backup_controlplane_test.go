package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgBackupControlPlaneRepositoryCreateBackupRunIfAbsentInsertsNewCoordinate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	policyID := uuid.New()
	run := backupRunFixture(&policyID)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_runs").
		WithArgs(run.ID, run.RecipeID, run.RepositoryID, policyID, run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
			run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus, pgxmock.AnyArg(), run.Error,
			pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)).
			AddRow(run.ID, run.RecipeID, run.RepositoryID, policyID.String(), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
				run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus, []byte(`{"relay":"accepted"}`), run.Error,
				[]byte(`{"source":"test"}`), nil, nil, now, now))

	created, wasCreated, err := repo.CreateBackupRunIfAbsent(ctx, run)

	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, run.ID, created.ID)
	require.Equal(t, policyID, *created.PolicyID)
	require.Equal(t, "accepted", created.PublishSummary["relay"])
	require.Equal(t, "test", created.Metadata["source"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryCreateBackupRunIfAbsentReturnsExistingCoordinate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	run := backupRunFixture(nil)
	existingID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_runs").
		WithArgs(run.ID, run.RecipeID, run.RepositoryID, nil, run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
			run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus, pgxmock.AnyArg(), run.Error,
			pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+backupRunColumns+" FROM backup_runs WHERE requested_by = $1 AND request_kind = $2 AND request_d_tag = $3")).
		WithArgs(run.RequestedBy, run.RequestKind, run.RequestDTag).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)).
			AddRow(existingID, run.RecipeID, run.RepositoryID, nil, run.RequestedBy, "event-original", run.RequestKind, run.RequestDTag,
				run.Status, run.Backend, run.TargetRef, false, "", run.VerificationStatus, []byte(`{}`), "", []byte(`{}`), nil, nil, now, now))

	existing, wasCreated, err := repo.CreateBackupRunIfAbsent(ctx, run)

	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, existingID, existing.ID)
	require.Equal(t, "event-original", existing.RequestEventID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryUpsertVerificationReturnsPersistedID(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	requestedID := uuid.New()
	persistedID := uuid.New()
	runID := uuid.New()
	record := &domain.BackupVerificationRecord{
		ID:          requestedID,
		BackupRunID: runID,
		Mode:        domain.BackupVerificationKopiaSnapshotVerify,
		Status:      domain.BackupVerificationSucceeded,
		Verified:    true,
		Evidence:    map[string]any{"snapshot_id": "snap-1"},
	}

	mock.ExpectQuery("INSERT INTO backup_verifications").
		WithArgs(record.ID, record.BackupRunID, record.Mode, record.Status, record.Verified, pgxmock.AnyArg(), record.Error, pgxmock.AnyArg(), record.VerifiedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(persistedID))

	require.NoError(t, repo.UpsertBackupVerification(ctx, record))
	require.Equal(t, persistedID, record.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryCreateBackupRestoreIfAbsentInsertsNewCoordinate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	policyID := uuid.New()
	restore := backupRestoreFixture(&policyID)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_restores").
		WithArgs(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, policyID, restore.SnapshotID, restore.RestoreTargetRef,
			restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalEventID, restore.ApprovedBy,
			restore.ApprovedAt, restore.ApprovalMessage, restore.Status, restore.Backend, restore.VerificationStatus, pgxmock.AnyArg(), pgxmock.AnyArg(), restore.Error,
			pgxmock.AnyArg(), restore.StartedAt, restore.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRestoreColumns)).
			AddRow(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, policyID.String(), restore.SnapshotID, restore.RestoreTargetRef,
				restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalEventID, restore.ApprovedBy,
				nil, restore.ApprovalMessage, restore.Status, restore.Backend, restore.VerificationStatus, []byte(`{"checked":true}`), []byte(`{"relay":"accepted"}`),
				restore.Error, []byte(`{"source":"test"}`), nil, nil, now, now))

	created, wasCreated, err := repo.CreateBackupRestoreIfAbsent(ctx, restore)

	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, restore.ID, created.ID)
	require.Equal(t, policyID, *created.PolicyID)
	require.Equal(t, true, created.Evidence["checked"])
	require.Equal(t, "accepted", created.PublishSummary["relay"])
	require.Equal(t, "test", created.Metadata["source"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryClaimNextQueuedBackupRestoreRequiresApproval(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	restore := backupRestoreFixture(nil)
	restore.ApprovalStatus = domain.BackupApprovalApproved
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("UPDATE backup_restores").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRestoreColumns)).
			AddRow(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, nil, restore.SnapshotID, restore.RestoreTargetRef,
				restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalEventID, restore.ApprovedBy,
				nil, restore.ApprovalMessage, domain.RunStatusRunning, restore.Backend, restore.VerificationStatus, []byte(`{}`), []byte(`{}`),
				restore.Error, []byte(`{}`), &now, nil, now, now))

	claimed, err := repo.ClaimNextQueuedBackupRestore(ctx)

	require.NoError(t, err)
	require.Equal(t, domain.RunStatusRunning, claimed.Status)
	require.Equal(t, domain.BackupApprovalApproved, claimed.ApprovalStatus)
	require.NotNil(t, claimed.StartedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryCreateBackupRetentionRunIfAbsentInsertsNewCoordinate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	policyID := uuid.New()
	run := backupRetentionRunFixture(&policyID)
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_retention_runs").
		WithArgs(run.ID, run.RepositoryID, policyID, run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag, run.Status, run.Backend,
			run.DryRun, pgxmock.AnyArg(), pgxmock.AnyArg(), run.Error, pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRetentionRunColumns)).
			AddRow(run.ID, run.RepositoryID, policyID.String(), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag, run.Status, run.Backend,
				run.DryRun, []byte(`{"deleted":0}`), []byte(`{"relay":"accepted"}`), run.Error, []byte(`{"source":"test"}`), nil, nil, now, now))

	created, wasCreated, err := repo.CreateBackupRetentionRunIfAbsent(ctx, run)

	require.NoError(t, err)
	require.True(t, wasCreated)
	require.Equal(t, run.ID, created.ID)
	require.Equal(t, policyID, *created.PolicyID)
	require.Equal(t, float64(0), created.Evidence["deleted"])
	require.Equal(t, "accepted", created.PublishSummary["relay"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryScansVerificationEvidence(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	id := uuid.New()
	runID := uuid.New()
	verifiedAt := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + backupVerificationColumns + " FROM backup_verifications WHERE backup_run_id = $1")).
		WithArgs(runID).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupVerificationColumns)).
			AddRow(id, runID, domain.BackupVerificationKopiaSnapshotVerify, domain.BackupVerificationSucceeded, true,
				[]byte(`{"snapshot_id":"snap-1"}`), "", []byte(`{"ok":true}`), &verifiedAt, verifiedAt, verifiedAt))

	record, err := repo.GetBackupVerificationByRunID(ctx, runID)

	require.NoError(t, err)
	require.Equal(t, id, record.ID)
	require.True(t, record.Verified)
	require.Equal(t, "snap-1", record.Evidence["snapshot_id"])
	require.Equal(t, true, record.PublishSummary["ok"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarshalJSONObjectStoresNilMapsAsEmptyObjects(t *testing.T) {
	encoded, err := marshalJSONObject(nil, "backup metadata")

	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(encoded))
}

func backupRunFixture(policyID *uuid.UUID) *domain.BackupRun {
	return &domain.BackupRun{
		ID:                 uuid.New(),
		RecipeID:           uuid.New(),
		RepositoryID:       uuid.New(),
		PolicyID:           policyID,
		RequestedBy:        "pubkey-1",
		RequestEventID:     "event-1",
		RequestKind:        38400,
		RequestDTag:        "daily:service-data",
		Status:             domain.RunStatusQueued,
		Backend:            domain.BackupBackendKopia,
		TargetRef:          "fs:/srv/data",
		VerificationStatus: domain.BackupVerificationPending,
		PublishSummary:     map[string]any{"relay": "accepted"},
		Metadata:           map[string]any{"source": "test"},
	}
}

func backupRestoreFixture(policyID *uuid.UUID) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{
		ID:                 uuid.New(),
		BackupRunID:        uuid.New(),
		RecipeID:           uuid.New(),
		RepositoryID:       uuid.New(),
		PolicyID:           policyID,
		SnapshotID:         "snap-1",
		RestoreTargetRef:   "fs:/restore/path",
		RequestedBy:        "pubkey-1",
		RequestEventID:     "event-restore",
		RequestKind:        38402,
		RequestDTag:        "restore:daily",
		ApprovalStatus:     domain.BackupApprovalPending,
		Status:             domain.RunStatusQueued,
		Backend:            domain.BackupBackendKopia,
		VerificationStatus: domain.BackupVerificationPending,
		Evidence:           map[string]any{"checked": true},
		PublishSummary:     map[string]any{"relay": "accepted"},
		Metadata:           map[string]any{"source": "test"},
	}
}

func backupRetentionRunFixture(policyID *uuid.UUID) *domain.BackupRetentionRun {
	return &domain.BackupRetentionRun{
		ID:             uuid.New(),
		RepositoryID:   uuid.New(),
		PolicyID:       policyID,
		RequestedBy:    "pubkey-1",
		RequestEventID: "event-retention",
		RequestKind:    38404,
		RequestDTag:    "retention:weekly",
		Status:         domain.RunStatusQueued,
		Backend:        domain.BackupBackendKopia,
		DryRun:         true,
		Evidence:       map[string]any{"deleted": 0},
		PublishSummary: map[string]any{"relay": "accepted"},
		Metadata:       map[string]any{"source": "test"},
	}
}
