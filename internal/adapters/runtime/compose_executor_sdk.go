package runtime

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/docker/go-connections/tlsconfig"
	"go.uber.org/zap"
)

// SDKComposeExecutor executes Compose in-process through the embedded Docker
// Compose v5 Go SDK (github.com/docker/compose/v5). All operations talk to the
// Docker-compatible Engine API directly — no docker compose CLI shell-out.
//
// Semantics mirror the CLIComposeExecutor command lines:
//
//   - Validate            ≙ docker compose -f <staged> config -q
//   - Up                  ≙ docker compose up -d --remove-orphans [--pull <policy>]
//   - ValidateWithFragment ≙ docker compose -f <main> -f <fragment> config -q
//   - UpService           ≙ docker compose -f <main> -f <fragment> up -d --no-deps [--pull <policy>] <svc>
//
// The SDK returns rich Go errors instead of process stdout/stderr, so the
// stdout/stderr return values are always empty strings.
type SDKComposeExecutor struct {
	runtime *ComposeRuntime
	logger  *zap.Logger

	mu        sync.Mutex
	svc       api.Compose
	dockerCli command.Cli

	// newService overrides SDK service construction in tests.
	newService func() (api.Compose, error)
}

// NewSDKComposeExecutor creates a Compose executor backed by the embedded
// Compose v5 SDK, bound to the runtime's Docker endpoint.
func NewSDKComposeExecutor(rt *ComposeRuntime, logger *zap.Logger) *SDKComposeExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SDKComposeExecutor{runtime: rt, logger: logger}
}

func (e *SDKComposeExecutor) ExecutionMode() RuntimeExecutionMode {
	return ExecutionModeSDK
}

// service lazily constructs (and caches) the SDK compose service so that
// executor construction never dials or depends on the Docker environment.
func (e *SDKComposeExecutor) service() (api.Compose, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.svc != nil {
		return e.svc, nil
	}
	build := e.newService
	if build == nil {
		build = e.buildService
	}
	svc, err := build()
	if err != nil {
		return nil, err
	}
	e.svc = svc
	return svc, nil
}

// buildService wires a docker CLI client from the runtime's endpoint
// configuration (host + TLS material) and constructs the SDK service.
// Progress output is discarded; failures surface as Go errors.
func (e *SDKComposeExecutor) buildService() (api.Compose, error) {
	if e.runtime == nil {
		return nil, fmt.Errorf("compose sdk executor runtime is nil")
	}
	dockerCli, err := command.NewDockerCli(command.WithCombinedStreams(io.Discard))
	if err != nil {
		return nil, fmt.Errorf("compose sdk: new docker cli: %w", err)
	}
	if err := dockerCli.Initialize(sdkClientOptions(e.runtime)); err != nil {
		return nil, fmt.Errorf("compose sdk: initialize docker cli: %w", err)
	}
	svc, err := compose.NewComposeService(dockerCli)
	if err != nil {
		if apiClient := dockerCli.Client(); apiClient != nil {
			_ = apiClient.Close()
		}
		return nil, fmt.Errorf("compose sdk: new compose service: %w", err)
	}
	e.dockerCli = dockerCli
	return svc, nil
}

// sdkClientOptions maps the runtime's endpoint configuration (Docker host +
// TLS material) onto docker CLI client options. Unlike the CLI compatibility
// path, certificate files are consumed at their configured paths — no
// DOCKER_CERT_PATH naming convention applies.
func sdkClientOptions(rt *ComposeRuntime) *flags.ClientOptions {
	opts := flags.NewClientOptions()
	if rt == nil {
		return opts
	}
	if host := strings.TrimSpace(rt.dockerHost); host != "" {
		opts.Hosts = []string{host}
	}
	endpoint := rt.endpoint
	if dockerEndpointUsesTLS(endpoint) {
		opts.TLS = true
		opts.TLSVerify = !endpoint.InsecureSkipVerify
		opts.TLSOptions = &tlsconfig.Options{
			CAFile:             strings.TrimSpace(endpoint.CACertFile),
			CertFile:           strings.TrimSpace(endpoint.ClientCertFile),
			KeyFile:            strings.TrimSpace(endpoint.ClientKeyFile),
			InsecureSkipVerify: endpoint.InsecureSkipVerify,
		}
	}
	return opts
}

// Close releases the cached docker client connection, if any. Safe to call
// multiple times and before first use.
func (e *SDKComposeExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.svc = nil
	if e.dockerCli == nil {
		return nil
	}
	var err error
	if apiClient := e.dockerCli.Client(); apiClient != nil {
		err = apiClient.Close()
	}
	e.dockerCli = nil
	return err
}

// loadProject loads and validates a Compose project through the SDK. Offline
// mode is always set: Bahia renders its own project files and must never
// resolve remote (OCI) includes during apply.
func (e *SDKComposeExecutor) loadProject(ctx context.Context, workingDir string, configPaths []string) (*types.Project, error) {
	svc, err := e.service()
	if err != nil {
		return nil, err
	}
	return svc.LoadProject(ctx, api.ProjectLoadOptions{
		WorkingDir:  workingDir,
		ConfigPaths: configPaths,
		Offline:     true,
	})
}

// Validate loads the staged project through the SDK, the in-process
// equivalent of `docker compose -f <staged> config -q`.
func (e *SDKComposeExecutor) Validate(ctx context.Context, staged *StagedFiles) (string, string, error) {
	if staged == nil {
		return "", "", fmt.Errorf("compose executor staged files is nil")
	}
	e.logger.Info("compose desired-state apply: validating staged project (sdk)",
		zap.String("compose_dir", staged.ComposeDir),
		zap.String("staging_dir", staged.StagingDir),
		zap.String("compose_file", staged.ComposeFile),
	)
	if _, err := e.loadProject(ctx, staged.StagingDir, []string{staged.ComposeFile}); err != nil {
		return "", "", fmt.Errorf("compose sdk config: %w", err)
	}
	staged.Validated = true
	return "", "", nil
}

// Up converges the full project, the in-process equivalent of
// `docker compose up -d --remove-orphans [--pull <policy>]`.
func (e *SDKComposeExecutor) Up(ctx context.Context, composeDir string, pullPolicy string) (string, string, error) {
	svc, err := e.service()
	if err != nil {
		return "", "", err
	}
	// No explicit config paths: use Compose default project discovery in the
	// working directory (docker-compose.yml plus any override files), matching
	// the CLI executor's `docker compose up` invocation, which also omits -f.
	project, err := e.loadProject(ctx, composeDir, nil)
	if err != nil {
		return "", "", fmt.Errorf("compose sdk up: load project: %w", err)
	}
	pullPolicy = normalizeComposePullPolicy(pullPolicy)
	applyComposePullPolicy(project, pullPolicy)

	e.logger.Info("compose desired-state apply: running up (sdk)",
		zap.String("compose_dir", composeDir),
		zap.String("project", project.Name),
		zap.String("pull_policy", pullPolicy),
	)

	if err := svc.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{
			RemoveOrphans: true,
			QuietPull:     true,
		},
	}); err != nil {
		return "", "", fmt.Errorf("compose sdk up: %w", err)
	}
	return "", "", nil
}

// ValidateWithFragment loads the merged full-project + fragment overlay, the
// in-process equivalent of
// `docker compose -f <main> -f <fragment> config -q`.
func (e *SDKComposeExecutor) ValidateWithFragment(ctx context.Context, composeDir string, fragmentFile string) (string, string, error) {
	mainFile := filepath.Join(composeDir, composeFileName)

	e.logger.Info("compose fragment apply: validating merged project (sdk)",
		zap.String("compose_dir", composeDir),
		zap.String("fragment_file", fragmentFile),
	)

	if _, err := e.loadProject(ctx, composeDir, []string{mainFile, fragmentFile}); err != nil {
		return "", "", fmt.Errorf("compose sdk config (fragment): %w", err)
	}
	return "", "", nil
}

// UpService applies a single service via the fragment overlay, the in-process
// equivalent of
// `docker compose -f <main> -f <fragment> up -d --no-deps [--pull <policy>] <svc>`.
// The --no-deps selection mirrors the upstream CLI exactly:
// project.WithSelectedServices(services, types.IgnoreDependencies).
func (e *SDKComposeExecutor) UpService(ctx context.Context, composeDir string, fragmentFile string, serviceKey string, pullPolicy string) (string, string, error) {
	svc, err := e.service()
	if err != nil {
		return "", "", err
	}
	mainFile := filepath.Join(composeDir, composeFileName)
	project, err := e.loadProject(ctx, composeDir, []string{mainFile, fragmentFile})
	if err != nil {
		return "", "", fmt.Errorf("compose sdk up service: load project: %w", err)
	}

	// --no-deps: select the target service without pulling in dependencies.
	project, err = project.WithSelectedServices([]string{serviceKey}, types.IgnoreDependencies)
	if err != nil {
		return "", "", fmt.Errorf("compose sdk up service: select service %q: %w", serviceKey, err)
	}

	pullPolicy = normalizeComposePullPolicy(pullPolicy)
	applyComposePullPolicy(project, pullPolicy)

	e.logger.Info("compose fragment apply: running up service (sdk)",
		zap.String("compose_dir", composeDir),
		zap.String("fragment_file", fragmentFile),
		zap.String("service_key", serviceKey),
		zap.String("pull_policy", pullPolicy),
	)

	if err := svc.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{
			Services: []string{serviceKey},
			// Service-scoped apply must not touch containers outside the
			// selected service; ignore (do not remove or warn about) other
			// project containers.
			IgnoreOrphans: true,
			QuietPull:     true,
		},
	}); err != nil {
		return "", "", fmt.Errorf("compose sdk up service: %w", err)
	}
	return "", "", nil
}

// applyComposePullPolicy sets the pull policy on every service, mirroring how
// the upstream CLI maps `--pull <policy>` onto the loaded project. An empty
// (normalized) policy leaves the project untouched, matching CLI behaviour of
// omitting the flag.
func applyComposePullPolicy(project *types.Project, normalizedPolicy string) {
	if project == nil || normalizedPolicy == "" {
		return
	}
	for name, svc := range project.Services {
		svc.PullPolicy = normalizedPolicy
		project.Services[name] = svc
	}
}

var _ ComposeExecutor = (*SDKComposeExecutor)(nil)
