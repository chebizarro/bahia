package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	defaultPgDumpBinary    = "pg_dump"
	defaultPgRestoreBinary = "pg_restore"
)

type PgBackendConfig struct {
	PgDumpBinary    string
	PgRestoreBinary string
	StagingDir      string
}

type PgBackend struct {
	config PgBackendConfig
	runner pgCommandRunner
}

type PgBackendOption func(*PgBackend)

func WithPgDumpBinary(path string) PgBackendOption {
	return func(b *PgBackend) { b.config.PgDumpBinary = strings.TrimSpace(path) }
}

func WithPgRestoreBinary(path string) PgBackendOption {
	return func(b *PgBackend) { b.config.PgRestoreBinary = strings.TrimSpace(path) }
}

func WithPgStagingDir(dir string) PgBackendOption {
	return func(b *PgBackend) { b.config.StagingDir = strings.TrimSpace(dir) }
}

func withPgCommandRunner(runner pgCommandRunner) PgBackendOption {
	return func(b *PgBackend) { b.runner = runner }
}

func NewPgBackend(opts ...PgBackendOption) *PgBackend {
	b := &PgBackend{
		config: PgBackendConfig{
			PgDumpBinary:    defaultPgDumpBinary,
			PgRestoreBinary: defaultPgRestoreBinary,
		},
		runner: defaultPgCommandRunner{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *PgBackend) BackendKind() domain.BackupBackendKind { return domain.BackupBackendPgDump }

func (b *PgBackend) Capabilities() service.BackendCapabilities {
	return service.BackendCapabilities{
		SnapshotCreate: true,
		SnapshotVerify: true,
		Restore:        true,
		Retention:      false,
		Probe:          true,
	}
}



func (b *PgBackend) run(ctx context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
	if b.runner == nil {
		return "", "", fmt.Errorf("%w: pg command runner is not configured", service.ErrBackupBackendConfiguration)
	}
	stdout, stderr, err := b.runner.Run(ctx, binary, args, extraEnv)
	if err == nil {
		return stdout, stderr, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return stdout, stderr, fmt.Errorf("%w: binary %q was not found", service.ErrBackupBackendConfiguration, binary)
	}
	if contextCancellationErr(ctx, err) {
		return stdout, stderr, err
	}
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = strings.TrimSpace(stdout)
	}
	if message == "" {
		message = err.Error()
	}
	return stdout, stderr, fmt.Errorf("%w: command failed: %s", service.ErrBackupBackendExecution, message)
}

func (b *PgBackend) Health(ctx context.Context, repo *domain.BackupRepository) error {
	versionArgs := []string{"--version"}
	stdout, _, err := b.run(ctx, b.config.PgDumpBinary, versionArgs, nil)
	if err != nil {
		return fmt.Errorf("%w: pg_dump health check failed: %v", service.ErrBackupBackendExecution, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "pg_dump") {
		return fmt.Errorf("%w: pg_dump binary returned unexpected output: %s", service.ErrBackupBackendExecution, strings.TrimSpace(stdout))
	}
	return nil
}

func (b *PgBackend) CreateSnapshot(ctx context.Context, req service.BackupSnapshotRequest) (*service.BackupSnapshotResult, error) {
	if req.Repository == nil || req.Recipe == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: pg_dump snapshot request requires repository, recipe, and run", service.ErrBackupBackendConfiguration)
	}
	dsn, err := pgDSN(req.Repository)
	if err != nil {
		return nil, err
	}
	stagingDir := b.stagingDir(req.Repository)
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return nil, fmt.Errorf("%w: creating pg_dump staging directory: %w", service.ErrBackupBackendConfiguration, err)
	}
	dumpFile := filepath.Join(stagingDir, req.Run.ID.String()+".dump")
	tmpFile := dumpFile + ".tmp"
	args := []string{
		"--dbname=" + dsn,
		"--format=custom",
		"--no-password",
		"--file=" + tmpFile,
	}
	stdout, stderr, err := b.run(ctx, b.config.PgDumpBinary, args, pgEnv(req.Repository))
	if err != nil {
		os.Remove(tmpFile)
		return nil, fmt.Errorf("%w: pg_dump failed: %v", service.ErrBackupBackendExecution, err)
	}
	if err := os.Rename(tmpFile, dumpFile); err != nil {
		os.Remove(tmpFile)
		return nil, fmt.Errorf("%w: atomic rename of pg_dump output failed: %w", service.ErrBackupBackendConfiguration, err)
	}
	info, err := os.Stat(dumpFile)
	if err != nil {
		return nil, fmt.Errorf("%w: stat pg_dump output: %w", service.ErrBackupBackendConfiguration, err)
	}
	evidence := map[string]any{
		"pg_dump": map[string]any{
			"dump_file":    dumpFile,
			"dump_size":    info.Size(),
			"database_url": redactDSN(dsn),
		},
	}
	captureStdStream(evidence, "stdout", stdout)
	captureStdStream(evidence, "stderr", stderr)
	return &service.BackupSnapshotResult{SnapshotID: req.Run.ID.String(), Evidence: evidence}, nil
}

func (b *PgBackend) VerifySnapshot(ctx context.Context, req service.BackupVerifyRequest) (*service.BackupVerifyResult, error) {
	if req.Repository == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: pg_dump verification request requires repository and run", service.ErrBackupBackendConfiguration)
	}
	if req.Mode != domain.BackupVerificationPgDumpVerify {
		return nil, fmt.Errorf("%w: pg_dump backend cannot execute verification mode %q", service.ErrBackupBackendUnsupported, req.Mode)
	}
	dumpFile := b.locateDumpFile(req)
	if dumpFile == "" {
		return nil, fmt.Errorf("%w: pg_dump dump file not found for snapshot %q", service.ErrBackupBackendConfiguration, req.SnapshotID)
	}
	if _, err := os.Stat(dumpFile); err != nil {
		return nil, fmt.Errorf("%w: pg_dump dump file %q is not accessible: %w", service.ErrBackupBackendConfiguration, dumpFile, err)
	}
	args := []string{"--list", dumpFile}
	stdout, stderr, err := b.run(ctx, b.config.PgRestoreBinary, args, nil)
	evidence := map[string]any{
		"pg_restore_list": map[string]any{
			"dump_file": dumpFile,
			"snapshot":  req.SnapshotID,
		},
	}
	captureStdStream(evidence, "list_stdout", stdout)
	captureStdStream(evidence, "list_stderr", stderr)
	if err != nil {
		return &service.BackupVerifyResult{Verified: false, Status: domain.BackupVerificationFailed, Evidence: evidence, Error: err.Error()}, fmt.Errorf("%w: pg_restore --list failed: %v", service.ErrBackupBackendExecution, err)
	}
	if strings.TrimSpace(stdout) == "" {
		return &service.BackupVerifyResult{Verified: false, Status: domain.BackupVerificationFailed, Evidence: evidence, Error: "pg_restore --list returned empty output"}, fmt.Errorf("%w: pg_restore --list returned empty output for dump file %q", service.ErrBackupBackendExecution, dumpFile)
	}
	evidence["pg_restore_list_size"] = len(strings.TrimSpace(stdout))
	return &service.BackupVerifyResult{Verified: true, Status: domain.BackupVerificationSucceeded, Evidence: evidence}, nil
}

func (b *PgBackend) Restore(ctx context.Context, req service.BackupRestoreRequest) (*service.BackupRestoreResult, error) {
	if req.Run == nil || req.SourceRun == nil || req.Repository == nil {
		return nil, fmt.Errorf("%w: pg_dump restore request requires restore run, source run, and repository", service.ErrBackupBackendConfiguration)
	}
	if req.Run.Backend != domain.BackupBackendPgDump || req.SourceRun.Backend != domain.BackupBackendPgDump {
		return nil, fmt.Errorf("%w: restore run and source run must use pg_dump backend", service.ErrBackupBackendUnsupported)
	}
	if req.Run.BackupRunID != req.SourceRun.ID {
		return nil, fmt.Errorf("%w: pg_dump restore run source mismatch: restore references backup_run_id %s but source run is %s", service.ErrBackupBackendConfiguration, req.Run.BackupRunID, req.SourceRun.ID)
	}
	if !domain.BackupRunRestoreEligible(req.SourceRun) {
		return nil, fmt.Errorf("%w: pg_dump restore requires a succeeded and verified source backup run", service.ErrBackupBackendConfiguration)
	}
	dumpFile := b.locateDumpFileFromRun(req.SourceRun)
	if dumpFile == "" {
		dumpFile = b.restoreDumpFile(req)
	}
	if dumpFile == "" {
		return nil, fmt.Errorf("%w: pg_dump dump file not found for restore, source run %q", service.ErrBackupBackendConfiguration, req.SourceRun.ID)
	}
	if _, err := os.Stat(dumpFile); err != nil {
		return nil, fmt.Errorf("%w: pg_dump dump file %q is not accessible: %w", service.ErrBackupBackendConfiguration, dumpFile, err)
	}
	targetDSN, err := pgRestoreTargetDSN(req)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--dbname=" + targetDSN,
		"--no-password",
		"--format=custom",
		"--clean",
		"--if-exists",
		"--exit-on-error",
		dumpFile,
	}
	stdout, stderr, err := b.run(ctx, b.config.PgRestoreBinary, args, pgEnv(req.Repository))
	evidence := map[string]any{
		"pg_restore": map[string]any{
			"dump_file":       dumpFile,
			"target_database": redactDSN(targetDSN),
			"source_run_id":   req.SourceRun.ID.String(),
		},
	}
	captureStdStream(evidence, "stdout", stdout)
	captureStdStream(evidence, "stderr", stderr)
	result := &service.BackupRestoreResult{Verified: false, VerificationStatus: domain.BackupVerificationSkipped, Evidence: evidence}
	if err != nil {
		result.Error = err.Error()
		result.VerificationStatus = domain.BackupVerificationFailed
		return result, fmt.Errorf("%w: pg_restore failed: %v", service.ErrBackupBackendExecution, err)
	}
	if strings.TrimSpace(req.Run.RestoreTargetRef) != "" && strings.HasPrefix(req.Run.RestoreTargetRef, "verify:") {
		listArgs := []string{"--list", dumpFile}
		listStdout, _, listErr := b.run(ctx, b.config.PgRestoreBinary, listArgs, nil)
		if listErr == nil && strings.TrimSpace(listStdout) != "" {
			result.Verified = true
			result.VerificationStatus = domain.BackupVerificationSucceeded
			evidence["restore_verified"] = true
			evidence["restore_verify_list"] = listStdout
		}
	}
	return result, nil
}

func (b *PgBackend) stagingDir(repo *domain.BackupRepository) string {
	if b.config.StagingDir != "" {
		return b.config.StagingDir
	}
	if repo != nil && repo.Metadata != nil {
		if dir, ok := repo.Metadata["pg_staging_dir"].(string); ok && strings.TrimSpace(dir) != "" {
			return strings.TrimSpace(dir)
		}
	}
	return filepath.Join(os.TempDir(), "bahia-pgdump")
}

func (b *PgBackend) locateDumpFile(req service.BackupVerifyRequest) string {
	if req.Run != nil && req.Run.Metadata != nil {
		if evidence, ok := req.Run.Metadata["snapshot_evidence"].(map[string]any); ok {
			if pgDump, ok := evidence["pg_dump"].(map[string]any); ok {
				if f, ok := pgDump["dump_file"].(string); ok && f != "" {
					return f
				}
			}
		}
	}
	stagingDir := b.stagingDir(req.Repository)
	candidate := filepath.Join(stagingDir, req.SnapshotID+".dump")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func (b *PgBackend) locateDumpFileFromRun(sourceRun *domain.BackupRun) string {
	if sourceRun == nil || sourceRun.Metadata == nil {
		return ""
	}
	if evidence, ok := sourceRun.Metadata["snapshot_evidence"].(map[string]any); ok {
		if pgDump, ok := evidence["pg_dump"].(map[string]any); ok {
			if f, ok := pgDump["dump_file"].(string); ok && f != "" {
				return f
			}
		}
	}
	return ""
}

func (b *PgBackend) restoreDumpFile(req service.BackupRestoreRequest) string {
	if req.SourceRun != nil && req.SourceRun.Metadata != nil {
		if evidence, ok := req.SourceRun.Metadata["snapshot_evidence"].(map[string]any); ok {
			if pgDump, ok := evidence["pg_dump"].(map[string]any); ok {
				if f, ok := pgDump["dump_file"].(string); ok && f != "" {
					return f
				}
			}
		}
	}
	repositoryConfig := req.Repository
	if repositoryConfig == nil {
		return ""
	}
	stagingDir := b.stagingDir(repositoryConfig)
	candidate := filepath.Join(stagingDir, req.SourceRun.ID.String()+".dump")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func pgDSN(repo *domain.BackupRepository) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("%w: backup repository is required", service.ErrBackupBackendConfiguration)
	}
	if strings.TrimSpace(repo.RepositoryURI) == "" {
		return "", fmt.Errorf("%w: backup repository URI must contain the PostgreSQL DSN", service.ErrBackupBackendConfiguration)
	}
	return strings.TrimSpace(repo.RepositoryURI), nil
}

func pgRestoreTargetDSN(req service.BackupRestoreRequest) (string, error) {
	if req.Repository == nil {
		return "", fmt.Errorf("%w: backup repository is required for restore", service.ErrBackupBackendConfiguration)
	}
	if req.Run != nil && strings.TrimSpace(req.Run.RestoreTargetRef) != "" {
		ref := strings.TrimSpace(req.Run.RestoreTargetRef)
		for _, prefix := range []string{"postgres:", "postgresql:", "pg:", "dsn:"} {
			if strings.HasPrefix(ref, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(ref, prefix)), nil
			}
		}
		if strings.HasPrefix(ref, "verify:") {
			dsn, err := pgDSN(req.Repository)
			if err != nil {
				return "", err
			}
			return dsn, nil
		}
		return ref, nil
	}
	return pgDSN(req.Repository)
}

func redactDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	// Handle URI-style DSN: postgres://user:password@host/db
	if strings.Contains(dsn, "://") {
		afterScheme := strings.Index(dsn, "://")
		rest := dsn[afterScheme+3:]
		atIdx := strings.Index(rest, "@")
		if atIdx > 0 {
			credentials := rest[:atIdx]
			colonIdx := strings.Index(credentials, ":")
			if colonIdx > 0 {
				user := credentials[:colonIdx]
				return dsn[:afterScheme+3] + user + ":*****" + rest[atIdx:]
			}
		}
		return dsn
	}
	// Handle key=value DSN
	redacted := dsn
	pwIdx := strings.Index(strings.ToLower(redacted), "password=")
	if pwIdx >= 0 {
		rest := redacted[pwIdx+9:]
		end := strings.IndexAny(rest, " \t\n")
		if end < 0 {
			end = len(rest)
		}
		redacted = redacted[:pwIdx+9] + strings.Repeat("*", end)
	}
	return redacted
}

func pgEnv(repo *domain.BackupRepository) []string {
	if repo == nil || repo.Metadata == nil {
		return nil
	}
	envVars := make([]string, 0, 4)
	if pgpass, ok := repo.Metadata["pgpass_file"].(string); ok && pgpass != "" {
		envVars = append(envVars, "PGPASSFILE="+pgpass)
	}
	if pgpassword, ok := repo.Metadata["pgpassword_envvar"].(string); ok && pgpassword != "" {
		if val, exists := os.LookupEnv(pgpassword); exists && val != "" {
			envVars = append(envVars, "PGPASSWORD="+val)
		}
	}
	if pgsslMode, ok := repo.Metadata["pgsslmode"].(string); ok && pgsslMode != "" {
		envVars = append(envVars, "PGSSLMODE="+pgsslMode)
	}
	if pgSSLKey, ok := repo.Metadata["pgsslkey"].(string); ok && pgSSLKey != "" {
		envVars = append(envVars, "PGSSLKEY="+pgSSLKey)
	}
	return envVars
}

type pgCommandRunner interface {
	Run(ctx context.Context, binary string, args []string, extraEnv []string) (stdout string, stderr string, err error)
}

type defaultPgCommandRunner struct{}

func (defaultPgCommandRunner) Run(ctx context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", "", exec.ErrNotFound
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func captureStdStream(evidence map[string]any, key string, output string) {
	if strings.TrimSpace(output) != "" {
		evidence[key] = strings.TrimSpace(output)
	}
}

var _ service.BackupBackend = (*PgBackend)(nil)
var _ service.BackupSnapshotCreateBackend = (*PgBackend)(nil)
var _ service.BackupSnapshotVerifyBackend = (*PgBackend)(nil)
var _ service.BackupRestoreBackend = (*PgBackend)(nil)


