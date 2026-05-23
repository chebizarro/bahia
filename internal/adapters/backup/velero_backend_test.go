package backup

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestVeleroBackendReportsLifecycleCapabilities(t *testing.T) {
	capabilities := NewVeleroBackend().Capabilities()

	require.Equal(t, service.BackendCapabilities{
		SnapshotCreate: false,
		SnapshotVerify: false,
		Restore:        true,
		Retention:      true,
		Probe:          true,
	}, capabilities)
}

func TestVeleroBackendRestoreCreatesAndReadsRealCLIResources(t *testing.T) {
	runner := &recordingVeleroRunner{results: []veleroRunnerResult{
		{stdout: `{"metadata":{"name":"restore-a"},"status":{"phase":"Completed"}}`},
		{stdout: `{"metadata":{"name":"restore-a"},"status":{"phase":"Completed"}}`},
	}}
	backend := NewVeleroBackend(withVeleroCommandRunner(runner))
	repo := veleroRepositoryFixture()
	sourceRun := veleroSourceRunFixture(repo.ID)
	restoreRun := veleroRestoreRunFixture(sourceRun)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{Run: restoreRun, SourceRun: sourceRun, Repository: repo})

	require.NoError(t, err)
	require.Equal(t, domain.BackupVerificationSkipped, result.VerificationStatus)
	require.Len(t, runner.calls, 2)
	require.Equal(t, []string{"--namespace", "velero-system", "--kubeconfig", "/secure/kube/config", "--kubecontext", "prod-a", "restore", "create", "restore-a", "--from-backup", "backup-a", "--wait", "-o", "json"}, runner.calls[0].args)
	require.Equal(t, []string{"--namespace", "velero-system", "--kubeconfig", "/secure/kube/config", "--kubecontext", "prod-a", "restore", "get", "restore-a", "-o", "json"}, runner.calls[1].args)
}

func TestVeleroBackendRestoreFailsClosedWithoutExplicitPhase(t *testing.T) {
	runner := &recordingVeleroRunner{results: []veleroRunnerResult{
		{stdout: `{"metadata":{"name":"restore-a"}}`},
		{stdout: `{"metadata":{"name":"restore-a"}}`},
	}}
	backend := NewVeleroBackend(withVeleroCommandRunner(runner))
	repo := veleroRepositoryFixture()
	sourceRun := veleroSourceRunFixture(repo.ID)
	restoreRun := veleroRestoreRunFixture(sourceRun)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{Run: restoreRun, SourceRun: sourceRun, Repository: repo})

	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)
	require.Contains(t, result.Error, "status.phase")
}

func TestVeleroBackendRetentionDryRunReadsBackupAndDeleteConfirmsBackupDeletion(t *testing.T) {
	runner := &recordingVeleroRunner{results: []veleroRunnerResult{
		{stdout: `{"metadata":{"name":"backup-a"},"status":{"phase":"Completed"}}`},
		{stdout: "Request to delete backup \"backup-a\" submitted successfully"},
	}}
	backend := NewVeleroBackend(withVeleroCommandRunner(runner))
	repo := veleroRepositoryFixture()
	policy := veleroRetentionPolicyFixture("backup:backup-a")
	run := veleroRetentionRunFixture(repo.ID, policy.ID, true)

	_, err := backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})

	require.NoError(t, err)
	require.Equal(t, []string{"--namespace", "velero-system", "--kubeconfig", "/secure/kube/config", "--kubecontext", "prod-a", "backup", "get", "backup-a", "-o", "json"}, runner.calls[0].args)

	run.DryRun = false
	_, err = backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})

	require.NoError(t, err)
	require.Equal(t, []string{"--namespace", "velero-system", "--kubeconfig", "/secure/kube/config", "--kubecontext", "prod-a", "backup", "delete", "backup-a", "--confirm"}, runner.calls[1].args)
}

func TestVeleroBackendFailsExplicitlyForMissingNamespaceAndUnsupportedRetentionSelector(t *testing.T) {
	backend := NewVeleroBackend(withVeleroCommandRunner(&recordingVeleroRunner{}))
	repo := veleroRepositoryFixture()
	repo.Metadata["velero_namespace"] = ""

	err := backend.Health(context.Background(), repo)
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "namespace")

	repo = veleroRepositoryFixture()
	policy := veleroRetentionPolicyFixture("schedule:daily")
	run := veleroRetentionRunFixture(repo.ID, policy.ID, false)
	_, err = backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "backup:<name>")
}

func TestVeleroBackendCommandFailureMapsToExecutionError(t *testing.T) {
	runner := &recordingVeleroRunner{results: []veleroRunnerResult{{stderr: "restore failed", err: errors.New("exit status 1")}}}
	backend := NewVeleroBackend(withVeleroCommandRunner(runner))
	repo := veleroRepositoryFixture()
	sourceRun := veleroSourceRunFixture(repo.ID)
	restoreRun := veleroRestoreRunFixture(sourceRun)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{Run: restoreRun, SourceRun: sourceRun, Repository: repo})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.Contains(t, result.Error, "restore failed")
}

type recordingVeleroRunner struct {
	results []veleroRunnerResult
	calls   []veleroRunnerCall
}

type veleroRunnerResult struct {
	stdout string
	stderr string
	err    error
}

type veleroRunnerCall struct {
	binary   string
	args     []string
	extraEnv []string
}

func (r *recordingVeleroRunner) Run(_ context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
	r.calls = append(r.calls, veleroRunnerCall{binary: binary, args: append([]string(nil), args...), extraEnv: append([]string(nil), extraEnv...)})
	if len(r.results) == 0 {
		return "", "", nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result.stdout, result.stderr, result.err
}

func veleroRepositoryFixture() *domain.BackupRepository {
	return &domain.BackupRepository{ID: uuid.New(), Name: "cluster-a", Backend: domain.BackupBackendVelero, RepositoryURI: "velero://cluster-a", Metadata: map[string]any{"velero_namespace": "velero-system", "velero_kubeconfig": "/secure/kube/config", "velero_kube_context": "prod-a"}}
}

func veleroSourceRunFixture(repoID uuid.UUID) *domain.BackupRun {
	return &domain.BackupRun{ID: uuid.New(), RecipeID: uuid.New(), RepositoryID: repoID, RequestedBy: "pubkey", RequestEventID: "backup-event", RequestKind: 38400, RequestDTag: "backup:velero", Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendVelero, TargetRef: "velero:cluster-a", SnapshotCreated: true, SnapshotID: "backup-a", VerificationStatus: domain.BackupVerificationSucceeded}
}

func veleroRestoreRunFixture(source *domain.BackupRun) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{ID: uuid.New(), BackupRunID: source.ID, RecipeID: source.RecipeID, RepositoryID: source.RepositoryID, SnapshotID: source.SnapshotID, RestoreTargetRef: "velero:restore-a", RequestedBy: "pubkey", RequestEventID: "restore-event", RequestKind: 38402, RequestDTag: "restore:velero", ApprovalStatus: domain.BackupApprovalNotRequired, Status: domain.RunStatusQueued, Backend: domain.BackupBackendVelero, VerificationStatus: domain.BackupVerificationPending}
}

func veleroRetentionPolicyFixture(selector string) *domain.BackupPolicy {
	return &domain.BackupPolicy{ID: uuid.New(), Name: "velero-retention", VerificationMode: domain.BackupVerificationNone, Metadata: map[string]any{service.BackupPolicyMetadataRetentionMode: string(service.BackupRetentionModeBackendNative), service.BackupPolicyMetadataRetentionSelector: selector}}
}

func veleroRetentionRunFixture(repoID, policyID uuid.UUID, dryRun bool) *domain.BackupRetentionRun {
	return &domain.BackupRetentionRun{ID: uuid.New(), RepositoryID: repoID, PolicyID: &policyID, RequestedBy: "pubkey", RequestEventID: "retention-event", RequestKind: 38404, RequestDTag: "retention:velero", Status: domain.RunStatusQueued, Backend: domain.BackupBackendVelero, DryRun: dryRun}
}
