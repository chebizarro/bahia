package nostr

// Backup replaceable read-model kinds. Keep aligned with docs/control-planes.md
// and internal/controlplane/backup_kinds.go without importing controlplane.
const (
	KindBackupDefinitionRegistry      = 31991
	KindBackupPolicyRegistry          = 31992
	KindBackupRepositoryRegistry      = 31993
	KindBackupRetentionRegistry       = 31994
	KindBackupRecipeRegistry          = 31995
	KindBackupRunState                = 31996
	KindBackupVerificationState       = 31997
	KindBackupRestoreState            = 31998
	KindBackupRuntimeObservationState = 31999
)
