package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// Resource defaults applied when neither config nor spec provide values.
const (
	DefaultVCPUs    = 2
	DefaultMemoryMB = 2048
)

// Config configures the shared VM runtime core.
type Config struct {
	// RuntimeType is vm-firecracker or vm-qemu.
	RuntimeType domain.RuntimeType
	// StateDir holds per-instance state (metadata.json, overlays, logs).
	StateDir string
	// ImageRoot holds VM image release channels.
	ImageRoot string
	// VsockGuestPort is the guest-agent vsock port (health/metrics, later).
	VsockGuestPort int
	// VCPUs is the default vCPU count (DefaultVCPUs when zero).
	VCPUs int
	// MemoryMB is the default memory size in MiB (DefaultMemoryMB when zero).
	MemoryMB int
	// NetworkProfile names the host network profile (unused in v1).
	NetworkProfile string
}

// DeployOptions mirrors the runtime adapter deploy options. The VM core
// accepts Labels (recorded as instance metadata) and rejects the
// container-shaped options it cannot honor, per the explicit-failure
// convention.
type DeployOptions struct {
	Environment map[string]string
	Labels      map[string]string
	Ports       []string
	Volumes     []string
	Restart     string
	Command     []string
	Entrypoint  []string
	WorkingDir  string
	NetworkMode string
	PullAlways  bool
}

// LogOptions configures console log streaming.
type LogOptions struct {
	Tail   int
	Follow bool
}

// LogEntry is a single console log line.
type LogEntry struct {
	Timestamp time.Time
	Stream    string
	Message   string
}

// Runtime is the shared VM runtime core. It implements the lifecycle
// contract (Observe/Deploy/Undeploy/StreamLogs/Restart/Stop) against a
// Hypervisor driver; the runtime adapter package wraps it to satisfy its
// Runtime and LifecycleRuntime interfaces.
type Runtime struct {
	cfg    Config
	hv     Hypervisor
	logger *zap.Logger
}

// NewRuntime validates config and creates the shared VM runtime core.
func NewRuntime(cfg Config, hv Hypervisor, logger *zap.Logger) (*Runtime, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	switch cfg.RuntimeType {
	case domain.RuntimeTypeVMFirecracker, domain.RuntimeTypeVMQEMU:
	default:
		return nil, fmt.Errorf("vm runtime core does not support runtime type %q", cfg.RuntimeType)
	}
	if hv == nil {
		return nil, fmt.Errorf("vm runtime requires a hypervisor driver")
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil, fmt.Errorf("vm.state_dir is required for runtime type %q", cfg.RuntimeType)
	}
	if strings.TrimSpace(cfg.ImageRoot) == "" {
		return nil, fmt.Errorf("vm.image_root is required for runtime type %q", cfg.RuntimeType)
	}
	if cfg.VCPUs < 0 || cfg.MemoryMB < 0 {
		return nil, fmt.Errorf("vm.vcpus and vm.memory_mb must not be negative")
	}
	if cfg.VCPUs == 0 {
		cfg.VCPUs = DefaultVCPUs
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = DefaultMemoryMB
	}
	return &Runtime{cfg: cfg, hv: hv, logger: logger}, nil
}

// Type returns the concrete VM runtime type.
func (r *Runtime) Type() domain.RuntimeType {
	return r.cfg.RuntimeType
}

// Hypervisor exposes the underlying driver (used by the guest metrics
// client follow-up and tests).
func (r *Runtime) Hypervisor() Hypervisor {
	return r.hv
}

func (r *Runtime) expectedFormat() string {
	if r.cfg.RuntimeType == domain.RuntimeTypeVMFirecracker {
		return FormatFirecrackerRootFS
	}
	return FormatQCOW2
}

func (r *Runtime) instancesDir() string {
	return InstancesDir(r.cfg.StateDir)
}

// Deploy resolves and verifies the image release, replaces any existing
// instance for the target, and creates + starts a fresh instance.
func (r *Runtime) Deploy(ctx context.Context, serviceName, image string, opts DeployOptions) error {
	if err := validateDeployOptions(opts); err != nil {
		return err
	}
	repo, digest, err := ParseImageRef(image)
	if err != nil {
		return err
	}
	release, err := ResolveRelease(r.cfg.ImageRoot, repo, digest, r.expectedFormat())
	if err != nil {
		return err
	}

	envID := uuid.Nil
	if raw := strings.TrimSpace(opts.Labels[LabelEnvironmentID]); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return fmt.Errorf("deploy label %s has invalid UUID %q: %w", LabelEnvironmentID, raw, err)
		}
		envID = parsed
	}
	name := InstanceName(envID, serviceName)

	// Replace semantics: tear down every existing instance recorded for
	// this target before creating the new one.
	existing, err := FindInstancesByService(r.instancesDir(), serviceName)
	if err != nil {
		return fmt.Errorf("scanning existing vm instances: %w", err)
	}
	for _, md := range existing {
		if err := r.removeInstance(ctx, md); err != nil {
			return fmt.Errorf("removing existing vm instance %q: %w", md.Name, err)
		}
	}

	instanceDir := filepath.Join(r.instancesDir(), name)
	if err := os.MkdirAll(r.instancesDir(), 0o700); err != nil {
		return fmt.Errorf("creating vm instances directory: %w", err)
	}
	// A leftover directory without live metadata (failed prior deploy) is
	// removed so instance creation is exclusive.
	if err := os.RemoveAll(instanceDir); err != nil {
		return fmt.Errorf("clearing stale vm instance directory: %w", err)
	}
	if err := os.Mkdir(instanceDir, 0o700); err != nil {
		return fmt.Errorf("creating vm instance directory: %w", err)
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = os.RemoveAll(instanceDir)
		}
	}()

	spec := InstanceSpec{
		Name:           name,
		InstanceDir:    instanceDir,
		Image:          release.ImageSpec(),
		VCPUs:          r.cfg.VCPUs,
		MemoryMB:       r.cfg.MemoryMB,
		NetworkProfile: r.cfg.NetworkProfile,
	}
	if r.cfg.VsockGuestPort > 0 {
		spec.VsockCID = deriveVsockCID(name)
	}
	specHash := ComputeSpecHash(string(r.cfg.RuntimeType), release.ManifestDigest, spec.VCPUs, spec.MemoryMB, spec.NetworkProfile)

	md := &InstanceMetadata{
		Name:                 name,
		ServiceName:          serviceName,
		RuntimeType:          string(r.cfg.RuntimeType),
		ImageRepo:            repo,
		ImageDigest:          release.ManifestDigest,
		ImageID:              release.Manifest.ImageID,
		ReleaseDir:           release.Dir,
		SpecHash:             specHash,
		VsockCID:             spec.VsockCID,
		AgentProtocolVersion: release.Manifest.AgentProtocolVersion,
		Labels:               copyLabels(opts.Labels),
		CreatedAt:            time.Now().UTC(),
	}
	if envID != uuid.Nil {
		md.EnvironmentID = envID.String()
	}
	if err := WriteInstanceMetadata(instanceDir, md); err != nil {
		return fmt.Errorf("writing vm instance metadata: %w", err)
	}

	if err := r.hv.Create(ctx, spec); err != nil {
		return fmt.Errorf("creating vm instance %q: %w", name, err)
	}
	if err := r.hv.Start(ctx, name); err != nil {
		// Best-effort teardown so a failed start does not leave a defined
		// but never-started instance behind.
		if destroyErr := r.hv.Destroy(ctx, name); destroyErr != nil {
			r.logger.Warn("cleanup after failed vm start also failed",
				zap.String("instance", name), zap.Error(destroyErr))
		}
		return fmt.Errorf("starting vm instance %q: %w", name, err)
	}
	cleanupOnError = false

	r.logger.Info("vm instance deployed",
		zap.String("service", serviceName),
		zap.String("instance", name),
		zap.String("image_repo", repo),
		zap.String("image_digest", release.ManifestDigest),
		zap.String("release_dir", release.Dir),
	)
	return nil
}

func copyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func validateDeployOptions(opts DeployOptions) error {
	var unsupported []string
	if len(opts.Environment) > 0 {
		unsupported = append(unsupported, "environment variables/secrets")
	}
	if len(opts.Ports) > 0 {
		unsupported = append(unsupported, "ports")
	}
	if len(opts.Volumes) > 0 {
		unsupported = append(unsupported, "volumes")
	}
	if len(opts.Command) > 0 {
		unsupported = append(unsupported, "command")
	}
	if len(opts.Entrypoint) > 0 {
		unsupported = append(unsupported, "entrypoint")
	}
	if strings.TrimSpace(opts.WorkingDir) != "" {
		unsupported = append(unsupported, "working_dir")
	}
	if strings.TrimSpace(opts.NetworkMode) != "" {
		unsupported = append(unsupported, "network_mode")
	}
	if strings.TrimSpace(opts.Restart) != "" {
		unsupported = append(unsupported, "restart policy")
	}
	if opts.PullAlways {
		unsupported = append(unsupported, "pull_always (VM image releases are pre-provisioned under image_root)")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("deploy options not supported by VM runtimes in v1: %s", strings.Join(unsupported, ", "))
	}
	return nil
}

// Undeploy destroys and removes any instance recorded for the target.
// Undeploying a target with no instance is a no-op, matching the container
// runtimes.
func (r *Runtime) Undeploy(ctx context.Context, serviceName string) error {
	matches, err := FindInstancesByService(r.instancesDir(), serviceName)
	if err != nil {
		return fmt.Errorf("scanning vm instances: %w", err)
	}
	for _, md := range matches {
		if err := r.removeInstance(ctx, md); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) removeInstance(ctx context.Context, md *InstanceMetadata) error {
	if err := r.hv.Destroy(ctx, md.Name); err != nil {
		return fmt.Errorf("destroying vm instance %q: %w", md.Name, err)
	}
	if err := os.RemoveAll(filepath.Join(r.instancesDir(), md.Name)); err != nil {
		return fmt.Errorf("removing vm instance state for %q: %w", md.Name, err)
	}
	return nil
}

// Observe reports the runtime state of the target's instance.
//
// Observation composes two sources: hypervisor state decides
// running/stopped, and — when the image declares a service-mode guest
// agent (protocol v2) and a vsock transport is configured — a guest-agent
// ping refines health: a running instance whose agent answers is healthy
// and contributes guest metrics to observation metadata; a running
// instance whose agent is unreachable degrades to unhealthy. Images
// without a v2 agent (or runtimes without vsock configured) are observed
// on hypervisor state alone. Guest probe failures never fail Observe.
func (r *Runtime) Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	observedAt := time.Now().UTC()
	matches, err := FindInstancesByService(r.instancesDir(), serviceName)
	if err != nil {
		return nil, fmt.Errorf("scanning vm instances: %w", err)
	}
	if len(matches) == 0 {
		return &domain.RuntimeObservation{
			ServiceID:     serviceID,
			EnvironmentID: envID,
			HealthStatus:  domain.HealthStatusStopped,
			Source:        string(r.cfg.RuntimeType),
			ObservedAt:    observedAt,
		}, nil
	}
	md := matches[0]
	state, err := r.hv.State(ctx, md.Name)
	if err != nil {
		return nil, fmt.Errorf("querying hypervisor state for %q: %w", md.Name, err)
	}
	health := mapInstanceState(state)
	metadata := map[string]any{
		"instance_name":    md.Name,
		"hypervisor_state": string(state),
		"image_id":         md.ImageID,
		"release_dir":      md.ReleaseDir,
		"spec_hash":        md.SpecHash,
	}
	if state == StateRunning && r.agentExpected(md) {
		probe, probeErr := probeGuestAgent(ctx, r.hv, md.Name, md.ImageID, uint32(r.cfg.VsockGuestPort))
		if probeErr != nil {
			health = domain.HealthStatusUnhealthy
			metadata["guest_agent"] = "unreachable"
			metadata["guest_agent_error"] = probeErr.Error()
			r.logger.Debug("vm guest agent probe failed",
				zap.String("instance", md.Name), zap.Error(probeErr))
		} else {
			metadata["guest_agent"] = "ok"
			if probe.Metrics != nil {
				metadata["guest_metrics"] = metricsMetadata(probe.Metrics)
			} else if probe.MetricsErr != nil {
				metadata["guest_metrics_error"] = probe.MetricsErr.Error()
				r.logger.Debug("vm guest metrics collection failed",
					zap.String("instance", md.Name), zap.Error(probe.MetricsErr))
			}
		}
	}
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: md.ImageDigest,
		ObservedImageRepo:   md.ImageRepo,
		ObservedContainerID: md.Name,
		ObservedHost:        "local",
		HealthStatus:        health,
		Source:              string(r.cfg.RuntimeType),
		Metadata:            metadata,
		ObservedAt:          observedAt,
	}, nil
}

// agentExpected reports whether the instance's image declares a
// service-mode (protocol v2+) guest agent and this runtime has a vsock
// transport configured to reach it. Pre-v2 images and vsock-less
// configurations observe on hypervisor state alone.
func (r *Runtime) agentExpected(md *InstanceMetadata) bool {
	return md.AgentProtocolVersion >= 2 && r.cfg.VsockGuestPort > 0 && md.VsockCID != 0
}

func mapInstanceState(state InstanceState) domain.HealthStatus {
	switch state {
	case StateRunning:
		return domain.HealthStatusHealthy
	case StateStopped, StateAbsent:
		return domain.HealthStatusStopped
	case StateCrashed, StatePaused:
		return domain.HealthStatusUnhealthy
	default:
		return domain.HealthStatusUnknown
	}
}

// Restart gracefully stops and restarts the target's instance.
func (r *Runtime) Restart(ctx context.Context, targetName string) error {
	md, err := r.requireInstance(targetName)
	if err != nil {
		return err
	}
	if err := r.hv.Stop(ctx, md.Name, true); err != nil {
		return fmt.Errorf("stopping vm instance %q for restart: %w", md.Name, err)
	}
	if err := r.hv.Start(ctx, md.Name); err != nil {
		return fmt.Errorf("restarting vm instance %q: %w", md.Name, err)
	}
	return nil
}

// Stop gracefully stops the target's instance.
func (r *Runtime) Stop(ctx context.Context, targetName string) error {
	md, err := r.requireInstance(targetName)
	if err != nil {
		return err
	}
	if err := r.hv.Stop(ctx, md.Name, true); err != nil {
		return fmt.Errorf("stopping vm instance %q: %w", md.Name, err)
	}
	return nil
}

func (r *Runtime) requireInstance(serviceName string) (*InstanceMetadata, error) {
	matches, err := FindInstancesByService(r.instancesDir(), serviceName)
	if err != nil {
		return nil, fmt.Errorf("scanning vm instances: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no vm instance found for target %q", serviceName)
	}
	return matches[0], nil
}

// DefaultLogTail is the number of historical console lines emitted when
// no tail is requested, matching the Docker collector's default.
const DefaultLogTail = 100

// StreamLogs tails the instance's serial console log with the Docker
// collector's semantics: Tail bounds the historical lines (default
// DefaultLogTail), Follow keeps polling for appended data until the
// context ends, and line timestamps are parsed from the log content where
// present (RFC3339 prefix), falling back to the read time. Follow mode
// survives log rotation/truncation: a shrunken or temporarily missing
// console file restarts the tail from the top instead of ending the
// stream.
func (r *Runtime) StreamLogs(ctx context.Context, serviceName string, opts LogOptions) (<-chan LogEntry, error) {
	md, err := r.requireInstance(serviceName)
	if err != nil {
		return nil, err
	}
	path, err := r.hv.ConsoleLogPath(md.Name)
	if err != nil {
		return nil, fmt.Errorf("resolving console log for %q: %w", md.Name, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("console log not available for vm instance %q: %w", md.Name, err)
	}
	tail := opts.Tail
	if tail <= 0 {
		tail = DefaultLogTail
	}
	lines := splitConsoleLines(string(data))
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}

	ch := make(chan LogEntry, 64)
	offset := int64(len(data))
	go func() {
		defer close(ch)
		for _, line := range lines {
			select {
			case ch <- consoleLogEntry(line, time.Now().UTC()):
			case <-ctx.Done():
				return
			}
		}
		if !opts.Follow {
			return
		}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var partial string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			chunk, newOffset, err := readConsoleFrom(path, offset)
			if err != nil {
				// Transient failures (e.g. the file vanishing mid-rotation)
				// must not end a followed stream; keep polling until the
				// context does.
				r.logger.Debug("vm console follow read failed", zap.String("instance", md.Name), zap.Error(err))
				continue
			}
			if newOffset < offset {
				// Truncated/rotated: the read restarted from the top, so any
				// partial line from the old file is stale.
				partial = ""
			}
			offset = newOffset
			if chunk == "" {
				continue
			}
			partial += chunk
			var emit []string
			emit, partial = splitCompleteLines(partial)
			for _, line := range emit {
				select {
				case ch <- consoleLogEntry(line, time.Now().UTC()):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// consoleLogEntry builds a LogEntry for one console line, parsing a
// leading RFC3339 timestamp when the guest emits one and falling back to
// the provided read time otherwise. Console output is a single serial
// stream, so entries are always stdout.
func consoleLogEntry(line string, fallback time.Time) LogEntry {
	if space := strings.IndexByte(line, ' '); space > 0 {
		if ts, err := time.Parse(time.RFC3339Nano, line[:space]); err == nil {
			return LogEntry{Timestamp: ts.UTC(), Stream: "stdout", Message: strings.TrimLeft(line[space+1:], " ")}
		}
	}
	return LogEntry{Timestamp: fallback, Stream: "stdout", Message: line}
}

func splitConsoleLines(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	lines := strings.Split(data, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func splitCompleteLines(buf string) (complete []string, rest string) {
	idx := strings.LastIndexByte(buf, '\n')
	if idx < 0 {
		return nil, buf
	}
	return splitConsoleLines(buf[:idx+1]), buf[idx+1:]
}

func readConsoleFrom(path string, offset int64) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", offset, err
	}
	size := info.Size()
	if size < offset {
		// The file shrank (rotation/truncation): restart from the top.
		offset = 0
	}
	if size == offset {
		return "", offset, nil
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return "", offset, err
	}
	buf := make([]byte, size-offset)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return "", offset, err
	}
	return string(buf[:n]), offset + int64(n), nil
}
