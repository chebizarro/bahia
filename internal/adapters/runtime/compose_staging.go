package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Staging layout constants
// ---------------------------------------------------------------------------

// bahiaStagingDir is the subdirectory under .bahia/ where staged files are
// written before validation and promotion.
const bahiaStagingDir = "staging"

// bahiaEnvDir is the subdirectory under .bahia/ for generated .env files.
const bahiaEnvDir = "env"

// composeFileName is the canonical generated Compose file name.
const composeFileName = "docker-compose.yml"

// ---------------------------------------------------------------------------
// StagedFiles — bookkeeping for staged output
// ---------------------------------------------------------------------------

// StagedFiles tracks the files written to the staging area, their intended
// live destinations, and the compose directory context.
type StagedFiles struct {
	// ComposeDir is the Bahia-owned compose project directory.
	ComposeDir string

	// StagingDir is the absolute path to the staging directory (.bahia/staging/).
	StagingDir string

	// ComposeFile is the staged docker-compose.yml path.
	ComposeFile string

	// EnvFiles maps service keys to their staged .env file paths.
	EnvFiles map[string]string

	// MetadataFile is the staged render-state.json path.
	MetadataFile string

	// LiveComposeFile is the target live docker-compose.yml path.
	LiveComposeFile string

	// LiveEnvFiles maps service keys to their target live .env file paths.
	LiveEnvFiles map[string]string

	// LiveMetadataFile is the target live render-state.json path.
	LiveMetadataFile string

	// Validated records whether docker compose config -q succeeded.
	Validated bool

	// StagedAt records when the staging was performed.
	StagedAt time.Time
}

// ---------------------------------------------------------------------------
// ComposeStagingManager — staged validation and atomic promotion
// ---------------------------------------------------------------------------

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	// RunCommand executes a command and returns combined stdout, stderr, and error.
	RunCommand(ctx context.Context, name string, args []string, dir string, env []string) (stdout string, stderr string, err error)
}

// execCommandRunner is the production CommandRunner that uses os/exec.
type execCommandRunner struct{}

func (r *execCommandRunner) RunCommand(ctx context.Context, name string, args []string, dir string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ComposeStagingManager handles staging rendered Compose output, validating
// it with docker compose config, and atomically promoting it to live files.
type ComposeStagingManager struct {
	logger *zap.Logger
	runner CommandRunner
}

// NewComposeStagingManager creates a new staging manager.
func NewComposeStagingManager(logger *zap.Logger) *ComposeStagingManager {
	return &ComposeStagingManager{
		logger: logger,
		runner: &execCommandRunner{},
	}
}

// NewComposeStagingManagerWithRunner creates a staging manager with a custom
// command runner, useful for testing without Docker.
func NewComposeStagingManagerWithRunner(logger *zap.Logger, runner CommandRunner) *ComposeStagingManager {
	return &ComposeStagingManager{
		logger: logger,
		runner: runner,
	}
}

// ---------------------------------------------------------------------------
// StageAndValidate — write staged files and validate with docker compose
// ---------------------------------------------------------------------------

// StageAndValidate writes the rendered Compose output to a staging directory
// under .bahia/staging/ and validates it with `docker compose config -q`.
//
// On success, the returned StagedFiles has Validated=true and is ready for
// Promote(). On validation failure, an error is returned and the caller
// should call Rollback() to clean up.
func (m *ComposeStagingManager) StageAndValidate(ctx context.Context, composeDir string, result *RenderResult) (*StagedFiles, error) {
	if result == nil {
		return nil, fmt.Errorf("compose staging: render result is nil")
	}

	composeDir, err := filepath.Abs(composeDir)
	if err != nil {
		return nil, fmt.Errorf("compose staging: resolve compose dir: %w", err)
	}

	staged := &StagedFiles{
		ComposeDir:       composeDir,
		StagingDir:       filepath.Join(composeDir, bahiaMarkerDir, bahiaStagingDir),
		LiveComposeFile:  filepath.Join(composeDir, composeFileName),
		LiveMetadataFile: filepath.Join(composeDir, bahiaMarkerDir, bahiaRenderStateFile),
		LiveEnvFiles:     make(map[string]string),
		EnvFiles:         make(map[string]string),
		StagedAt:         time.Now().UTC(),
	}

	// Ensure staging directory exists (clean slate).
	if err := os.RemoveAll(staged.StagingDir); err != nil {
		return nil, fmt.Errorf("compose staging: clean staging dir: %w", err)
	}
	if err := os.MkdirAll(staged.StagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("compose staging: create staging dir: %w", err)
	}

	// Stage docker-compose.yml.
	staged.ComposeFile = filepath.Join(staged.StagingDir, composeFileName)
	if err := os.WriteFile(staged.ComposeFile, result.ComposeYAML, 0o644); err != nil {
		return staged, fmt.Errorf("compose staging: write compose file: %w", err)
	}

	// Stage env files.
	stagingEnvDir := filepath.Join(staged.StagingDir, bahiaEnvDir)
	if len(result.EnvMaterial) > 0 {
		if err := os.MkdirAll(stagingEnvDir, 0o755); err != nil {
			return staged, fmt.Errorf("compose staging: create env dir: %w", err)
		}
	}
	for svcKey, content := range result.EnvMaterial {
		envFileName := svcKey + ".env"
		stagedPath := filepath.Join(stagingEnvDir, envFileName)
		// Env files may contain secrets — restrict permissions.
		if err := os.WriteFile(stagedPath, []byte(content), 0o600); err != nil {
			return staged, fmt.Errorf("compose staging: write env file %s: %w", svcKey, err)
		}
		staged.EnvFiles[svcKey] = stagedPath
		staged.LiveEnvFiles[svcKey] = filepath.Join(composeDir, bahiaMarkerDir, bahiaEnvDir, envFileName)
	}

	// Stage render-state.json.
	metadataJSON, err := result.Metadata.MetadataJSON()
	if err != nil {
		return staged, fmt.Errorf("compose staging: marshal metadata: %w", err)
	}
	staged.MetadataFile = filepath.Join(staged.StagingDir, bahiaRenderStateFile)
	if err := os.WriteFile(staged.MetadataFile, metadataJSON, 0o644); err != nil {
		return staged, fmt.Errorf("compose staging: write metadata: %w", err)
	}

	// Validate with docker compose config -q.
	if err := m.validate(ctx, staged); err != nil {
		return staged, fmt.Errorf("compose staging: validation failed: %w", err)
	}

	staged.Validated = true
	m.logger.Info("compose staging: validated successfully",
		zap.String("compose_dir", composeDir),
		zap.String("staging_dir", staged.StagingDir),
		zap.Int("env_files", len(staged.EnvFiles)),
	)

	return staged, nil
}

// validate runs `docker compose config -q` against the staged compose file.
func (m *ComposeStagingManager) validate(ctx context.Context, staged *StagedFiles) error {
	// Build compose config command targeting the staged file.
	args := []string{"compose", "-f", staged.ComposeFile, "config", "-q"}

	stdout, stderr, err := m.runner.RunCommand(ctx, "docker", args, staged.StagingDir, nil)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}

	return nil
}

// ---------------------------------------------------------------------------
// Promote — atomic move staged → live
// ---------------------------------------------------------------------------

// Promote atomically replaces live generated files with validated staged files.
// It requires that StageAndValidate was called successfully (Validated=true).
//
// The promotion strategy:
//  1. Ensure live directories exist (.bahia/, .bahia/env/)
//  2. Rename staged files to live locations (atomic on same filesystem)
//  3. Clean up the staging directory
//
// If any step fails, previously promoted files remain in place (best-effort
// atomicity). The caller should inspect errors and may call Rollback() if
// partial promotion is unacceptable.
func (m *ComposeStagingManager) Promote(ctx context.Context, staged *StagedFiles) error {
	if staged == nil {
		return fmt.Errorf("compose promote: staged files is nil")
	}
	if !staged.Validated {
		return fmt.Errorf("compose promote: staged files have not been validated")
	}

	bahiaDir := filepath.Join(staged.ComposeDir, bahiaMarkerDir)
	liveEnvDir := filepath.Join(bahiaDir, bahiaEnvDir)

	// Ensure live directories exist.
	if err := os.MkdirAll(bahiaDir, 0o755); err != nil {
		return fmt.Errorf("compose promote: create .bahia dir: %w", err)
	}
	if len(staged.EnvFiles) > 0 {
		if err := os.MkdirAll(liveEnvDir, 0o755); err != nil {
			return fmt.Errorf("compose promote: create env dir: %w", err)
		}
	}

	// Promote docker-compose.yml: staged → live.
	if err := atomicReplaceFile(staged.ComposeFile, staged.LiveComposeFile); err != nil {
		return fmt.Errorf("compose promote: compose file: %w", err)
	}

	// Promote env files: staged → live.
	for svcKey, stagedPath := range staged.EnvFiles {
		livePath := staged.LiveEnvFiles[svcKey]
		if err := atomicReplaceFile(stagedPath, livePath); err != nil {
			return fmt.Errorf("compose promote: env file %s: %w", svcKey, err)
		}
	}

	// Promote render-state.json: staged → live.
	if err := atomicReplaceFile(staged.MetadataFile, staged.LiveMetadataFile); err != nil {
		return fmt.Errorf("compose promote: metadata file: %w", err)
	}

	// Clean up staging directory.
	if err := os.RemoveAll(staged.StagingDir); err != nil {
		m.logger.Warn("compose promote: failed to clean staging dir",
			zap.String("staging_dir", staged.StagingDir),
			zap.Error(err),
		)
		// Non-fatal: live files are already in place.
	}

	m.logger.Info("compose promote: live files replaced",
		zap.String("compose_dir", staged.ComposeDir),
		zap.String("compose_file", staged.LiveComposeFile),
		zap.Int("env_files", len(staged.EnvFiles)),
	)

	return nil
}

// atomicReplaceFile replaces dst with src using rename (atomic on same
// filesystem). It ensures the destination directory exists.
func atomicReplaceFile(src, dst string) error {
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	return os.Rename(src, dst)
}

// ---------------------------------------------------------------------------
// Rollback — cleanup staging on failure
// ---------------------------------------------------------------------------

// Rollback removes the staging directory and all staged files. It is safe
// to call even if staging was only partially completed. Errors are logged
// but not returned since rollback is best-effort cleanup.
func (m *ComposeStagingManager) Rollback(_ context.Context, staged *StagedFiles) {
	if staged == nil || staged.StagingDir == "" {
		return
	}

	if err := os.RemoveAll(staged.StagingDir); err != nil {
		m.logger.Warn("compose rollback: failed to remove staging dir",
			zap.String("staging_dir", staged.StagingDir),
			zap.Error(err),
		)
		return
	}

	m.logger.Info("compose rollback: staging directory cleaned up",
		zap.String("staging_dir", staged.StagingDir),
	)
}

// ---------------------------------------------------------------------------
// Validation error type
// ---------------------------------------------------------------------------

// ComposeValidationError wraps a docker compose config validation failure
// with structured detail for logging and event reporting.
type ComposeValidationError struct {
	// StagedFile is the path to the staged compose file that failed validation.
	StagedFile string
	// Detail is the stderr/stdout from docker compose config -q.
	Detail string
	// Err is the underlying exec error.
	Err error
}

func (e *ComposeValidationError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("compose validation failed for %s: %s", e.StagedFile, e.Detail)
	}
	return fmt.Sprintf("compose validation failed for %s: %v", e.StagedFile, e.Err)
}

func (e *ComposeValidationError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readFileString reads a file and returns its content as a string.
// Used in tests and validation paths.
func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// isValidJSON checks if data is valid JSON (used in staging validation).
func isValidJSON(data []byte) bool {
	var v json.RawMessage
	return json.Unmarshal(data, &v) == nil
}
