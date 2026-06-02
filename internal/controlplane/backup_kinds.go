package controlplane

import "github.com/openagentsinc/bahia/internal/kinds"

// Backup control-plane kinds are aliases to the canonical internal/kinds catalog.
const (
	KindBackupRunRequest               = kinds.BackupRunRequest
	KindBackupVerificationRequest      = kinds.BackupVerificationRequest
	KindBackupRestoreRequest           = kinds.BackupRestoreRequest
	KindBackupRestoreApproval          = kinds.BackupRestoreApproval
	KindBackupRetentionEnforce         = kinds.BackupRetentionEnforce
	KindBackupRepositoryRegister       = kinds.BackupRepositoryRegister
	KindBackupPolicyApply              = kinds.BackupPolicyApply
	KindBackupRecipeApply              = kinds.BackupRecipeApply
	KindBackupDefinitionApply          = kinds.BackupDefinitionApply
	KindBackupRepositoryProbe          = kinds.BackupRepositoryProbe
	KindBackupRunResult                = kinds.BackupRunResult
	KindBackupVerificationResult       = kinds.BackupVerificationResult
	KindBackupRestoreResult            = kinds.BackupRestoreResult
	KindBackupRestoreApprovalResult    = kinds.BackupRestoreApprovalResult
	KindBackupRetentionResult          = kinds.BackupRetentionResult
	KindBackupRepositoryRegisterResult = kinds.BackupRepositoryRegisterResult
	KindBackupPolicyApplyResult        = kinds.BackupPolicyApplyResult
	KindBackupRecipeApplyResult        = kinds.BackupRecipeApplyResult
	KindBackupDefinitionApplyResult    = kinds.BackupDefinitionApplyResult
	KindBackupRepositoryProbeResult    = kinds.BackupRepositoryProbeResult

	KindBackupRunStatus          = kinds.BackupRunStatus
	KindBackupRestoreStatus      = kinds.BackupRestoreStatus
	KindBackupVerificationStatus = kinds.BackupVerificationStatus
	KindBackupObservation        = kinds.BackupObservation

	KindBackupRunAttestation          = kinds.BackupRunAttestation
	KindBackupVerificationAttestation = kinds.BackupVerificationAttestation
)

func backupRequestKinds() []int {
	return []int{
		KindBackupRunRequest,
		KindBackupVerificationRequest,
		KindBackupRestoreRequest,
		KindBackupRestoreApproval,
		KindBackupRetentionEnforce,
		KindBackupRepositoryRegister,
		KindBackupPolicyApply,
		KindBackupRecipeApply,
		KindBackupDefinitionApply,
		KindBackupRepositoryProbe,
	}
}
