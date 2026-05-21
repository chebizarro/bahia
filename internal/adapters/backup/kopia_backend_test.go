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
