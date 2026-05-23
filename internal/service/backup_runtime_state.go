package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	backupMetadataEffectiveVerificationMode  = "effective_verification_mode"
	backupMetadataPolicyRequiresVerification = "policy_requires_verification"
	backupMetadataRestoreVerificationMode    = "restore_verification_mode"
	backupMetadataRestoreApprovalRequirement = "restore_approval_requirement"
	backupMetadataRestoreApprovalRequired    = "restore_approval_required"
)

func refreshBackupRunEligibility(run *domain.BackupRun) {
	if run == nil {
		return
	}
	run.RestoreEligibility, run.RestoreEligibilityReason = domain.BackupRunRestoreEligibility(run)
}

func backupFailureCategoryForStep(step string, cause error) domain.BackupFailureCategory {
	if errors.Is(cause, context.Canceled) {
		return domain.BackupFailureCancelled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return domain.BackupFailureTimeout
	}
	switch strings.TrimSpace(step) {
	case "load_inputs":
		return domain.BackupFailureLoadInputs
	case "backend_resolve":
		return domain.BackupFailureBackendResolve
	case "backend_health":
		return domain.BackupFailureBackendHealth
	case "snapshotting":
		return domain.BackupFailureSnapshot
	case "verifying":
		return domain.BackupFailureVerification
	case "restoring":
		return domain.BackupFailureRestoreExecution
	case "enforcing_retention":
		return domain.BackupFailureRetentionExecution
	case "policy":
		return domain.BackupFailurePolicy
	default:
		return domain.BackupFailureUnknown
	}
}

func snapshotRestoreVerificationMode(restore *domain.BackupRestoreRun, mode domain.BackupVerificationMode) {
	if restore == nil {
		return
	}
	if restore.Metadata == nil {
		restore.Metadata = map[string]any{}
	}
	restore.Metadata[backupMetadataRestoreVerificationMode] = string(mode)
}

func restoreVerificationModeSnapshot(restore *domain.BackupRestoreRun) domain.BackupVerificationMode {
	if restore == nil || restore.Metadata == nil {
		return ""
	}
	if raw, ok := restore.Metadata[backupMetadataRestoreVerificationMode]; ok {
		mode := domain.BackupVerificationMode(toString(raw))
		if mode.IsValid() {
			return mode
		}
	}
	return ""
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
