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
