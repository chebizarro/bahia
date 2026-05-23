package domain

import (
	"testing"
	"time"

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

func TestValidateBackupRepositoryAcceptsVeleroBackendKind(t *testing.T) {
	repo := &BackupRepository{
		ID:            uuid.New(),
		Name:          "cluster-a",
		Backend:       BackupBackendVelero,
		RepositoryURI: "velero://cluster-a",
		Metadata:      map[string]any{"velero_namespace": "velero-system"},
	}

	require.NoError(t, ValidateBackupRepository(repo))
	require.True(t, BackupBackendVelero.IsValid())
}

func TestValidateBackupDefinitionTrimsAndRequiresComposedReferences(t *testing.T) {
	definition := &BackupDefinition{
		Name:                   " daily-service ",
		RepositoryID:           uuid.New(),
		RepositoryName:         " primary ",
		PolicyID:               uuid.New(),
		PolicyName:             " verified ",
		RecipeID:               uuid.New(),
		RecipeName:             " service-data ",
		RecipeVersion:          " v1 ",
		ScheduleExpression:     " 0 2 * * * ",
		ScheduleEnabled:        true,
		ScheduleJitterWindow:   " 15m ",
		OwnerPubkey:            " owner-pubkey ",
		RequiresApproval:       true,
		ApprovalPolicy:         " restore-admin ",
		ExecutorLabels:         []string{"site:west"},
		CapabilityRequirements: []string{"backup.snapshot_create"},
		Labels:                 map[string]any{"service": "api"},
		Group:                  " production ",
		CreatedBy:              " creator-pubkey ",
	}

	require.NoError(t, ValidateBackupDefinition(definition))
	require.Equal(t, "daily-service", definition.Name)
	require.Equal(t, "primary", definition.RepositoryName)
	require.Equal(t, "verified", definition.PolicyName)
	require.Equal(t, "service-data", definition.RecipeName)
	require.Equal(t, "v1", definition.RecipeVersion)
	require.Equal(t, "production", definition.Group)
}

func TestValidateBackupDefinitionRequiresCreatedBy(t *testing.T) {
	definition := &BackupDefinition{
		Name:           "daily-service",
		RepositoryID:   uuid.New(),
		RepositoryName: "primary",
		PolicyID:       uuid.New(),
		PolicyName:     "verified",
		RecipeID:       uuid.New(),
		RecipeName:     "service-data",
		RecipeVersion:  "v1",
	}

	err := ValidateBackupDefinition(definition)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrEmptyField)
}

func TestValidateBackupDefinitionRejectsIncompleteScheduleAndApprovalIntent(t *testing.T) {
	definition := &BackupDefinition{
		Name:            "daily-service",
		RepositoryID:    uuid.New(),
		RepositoryName:  "primary",
		PolicyID:        uuid.New(),
		PolicyName:      "verified",
		RecipeID:        uuid.New(),
		RecipeName:      "service-data",
		RecipeVersion:   "v1",
		ScheduleEnabled: true,
		CreatedBy:       "creator-pubkey",
	}

	err := ValidateBackupDefinition(definition)
	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "schedule_expression")

	definition.ScheduleExpression = "0 2 * * *"
	definition.RequiresApproval = true
	err = ValidateBackupDefinition(definition)
	require.ErrorIs(t, err, ErrEmptyField)
	require.Contains(t, err.Error(), "approval_policy")
}

func TestValidateBackupDefinitionRejectsInvalidScheduleJitterWindow(t *testing.T) {
	definition := &BackupDefinition{
		Name:                 "daily-service",
		RepositoryID:         uuid.New(),
		RepositoryName:       "primary",
		PolicyID:             uuid.New(),
		PolicyName:           "verified",
		RecipeID:             uuid.New(),
		RecipeName:           "service-data",
		RecipeVersion:        "v1",
		ScheduleExpression:   "0 2 * * *",
		ScheduleEnabled:      true,
		ScheduleJitterWindow: "not-a-duration",
		CreatedBy:            "creator-pubkey",
	}

	err := ValidateBackupDefinition(definition)
	require.ErrorIs(t, err, ErrInvalidValue)
	require.Contains(t, err.Error(), "schedule_jitter_window")
}

func TestValidateBackupScheduleStateTracksPauseDisableAndMaintenanceIntent(t *testing.T) {
	now := time.Now().UTC()
	state := &BackupScheduleState{
		DefinitionID:              uuid.New(),
		MissedRunCount:            2,
		PauseReason:               " operator maintenance ",
		PausedBy:                  " scheduler-admin ",
		PausedAt:                  &now,
		DisabledBy:                " scheduler-admin ",
		DisableReason:             " retired ",
		DisabledAt:                &now,
		MaintenanceWindow:         " 01:00-03:00 ",
		MaintenanceWindowTimeZone: " UTC ",
	}

	require.NoError(t, ValidateBackupScheduleState(state))
	require.True(t, state.Paused())
	require.True(t, state.Disabled())
	require.Equal(t, "operator maintenance", state.PauseReason)
	require.Equal(t, "01:00-03:00", state.MaintenanceWindow)
}

func TestValidateBackupScheduleStateRejectsIncompletePauseAndNegativeMissedCount(t *testing.T) {
	state := &BackupScheduleState{DefinitionID: uuid.New(), PauseReason: "maintenance"}

	err := ValidateBackupScheduleState(state)
	require.ErrorIs(t, err, ErrEmptyField)

	state.PauseReason = ""
	state.MissedRunCount = -1
	err = ValidateBackupScheduleState(state)
	require.ErrorIs(t, err, ErrInvalidValue)
}

func TestBackupDefinitionMaintenanceWindowReadsScheduleMetadata(t *testing.T) {
	definition := &BackupDefinition{Metadata: map[string]any{
		BackupDefinitionMetadataMaintenanceWindow: " 02:00-04:00 ",
		BackupDefinitionMetadataMaintenanceTZ:     " UTC ",
	}}

	require.Equal(t, "02:00-04:00", BackupDefinitionMaintenanceWindow(definition))
	require.Equal(t, "UTC", BackupDefinitionMaintenanceWindowTimeZone(definition))
}

func TestValidateBackupRecipeAndRunRejectVeleroUntilSnapshotCapabilityExists(t *testing.T) {
	repoID := uuid.New()
	recipe := &BackupRecipe{Name: "velero", Version: "v1", Backend: BackupBackendVelero, RepositoryID: repoID, TargetRef: "velero:cluster-a"}
	err := ValidateBackupRecipe(recipe)
	require.ErrorIs(t, err, ErrInvalidValue)
	require.Contains(t, err.Error(), "cannot create backup runs")

	run := &BackupRun{RecipeID: uuid.New(), RepositoryID: repoID, RequestedBy: "pubkey", RequestEventID: "event", RequestKind: 38400, RequestDTag: "run", Backend: BackupBackendVelero, TargetRef: "velero:cluster-a"}
	err = ValidateBackupRun(run)
	require.ErrorIs(t, err, ErrInvalidValue)
	require.Contains(t, err.Error(), "cannot create backup runs")
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
