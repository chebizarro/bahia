package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/strutil"
	"go.uber.org/zap"
)

const nginxOwnershipHeader = "# Managed by Bahia internal routing v1; DO NOT EDIT."

// ArgvRunner executes one already-tokenized command without a shell.
type ArgvRunner func(ctx context.Context, argv []string) error

// EnvArgvRunner executes one already-tokenized command with explicit environment additions.
type EnvArgvRunner func(ctx context.Context, argv, commandEnv []string) error

type NginxConfig struct {
	IncludeDir    string
	FilePrefix    string
	TestCommand   []string
	ReloadCommand []string
	CommandEnv    []string
	CertFile      string
	KeyFile       string
	ConfigHash    string
	Runner        ArgvRunner
	EnvRunner     EnvArgvRunner
	Logger        *zap.Logger
}

// NginxBackend manages exactly one Bahia-owned nginx include per hostname.
type NginxBackend struct {
	cfg     NginxConfig
	runner  EnvArgvRunner
	applyMu sync.Mutex
}

func NewNginxBackend(cfg NginxConfig) (*NginxBackend, error) {
	cfg.IncludeDir = filepath.Clean(strings.TrimSpace(cfg.IncludeDir))
	cfg.FilePrefix = strings.TrimSpace(cfg.FilePrefix)
	cfg.CertFile = filepath.Clean(strings.TrimSpace(cfg.CertFile))
	cfg.KeyFile = filepath.Clean(strings.TrimSpace(cfg.KeyFile))
	cfg.ConfigHash = strings.TrimSpace(cfg.ConfigHash)
	if !filepath.IsAbs(cfg.IncludeDir) || !filepath.IsAbs(cfg.CertFile) || !filepath.IsAbs(cfg.KeyFile) {
		return nil, fmt.Errorf("nginx include, certificate, and key paths must be absolute")
	}
	if cfg.FilePrefix == "" || filepath.Base(cfg.FilePrefix) != cfg.FilePrefix || strings.ContainsAny(cfg.FilePrefix, `/\\`) {
		return nil, fmt.Errorf("nginx file prefix must be a filename prefix")
	}
	if len(cfg.TestCommand) == 0 || len(cfg.ReloadCommand) == 0 || cfg.ConfigHash == "" {
		return nil, fmt.Errorf("nginx test command, reload command, and config hash are required")
	}
	for _, command := range [][]string{cfg.TestCommand, cfg.ReloadCommand} {
		for i := range command {
			command[i] = strings.TrimSpace(command[i])
			if command[i] == "" {
				return nil, fmt.Errorf("nginx command argv entries must not be empty")
			}
		}
	}
	runner := cfg.EnvRunner
	if runner == nil && cfg.Runner != nil {
		runner = func(ctx context.Context, argv, _ []string) error {
			return cfg.Runner(ctx, argv)
		}
	}
	if runner == nil {
		runner = runArgv
	}
	for i, entry := range cfg.CommandEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !strutil.ValidEnvironmentKey(key) || strings.IndexByte(entry, 0) >= 0 {
			return nil, fmt.Errorf("nginx command environment entry %d must be KEY=VALUE with a non-empty valid key", i)
		}
	}
	cfg.TestCommand = append([]string(nil), cfg.TestCommand...)
	cfg.ReloadCommand = append([]string(nil), cfg.ReloadCommand...)
	cfg.CommandEnv = append([]string(nil), cfg.CommandEnv...)
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	cfg.Logger.Info("effective internal nginx routing commands",
		zap.Strings("test_argv", cfg.TestCommand),
		zap.Strings("reload_argv", cfg.ReloadCommand),
		zap.Strings("command_env_keys", commandEnvKeys(cfg.CommandEnv)),
	)
	return &NginxBackend{cfg: cfg, runner: runner}, nil
}

func formatArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = strconv.Quote(arg)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func commandEnvKeys(commandEnv []string) []string {
	keys := make([]string, 0, len(commandEnv))
	for _, entry := range commandEnv {
		key, _, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
	}
	return keys
}

func runArgv(ctx context.Context, argv, commandEnv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("command argv is empty")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = append(os.Environ(), commandEnv...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("command argv %s failed: %w; output=%q", formatArgv(argv), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (b *NginxBackend) runCommand(ctx context.Context, step string, argv []string) error {
	argv = append([]string(nil), argv...)
	commandEnv := append([]string(nil), b.cfg.CommandEnv...)
	b.cfg.Logger.Info("executing internal nginx route command",
		zap.String("step", step),
		zap.Strings("argv", argv),
		zap.Strings("command_env_keys", commandEnvKeys(commandEnv)),
	)
	if err := b.runner(ctx, argv, commandEnv); err != nil {
		wrapped := fmt.Errorf("internal nginx %s command argv %s failed: %w", step, formatArgv(argv), err)
		b.cfg.Logger.Error("internal nginx route command failed",
			zap.String("step", step),
			zap.Strings("argv", argv),
			zap.Error(wrapped),
		)
		return wrapped
	}
	return nil
}

func (b *NginxBackend) HealthCheck(ctx context.Context) error {
	if b == nil {
		return fmt.Errorf("nginx internal routing backend is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(b.cfg.IncludeDir)
	if err != nil {
		return fmt.Errorf("nginx include directory %q is not accessible: %w", b.cfg.IncludeDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("nginx include path %q is not a directory", b.cfg.IncludeDir)
	}
	probe, err := os.CreateTemp(b.cfg.IncludeDir, ".bahia-health-*.tmp")
	if err != nil {
		return fmt.Errorf("nginx include directory %q is not writable: %w", b.cfg.IncludeDir, err)
	}
	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probeName)
		return fmt.Errorf("close nginx health probe: %w", err)
	}
	if err := os.Remove(probeName); err != nil {
		return fmt.Errorf("remove nginx health probe: %w", err)
	}
	if err := readableRegularFile(b.cfg.CertFile); err != nil {
		return fmt.Errorf("nginx certificate file: %w", err)
	}
	if err := readableRegularFile(b.cfg.KeyFile); err != nil {
		return fmt.Errorf("nginx key file: %w", err)
	}
	return nil
}

func readableRegularFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", path)
	}
	return nil
}

func (b *NginxBackend) Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if b == nil || plan == nil {
		return fmt.Errorf("nginx internal route plan is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	if internal := plan.InternalHTTPS; internal != nil {
		if internal.ConfigHash != b.cfg.ConfigHash || internal.CertFile != b.cfg.CertFile || internal.KeyFile != b.cfg.KeyFile {
			return fmt.Errorf("internal routing configuration changed after review")
		}
		if err := readableRegularFile(internal.CertFile); err != nil {
			return fmt.Errorf("internal HTTPS certificate: %w", err)
		}
		if err := readableRegularFile(internal.KeyFile); err != nil {
			return fmt.Errorf("internal HTTPS key: %w", err)
		}
	}
	path, err := b.vhostPath(plan.Hostname)
	if err != nil {
		return err
	}
	snapshot, err := captureNginxSnapshot(path)
	if err != nil {
		return err
	}
	if snapshot.Exists && !ownedNginxVhost(snapshot.Data) {
		return fmt.Errorf("nginx vhost %q collides with a foreign file", path)
	}
	return nil
}

func (b *NginxBackend) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	if err := b.Check(ctx, plan); err != nil {
		return err
	}
	path, err := b.vhostPath(plan.Hostname)
	if err != nil {
		return err
	}
	previous, err := captureNginxSnapshot(path)
	if err != nil {
		return err
	}
	if plan.InternalHTTPS == nil && !previous.Exists {
		return nil
	}
	if plan.InternalHTTPS == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove Bahia-owned nginx vhost %q: %w", path, err)
		}
	} else {
		data, err := RenderNginxVhost(plan.InternalHTTPS)
		if err != nil {
			return err
		}
		if err := writeNginxAtomic(ctx, path, plan.Hostname, data, 0o644); err != nil {
			return err
		}
	}

	if err := b.runCommand(ctx, "test", b.cfg.TestCommand); err != nil {
		cause := fmt.Errorf("validate nginx after converging internal route %q: %w", plan.Hostname, err)
		return b.restoreAndActivate(path, previous, cause)
	}
	if err := b.runCommand(ctx, "reload", b.cfg.ReloadCommand); err != nil {
		cause := fmt.Errorf("reload nginx after converging internal route %q: %w", plan.Hostname, err)
		return b.restoreAndActivate(path, previous, cause)
	}
	return nil
}

func (b *NginxBackend) restoreAndActivate(path string, snapshot atomicfile.Snapshot, cause error) error {
	if err := atomicfile.Restore(path, ".nginx-rollback-*.tmp", snapshot); err != nil {
		return errors.Join(cause, fmt.Errorf("restore previous nginx vhost: %w", err))
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.runCommand(cleanupCtx, "rollback-test", b.cfg.TestCommand); err != nil {
		return errors.Join(cause, fmt.Errorf("validate nginx after restoring previous vhost: %w", err))
	}
	if err := b.runCommand(cleanupCtx, "rollback-reload", b.cfg.ReloadCommand); err != nil {
		return errors.Join(cause, fmt.Errorf("reload nginx after restoring previous vhost: %w", err))
	}
	return fmt.Errorf("%w; previous internal route restored", cause)
}

func RenderNginxVhost(plan *domain.DesiredInternalHTTPSPlan) ([]byte, error) {
	if plan == nil {
		return nil, fmt.Errorf("internal HTTPS plan is required")
	}
	if plan.SchemaVersion != domain.InternalHTTPSPlanSchemaVersion || plan.Listen != "443 ssl" {
		return nil, fmt.Errorf("unsupported internal HTTPS plan")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", nginxOwnershipHeader)
	fmt.Fprintln(&out, "server {")
	fmt.Fprintf(&out, "    listen %s;\n", plan.Listen)
	fmt.Fprintf(&out, "    server_name %s;\n", plan.Hostname)
	fmt.Fprintf(&out, "    ssl_certificate %s;\n", plan.CertFile)
	fmt.Fprintf(&out, "    ssl_certificate_key %s;\n", plan.KeyFile)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "    location / {")
	fmt.Fprintf(&out, "        proxy_pass %s;\n", plan.UpstreamURL)
	fmt.Fprintln(&out, "        proxy_set_header Host $host;")
	fmt.Fprintln(&out, "        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;")
	fmt.Fprintln(&out, "        proxy_set_header X-Forwarded-Proto $scheme;")
	fmt.Fprintln(&out, "    }")
	fmt.Fprintln(&out, "}")
	return []byte(out.String()), nil
}

func ownedNginxVhost(data []byte) bool {
	return bytes.HasPrefix(data, []byte(nginxOwnershipHeader+"\n"))
}

func (b *NginxBackend) vhostPath(hostname string) (string, error) {
	hostname, err := domain.NormalizePublicHostname(hostname)
	if err != nil {
		return "", err
	}
	name := b.cfg.FilePrefix + hostname + ".conf"
	path := filepath.Join(b.cfg.IncludeDir, name)
	if filepath.Dir(path) != b.cfg.IncludeDir || !strings.HasPrefix(filepath.Base(path), b.cfg.FilePrefix) {
		return "", fmt.Errorf("nginx vhost path escapes include directory")
	}
	return path, nil
}

func writeNginxAtomic(ctx context.Context, path, hostname string, data []byte, mode os.FileMode) error {
	err := atomicfile.WriteFile(ctx, path, "."+hostname+"-*.tmp", data, mode)
	if err == nil {
		return nil
	}
	var operationErr *atomicfile.Error
	if !errors.As(err, &operationErr) {
		return err
	}
	switch operationErr.Operation {
	case atomicfile.OperationCreateTemp:
		return fmt.Errorf("create nginx vhost temp file: %w", operationErr.Err)
	case atomicfile.OperationWrite:
		return fmt.Errorf("write nginx vhost temp file: %w", operationErr.Err)
	case atomicfile.OperationChmod:
		return fmt.Errorf("chmod nginx vhost temp file: %w", operationErr.Err)
	case atomicfile.OperationSync:
		return fmt.Errorf("sync nginx vhost temp file: %w", operationErr.Err)
	case atomicfile.OperationClose:
		return fmt.Errorf("close nginx vhost temp file: %w", operationErr.Err)
	case atomicfile.OperationRename:
		return fmt.Errorf("replace nginx vhost %q: %w", path, operationErr.Err)
	default:
		return err
	}
}

func captureNginxSnapshot(path string) (atomicfile.Snapshot, error) {
	snapshot, err := atomicfile.Capture(path, 0o644)
	if err == nil {
		return snapshot, nil
	}
	var operationErr *atomicfile.Error
	if !errors.As(err, &operationErr) {
		return atomicfile.Snapshot{}, err
	}
	switch operationErr.Operation {
	case atomicfile.OperationRead:
		return atomicfile.Snapshot{}, fmt.Errorf("read previous nginx vhost %q: %w", path, operationErr.Err)
	case atomicfile.OperationStat:
		return atomicfile.Snapshot{}, fmt.Errorf("stat previous nginx vhost %q: %w", path, operationErr.Err)
	default:
		return atomicfile.Snapshot{}, err
	}
}
