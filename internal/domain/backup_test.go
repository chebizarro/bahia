package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateBackupPolicyRequiresConcreteVerificationMode(t *testing.T) {
	policy := &BackupPolicy{Name: "verified", RequireVerification: true, VerificationMode: BackupVerificationNone}

	err := ValidateBackupPolicy(policy)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidValue)
}

func TestValidateBackupRunDefaultsQueuedAndPending(t *testing.T) {
	run := &BackupRun{
		RecipeID:       uuid.New(),
		RepositoryID:   uuid.New(),
		RequestedBy:    " requester ",
		RequestEventID: "event-1",
		RequestKind:    38400,
		RequestDTag:    " run:daily ",
		Backend:        BackupBackendKopia,
		TargetRef:      "fs:/srv/data",
	}

	require.NoError(t, ValidateBackupRun(run))
	require.Equal(t, "requester", run.RequestedBy)
	require.Equal(t, "run:daily", run.RequestDTag)
	require.Equal(t, RunStatusQueued, run.Status)
	require.Equal(t, BackupVerificationPending, run.VerificationStatus)
}

func TestValidateBackupVerificationRecordRequiresVerifiedSucceededStatus(t *testing.T) {
	record := &BackupVerificationRecord{
		BackupRunID: uuid.New(),
		Mode:        BackupVerificationKopiaSnapshotVerify,
		Status:      BackupVerificationSucceeded,
		Verified:    false,
	}

	err := ValidateBackupVerificationRecord(record)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidValue)
}

func TestValidateBackupRestoreRunDefaultsPendingApprovalQueuedAndPendingVerification(t *testing.T) {
	restore := &BackupRestoreRun{
		BackupRunID:      uuid.New(),
		RecipeID:         uuid.New(),
		RepositoryID:     uuid.New(),
		SnapshotID:       " snap-1 ",
		RestoreTargetRef: " fs:/restore/path ",
		RequestedBy:      " requester ",
		RequestEventID:   "event-restore",
		RequestKind:      38402,
		RequestDTag:      " restore:daily ",
		Backend:          BackupBackendKopia,
	}

	require.NoError(t, ValidateBackupRestoreRun(restore))
	require.Equal(t, "snap-1", restore.SnapshotID)
	require.Equal(t, "fs:/restore/path", restore.RestoreTargetRef)
	require.Equal(t, "requester", restore.RequestedBy)
	require.Equal(t, "restore:daily", restore.RequestDTag)
	require.Equal(t, BackupApprovalPending, restore.ApprovalStatus)
	require.Equal(t, RunStatusQueued, restore.Status)
	require.Equal(t, BackupVerificationPending, restore.VerificationStatus)
}

func TestValidateBackupRestoreRunRequiresSourceSnapshotAndTarget(t *testing.T) {
	restore := &BackupRestoreRun{
		BackupRunID:    uuid.New(),
		RecipeID:       uuid.New(),
		RepositoryID:   uuid.New(),
		RequestedBy:    "requester",
		RequestEventID: "event-restore",
		RequestKind:    38402,
		RequestDTag:    "restore:daily",
		Backend:        BackupBackendKopia,
	}

	err := ValidateBackupRestoreRun(restore)

	require.Error(t, err)
}

func TestValidateBackupRestoreRunRequiresApprovalProvenanceWhenApproved(t *testing.T) {
	restore := &BackupRestoreRun{
		BackupRunID:      uuid.New(),
		RecipeID:         uuid.New(),
		RepositoryID:     uuid.New(),
		SnapshotID:       "snap-1",
		RestoreTargetRef: "fs:/restore/path",
		RequestedBy:      "requester",
		RequestEventID:   "event-restore",
		RequestKind:      38402,
		RequestDTag:      "restore:daily",
		Backend:          BackupBackendKopia,
		ApprovalStatus:   BackupApprovalApproved,
	}

	err := ValidateBackupRestoreRun(restore)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrEmptyField)
}

func TestValidateBackupRetentionRunDefaultsQueued(t *testing.T) {
	retention := &BackupRetentionRun{
		RepositoryID:   uuid.New(),
		RequestedBy:    " requester ",
		RequestEventID: "event-retention",
		RequestKind:    38404,
		RequestDTag:    " retention:weekly ",
		Backend:        BackupBackendKopia,
		DryRun:         true,
	}

	require.NoError(t, ValidateBackupRetentionRun(retention))
	require.Equal(t, "requester", retention.RequestedBy)
	require.Equal(t, "retention:weekly", retention.RequestDTag)
	require.Equal(t, RunStatusQueued, retention.Status)
	require.True(t, retention.DryRun)
}

func TestBackupRunRestoreEligibleRequiresSuccessfulRunAndVerification(t *testing.T) {
	run := &BackupRun{Status: RunStatusSucceeded, VerificationStatus: BackupVerificationSkipped}
	require.False(t, BackupRunRestoreEligible(run))

	run.VerificationStatus = BackupVerificationSucceeded
	require.True(t, BackupRunRestoreEligible(run))

	run.Status = RunStatusFailed
	require.False(t, BackupRunRestoreEligible(run))
}
