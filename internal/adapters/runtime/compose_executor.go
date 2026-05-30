package runtime

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ComposeExecutor owns Compose CLI execution for compatibility-mode apply.
type ComposeExecutor interface {
	RuntimeControlClient
	Validate(ctx context.Context, staged *StagedFiles) (stdout string, stderr string, err error)
	Up(ctx context.Context, composeDir string, pullPolicy string) (stdout string, stderr string, err error)
}

// CLIComposeExecutor executes Compose through docker compose/docker-compose.
type CLIComposeExecutor struct {
	runtime *ComposeRuntime
	runner  CommandRunner
	logger  *zap.Logger
}

func NewCLIComposeExecutor(rt *ComposeRuntime, runner CommandRunner, logger *zap.Logger) *CLIComposeExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CLIComposeExecutor{runtime: rt, runner: runner, logger: logger}
}

func (e *CLIComposeExecutor) ExecutionMode() RuntimeExecutionMode {
	return ExecutionModeCLI
}

func (e *CLIComposeExecutor) Validate(ctx context.Context, staged *StagedFiles) (string, string, error) {
	if e.runner == nil {
		return "", "", fmt.Errorf("compose executor command runner is nil")
	}
	if staged == nil {
		return "", "", fmt.Errorf("compose executor staged files is nil")
	}
	args := []string{"compose", "-f", staged.ComposeFile, "config", "-q"}
	e.logger.Info("compose desired-state apply: validating staged project",
		zap.String("compose_dir", staged.ComposeDir),
		zap.String("staging_dir", staged.StagingDir),
		zap.Strings("args", args),
	)
	stdout, stderr, err := e.runner.RunCommand(ctx, "docker", args, staged.StagingDir, nil)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			return stdout, stderr, fmt.Errorf("docker compose config: %s: %w", detail, err)
		}
		return stdout, stderr, fmt.Errorf("docker compose config: %w", err)
	}
	staged.Validated = true
	return stdout, stderr, nil
}

func (e *CLIComposeExecutor) Up(ctx context.Context, composeDir string, pullPolicy string) (string, string, error) {
	if e.runtime == nil {
		return "", "", fmt.Errorf("compose executor runtime is nil")
	}
	if e.runner == nil {
		return "", "", fmt.Errorf("compose executor command runner is nil")
	}

	args := e.runtime.composeArgs("up", "-d", "--remove-orphans")
	pullPolicy = normalizeComposePullPolicy(pullPolicy)
	if pullPolicy != "" {
		args = append(args, "--pull", pullPolicy)
	}

	e.logger.Info("compose desired-state apply: running up",
		zap.String("compose_dir", composeDir),
		zap.Strings("args", args),
		zap.String("pull_policy", pullPolicy),
	)

	env := e.runtime.commandEnv(nil)
	stdout, stderr, err := e.runner.RunCommand(ctx, args[0], args[1:], composeDir, env)
	if err != nil {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail != "" {
			return stdout, stderr, fmt.Errorf("docker compose up: %s: %w", detail, err)
		}
		return stdout, stderr, fmt.Errorf("docker compose up: %w", err)
	}
	return stdout, stderr, nil
}

var _ ComposeExecutor = (*CLIComposeExecutor)(nil)
