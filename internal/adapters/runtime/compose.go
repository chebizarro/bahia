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
	"unicode"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ComposeRuntime implements Runtime using Docker Compose CLI.
type ComposeRuntime struct {
	projectDir   string // working directory with docker-compose.yml
	dockerHost   string // optional DOCKER_HOST override for compose commands
	dockerTLSEnv []string
	binary       string // "docker-compose" or "docker compose"
	logger       *zap.Logger
}

// NewComposeRuntime creates a new Docker Compose runtime.
// projectDir is the directory containing the docker-compose.yml file.
func NewComposeRuntime(projectDir string, logger *zap.Logger) *ComposeRuntime {
	return NewComposeRuntimeWithDockerHost(projectDir, "", logger)
}

// NewComposeRuntimeWithDockerHost creates a new Docker Compose runtime bound to an optional Docker host.
func NewComposeRuntimeWithDockerHost(projectDir, dockerHost string, logger *zap.Logger) *ComposeRuntime {
	binary := detectComposeBinary()
	return &ComposeRuntime{
		projectDir: projectDir,
		dockerHost: dockerHost,
		binary:     binary,
		logger:     logger,
	}
}

// NewComposeRuntimeWithEndpoint creates a Docker Compose runtime bound to a
// server-managed Docker endpoint, including CLI TLS environment wiring.
func NewComposeRuntimeWithEndpoint(projectDir string, endpoint config.RuntimeEndpointConfig, logger *zap.Logger) (*ComposeRuntime, error) {
	r := NewComposeRuntimeWithDockerHost(projectDir, strings.TrimSpace(endpoint.DockerHost), logger)
	tlsEnv, err := composeDockerTLSEnv(endpoint)
	if err != nil {
		return nil, err
	}
	r.dockerTLSEnv = tlsEnv
	return r, nil
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
	output, stderr, err := r.runCommandStdout(ctx, nil, args...)
	if err != nil {
		if strings.TrimSpace(stderr) != "" {
			return nil, fmt.Errorf("compose ps: %w: %s", err, strings.TrimSpace(stderr))
		}
		return nil, fmt.Errorf("compose ps: %w", err)
	}

	entries, err := parseComposePSOutput(output)
	if err != nil {
		if strings.TrimSpace(stderr) != "" {
			return nil, fmt.Errorf("parse compose ps output: %w: %s", err, strings.TrimSpace(stderr))
		}
		return nil, fmt.Errorf("parse compose ps output: %w", err)
	}

	health := domain.HealthStatusStopped
	containerID := ""
	image := ""
	if len(entries) > 0 {
		representative := entries[0]
		for _, entry := range entries {
			if mapDockerState(entry.State) == domain.HealthStatusHealthy {
				representative = entry
				health = domain.HealthStatusHealthy
				break
			}
		}
		if health != domain.HealthStatusHealthy {
			health = mapDockerState(representative.State)
		}
		containerID = representative.ID
		image = representative.Image
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
	envVars := make([]string, 0, len(opts.Environment)+1)
	for k, v := range opts.Environment {
		envVars = append(envVars, k+"="+v)
	}
	if image = strings.TrimSpace(image); image != "" {
		envVars = append(envVars, composeImageEnvKey(serviceName)+"="+image)
	}

	// Pull the new image.
	args := r.composeArgs("pull", serviceName)
	if _, err := r.runCommand(ctx, envVars, args...); err != nil {
		r.logger.Warn("compose pull failed, continuing", zap.Error(err))
	}

	// Recreate the service with the new image.
	args = r.composeArgs("up", "-d", "--force-recreate", "--no-deps", serviceName)
	if _, err := r.runCommand(ctx, envVars, args...); err != nil {
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
	if _, err := r.runCommand(ctx, nil, args...); err != nil {
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
	cmd := r.buildCommand(ctx, nil, args...)

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

func (r *ComposeRuntime) buildCommand(ctx context.Context, extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if r.projectDir != "" {
		cmd.Dir = r.projectDir
	}
	cmd.Env = r.commandEnv(extraEnv)
	return cmd
}

func (r *ComposeRuntime) runCommand(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	cmd := r.buildCommand(ctx, extraEnv, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (r *ComposeRuntime) runCommandStdout(ctx context.Context, extraEnv []string, args ...string) (string, string, error) {
	cmd := r.buildCommand(ctx, extraEnv, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (r *ComposeRuntime) commandEnv(extraEnv []string) []string {
	env := os.Environ()
	if r.dockerHost != "" {
		env = upsertEnv(env, "DOCKER_HOST="+r.dockerHost)
	}
	for _, entry := range r.dockerTLSEnv {
		env = upsertEnv(env, entry)
	}
	for _, entry := range extraEnv {
		env = upsertEnv(env, entry)
	}
	return env
}

func composeDockerTLSEnv(endpoint config.RuntimeEndpointConfig) ([]string, error) {
	if !dockerEndpointUsesTLS(endpoint) {
		return nil, nil
	}
	env := []string{}
	if endpoint.InsecureSkipVerify {
		env = append(env, "DOCKER_TLS=1")
		env = append(env, "DOCKER_TLS_VERIFY=")
	} else {
		env = append(env, "DOCKER_TLS_VERIFY=1")
	}

	caFile := strings.TrimSpace(endpoint.CACertFile)
	certFile := strings.TrimSpace(endpoint.ClientCertFile)
	keyFile := strings.TrimSpace(endpoint.ClientKeyFile)
	if caFile == "" && certFile == "" && keyFile == "" {
		return env, nil
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("docker endpoint client_cert_file and client_key_file must be configured together")
	}
	certDir, err := composeCertDir(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	env = append(env, "DOCKER_CERT_PATH="+certDir)
	return env, nil
}

func composeCertDir(caFile, certFile, keyFile string) (string, error) {
	var certDir string
	files := map[string]string{
		caFile:   "ca.pem",
		certFile: "cert.pem",
		keyFile:  "key.pem",
	}
	for file, wantBase := range files {
		if file == "" {
			continue
		}
		if filepath.Base(file) != wantBase {
			return "", fmt.Errorf("compose docker TLS requires %s to be named %s for DOCKER_CERT_PATH", file, wantBase)
		}
		dir := filepath.Dir(file)
		if certDir == "" {
			certDir = dir
		} else if certDir != dir {
			return "", fmt.Errorf("compose docker TLS files must share one directory for DOCKER_CERT_PATH")
		}
		if _, err := os.Stat(file); err != nil {
			return "", fmt.Errorf("checking docker cert file %q: %w", file, err)
		}
	}
	return certDir, nil
}

func upsertEnv(env []string, entry string) []string {
	key, _, ok := strings.Cut(entry, "=")
	if !ok || key == "" {
		return env
	}
	filtered := env[:0]
	prefix := key + "="
	for _, existing := range env {
		if !strings.HasPrefix(existing, prefix) {
			filtered = append(filtered, existing)
		}
	}
	return append(filtered, entry)
}

type composePSEntry struct {
	ID     string `json:"ID"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
}

func parseComposePSOutput(output string) ([]composePSEntry, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, nil
	}

	var entries []composePSEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err == nil {
		return entries, nil
	}

	entries = nil
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry composePSEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func composeImageEnvKey(serviceName string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(serviceName) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("_IMAGE")
	return b.String()
}

// Compile-time interface check.
var _ Runtime = (*ComposeRuntime)(nil)
