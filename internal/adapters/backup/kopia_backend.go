package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const (
	defaultKopiaBinary             = "kopia"
	defaultKopiaVerifyFilesPercent = 100
)

// KopiaBackendConfig configures the real Kopia CLI adapter.
type KopiaBackendConfig struct {
	BinaryPath                string
	DefaultVerifyFilesPercent float64
	Parallelism               int
	FileParallelism           int
}

// KopiaBackend executes backup work by invoking the real Kopia CLI.
type KopiaBackend struct {
	config KopiaBackendConfig
	runner kopiaCommandRunner
}

type KopiaBackendOption func(*KopiaBackend)

func WithKopiaBinary(path string) KopiaBackendOption {
	return func(b *KopiaBackend) { b.config.BinaryPath = strings.TrimSpace(path) }
}

func WithKopiaVerifyFilesPercent(percent float64) KopiaBackendOption {
	return func(b *KopiaBackend) {
		if percent > 0 {
			b.config.DefaultVerifyFilesPercent = percent
		}
	}
}

func WithKopiaParallelism(parallelism int) KopiaBackendOption {
	return func(b *KopiaBackend) {
		if parallelism > 0 {
			b.config.Parallelism = parallelism
		}
	}
}

func WithKopiaFileParallelism(parallelism int) KopiaBackendOption {
	return func(b *KopiaBackend) {
		if parallelism > 0 {
			b.config.FileParallelism = parallelism
		}
	}
}

func withKopiaCommandRunner(runner kopiaCommandRunner) KopiaBackendOption {
	return func(b *KopiaBackend) { b.runner = runner }
}

func NewKopiaBackend(opts ...KopiaBackendOption) *KopiaBackend {
	b := &KopiaBackend{
		config: KopiaBackendConfig{
			BinaryPath:                defaultKopiaBinary,
			DefaultVerifyFilesPercent: defaultKopiaVerifyFilesPercent,
		},
		runner: defaultKopiaCommandRunner{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *KopiaBackend) BackendKind() domain.BackupBackendKind { return domain.BackupBackendKopia }

func (b *KopiaBackend) Health(ctx context.Context, repo *domain.BackupRepository) error {
	cfg, err := b.commandConfig(repo)
	if err != nil {
		return err
	}
	args := append(cfg.baseArgs(), "repository", "status", "--json")
	_, _, err = b.run(ctx, cfg, args...)
	if err != nil {
		return err
	}
	return nil
}

func (b *KopiaBackend) CreateSnapshot(ctx context.Context, req service.BackupSnapshotRequest) (*service.BackupSnapshotResult, error) {
	if req.Repository == nil || req.Recipe == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: backup snapshot request requires repository, recipe, and run", service.ErrBackupBackendConfiguration)
	}
	cfg, err := b.commandConfig(req.Repository)
	if err != nil {
		return nil, err
	}
	source, err := kopiaSourcePath(req.Recipe.TargetRef)
	if err != nil {
		return nil, err
	}
	if err := validateRecipePathScope(req.Recipe); err != nil {
		return nil, err
	}
	args := append(cfg.baseArgs(), "snapshot", "create", "--json", source)
	stdout, stderr, err := b.run(ctx, cfg, args...)
	if err != nil {
		return nil, err
	}
	snapshotID, evidence, err := parseKopiaSnapshotCreate(stdout)
	if err != nil {
		return nil, err
	}
	evidence["repository_uri"] = req.Repository.RepositoryURI
	evidence["source"] = source
	if strings.TrimSpace(stderr) != "" {
		evidence["stderr"] = strings.TrimSpace(stderr)
	}
	return &service.BackupSnapshotResult{SnapshotID: snapshotID, Evidence: evidence}, nil
}

func (b *KopiaBackend) VerifySnapshot(ctx context.Context, req service.BackupVerifyRequest) (*service.BackupVerifyResult, error) {
	if req.Repository == nil || req.Run == nil {
		return nil, fmt.Errorf("%w: backup verification request requires repository and run", service.ErrBackupBackendConfiguration)
	}
	if req.Mode != domain.BackupVerificationKopiaSnapshotVerify {
		return nil, fmt.Errorf("%w: Kopia backend cannot execute verification mode %q", service.ErrBackupBackendUnsupported, req.Mode)
	}
	snapshotID := strings.TrimSpace(req.SnapshotID)
	if snapshotID == "" {
		return nil, fmt.Errorf("%w: snapshot id is required for Kopia verification", service.ErrBackupBackendConfiguration)
	}
	cfg, err := b.commandConfig(req.Repository)
	if err != nil {
		return nil, err
	}
	percent := req.VerifyFilesPercent
	if percent <= 0 {
		percent = b.config.DefaultVerifyFilesPercent
	}
	if percent <= 0 || percent > 100 {
		return nil, fmt.Errorf("%w: verify files percent %.2f must be within (0,100]", service.ErrBackupBackendConfiguration, percent)
	}
	args := append(cfg.baseArgs(), "snapshot", "verify", "--json", "--verify-files-percent="+formatPercent(percent))
	if b.config.FileParallelism > 0 {
		args = append(args, "--file-parallelism="+strconv.Itoa(b.config.FileParallelism))
	}
	if b.config.Parallelism > 0 {
		args = append(args, "--parallel="+strconv.Itoa(b.config.Parallelism))
	}
	args = append(args, snapshotID)
	stdout, stderr, err := b.run(ctx, cfg, args...)
	evidence := map[string]any{"snapshot_id": snapshotID, "verify_files_percent": percent}
	if strings.TrimSpace(stderr) != "" {
		evidence["stderr"] = strings.TrimSpace(stderr)
	}
	if err != nil {
		if strings.TrimSpace(stdout) != "" {
			evidence["stdout"] = strings.TrimSpace(stdout)
		}
		return &service.BackupVerifyResult{Verified: false, Status: domain.BackupVerificationFailed, Evidence: evidence, Error: err.Error()}, err
	}
	verifiedEvidence, err := parseKopiaSnapshotVerify(stdout)
	if err != nil {
		status := domain.BackupVerificationUnsupported
		if errors.Is(err, service.ErrBackupBackendExecution) {
			status = domain.BackupVerificationFailed
		}
		return &service.BackupVerifyResult{Verified: false, Status: status, Evidence: evidence, Error: err.Error()}, err
	}
	for key, value := range verifiedEvidence {
		evidence[key] = value
	}
	return &service.BackupVerifyResult{Verified: true, Status: domain.BackupVerificationSucceeded, Evidence: evidence}, nil
}

func (b *KopiaBackend) commandConfig(repo *domain.BackupRepository) (*kopiaCommandConfig, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: Kopia backend is not configured", service.ErrBackupBackendConfiguration)
	}
	if repo == nil {
		return nil, fmt.Errorf("%w: backup repository is required", service.ErrBackupBackendConfiguration)
	}
	if repo.Backend != domain.BackupBackendKopia {
		return nil, fmt.Errorf("%w: repository backend %q is not Kopia", service.ErrBackupBackendUnsupported, repo.Backend)
	}
	binary := strings.TrimSpace(b.config.BinaryPath)
	if binary == "" {
		return nil, fmt.Errorf("%w: Kopia binary path is required", service.ErrBackupBackendConfiguration)
	}
	configFile := stringMetadata(repo.Metadata, "kopia_config_file")
	if configFile == "" {
		configFile = stringMetadata(repo.Metadata, "config_file")
	}
	if configFile == "" {
		configFile = strings.TrimSpace(repo.CredentialProfile)
	}
	if configFile == "" {
		return nil, fmt.Errorf("%w: Kopia repository config file must be set in metadata.kopia_config_file or credential_profile", service.ErrBackupBackendConfiguration)
	}
	if !filepath.IsAbs(configFile) {
		return nil, fmt.Errorf("%w: Kopia config file path must be absolute", service.ErrBackupBackendConfiguration)
	}
	passwordEnvName := stringMetadata(repo.Metadata, "kopia_password_env")
	if passwordEnvName == "" {
		passwordEnvName = stringMetadata(repo.Metadata, "password_env")
	}
	extraEnv := make([]string, 0, 1)
	if passwordEnvName != "" {
		password, ok := os.LookupEnv(passwordEnvName)
		if !ok || password == "" {
			return nil, fmt.Errorf("%w: environment variable %s for Kopia repository password is not set", service.ErrBackupBackendConfiguration, passwordEnvName)
		}
		extraEnv = append(extraEnv, "KOPIA_PASSWORD="+password)
	}
	return &kopiaCommandConfig{binary: binary, configFile: configFile, extraEnv: extraEnv}, nil
}

func (b *KopiaBackend) run(ctx context.Context, cfg *kopiaCommandConfig, args ...string) (string, string, error) {
	if b.runner == nil {
		return "", "", fmt.Errorf("%w: Kopia command runner is not configured", service.ErrBackupBackendConfiguration)
	}
	stdout, stderr, err := b.runner.Run(ctx, cfg.binary, args, cfg.extraEnv)
	if err == nil {
		return stdout, stderr, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return stdout, stderr, fmt.Errorf("%w: Kopia binary %q was not found", service.ErrBackupBackendConfiguration, cfg.binary)
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
	return stdout, stderr, fmt.Errorf("%w: Kopia command failed: %s", service.ErrBackupBackendExecution, message)
}

type kopiaCommandConfig struct {
	binary     string
	configFile string
	extraEnv   []string
}

func (c *kopiaCommandConfig) baseArgs() []string {
	return []string{"--config-file", c.configFile}
}

type kopiaCommandRunner interface {
	Run(ctx context.Context, binary string, args []string, extraEnv []string) (stdout string, stderr string, err error)
}

type defaultKopiaCommandRunner struct{}

func (defaultKopiaCommandRunner) Run(ctx context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
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

func kopiaSourcePath(targetRef string) (string, error) {
	targetRef = strings.TrimSpace(targetRef)
	for _, prefix := range []string{"fs:", "file:", "filesystem:"} {
		if strings.HasPrefix(targetRef, prefix) {
			path := strings.TrimSpace(strings.TrimPrefix(targetRef, prefix))
			if path == "" {
				return "", fmt.Errorf("%w: filesystem backup target path is empty", service.ErrBackupBackendConfiguration)
			}
			if !filepath.IsAbs(path) {
				return "", fmt.Errorf("%w: filesystem backup target path must be absolute", service.ErrBackupBackendConfiguration)
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: Kopia first-slice snapshot targets must use fs:, file:, or filesystem: target_ref", service.ErrBackupBackendUnsupported)
}

func validateRecipePathScope(recipe *domain.BackupRecipe) error {
	if recipe == nil {
		return fmt.Errorf("%w: backup recipe is required", service.ErrBackupBackendConfiguration)
	}
	if len(recipe.Include) > 0 || len(recipe.Exclude) > 0 {
		return fmt.Errorf("%w: Kopia adapter first slice requires include/exclude policy to be configured in Kopia, not Bahia recipe paths", service.ErrBackupBackendUnsupported)
	}
	return nil
}

func parseKopiaSnapshotCreate(stdout string) (string, map[string]any, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return "", nil, fmt.Errorf("%w: Kopia snapshot create returned empty JSON output", service.ErrBackupBackendExecution)
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return "", nil, fmt.Errorf("%w: parsing Kopia snapshot create JSON: %v", service.ErrBackupBackendExecution, err)
	}
	snapshotID := topLevelSnapshotID(payload)
	if snapshotID == "" {
		return "", nil, fmt.Errorf("%w: Kopia snapshot create JSON did not include a top-level snapshot id", service.ErrBackupBackendUnsupported)
	}
	evidence := map[string]any{"kopia_snapshot_create": payload}
	return snapshotID, evidence, nil
}

func topLevelSnapshotID(v any) string {
	switch value := v.(type) {
	case map[string]any:
		return snapshotIDFromMap(value)
	case []any:
		if len(value) != 1 {
			return ""
		}
		m, ok := value[0].(map[string]any)
		if !ok {
			return ""
		}
		return snapshotIDFromMap(m)
	default:
		return ""
	}
}

func snapshotIDFromMap(value map[string]any) string {
	for _, key := range []string{"snapshotID", "snapshot_id", "manifestID", "manifest_id", "id"} {
		if s, ok := value[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func parseKopiaSnapshotVerify(stdout string) (map[string]any, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return nil, fmt.Errorf("%w: Kopia snapshot verify returned empty JSON output", service.ErrBackupBackendUnsupported)
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return nil, fmt.Errorf("%w: parsing Kopia snapshot verify JSON: %v", service.ErrBackupBackendExecution, err)
	}
	status, ok := explicitVerifyStatus(payload)
	if !ok {
		return nil, fmt.Errorf("%w: Kopia snapshot verify JSON did not include an explicit successful verification status", service.ErrBackupBackendUnsupported)
	}
	if !status {
		return nil, fmt.Errorf("%w: Kopia snapshot verify JSON reported verification failure", service.ErrBackupBackendExecution)
	}
	return map[string]any{"kopia_snapshot_verify": payload}, nil
}

func explicitVerifyStatus(v any) (bool, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return false, false
	}
	for _, key := range []string{"verified", "success", "succeeded", "ok"} {
		if value, ok := m[key].(bool); ok {
			return value, true
		}
	}
	if raw, ok := m["status"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "succeeded", "success", "verified", "passed", "pass", "ok":
			return true, true
		case "failed", "failure", "unverified", "error", "errors":
			return false, true
		}
	}
	return false, false
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch v := metadata[key].(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func formatPercent(percent float64) string {
	if percent == float64(int64(percent)) {
		return strconv.FormatInt(int64(percent), 10)
	}
	return strconv.FormatFloat(percent, 'f', -1, 64)
}

func contextCancellationErr(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}
