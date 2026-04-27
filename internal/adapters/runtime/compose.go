package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ComposeRuntime implements Runtime using Docker Compose CLI.
type ComposeRuntime struct {
	projectDir string // working directory with docker-compose.yml
	binary     string // "docker-compose" or "docker compose"
	logger     *zap.Logger
}

// NewComposeRuntime creates a new Docker Compose runtime.
// projectDir is the directory containing the docker-compose.yml file.
func NewComposeRuntime(projectDir string, logger *zap.Logger) *ComposeRuntime {
	binary := detectComposeBinary()
	return &ComposeRuntime{
		projectDir: projectDir,
		binary:     binary,
		logger:     logger,
	}
}

// detectComposeBinary checks if "docker compose" (v2) or "docker-compose" (v1) is available.
func detectComposeBinary() string {
	if _, err := exec.LookPath("docker"); err == nil {
		// Try "docker compose" subcommand (v2).
		cmd := exec.Command("docker", "compose", "version")
		if err := cmd.Run(); err == nil {
			return "docker compose"
		}
	}
	// Fall back to standalone docker-compose.
	return "docker-compose"
}

func (r *ComposeRuntime) Type() domain.RuntimeType {
	return domain.RuntimeTypeCompose
}

// Observe uses "docker compose ps" to query service state.
func (r *ComposeRuntime) Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	args := r.composeArgs("ps", "--format", "json", serviceName)
	output, err := r.runCommand(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("compose ps: %w", err)
	}

	health := domain.HealthStatusStopped
	containerID := ""
	image := ""

	// Parse output lines for running state.
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Simple parsing — docker compose ps --format json gives JSON per line.
		if strings.Contains(line, "running") {
			health = domain.HealthStatusHealthy
		}
		// Extract container ID if present.
		if idx := strings.Index(line, "\"ID\":\""); idx >= 0 {
			rest := line[idx+6:]
			if end := strings.Index(rest, "\""); end >= 0 {
				containerID = rest[:end]
			}
		}
		if idx := strings.Index(line, "\"Image\":\""); idx >= 0 {
			rest := line[idx+9:]
			if end := strings.Index(rest, "\""); end >= 0 {
				image = rest[:end]
			}
		}
	}

	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageRepo:   image,
		ObservedContainerID: containerID,
		HealthStatus:        health,
		Source:              "compose",
		ObservedAt:          time.Now().UTC(),
	}, nil
}

// Deploy updates a Compose service with a new image and restarts it.
func (r *ComposeRuntime) Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error {
	// Set environment variables for the image override.
	envVars := make([]string, 0)
	for k, v := range opts.Environment {
		envVars = append(envVars, k+"="+v)
	}

	// Pull the new image.
	args := r.composeArgs("pull", serviceName)
	if _, err := r.runCommand(ctx, args...); err != nil {
		r.logger.Warn("compose pull failed, continuing", zap.Error(err))
	}

	// Recreate the service with the new image.
	args = r.composeArgs("up", "-d", "--force-recreate", "--no-deps", serviceName)
	if _, err := r.runCommand(ctx, args...); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}

	r.logger.Info("compose service deployed",
		zap.String("service", serviceName),
		zap.String("image", image),
	)
	return nil
}

// Undeploy stops and removes a Compose service.
func (r *ComposeRuntime) Undeploy(ctx context.Context, serviceName string) error {
	args := r.composeArgs("rm", "-s", "-f", serviceName)
	if _, err := r.runCommand(ctx, args...); err != nil {
		return fmt.Errorf("compose rm: %w", err)
	}
	return nil
}

// StreamLogs streams Compose service logs.
func (r *ComposeRuntime) StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error) {
	logArgs := []string{"logs", "--no-log-prefix"}
	if opts.Follow {
		logArgs = append(logArgs, "-f")
	}
	if opts.Tail > 0 {
		logArgs = append(logArgs, fmt.Sprintf("--tail=%d", opts.Tail))
	} else {
		logArgs = append(logArgs, "--tail=100")
	}
	logArgs = append(logArgs, serviceName)

	args := r.composeArgs(logArgs...)
	cmd := r.buildCommand(ctx, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("getting stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting compose logs: %w", err)
	}

	ch := make(chan LogEntry, 64)
	go func() {
		defer close(ch)
		defer cmd.Wait()

		buf := make([]byte, 8192)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				for _, line := range strings.Split(string(buf[:n]), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					select {
					case ch <- LogEntry{
						Timestamp: time.Now().UTC(),
						Stream:    "stdout",
						Message:   line,
					}:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return ch, nil
}

// composeArgs builds the full command arguments for docker compose.
func (r *ComposeRuntime) composeArgs(subArgs ...string) []string {
	if r.binary == "docker compose" {
		args := []string{"docker", "compose"}
		if r.projectDir != "" {
			args = append(args, "--project-directory", r.projectDir)
		}
		return append(args, subArgs...)
	}
	// docker-compose v1.
	args := []string{r.binary}
	if r.projectDir != "" {
		args = append(args, "--project-directory", r.projectDir)
	}
	return append(args, subArgs...)
}

func (r *ComposeRuntime) buildCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if r.projectDir != "" {
		cmd.Dir = r.projectDir
	}
	return cmd
}

func (r *ComposeRuntime) runCommand(ctx context.Context, args ...string) (string, error) {
	cmd := r.buildCommand(ctx, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// Compile-time interface check.
var _ Runtime = (*ComposeRuntime)(nil)
