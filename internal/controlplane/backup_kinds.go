package controlplane

// Backup control-plane kinds. These values are allocated in docs/control-planes.md
// and intentionally avoid the ML 38390-38399 and artifact attestation 31200 ranges.
const (
	KindBackupRunRequest               = 38400
	KindBackupVerificationRequest      = 38401
	KindBackupRestoreRequest           = 38402
	KindBackupRestoreApproval          = 38403
	KindBackupRetentionEnforce         = 38404
	KindBackupRepositoryRegister       = 38405
	KindBackupPolicyApply              = 38406
	KindBackupRecipeApply              = 38407
	KindBackupDefinitionApply          = 38408
	KindBackupRepositoryProbe          = 38409
	KindBackupRunResult                = 38410
	KindBackupVerificationResult       = 38411
	KindBackupRestoreResult            = 38412
	KindBackupRestoreApprovalResult    = 38413
	KindBackupRetentionResult          = 38414
	KindBackupRepositoryRegisterResult = 38415
	KindBackupPolicyApplyResult        = 38416
	KindBackupRecipeApplyResult        = 38417
	KindBackupDefinitionApplyResult    = 38418
	KindBackupRepositoryProbeResult    = 38419

	KindBackupRunStatus          = 6981
	KindBackupRestoreStatus      = 6982
	KindBackupVerificationStatus = 6983
	KindBackupObservation        = 6984

	KindBackupRunAttestation          = 31310
	KindBackupVerificationAttestation = 31311
)

func backupRequestKinds() []int {
	return []int{KindBackupRunRequest}
}
