package backup

import (
	"context"
	"errors"
	"strings"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPgBackendReportsLifecycleCapabilities(t *testing.T) {
	capabilities := NewPgBackend().Capabilities()

	require.Equal(t, service.BackendCapabilities{
		SnapshotCreate: true,
		SnapshotVerify: true,
		Restore:        true,
		Retention:      false,
		Probe:          true,
	}, capabilities)
}

func TestPgBackendHealthRunsPgDumpVersionCheck(t *testing.T) {
	runner := &recordingPgRunner{stdout: "pg_dump (PostgreSQL) 16.0\n"}
	backend := NewPgBackend(withPgCommandRunner(runner))
	repo := pgRepositoryFixture()

	err := backend.Health(context.Background(), repo)

	require.NoError(t, err)
	require.Len(t, runner.calls, 1)
	require.Equal(t, "pg_dump", runner.calls[0].binary)
	require.Equal(t, []string{"--version"}, runner.calls[0].args)
}

func TestPgBackendHealthFailsOnUnexpectedBinaryOutput(t *testing.T) {
	runner := &recordingPgRunner{stdout: "not pg_dump\n"}
	backend := NewPgBackend(withPgCommandRunner(runner))
	repo := pgRepositoryFixture()

	err := backend.Health(context.Background(), repo)

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.Contains(t, err.Error(), "unexpected output")
}

func TestPgBackendCreateSnapshotRunsPgDumpToStaging(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stdout: "pg_dump: writing dump\n", createFile: true}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	run := pgRunFixture(recipe)

	result, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})

	require.NoError(t, err)
	require.Equal(t, run.ID.String(), result.SnapshotID)
	require.Len(t, runner.calls, 1)
	require.Contains(t, runner.calls[0].args[0], "--dbname=")
	require.Contains(t, runner.calls[0].args[1], "--format=custom")
	// Verify the dump file was created
	dumpFile := filepath.Join(stagingDir, run.ID.String()+".dump")
	_, err = os.Stat(dumpFile)
	require.NoError(t, err)
	evidence, ok := result.Evidence["pg_dump"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, dumpFile, evidence["dump_file"])
	require.Contains(t, evidence["database_url"].(string), "localhost")
	require.NotContains(t, evidence["database_url"].(string), "secret")
}

func TestPgBackendCreateSnapshotFailsOnMissingInputs(t *testing.T) {
	backend := NewPgBackend(withPgCommandRunner(&recordingPgRunner{}))

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{})
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
}

func TestPgBackendCreateSnapshotFailsOnPgDumpError(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stderr: "connection refused", err: errors.New("exit status 1")}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	run := pgRunFixture(recipe)

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{Run: run, Recipe: recipe, Repository: repo})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	// Temp file should be cleaned up
	tmpFile := filepath.Join(stagingDir, run.ID.String()+".dump.tmp")
	_, err = os.Stat(tmpFile)
	require.True(t, os.IsNotExist(err))
}

func TestPgBackendVerifySnapshotUsesPgRestoreList(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stdout: ";\n; PostgreSQL database dump\n;\n2084; 21731 15221 TABLE public users root\n"}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	run := pgRunFixture(recipe)

	// First create a dump file
	dumpFile := filepath.Join(stagingDir, run.ID.String()+".dump")
	os.WriteFile(dumpFile, []byte("dummy dump content"), 0600)

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run:        run,
		Repository: repo,
		SnapshotID: run.ID.String(),
		Mode:       domain.BackupVerificationPgDumpVerify,
	})

	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Equal(t, domain.BackupVerificationSucceeded, result.Status)
	require.Equal(t, []string{"--list", dumpFile}, runner.calls[0].args)
}

func TestPgBackendVerifySnapshotFailsWithoutModeMatch(t *testing.T) {
	backend := NewPgBackend(withPgCommandRunner(&recordingPgRunner{}))
	repo := pgRepositoryFixture()
	run := pgRunFixture(pgRecipeFixture(repo.ID))

	_, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run: run, Repository: repo,
		Mode: domain.BackupVerificationKopiaSnapshotVerify,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendUnsupported)
}

func TestPgBackendVerifySnapshotFailsWhenDumpFileMissing(t *testing.T) {
	runner := &recordingPgRunner{}
	backend := NewPgBackend(withPgCommandRunner(runner))
	repo := pgRepositoryFixture()
	run := pgRunFixture(pgRecipeFixture(repo.ID))

	_, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run:        run,
		Repository: repo,
		SnapshotID: "nonexistent",
		Mode:       domain.BackupVerificationPgDumpVerify,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "not found")
}

func TestPgBackendVerifySnapshotFailsOnPgRestoreListError(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stderr: "malformed dump", err: errors.New("exit status 2")}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	run := pgRunFixture(recipe)
	dumpFile := filepath.Join(stagingDir, run.ID.String()+".dump")
	os.WriteFile(dumpFile, []byte("corrupted"), 0600)

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run:        run,
		Repository: repo,
		SnapshotID: run.ID.String(),
		Mode:       domain.BackupVerificationPgDumpVerify,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.False(t, result.Verified)
	require.Equal(t, domain.BackupVerificationFailed, result.Status)
}

func TestPgBackendRestoreFailsWithoutEligibleSource(t *testing.T) {
	runner := &recordingPgRunner{}
	backend := NewPgBackend(withPgCommandRunner(runner))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	sourceRun := pgRunFixture(recipe)
	restoreRun := pgRestoreRunFixture(sourceRun)

	_, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "restore requires a succeeded")
}

func TestPgBackendRestoreRunsPgRestore(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stdout: "pg_restore: restoring data for table users\n"}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	sourceRun := pgSucceededSourceRunFixture(recipe)
	restoreRun := pgRestoreRunFixture(sourceRun)

	// Create the dump file
	dumpFile := filepath.Join(stagingDir, sourceRun.ID.String()+".dump")
	os.WriteFile(dumpFile, []byte("dump content"), 0600)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.NoError(t, err)
	require.Equal(t, domain.BackupVerificationSkipped, result.VerificationStatus)
	require.Len(t, runner.calls, 1)
	require.Contains(t, runner.calls[0].args[0], "--dbname=")
	require.Contains(t, runner.calls[0].args, "--clean")
	require.Contains(t, runner.calls[0].args, "--exit-on-error")
}

func TestPgBackendRestoreMismatchedSourceFails(t *testing.T) {
	runner := &recordingPgRunner{}
	backend := NewPgBackend(withPgCommandRunner(runner))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	sourceRun := pgSucceededSourceRunFixture(recipe)
	restoreRun := pgRestoreRunFixture(sourceRun)
	restoreRun.BackupRunID = uuid.New()

	_, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
	require.Contains(t, err.Error(), "source mismatch")
}

func TestPgBackendPasswordNotInEvidence(t *testing.T) {
	redacted := redactDSN("postgres://user:secretpassword@localhost:5432/db")
	require.NotContains(t, redacted, "secretpassword")
	require.Contains(t, redacted, "user:")
	require.Contains(t, redacted, "*****")

	redacted = redactDSN("host=localhost dbname=test user=admin password=supersecret")
	require.NotContains(t, redacted, "supersecret")
	require.Contains(t, redacted, "password=***********")
}

func TestPgBackendRestoreWithVerifyTargetRunsIntegrityCheck(t *testing.T) {
	stagingDir := t.TempDir()
	runner := &recordingPgRunner{stdout: "pg_restore: restoring data for table users\n", listOutput: ";\n; PostgreSQL database dump\n;\n"}
	backend := NewPgBackend(withPgCommandRunner(runner), WithPgStagingDir(stagingDir))
	repo := pgRepositoryFixture()
	recipe := pgRecipeFixture(repo.ID)
	sourceRun := pgSucceededSourceRunFixture(recipe)
	restoreRun := pgRestoreRunFixture(sourceRun)
	restoreRun.RestoreTargetRef = "verify:"

	dumpFile := filepath.Join(stagingDir, sourceRun.ID.String()+".dump")
	os.WriteFile(dumpFile, []byte("dump content"), 0600)

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.NoError(t, err)
	require.True(t, result.Verified)
}

type recordingPgRunner struct {
	stdout     string
	stderr     string
	err        error
	calls      []pgRunnerCall
	createFile bool
	listOutput string
}

type pgRunnerCall struct {
	binary   string
	args     []string
	extraEnv []string
}

func (r *recordingPgRunner) Run(_ context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
	r.calls = append(r.calls, pgRunnerCall{binary: binary, args: append([]string(nil), args...), extraEnv: append([]string(nil), extraEnv...)})
	// Create the dump file if requested (simulates pg_dump writing output)
	if r.createFile {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--file=") {
				filePath := arg[7:]
				if strings.HasSuffix(filePath, ".tmp") {
					os.WriteFile(filePath, []byte("dump content"), 0600)
				} else {
					os.WriteFile(filePath, []byte("dump content"), 0600)
				}
			}
		}
	}
	if len(r.calls) >= 2 && r.listOutput != "" {
		return r.listOutput, "", nil
	}
	if len(r.calls) >= 2 {
		return r.stdout, r.stderr, r.err
	}
	return r.stdout, r.stderr, r.err
}

func pgRepositoryFixture() *domain.BackupRepository {
	return &domain.BackupRepository{
		ID:                uuid.New(),
		Name:              "pg-primary",
		Backend:           domain.BackupBackendPgDump,
		RepositoryURI:     "postgres://user@localhost:5432/testdb",
		CredentialProfile: "",
		Metadata:          map[string]any{},
	}
}

func pgRecipeFixture(repoID uuid.UUID) *domain.BackupRecipe {
	return &domain.BackupRecipe{
		ID: uuid.New(), Name: "pg-daily", Version: "v1",
		Backend:          domain.BackupBackendPgDump,
		RepositoryID:     repoID,
		TargetRef:        "verify:",
		VerificationMode: domain.BackupVerificationPgDumpVerify,
	}
}

func pgRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	return &domain.BackupRun{
		ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: recipe.RepositoryID,
		RequestedBy: "pubkey", RequestEventID: "event", RequestKind: 38400, RequestDTag: "pg-daily",
		Status: domain.RunStatusQueued, Backend: domain.BackupBackendPgDump,
		TargetRef: recipe.TargetRef, VerificationStatus: domain.BackupVerificationPending,
	}
}

func pgSucceededSourceRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	run := pgRunFixture(recipe)
	run.Status = domain.RunStatusSucceeded
	run.SnapshotCreated = true
	run.SnapshotID = run.ID.String()
	run.VerificationStatus = domain.BackupVerificationSucceeded
	run.Metadata = map[string]any{}
	return run
}

func pgRestoreRunFixture(source *domain.BackupRun) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{
		ID: uuid.New(), BackupRunID: source.ID,
		RecipeID: source.RecipeID, RepositoryID: source.RepositoryID,
		SnapshotID: source.SnapshotID, RestoreTargetRef: "postgres://user@localhost:5433/restoredb",
		RequestedBy: "pubkey", RequestEventID: "restore-event", RequestKind: 38402,
		RequestDTag: "restore:pg-daily", ApprovalStatus: domain.BackupApprovalNotRequired,
		Status: domain.RunStatusQueued, Backend: domain.BackupBackendPgDump,
		VerificationStatus: domain.BackupVerificationPending,
	}
}
