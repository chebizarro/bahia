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

func TestKopiaBackendReportsLifecycleCapabilities(t *testing.T) {
	capabilities := NewKopiaBackend().Capabilities()

	require.Equal(t, service.BackendCapabilities{
		SnapshotCreate: true,
		SnapshotVerify: true,
		Restore:        true,
		Retention:      true,
		Probe:          true,
	}, capabilities)
}

func TestKopiaBackendCreateSnapshotRunsRealCLICommandBoundary(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: `{"id":"kopia-snapshot-1","rootEntry":{"obj":"kabcdef"}}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	recipe := kopiaRecipeFixture(repo.ID)
	run := kopiaRunFixture(recipe)

	result, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})

	require.NoError(t, err)
	require.Equal(t, "kopia-snapshot-1", result.SnapshotID)
	require.Len(t, runner.calls, 1)
	require.Equal(t, "kopia", runner.calls[0].binary)
	require.Equal(t, []string{"--config-file", "/secure/kopia/repository.config", "snapshot", "create", "--json", "/srv/data"}, runner.calls[0].args)
	require.Empty(t, runner.calls[0].extraEnv)
}

func TestKopiaBackendUsesPasswordEnvironmentReference(t *testing.T) {
	t.Setenv("KOPIA_TEST_PASSWORD", "secret-value")
	runner := &recordingKopiaRunner{stdout: `{"id":"snap-env"}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	repo.Metadata["kopia_password_env"] = "KOPIA_TEST_PASSWORD"
	recipe := kopiaRecipeFixture(repo.ID)
	run := kopiaRunFixture(recipe)

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})

	require.NoError(t, err)
	require.Equal(t, []string{"KOPIA_PASSWORD=secret-value"}, runner.calls[0].extraEnv)
}

func TestKopiaBackendMissingConfigFailsExplicitly(t *testing.T) {
	backend := NewKopiaBackend(withKopiaCommandRunner(&recordingKopiaRunner{}))
	repo := kopiaRepositoryFixture()
	repo.CredentialProfile = ""
	repo.Metadata = nil

	err := backend.Health(context.Background(), repo)

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "config file")
}

func TestKopiaBackendUnsupportedTargetAndPathScopeFailExplicitly(t *testing.T) {
	backend := NewKopiaBackend(withKopiaCommandRunner(&recordingKopiaRunner{}))
	repo := kopiaRepositoryFixture()
	recipe := kopiaRecipeFixture(repo.ID)
	run := kopiaRunFixture(recipe)

	recipe.TargetRef = "s3://bucket/path"
	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})
	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)

	recipe.TargetRef = "fs:/srv/data"
	recipe.Include = []string{"/srv/data/app"}
	_, err = backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})
	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)
}

func TestKopiaBackendSnapshotJSONWithoutIDIsUnsupported(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: `{"rootEntry":{"obj":"kabcdef"}}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	recipe := kopiaRecipeFixture(repo.ID)
	run := kopiaRunFixture(recipe)

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})

	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)
}

func TestKopiaBackendVerifySnapshotRunsVerifyCommand(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: `{"verified":true}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner), WithKopiaParallelism(10), WithKopiaFileParallelism(4))
	repo := kopiaRepositoryFixture()
	run := kopiaRunFixture(kopiaRecipeFixture(repo.ID))

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{Run: run, Repository: repo, SnapshotID: "snap-1", Mode: domain.BackupVerificationKopiaSnapshotVerify, VerifyFilesPercent: 25})

	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Equal(t, domain.BackupVerificationSucceeded, result.Status)
	require.Equal(t, []string{"--config-file", "/secure/kopia/repository.config", "snapshot", "verify", "--json", "--verify-files-percent=25", "--file-parallelism=4", "--parallel=10", "snap-1"}, runner.calls[0].args)
}

func TestKopiaBackendVerifySnapshotFailureReturnsFailedResult(t *testing.T) {
	runner := &recordingKopiaRunner{stderr: "corruption detected", err: errors.New("exit status 1")}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	run := kopiaRunFixture(kopiaRecipeFixture(repo.ID))

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{Run: run, Repository: repo, SnapshotID: "snap-1", Mode: domain.BackupVerificationKopiaSnapshotVerify})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.False(t, result.Verified)
	require.Equal(t, domain.BackupVerificationFailed, result.Status)
	require.Contains(t, result.Error, "corruption detected")
}

func TestKopiaBackendVerifySnapshotZeroExitFailureJSONFailsClosed(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: `{"verified":false}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	run := kopiaRunFixture(kopiaRecipeFixture(repo.ID))

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{Run: run, Repository: repo, SnapshotID: "snap-1", Mode: domain.BackupVerificationKopiaSnapshotVerify})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.False(t, result.Verified)
	require.Equal(t, domain.BackupVerificationFailed, result.Status)
}

func TestKopiaBackendVerifySnapshotMissingExplicitStatusFailsClosed(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: `{"checked_files":12}`}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	run := kopiaRunFixture(kopiaRecipeFixture(repo.ID))

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{Run: run, Repository: repo, SnapshotID: "snap-1", Mode: domain.BackupVerificationKopiaSnapshotVerify})

	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)
	require.False(t, result.Verified)
	require.Equal(t, domain.BackupVerificationUnsupported, result.Status)
}

func TestKopiaBackendRestoreUsesStoredRootEntryEvidence(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: "restore completed\n"}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner), WithKopiaParallelism(3))
	repo := kopiaRepositoryFixture()
	recipe := kopiaRecipeFixture(repo.ID)
	sourceRun := kopiaSucceededSourceRunFixture(recipe)
	restoreRun := kopiaRestoreRunFixture(sourceRun)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo})

	require.NoError(t, err)
	require.Equal(t, domain.BackupVerificationSkipped, result.VerificationStatus)
	require.Len(t, runner.calls, 1)
	require.Equal(t, []string{"--config-file", "/secure/kopia/repository.config", "snapshot", "restore", "--parallel=3", "kabcdef", "/restore/path"}, runner.calls[0].args)
}

func TestKopiaBackendRestoreFailsClosedWhenSnapshotEvidenceCannotMapToRestoreSource(t *testing.T) {
	runner := &recordingKopiaRunner{}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	recipe := kopiaRecipeFixture(repo.ID)
	sourceRun := kopiaSucceededSourceRunFixture(recipe)
	sourceRun.Metadata = nil
	restoreRun := kopiaRestoreRunFixture(sourceRun)

	_, err := backend.Restore(context.Background(), service.BackupRestoreRequest{Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "snapshot_id alone cannot be safely used")
	require.Empty(t, runner.calls)
}

func TestKopiaBackendEnforceRetentionRunsExpireDryRunAndDelete(t *testing.T) {
	runner := &recordingKopiaRunner{stdout: "would expire 2 snapshots"}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	policy := kopiaRetentionPolicyFixture("fs:/srv/data")
	run := kopiaRetentionRunFixture(repo.ID, policy.ID, true)

	result, err := backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})

	require.NoError(t, err)
	require.Contains(t, result.Evidence["stdout"], "would expire")
	require.Equal(t, []string{"--config-file", "/secure/kopia/repository.config", "snapshot", "expire", "/srv/data"}, runner.calls[0].args)

	run.DryRun = false
	_, err = backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})

	require.NoError(t, err)
	require.Equal(t, []string{"--config-file", "/secure/kopia/repository.config", "snapshot", "expire", "/srv/data", "--delete"}, runner.calls[1].args)
}

func TestKopiaBackendEnforceRetentionRequiresBackendNativeSelector(t *testing.T) {
	runner := &recordingKopiaRunner{}
	backend := NewKopiaBackend(withKopiaCommandRunner(runner))
	repo := kopiaRepositoryFixture()
	policy := kopiaRetentionPolicyFixture("relative/path")
	run := kopiaRetentionRunFixture(repo.ID, policy.ID, true)

	_, err := backend.EnforceRetention(context.Background(), service.BackupRetentionRequest{Run: run, Repository: repo, Policy: policy})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Empty(t, runner.calls)
}

type recordingKopiaRunner struct {
	stdout string
	stderr string
	err    error
	calls  []kopiaRunnerCall
}

type kopiaRunnerCall struct {
	binary   string
	args     []string
	extraEnv []string
}

func (r *recordingKopiaRunner) Run(_ context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
	r.calls = append(r.calls, kopiaRunnerCall{binary: binary, args: append([]string(nil), args...), extraEnv: append([]string(nil), extraEnv...)})
	return r.stdout, r.stderr, r.err
}

func kopiaRepositoryFixture() *domain.BackupRepository {
	return &domain.BackupRepository{ID: uuid.New(), Name: "primary", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://primary", CredentialProfile: "/secure/kopia/repository.config", Metadata: map[string]any{}}
}

func kopiaRecipeFixture(repoID uuid.UUID) *domain.BackupRecipe {
	return &domain.BackupRecipe{ID: uuid.New(), Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, TargetRef: "fs:/srv/data", VerificationMode: domain.BackupVerificationKopiaSnapshotVerify}
}

func kopiaRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	return &domain.BackupRun{ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: recipe.RepositoryID, RequestedBy: "pubkey", RequestEventID: "event", RequestKind: 38400, RequestDTag: "daily", Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, TargetRef: recipe.TargetRef, VerificationStatus: domain.BackupVerificationPending}
}

func kopiaSucceededSourceRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	run := kopiaRunFixture(recipe)
	run.Status = domain.RunStatusSucceeded
	run.SnapshotCreated = true
	run.SnapshotID = "manifest-id"
	run.VerificationStatus = domain.BackupVerificationSucceeded
	run.Metadata = map[string]any{
		"snapshot_evidence": map[string]any{
			"kopia_snapshot_create": map[string]any{
				"id":        "manifest-id",
				"rootEntry": map[string]any{"obj": "kabcdef"},
			},
		},
	}
	return run
}

func kopiaRestoreRunFixture(source *domain.BackupRun) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{ID: uuid.New(), BackupRunID: source.ID, RecipeID: source.RecipeID, RepositoryID: source.RepositoryID, SnapshotID: source.SnapshotID, RestoreTargetRef: "fs:/restore/path", RequestedBy: "pubkey", RequestEventID: "restore-event", RequestKind: 38402, RequestDTag: "restore:daily", ApprovalStatus: domain.BackupApprovalNotRequired, Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, VerificationStatus: domain.BackupVerificationPending}
}

func kopiaRetentionPolicyFixture(selector string) *domain.BackupPolicy {
	return &domain.BackupPolicy{ID: uuid.New(), Name: "retention", VerificationMode: domain.BackupVerificationNone, Metadata: map[string]any{service.BackupPolicyMetadataRetentionMode: string(service.BackupRetentionModeBackendNative), service.BackupPolicyMetadataRetentionSelector: selector}}
}

func kopiaRetentionRunFixture(repoID, policyID uuid.UUID, dryRun bool) *domain.BackupRetentionRun {
	return &domain.BackupRetentionRun{ID: uuid.New(), RepositoryID: repoID, PolicyID: &policyID, RequestedBy: "pubkey", RequestEventID: "retention-event", RequestKind: 38404, RequestDTag: "retention:weekly", Status: domain.RunStatusQueued, Backend: domain.BackupBackendKopia, DryRun: dryRun}
}
