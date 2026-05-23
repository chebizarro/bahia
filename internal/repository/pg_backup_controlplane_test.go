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
	require.NoError(t, domain.ValidateBackupRun(run))
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_runs").
		WithArgs(run.ID, run.RecipeID, run.RepositoryID, policyID, run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
			run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationMode, run.VerificationStatus,
			run.RestoreEligibility, run.RestoreEligibilityReason, run.VerificationPolicyFailure, run.FailureCategory, pgxmock.AnyArg(), run.Error,
			pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)).
			AddRow(run.ID, run.RecipeID, run.RepositoryID, policyID.String(), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
				run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationMode, run.VerificationStatus,
				run.RestoreEligibility, run.RestoreEligibilityReason, run.VerificationPolicyFailure, run.FailureCategory, []byte(`{"relay":"accepted"}`), run.Error,
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
	require.NoError(t, domain.ValidateBackupRun(run))
	existingID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_runs").
		WithArgs(run.ID, run.RecipeID, run.RepositoryID, nil, run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
			run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationMode, run.VerificationStatus,
			run.RestoreEligibility, run.RestoreEligibilityReason, run.VerificationPolicyFailure, run.FailureCategory, pgxmock.AnyArg(), run.Error,
			pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+backupRunColumns+" FROM backup_runs WHERE requested_by = $1 AND request_kind = $2 AND request_d_tag = $3")).
		WithArgs(run.RequestedBy, run.RequestKind, run.RequestDTag).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRunColumns)).
			AddRow(existingID, run.RecipeID, run.RepositoryID, nil, run.RequestedBy, "event-original", run.RequestKind, run.RequestDTag,
				run.Status, run.Backend, run.TargetRef, false, "", run.VerificationMode, run.VerificationStatus,
				run.RestoreEligibility, run.RestoreEligibilityReason, run.VerificationPolicyFailure, run.FailureCategory, []byte(`{}`), "", []byte(`{}`), nil, nil, now, now))

	existing, wasCreated, err := repo.CreateBackupRunIfAbsent(ctx, run)

	require.NoError(t, err)
	require.False(t, wasCreated)
	require.Equal(t, existingID, existing.ID)
	require.Equal(t, "event-original", existing.RequestEventID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryUpsertAndGetBackupDefinition(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	definition := backupDefinitionFixture()
	tenantID := uuid.New()
	environmentID := uuid.New()
	definition.TenantID = &tenantID
	definition.EnvironmentID = &environmentID
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectExec("INSERT INTO backup_definitions").
		WithArgs(definition.ID, definition.Name, definition.RepositoryID, definition.RepositoryName, definition.PolicyID, definition.PolicyName, definition.RecipeID, definition.RecipeName,
			definition.RecipeVersion, definition.ScheduleExpression, definition.ScheduleEnabled, definition.ScheduleJitterWindow, tenantID, definition.TenantName,
			environmentID, definition.EnvironmentName, definition.OwnerPubkey, definition.RequiresApproval, definition.ApprovalPolicy, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), definition.Group, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), definition.CreatedBy).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, repo.UpsertBackupDefinition(ctx, definition))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + backupDefinitionColumns + " FROM backup_definitions WHERE id = $1")).
		WithArgs(definition.ID).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupDefinitionColumns)).
			AddRow(definition.ID, definition.Name, definition.RepositoryID, definition.RepositoryName, definition.PolicyID, definition.PolicyName, definition.RecipeID, definition.RecipeName,
				definition.RecipeVersion, definition.ScheduleExpression, definition.ScheduleEnabled, definition.ScheduleJitterWindow, tenantID.String(), definition.TenantName,
				environmentID.String(), definition.EnvironmentName, definition.OwnerPubkey, definition.RequiresApproval, definition.ApprovalPolicy, []byte(`{"allowed_prefixes":["fs:/restore"]}`),
				[]byte(`["site:west"]`), []byte(`["backup.snapshot_create"]`), []byte(`{"service":"api"}`), definition.Group, []byte(`{"source":"test"}`), now, now, definition.CreatedBy))

	stored, err := repo.GetBackupDefinition(ctx, definition.ID)

	require.NoError(t, err)
	require.Equal(t, definition.ID, stored.ID)
	require.Equal(t, tenantID, *stored.TenantID)
	require.Equal(t, environmentID, *stored.EnvironmentID)
	require.Equal(t, []string{"site:west"}, stored.ExecutorLabels)
	require.Equal(t, []string{"backup.snapshot_create"}, stored.CapabilityRequirements)
	require.Equal(t, "api", stored.Labels["service"])
	require.Equal(t, "test", stored.Metadata["source"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgBackupControlPlaneRepositoryDeleteBackupDefinitionReportsMissing(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgBackupControlPlaneRepositoryWithDB(mock)
	id := uuid.New()

	mock.ExpectExec("DELETE FROM backup_definitions").
		WithArgs(id).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = repo.DeleteBackupDefinition(ctx, id)

	require.ErrorIs(t, err, ErrNotFound)
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
		WithArgs(record.ID, record.BackupRunID, record.Mode, record.Status, record.Verified, pgxmock.AnyArg(), pgxmock.AnyArg(), record.Error, pgxmock.AnyArg(), record.VerifiedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
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
	restore.ApprovalRequired = true
	restore.ApprovalRequirement = domain.BackupApprovalRequirementPolicy
	require.NoError(t, domain.ValidateBackupRestoreRun(restore))
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("INSERT INTO backup_restores").
		WithArgs(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, policyID, restore.SnapshotID, restore.RestoreTargetRef,
			restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalRequired,
			restore.ApprovalRequirement, restore.ApprovalEventID, restore.ApprovedBy, restore.ApprovedAt, restore.ApprovalMessage,
			restore.ApprovalReasonCode, pgxmock.AnyArg(), restore.Status, restore.Backend, restore.VerificationStatus, pgxmock.AnyArg(), pgxmock.AnyArg(), restore.Error,
			restore.VerificationPolicyFailure, restore.FailureCategory, pgxmock.AnyArg(), restore.StartedAt, restore.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRestoreColumns)).
			AddRow(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, policyID.String(), restore.SnapshotID, restore.RestoreTargetRef,
				restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalRequired,
				restore.ApprovalRequirement, restore.ApprovalEventID, restore.ApprovedBy, nil, restore.ApprovalMessage,
				restore.ApprovalReasonCode, []byte(`{}`), restore.Status, restore.Backend, restore.VerificationStatus, []byte(`{"checked":true}`), []byte(`{"relay":"accepted"}`),
				restore.Error, restore.VerificationPolicyFailure, restore.FailureCategory, []byte(`{"source":"test"}`), nil, nil, now, now))

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
	restore.ApprovalRequired = true
	restore.ApprovalRequirement = domain.BackupApprovalRequirementPolicy
	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery("UPDATE backup_restores").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRestoreColumns)).
			AddRow(restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, nil, restore.SnapshotID, restore.RestoreTargetRef,
				restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalRequired,
				restore.ApprovalRequirement, restore.ApprovalEventID, restore.ApprovedBy, nil, restore.ApprovalMessage,
				restore.ApprovalReasonCode, []byte(`{}`), domain.RunStatusRunning, restore.Backend, restore.VerificationStatus, []byte(`{}`), []byte(`{}`),
				restore.Error, restore.VerificationPolicyFailure, restore.FailureCategory, []byte(`{}`), &now, nil, now, now))

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
			run.DryRun, pgxmock.AnyArg(), pgxmock.AnyArg(), run.Error, run.FailureCategory, pgxmock.AnyArg(), run.StartedAt, run.FinishedAt, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(splitColumns(backupRetentionRunColumns)).
			AddRow(run.ID, run.RepositoryID, policyID.String(), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag, run.Status, run.Backend,
				run.DryRun, []byte(`{"deleted":0}`), []byte(`{"relay":"accepted"}`), run.Error, run.FailureCategory, []byte(`{"source":"test"}`), nil, nil, now, now))

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
				[]byte(`{"snapshot_id":"snap-1"}`), []byte(`{"snapshot_id":"snap-1"}`), "", []byte(`{"ok":true}`), &verifiedAt, verifiedAt, verifiedAt))

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

func TestMarshalJSONArrayStoresNilSlicesAsEmptyArrays(t *testing.T) {
	encoded, err := marshalJSONArray(nil, "backup executor labels")

	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(encoded))
}

func backupDefinitionFixture() *domain.BackupDefinition {
	return &domain.BackupDefinition{
		ID:                     uuid.New(),
		Name:                   "daily-service",
		RepositoryID:           uuid.New(),
		RepositoryName:         "primary",
		PolicyID:               uuid.New(),
		PolicyName:             "verified",
		RecipeID:               uuid.New(),
		RecipeName:             "service-data",
		RecipeVersion:          "v1",
		ScheduleExpression:     "0 2 * * *",
		ScheduleEnabled:        true,
		ScheduleJitterWindow:   "15m",
		TenantName:             "fleet",
		EnvironmentName:        "prod",
		OwnerPubkey:            "owner-pubkey",
		RequiresApproval:       true,
		ApprovalPolicy:         "restore-admin",
		RestoreTargetRules:     map[string]any{"allowed_prefixes": []string{"fs:/restore"}},
		ExecutorLabels:         []string{"site:west"},
		CapabilityRequirements: []string{"backup.snapshot_create"},
		Labels:                 map[string]any{"service": "api"},
		Group:                  "production",
		Metadata:               map[string]any{"source": "test"},
		CreatedBy:              "creator-pubkey",
	}
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
