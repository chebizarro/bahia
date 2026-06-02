package nostr

import "github.com/openagentsinc/bahia/internal/kinds"

// Backup replaceable read-model kinds are aliases to the canonical internal/kinds catalog.
const (
	KindBackupDefinitionRegistry      = kinds.BackupDefinitionRegistry
	KindBackupPolicyRegistry          = kinds.BackupPolicyRegistry
	KindBackupRepositoryRegistry      = kinds.BackupRepositoryRegistry
	KindBackupRetentionRegistry       = kinds.BackupRetentionRegistry
	KindBackupRecipeRegistry          = kinds.BackupRecipeRegistry
	KindBackupRunState                = kinds.BackupRunState
	KindBackupVerificationState       = kinds.BackupVerificationState
	KindBackupRestoreState            = kinds.BackupRestoreState
	KindBackupRuntimeObservationState = kinds.BackupRuntimeObservationState
)
