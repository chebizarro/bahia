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
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const defaultVeleroBinary = "velero"

// VeleroBackendConfig configures the real Velero CLI adapter.
type VeleroBackendConfig struct {
	BinaryPath string
	Namespace  string
}

// VeleroBackend delegates Kubernetes backup restore and retention operations to the real Velero CLI.
type VeleroBackend struct {
	config VeleroBackendConfig
	runner veleroCommandRunner
}

type VeleroBackendOption func(*VeleroBackend)

func WithVeleroBinary(path string) VeleroBackendOption {
	return func(b *VeleroBackend) { b.config.BinaryPath = strings.TrimSpace(path) }
}

func WithVeleroNamespace(namespace string) VeleroBackendOption {
	return func(b *VeleroBackend) { b.config.Namespace = strings.TrimSpace(namespace) }
}

func withVeleroCommandRunner(runner veleroCommandRunner) VeleroBackendOption {
	return func(b *VeleroBackend) { b.runner = runner }
}

func NewVeleroBackend(opts ...VeleroBackendOption) *VeleroBackend {
	b := &VeleroBackend{
		config: VeleroBackendConfig{BinaryPath: defaultVeleroBinary},
		runner: defaultVeleroCommandRunner{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *VeleroBackend) BackendKind() domain.BackupBackendKind { return domain.BackupBackendVelero }

func (b *VeleroBackend) Health(ctx context.Context, repo *domain.BackupRepository) error {
	cfg, err := b.commandConfig(repo)
	if err != nil {
		return err
	}
	args := append(cfg.baseArgs(), "backup", "get", "-o", "json")
	_, _, err = b.run(ctx, cfg, args...)
	return err
}

func (b *VeleroBackend) Restore(ctx context.Context, req service.BackupRestoreRequest) (*service.BackupRestoreResult, error) {
	if req.Run == nil || req.SourceRun == nil || req.Repository == nil {
		return nil, fmt.Errorf("%w: Velero restore request requires restore run, source run, and repository", service.ErrBackupBackendConfiguration)
	}
	if req.Run.Backend != domain.BackupBackendVelero || req.SourceRun.Backend != domain.BackupBackendVelero {
		return nil, fmt.Errorf("%w: restore run and source run must use Velero backend", service.ErrBackupBackendUnsupported)
	}
	if req.Run.BackupRunID != req.SourceRun.ID {
		return nil, fmt.Errorf("%w: Velero restore run source mismatch: restore references backup_run_id %s but source run is %s", service.ErrBackupBackendConfiguration, req.Run.BackupRunID, req.SourceRun.ID)
	}
	if strings.TrimSpace(req.Run.SnapshotID) != "" && strings.TrimSpace(req.SourceRun.SnapshotID) != "" && strings.TrimSpace(req.Run.SnapshotID) != strings.TrimSpace(req.SourceRun.SnapshotID) {
		return nil, fmt.Errorf("%w: Velero restore snapshot_id %q does not match source backup snapshot_id %q", service.ErrBackupBackendConfiguration, req.Run.SnapshotID, req.SourceRun.SnapshotID)
	}
	if !domain.BackupRunRestoreEligible(req.SourceRun) {
		return nil, fmt.Errorf("%w: Velero restore requires a succeeded and verified source backup run", service.ErrBackupBackendConfiguration)
	}
	cfg, err := b.commandConfig(req.Repository)
	if err != nil {
		return nil, err
	}
	backupName := strings.TrimSpace(req.Run.SnapshotID)
	if backupName == "" {
		backupName = strings.TrimSpace(req.SourceRun.SnapshotID)
	}
	if backupName == "" {
		return nil, fmt.Errorf("%w: Velero restore requires snapshot_id to contain the Velero backup name", service.ErrBackupBackendConfiguration)
	}
	restoreName, err := veleroRestoreName(req.Run.RestoreTargetRef, req.Run.Metadata)
	if err != nil {
		return nil, err
	}
	createArgs := append(cfg.baseArgs(), "restore", "create", restoreName, "--from-backup", backupName, "--wait", "-o", "json")
	createStdout, createStderr, err := b.run(ctx, cfg, createArgs...)
	evidence := map[string]any{
		"velero_restore": map[string]any{
			"restore_name": restoreName,
			"backup_name":  backupName,
		},
	}
	captureCommandOutput(evidence, "create_stdout", createStdout)
	captureCommandOutput(evidence, "create_stderr", createStderr)
	result := &service.BackupRestoreResult{Verified: false, VerificationStatus: domain.BackupVerificationSkipped, Evidence: evidence}
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	getArgs := append(cfg.baseArgs(), "restore", "get", restoreName, "-o", "json")
	getStdout, getStderr, err := b.run(ctx, cfg, getArgs...)
	captureCommandOutput(evidence, "get_stderr", getStderr)
	if err != nil {
		captureCommandOutput(evidence, "get_stdout", getStdout)
		result.Error = err.Error()
		return result, err
	}
	phase, payload, err := parseVeleroPhase(getStdout)
	if err != nil {
		captureCommandOutput(evidence, "get_stdout", getStdout)
		result.Error = err.Error()
		return result, err
	}
	evidence["velero_restore_status"] = payload
	if !veleroPhaseSucceeded(phase) {
		err := fmt.Errorf("%w: Velero restore %q completed with phase %q", service.ErrBackupBackendExecution, restoreName, phase)
		result.Error = err.Error()
		return result, err
	}
	return result, nil
}

func (b *VeleroBackend) EnforceRetention(ctx context.Context, req service.BackupRetentionRequest) (*service.BackupRetentionResult, error) {
	if req.Run == nil || req.Repository == nil || req.Policy == nil {
		return nil, fmt.Errorf("%w: Velero retention request requires retention run, repository, and policy", service.ErrBackupBackendConfiguration)
	}
	if req.Run.Backend != domain.BackupBackendVelero {
		return nil, fmt.Errorf("%w: retention run backend %q is not Velero", service.ErrBackupBackendUnsupported, req.Run.Backend)
	}
	cfg, err := b.commandConfig(req.Repository)
	if err != nil {
		return nil, err
	}
	contract, err := service.ParseBackupPolicyRuntimeContract(req.Policy)
	if err != nil {
		return nil, err
	}
	if contract.RetentionMode != service.BackupRetentionModeBackendNative {
		return nil, fmt.Errorf("%w: Velero retention requires policy metadata.%s=backend_native", service.ErrBackupBackendConfiguration, service.BackupPolicyMetadataRetentionMode)
	}
	backupName, err := veleroRetentionBackupName(contract.RetentionSelector)
	if err != nil {
		return nil, err
	}
	evidence := map[string]any{
		"velero_retention": map[string]any{
			"backup_name": backupName,
			"dry_run":     req.Run.DryRun,
		},
	}
	var args []string
	if req.Run.DryRun {
		args = append(cfg.baseArgs(), "backup", "get", backupName, "-o", "json")
	} else {
		args = append(cfg.baseArgs(), "backup", "delete", backupName, "--confirm")
	}
	stdout, stderr, err := b.run(ctx, cfg, args...)
	captureCommandOutput(evidence, "stdout", stdout)
	captureCommandOutput(evidence, "stderr", stderr)
	result := &service.BackupRetentionResult{Evidence: evidence}
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	if req.Run.DryRun {
		phase, payload, err := parseVeleroPhase(stdout)
		if err != nil {
			result.Error = err.Error()
			return result, err
		}
		evidence["velero_backup_status"] = payload
		evidence["velero_backup_phase"] = phase
	}
	return result, nil
}

func (b *VeleroBackend) commandConfig(repo *domain.BackupRepository) (*veleroCommandConfig, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: Velero backend is not configured", service.ErrBackupBackendConfiguration)
	}
	if repo == nil {
		return nil, fmt.Errorf("%w: backup repository is required", service.ErrBackupBackendConfiguration)
	}
	if repo.Backend != domain.BackupBackendVelero {
		return nil, fmt.Errorf("%w: repository backend %q is not Velero", service.ErrBackupBackendUnsupported, repo.Backend)
	}
	binary := strings.TrimSpace(b.config.BinaryPath)
	if binary == "" {
		return nil, fmt.Errorf("%w: Velero binary path is required", service.ErrBackupBackendConfiguration)
	}
	namespace := stringMetadata(repo.Metadata, "velero_namespace")
	if namespace == "" {
		namespace = strings.TrimSpace(b.config.Namespace)
	}
	if namespace == "" {
		return nil, fmt.Errorf("%w: Velero namespace must be set in metadata.velero_namespace or adapter config", service.ErrBackupBackendConfiguration)
	}
	kubeconfig := stringMetadata(repo.Metadata, "velero_kubeconfig")
	if kubeconfig != "" && !filepath.IsAbs(kubeconfig) {
		return nil, fmt.Errorf("%w: Velero kubeconfig path must be absolute", service.ErrBackupBackendConfiguration)
	}
	kubeContext := stringMetadata(repo.Metadata, "velero_kube_context")
	if kubeContext == "" {
		kubeContext = stringMetadata(repo.Metadata, "velero_kubecontext")
	}
	return &veleroCommandConfig{binary: binary, namespace: namespace, kubeconfig: kubeconfig, kubeContext: kubeContext}, nil
}

func (b *VeleroBackend) run(ctx context.Context, cfg *veleroCommandConfig, args ...string) (string, string, error) {
	if b.runner == nil {
		return "", "", fmt.Errorf("%w: Velero command runner is not configured", service.ErrBackupBackendConfiguration)
	}
	stdout, stderr, err := b.runner.Run(ctx, cfg.binary, args, nil)
	if err == nil {
		return stdout, stderr, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return stdout, stderr, fmt.Errorf("%w: Velero binary %q was not found", service.ErrBackupBackendConfiguration, cfg.binary)
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
	return stdout, stderr, fmt.Errorf("%w: Velero command failed: %s", service.ErrBackupBackendExecution, message)
}

type veleroCommandConfig struct {
	binary      string
	namespace   string
	kubeconfig  string
	kubeContext string
}

func (c *veleroCommandConfig) baseArgs() []string {
	args := []string{"--namespace", c.namespace}
	if c.kubeconfig != "" {
		args = append(args, "--kubeconfig", c.kubeconfig)
	}
	if c.kubeContext != "" {
		args = append(args, "--kubecontext", c.kubeContext)
	}
	return args
}

type veleroCommandRunner interface {
	Run(ctx context.Context, binary string, args []string, extraEnv []string) (stdout string, stderr string, err error)
}

type defaultVeleroCommandRunner struct{}

func (defaultVeleroCommandRunner) Run(ctx context.Context, binary string, args []string, extraEnv []string) (string, string, error) {
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

func veleroRestoreName(targetRef string, metadata map[string]any) (string, error) {
	if name := stringMetadata(metadata, "velero_restore_name"); name != "" {
		return name, nil
	}
	targetRef = strings.TrimSpace(targetRef)
	for _, prefix := range []string{"velero:restore/", "velero:"} {
		if strings.HasPrefix(targetRef, prefix) {
			name := strings.TrimSpace(strings.TrimPrefix(targetRef, prefix))
			if name == "" {
				return "", fmt.Errorf("%w: Velero restore target name is empty", service.ErrBackupBackendConfiguration)
			}
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: Velero restore target must use velero:<restore-name> or metadata.velero_restore_name", service.ErrBackupBackendUnsupported)
}

func veleroRetentionBackupName(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	for _, prefix := range []string{"backup:", "velero:backup/"} {
		if strings.HasPrefix(selector, prefix) {
			name := strings.TrimSpace(strings.TrimPrefix(selector, prefix))
			if name == "" {
				return "", fmt.Errorf("%w: Velero retention backup selector is empty", service.ErrBackupBackendConfiguration)
			}
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: Velero retention selector must identify one backend backup as backup:<name>", service.ErrBackupBackendConfiguration)
}

func parseVeleroPhase(stdout string) (string, map[string]any, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return "", nil, fmt.Errorf("%w: Velero command returned empty JSON output", service.ErrBackupBackendUnsupported)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		return "", nil, fmt.Errorf("%w: parsing Velero JSON output: %v", service.ErrBackupBackendExecution, err)
	}
	status, _ := payload["status"].(map[string]any)
	phase, _ := status["phase"].(string)
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return "", payload, fmt.Errorf("%w: Velero JSON output did not include status.phase", service.ErrBackupBackendUnsupported)
	}
	return phase, payload, nil
}

func veleroPhaseSucceeded(phase string) bool {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "completed", "succeeded", "success":
		return true
	default:
		return false
	}
}

func captureCommandOutput(evidence map[string]any, key string, output string) {
	if strings.TrimSpace(output) != "" {
		evidence[key] = strings.TrimSpace(output)
	}
}
