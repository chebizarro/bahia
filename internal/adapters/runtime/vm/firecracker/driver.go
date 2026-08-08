// Package firecracker implements the vm.Hypervisor driver for long-lived
// Firecracker microVM instances. Each instance is a supervised VMM process
// launched detached from bahia (pidfile-style vmm.json record + API socket
// + console log under <state_dir>/instances/<name>/), so VMs survive
// deploy-call completion and bahia restarts, and can be re-adopted by the
// instance registry on startup.
//
// All OS interaction goes through the ProcessManager boundary (pattern:
// the libvirt driver's CommandRunner), so the driver is testable with a
// fake process manager and compiles on non-Linux hosts; the real process
// manager is Linux-only. V1 runs the firecracker binary directly (no
// jailer) and instances are vsock+console only.
package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"go.uber.org/zap"
)

const (
	// DefaultBinary is the firecracker binary used when none is configured.
	DefaultBinary = "firecracker"
	// DefaultKernelArgs is the guest kernel command line used when none is
	// configured: serial console on ttyS0 (captured into console.log) and
	// the per-instance writable rootfs copy as the root device.
	DefaultKernelArgs = "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw"
	// DefaultShutdownTimeout bounds how long a graceful Stop waits after
	// SendCtrlAltDel before falling back to SIGKILL.
	DefaultShutdownTimeout = 30 * time.Second

	vmConfigFileName    = "vmconfig.json"
	vmmRecordFileName   = "vmm.json"
	apiSocketFileName   = "api.socket"
	vsockSocketFileName = "vsock.sock"
	consoleLogFileName  = "console.log"
	rootfsFileName      = "rootfs.ext4"

	exitPollInterval = 25 * time.Millisecond
)

// Config configures the firecracker driver.
type Config struct {
	// InstancesDir is the directory holding per-instance state
	// directories; instance files (vmconfig.json, rootfs copy, vmm.json,
	// api.socket, vsock.sock, console.log) live under
	// <InstancesDir>/<name>/.
	InstancesDir string
	// Binary overrides the firecracker binary (DefaultBinary when empty).
	Binary string
	// KernelArgs overrides the guest kernel command line
	// (DefaultKernelArgs when empty).
	KernelArgs string
	// ShutdownTimeout overrides the graceful-stop bound
	// (DefaultShutdownTimeout when zero).
	ShutdownTimeout time.Duration
	// Processes overrides VMM process supervision (tests). Defaults to
	// the OS process manager (Linux-only for real launches).
	Processes ProcessManager
}

// Driver implements vm.Hypervisor with one supervised long-lived
// firecracker VMM process per instance.
type Driver struct {
	cfg    Config
	logger *zap.Logger
}

// New creates a firecracker driver. Construction is side-effect free; host
// requirements (Linux, KVM, the firecracker binary) surface as call-time
// errors.
func New(cfg Config, logger *zap.Logger) *Driver {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(cfg.Binary) == "" {
		cfg.Binary = DefaultBinary
	}
	if strings.TrimSpace(cfg.KernelArgs) == "" {
		cfg.KernelArgs = DefaultKernelArgs
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.Processes == nil {
		cfg.Processes = newOSProcessManager()
	}
	return &Driver{cfg: cfg, logger: logger}
}

func (d *Driver) instanceDir(name string) string {
	return filepath.Join(d.cfg.InstancesDir, name)
}

func (d *Driver) apiSocketPath(name string) string {
	return filepath.Join(d.instanceDir(name), apiSocketFileName)
}

// vmConfigFile is the firecracker --config-file document describing the
// full VM so the VMM boots immediately on process start, with no pre-boot
// API choreography to replay after bahia restarts.
type vmConfigFile struct {
	BootSource    bootSourceConfig `json:"boot-source"`
	Drives        []driveConfig    `json:"drives"`
	MachineConfig machineConfig    `json:"machine-config"`
	Vsock         *vsockConfig     `json:"vsock,omitempty"`
}

type bootSourceConfig struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

type driveConfig struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type machineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

type vsockConfig struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// vmmRecord is the on-disk pidfile record for a launched VMM. Marker is
// the instance's API socket path, which appears on the VMM command line
// and (with the start time) makes liveness checks safe against PID reuse.
type vmmRecord struct {
	VMMIdentity
	Marker string `json:"marker"`
}

// Create prepares an instance: a writable per-instance copy of the
// verified base rootfs and the firecracker config file wiring kernel,
// rootfs, machine resources, and the optional vsock device.
func (d *Driver) Create(ctx context.Context, spec vm.InstanceSpec) error {
	if spec.Image.Format != vm.FormatFirecrackerRootFS {
		return fmt.Errorf("firecracker driver requires a %q image release, got format %q", vm.FormatFirecrackerRootFS, spec.Image.Format)
	}
	expectedDir := d.instanceDir(spec.Name)
	if filepath.Clean(spec.InstanceDir) != filepath.Clean(expectedDir) {
		return fmt.Errorf("instance directory %q does not match the driver's instances layout (%q)", spec.InstanceDir, expectedDir)
	}
	if strings.TrimSpace(spec.NetworkProfile) != "" {
		return fmt.Errorf("network profile %q is not supported: v1 firecracker instances are vsock+console only", spec.NetworkProfile)
	}
	if strings.TrimSpace(spec.Image.KernelPath) == "" || strings.TrimSpace(spec.Image.RootFSPath) == "" {
		return fmt.Errorf("firecracker image release is missing kernel or rootfs paths (release %q)", spec.Image.ReleaseDir)
	}
	state, err := d.State(ctx, spec.Name)
	if err != nil {
		return err
	}
	if state != vm.StateAbsent {
		return fmt.Errorf("firecracker instance %q already exists (state %s)", spec.Name, state)
	}

	rootfs := filepath.Join(spec.InstanceDir, rootfsFileName)
	if err := copyFile(spec.Image.RootFSPath, rootfs, 0o600); err != nil {
		return fmt.Errorf("creating writable rootfs copy: %w", err)
	}

	cfg := vmConfigFile{
		BootSource: bootSourceConfig{
			// The kernel is read-only and hash-pinned; reference it in the
			// release directory instead of copying per instance.
			KernelImagePath: spec.Image.KernelPath,
			BootArgs:        d.cfg.KernelArgs,
		},
		Drives: []driveConfig{{
			DriveID:      "rootfs",
			PathOnHost:   rootfs,
			IsRootDevice: true,
			IsReadOnly:   false,
		}},
		MachineConfig: machineConfig{
			VcpuCount:  spec.VCPUs,
			MemSizeMib: spec.MemoryMB,
			Smt:        false,
		},
	}
	if spec.VsockCID != 0 {
		cfg.Vsock = &vsockConfig{
			GuestCID: spec.VsockCID,
			UDSPath:  filepath.Join(spec.InstanceDir, vsockSocketFileName),
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(spec.InstanceDir, vmConfigFileName), data, 0o600); err != nil {
		return fmt.Errorf("writing firecracker VM config: %w", err)
	}
	return nil
}

// Start launches the instance's VMM as a detached long-lived process; the
// config file boots the VM immediately. The process identity is recorded
// in vmm.json so State/Stop/adoption can find it across bahia restarts.
func (d *Driver) Start(ctx context.Context, name string) error {
	dir := d.instanceDir(name)
	configPath := filepath.Join(dir, vmConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("firecracker instance %q does not exist", name)
		}
		return fmt.Errorf("checking firecracker instance %q: %w", name, err)
	}
	record, err := d.readRecord(name)
	if err != nil {
		return err
	}
	if record != nil && d.cfg.Processes.Alive(record.VMMIdentity, record.Marker) {
		return fmt.Errorf("firecracker instance %q is already running (pid %d)", name, record.PID)
	}
	// Clear leftovers from a previous run: firecracker refuses to start
	// when its API socket path already exists.
	for _, stale := range []string{d.apiSocketPath(name), filepath.Join(dir, vsockSocketFileName), filepath.Join(dir, vmmRecordFileName)} {
		if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing stale %s for instance %q: %w", filepath.Base(stale), name, err)
		}
	}

	marker := d.apiSocketPath(name)
	id, err := d.cfg.Processes.Start(ctx, StartVMMRequest{
		Binary:         d.cfg.Binary,
		Args:           []string{"--id", name, "--api-sock", marker, "--config-file", configPath},
		ConsoleLogPath: filepath.Join(dir, consoleLogFileName),
	})
	if err != nil {
		return fmt.Errorf("starting firecracker instance %q: %w", name, err)
	}
	if err := writeRecord(dir, &vmmRecord{VMMIdentity: id, Marker: marker}); err != nil {
		_ = d.cfg.Processes.Kill(id, marker)
		return fmt.Errorf("recording VMM identity for instance %q: %w", name, err)
	}
	return nil
}

// Stop shuts an instance down. Graceful stops send Ctrl+Alt+Del through
// the VMM API socket and wait up to the shutdown timeout (and ctx) for the
// VMM to exit, then fall back to SIGKILL; forced stops SIGKILL directly.
// Stopping an already stopped instance is a no-op.
func (d *Driver) Stop(ctx context.Context, name string, graceful bool) error {
	state, err := d.State(ctx, name)
	if err != nil {
		return err
	}
	switch state {
	case vm.StateAbsent:
		return fmt.Errorf("firecracker instance %q does not exist", name)
	case vm.StateStopped:
		return nil
	}
	record, err := d.readRecord(name)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if graceful {
		if err := sendCtrlAltDel(ctx, d.apiSocketPath(name)); err != nil {
			d.logger.Warn("graceful shutdown request failed; falling back to SIGKILL",
				zap.String("instance", name), zap.Error(err))
		} else {
			waitCtx, cancel := context.WithTimeout(ctx, d.cfg.ShutdownTimeout)
			waitErr := d.waitForExit(waitCtx, record)
			cancel()
			if waitErr == nil {
				return nil
			}
			if ctx.Err() != nil {
				return fmt.Errorf("firecracker instance %q shutdown not confirmed: %w", name, ctx.Err())
			}
			d.logger.Warn("guest did not shut down within the timeout; sending SIGKILL",
				zap.String("instance", name), zap.Duration("timeout", d.cfg.ShutdownTimeout))
		}
	}
	if err := d.cfg.Processes.Kill(record.VMMIdentity, record.Marker); err != nil {
		return fmt.Errorf("killing firecracker instance %q: %w", name, err)
	}
	if err := d.waitForExit(ctx, record); err != nil {
		return fmt.Errorf("firecracker instance %q VMM exit not confirmed: %w", name, err)
	}
	return nil
}

func (d *Driver) waitForExit(ctx context.Context, record *vmmRecord) error {
	for {
		if !d.cfg.Processes.Alive(record.VMMIdentity, record.Marker) {
			return nil
		}
		timer := time.NewTimer(exitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Destroy force-stops the VMM if needed and removes the instance
// definition (config, pid record, sockets), leaving State absent.
// Remaining instance files are removed with the instance directory by the
// core. Destroying an absent instance returns nil.
func (d *Driver) Destroy(ctx context.Context, name string) error {
	state, err := d.State(ctx, name)
	if err != nil {
		return err
	}
	if state == vm.StateAbsent {
		return nil
	}
	record, err := d.readRecord(name)
	if err == nil && record != nil && d.cfg.Processes.Alive(record.VMMIdentity, record.Marker) {
		if err := d.cfg.Processes.Kill(record.VMMIdentity, record.Marker); err != nil {
			return fmt.Errorf("killing firecracker instance %q: %w", name, err)
		}
		if err := d.waitForExit(ctx, record); err != nil {
			return fmt.Errorf("firecracker instance %q VMM exit not confirmed: %w", name, err)
		}
	}
	dir := d.instanceDir(name)
	var errs []error
	for _, file := range []string{vmConfigFileName, vmmRecordFileName, apiSocketFileName, vsockSocketFileName} {
		if err := os.Remove(filepath.Join(dir, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("removing firecracker instance %q definition: %w", name, errors.Join(errs...))
	}
	return nil
}

// State derives the instance state from the on-disk registry: no VM config
// file means absent; a recorded VMM process that is alive (PID + start
// time + command-line marker all matching) means running; anything else is
// stopped. A crashed VMM is indistinguishable from a stopped one here —
// the guest-agent health probe (plan item 7) refines that.
func (d *Driver) State(_ context.Context, name string) (vm.InstanceState, error) {
	if strings.TrimSpace(d.cfg.InstancesDir) == "" {
		return vm.StateUnknown, errors.New("firecracker driver has no instances directory configured")
	}
	if _, err := os.Stat(filepath.Join(d.instanceDir(name), vmConfigFileName)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return vm.StateAbsent, nil
		}
		return vm.StateUnknown, fmt.Errorf("checking firecracker instance %q: %w", name, err)
	}
	record, err := d.readRecord(name)
	if err != nil {
		return vm.StateUnknown, err
	}
	if record != nil && d.cfg.Processes.Alive(record.VMMIdentity, record.Marker) {
		return vm.StateRunning, nil
	}
	return vm.StateStopped, nil
}

// List scans the instances directory for defined instances (directories
// containing a VM config file) whose name starts with prefix.
func (d *Driver) List(_ context.Context, prefix string) ([]string, error) {
	entries, err := os.ReadDir(d.cfg.InstancesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing firecracker instances: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if _, err := os.Stat(filepath.Join(d.cfg.InstancesDir, entry.Name(), vmConfigFileName)); err != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ConsoleLogPath returns the console log file (VMM stdout/stderr, carrying
// the guest serial console) for an instance.
func (d *Driver) ConsoleLogPath(name string) (string, error) {
	if strings.TrimSpace(d.cfg.InstancesDir) == "" {
		return "", errors.New("firecracker driver has no instances directory configured")
	}
	return filepath.Join(d.instanceDir(name), consoleLogFileName), nil
}

// VsockDial opens a connection to the guest listener on port through the
// instance's hybrid-vsock unix socket. This is the transport seam for the
// guest health/metrics client (plan item 7).
func (d *Driver) VsockDial(ctx context.Context, name string, port uint32) (net.Conn, error) {
	cfg, err := d.readVMConfig(name)
	if err != nil {
		return nil, err
	}
	if cfg.Vsock == nil {
		return nil, fmt.Errorf("firecracker instance %q has no vsock device configured", name)
	}
	return dialHybridVsock(ctx, cfg.Vsock.UDSPath, port)
}

// AdoptOrphans reconciles the on-disk instance registry with reality after
// a bahia restart: VMMs that are still running are adopted as-is (their
// vmm.json record already identifies them); instances whose VMM died while
// bahia was away are reaped — stale pid records and socket files are
// removed so the instance reads as cleanly stopped and can be restarted.
// (Pattern: cascadia-go firecracker platform Reap, minus teardown — bahia
// instances are long-lived services, not one-shot jobs.)
func (d *Driver) AdoptOrphans(ctx context.Context) error {
	entries, err := os.ReadDir(d.cfg.InstancesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("scanning firecracker instances: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dir := d.instanceDir(name)
		if _, err := os.Stat(filepath.Join(dir, vmConfigFileName)); err != nil {
			// Not a defined firecracker instance (e.g. core metadata only
			// or a foreign directory); leave it to the core.
			continue
		}
		record, recordErr := d.readRecord(name)
		if recordErr == nil && record != nil && d.cfg.Processes.Alive(record.VMMIdentity, record.Marker) {
			d.logger.Info("adopted running firecracker instance",
				zap.String("instance", name), zap.Int("pid", record.PID))
			continue
		}
		if recordErr != nil {
			d.logger.Warn("firecracker instance has a corrupt VMM record; reaping",
				zap.String("instance", name), zap.Error(recordErr))
		}
		reaped := false
		for _, stale := range []string{vmmRecordFileName, apiSocketFileName, vsockSocketFileName} {
			path := filepath.Join(dir, stale)
			if err := os.Remove(path); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					errs = append(errs, fmt.Errorf("reaping instance %q: %w", name, err))
				}
				continue
			}
			reaped = true
		}
		if reaped {
			d.logger.Info("reaped dead firecracker instance", zap.String("instance", name))
		}
	}
	return errors.Join(errs...)
}

func (d *Driver) readVMConfig(name string) (*vmConfigFile, error) {
	data, err := os.ReadFile(filepath.Join(d.instanceDir(name), vmConfigFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("firecracker instance %q does not exist", name)
		}
		return nil, err
	}
	var cfg vmConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decoding VM config for instance %q: %w", name, err)
	}
	return &cfg, nil
}

// readRecord reads the instance's vmm.json. A missing record returns
// (nil, nil) — the instance has never started or was cleanly reaped.
func (d *Driver) readRecord(name string) (*vmmRecord, error) {
	data, err := os.ReadFile(filepath.Join(d.instanceDir(name), vmmRecordFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var record vmmRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decoding VMM record for instance %q: %w", name, err)
	}
	return &record, nil
}

func writeRecord(dir string, record *vmmRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, vmmRecordFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, vmmRecordFileName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	syncErr := dst.Sync()
	closeErr := dst.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

var _ vm.Hypervisor = (*Driver)(nil)
