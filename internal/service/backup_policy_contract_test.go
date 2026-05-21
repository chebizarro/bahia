package service

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestParseBackupPolicyRuntimeContractDefaults(t *testing.T) {
	contract, err := ParseBackupPolicyRuntimeContract(nil)

	require.NoError(t, err)
	require.True(t, contract.RestoreApprovalRequired)
	require.Equal(t, domain.BackupVerificationNone, contract.RestoreVerificationMode)
	require.Equal(t, BackupRetentionModeNone, contract.RetentionMode)
	require.False(t, contract.RetentionDryRunDefault)
}

func TestParseBackupPolicyRuntimeContractParsesSupportedMetadata(t *testing.T) {
	policy := &domain.BackupPolicy{Metadata: map[string]any{
		BackupPolicyMetadataRestoreApprovalRequired: false,
		BackupPolicyMetadataRestoreVerificationMode: string(domain.BackupVerificationKopiaSnapshotVerify),
		BackupPolicyMetadataRetentionMode:           string(BackupRetentionModeBackendNative),
		BackupPolicyMetadataRetentionSelector:       " keep-latest-7 ",
		BackupPolicyMetadataRetentionDryRunDefault:  true,
	}}

	contract, err := ParseBackupPolicyRuntimeContract(policy)

	require.NoError(t, err)
	require.False(t, contract.RestoreApprovalRequired)
	require.Equal(t, domain.BackupVerificationKopiaSnapshotVerify, contract.RestoreVerificationMode)
	require.Equal(t, BackupRetentionModeBackendNative, contract.RetentionMode)
	require.Equal(t, "keep-latest-7", contract.RetentionSelector)
	require.True(t, contract.RetentionDryRunDefault)
}

func TestParseBackupPolicyRuntimeContractRejectsMalformedMetadata(t *testing.T) {
	policy := &domain.BackupPolicy{Metadata: map[string]any{
		BackupPolicyMetadataRetentionDryRunDefault: "yes",
	}}

	_, err := ParseBackupPolicyRuntimeContract(policy)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
}

func TestParseBackupPolicyRuntimeContractRejectsUnsupportedRetentionMode(t *testing.T) {
	policy := &domain.BackupPolicy{Metadata: map[string]any{
		BackupPolicyMetadataRetentionMode: "bahia_computed_delete_list",
	}}

	_, err := ParseBackupPolicyRuntimeContract(policy)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
}

func TestParseBackupPolicyRuntimeContractRequiresSelectorForBackendNativeRetention(t *testing.T) {
	policy := &domain.BackupPolicy{Metadata: map[string]any{
		BackupPolicyMetadataRetentionMode: string(BackupRetentionModeBackendNative),
	}}

	_, err := ParseBackupPolicyRuntimeContract(policy)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackupBackendConfiguration)
}
