package service

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	BackupPolicyMetadataRestoreApprovalRequired = "restore_approval_required"
	BackupPolicyMetadataRestoreVerificationMode = "restore_verification_mode"
	BackupPolicyMetadataRetentionMode           = "retention_mode"
	BackupPolicyMetadataRetentionSelector       = "retention_selector"
	BackupPolicyMetadataRetentionDryRunDefault  = "retention_dry_run_default"
)

type BackupRetentionMode string

const (
	BackupRetentionModeNone          BackupRetentionMode = "none"
	BackupRetentionModeBackendNative BackupRetentionMode = "backend_native"
)

type BackupPolicyRuntimeContract struct {
	RestoreApprovalRequired bool
	RestoreVerificationMode domain.BackupVerificationMode
	RetentionMode           BackupRetentionMode
	RetentionSelector       string
	RetentionDryRunDefault  bool
}

func ParseBackupPolicyRuntimeContract(policy *domain.BackupPolicy) (BackupPolicyRuntimeContract, error) {
	contract := BackupPolicyRuntimeContract{
		RestoreApprovalRequired: true,
		RestoreVerificationMode: domain.BackupVerificationNone,
		RetentionMode:           BackupRetentionModeNone,
	}
	if policy == nil || len(policy.Metadata) == 0 {
		return contract, nil
	}
	metadata := policy.Metadata
	if raw, ok := metadata[BackupPolicyMetadataRestoreApprovalRequired]; ok {
		value, ok := raw.(bool)
		if !ok {
			return contract, fmt.Errorf("%w: backup policy metadata.%s must be a boolean", ErrBackupBackendConfiguration, BackupPolicyMetadataRestoreApprovalRequired)
		}
		contract.RestoreApprovalRequired = value
	}
	if raw, ok := metadata[BackupPolicyMetadataRestoreVerificationMode]; ok {
		value, ok := raw.(string)
		if !ok {
			return contract, fmt.Errorf("%w: backup policy metadata.%s must be a string", ErrBackupBackendConfiguration, BackupPolicyMetadataRestoreVerificationMode)
		}
		mode := domain.BackupVerificationMode(strings.TrimSpace(value))
		if mode == "" {
			mode = domain.BackupVerificationNone
		}
		if !mode.IsValid() {
			return contract, fmt.Errorf("%w: backup policy restore verification mode %q is not valid", ErrBackupBackendConfiguration, mode)
		}
		contract.RestoreVerificationMode = mode
	}
	if raw, ok := metadata[BackupPolicyMetadataRetentionMode]; ok {
		value, ok := raw.(string)
		if !ok {
			return contract, fmt.Errorf("%w: backup policy metadata.%s must be a string", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionMode)
		}
		mode := BackupRetentionMode(strings.TrimSpace(value))
		if mode == "" {
			mode = BackupRetentionModeNone
		}
		switch mode {
		case BackupRetentionModeNone, BackupRetentionModeBackendNative:
			contract.RetentionMode = mode
		default:
			return contract, fmt.Errorf("%w: backup policy retention mode %q is not valid", ErrBackupBackendConfiguration, mode)
		}
	}
	if raw, ok := metadata[BackupPolicyMetadataRetentionSelector]; ok {
		value, ok := raw.(string)
		if !ok {
			return contract, fmt.Errorf("%w: backup policy metadata.%s must be a string", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionSelector)
		}
		contract.RetentionSelector = strings.TrimSpace(value)
	}
	if raw, ok := metadata[BackupPolicyMetadataRetentionDryRunDefault]; ok {
		value, ok := raw.(bool)
		if !ok {
			return contract, fmt.Errorf("%w: backup policy metadata.%s must be a boolean", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionDryRunDefault)
		}
		contract.RetentionDryRunDefault = value
	}
	if contract.RetentionMode == BackupRetentionModeBackendNative && contract.RetentionSelector == "" {
		return contract, fmt.Errorf("%w: backup policy metadata.%s must be set when retention_mode is backend_native", ErrBackupBackendConfiguration, BackupPolicyMetadataRetentionSelector)
	}
	return contract, nil
}
