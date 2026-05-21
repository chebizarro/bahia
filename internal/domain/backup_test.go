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

func TestBackupRunRestoreEligibleRequiresSuccessfulRunAndVerification(t *testing.T) {
	run := &BackupRun{Status: RunStatusSucceeded, VerificationStatus: BackupVerificationSkipped}
	require.False(t, BackupRunRestoreEligible(run))

	run.VerificationStatus = BackupVerificationSucceeded
	require.True(t, BackupRunRestoreEligible(run))

	run.Status = RunStatusFailed
	require.False(t, BackupRunRestoreEligible(run))
}
