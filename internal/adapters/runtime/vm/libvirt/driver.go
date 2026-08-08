// Package libvirt implements the vm.Hypervisor driver for persistent
// QEMU/KVM domains via libvirt. All hypervisor interaction goes through a
// CGO-free virsh/qemu-img exec boundary (pattern: cascadia-go
// worker/vmexec/libvirt), so the driver is testable with a fake command
// runner and needs no libvirt client library.
package libvirt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"go.uber.org/zap"
)

const (
	// DefaultURI is the libvirt connection URI used when none is
	// configured.
	DefaultURI = "qemu:///system"
	// DefaultFirmwareCodePath is the OVMF firmware code image used for
	// UEFI (Windows-capable) guests when none is configured.
	DefaultFirmwareCodePath = "/usr/share/OVMF/OVMF_CODE.fd"

	overlayFileName    = "disk.qcow2"
	nvramFileName      = "nvram.fd"
	domainXMLFileName  = "domain.xml"
	consoleLogFileName = "console.log"

	statePollInterval = 250 * time.Millisecond
)

// CommandRunner executes a host command and returns its combined output.
// It is the driver's only side-effect boundary; tests substitute a fake.
type CommandRunner func(ctx context.Context, binary string, args ...string) ([]byte, error)

// Config configures the libvirt driver.
type Config struct {
	// URI is the libvirt connection URI (DefaultURI when empty).
	URI string
	// InstancesDir is the directory holding per-instance state
	// directories; instance files (overlay, nvram, domain.xml,
	// console.log) live under <InstancesDir>/<name>/.
	InstancesDir string
	// VirshBinary overrides the virsh binary ("virsh" when empty).
	VirshBinary string
	// QEMUImgBinary overrides the qemu-img binary ("qemu-img" when empty).
	QEMUImgBinary string
	// FirmwareCodePath is the read-only UEFI firmware code image used for
	// releases that ship UEFI vars (DefaultFirmwareCodePath when empty).
	FirmwareCodePath string
	// Runner overrides command execution (tests). Defaults to exec.
	Runner CommandRunner
}

// Driver implements vm.Hypervisor over a virsh exec boundary with
// persistent domains (virsh define + start).
type Driver struct {
	cfg    Config
	logger *zap.Logger
}

// New creates a libvirt driver. Construction is side-effect free; host
// requirements (virsh, KVM, connectivity) surface as call-time errors.
func New(cfg Config, logger *zap.Logger) *Driver {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(cfg.URI) == "" {
		cfg.URI = DefaultURI
	}
	if strings.TrimSpace(cfg.VirshBinary) == "" {
		cfg.VirshBinary = "virsh"
	}
	if strings.TrimSpace(cfg.QEMUImgBinary) == "" {
		cfg.QEMUImgBinary = "qemu-img"
	}
	if strings.TrimSpace(cfg.FirmwareCodePath) == "" {
		cfg.FirmwareCodePath = DefaultFirmwareCodePath
	}
	if cfg.Runner == nil {
		cfg.Runner = execRunner
	}
	return &Driver{cfg: cfg, logger: logger}
}

func execRunner(ctx context.Context, binary string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", filepath.Base(binary), strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (d *Driver) virsh(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-c", d.cfg.URI}, args...)
	return d.cfg.Runner(ctx, d.cfg.VirshBinary, full...)
}

func (d *Driver) qemuImg(ctx context.Context, args ...string) ([]byte, error) {
	return d.cfg.Runner(ctx, d.cfg.QEMUImgBinary, args...)
}

func (d *Driver) instanceDir(name string) string {
	return filepath.Join(d.cfg.InstancesDir, name)
}

// Create prepares a persistent domain: qcow2 overlay backed by the
// verified release disk, per-instance NVRAM for UEFI images, serial
// console log wiring, and virsh define.
func (d *Driver) Create(ctx context.Context, spec vm.InstanceSpec) error {
	if spec.Image.Format != vm.FormatQCOW2 {
		return fmt.Errorf("libvirt driver requires a %q image release, got format %q", vm.FormatQCOW2, spec.Image.Format)
	}
	expectedDir := d.instanceDir(spec.Name)
	if filepath.Clean(spec.InstanceDir) != filepath.Clean(expectedDir) {
		return fmt.Errorf("instance directory %q does not match the driver's instances layout (%q)", spec.InstanceDir, expectedDir)
	}
	state, err := d.State(ctx, spec.Name)
	if err != nil {
		return err
	}
	if state != vm.StateAbsent {
		return fmt.Errorf("libvirt domain %q already exists (state %s)", spec.Name, state)
	}

	overlay := filepath.Join(spec.InstanceDir, overlayFileName)
	if _, err := d.qemuImg(ctx, "create", "-f", "qcow2", "-F", "qcow2", "-b", spec.Image.DiskPath, overlay); err != nil {
		return fmt.Errorf("creating qcow2 overlay: %w", err)
	}

	var nvram string
	if spec.Image.UEFIVarsPath != "" {
		nvram = filepath.Join(spec.InstanceDir, nvramFileName)
		if err := copyFile(spec.Image.UEFIVarsPath, nvram, 0o600); err != nil {
			return fmt.Errorf("creating per-instance UEFI vars: %w", err)
		}
	}

	consoleLog := filepath.Join(spec.InstanceDir, consoleLogFileName)
	xmlData, err := domainXML(domainParams{
		Name:         spec.Name,
		MemoryMB:     spec.MemoryMB,
		VCPUs:        spec.VCPUs,
		Arch:         spec.Image.Arch,
		Overlay:      overlay,
		NVRAM:        nvram,
		FirmwareCode: d.cfg.FirmwareCodePath,
		ConsoleLog:   consoleLog,
		VsockCID:     spec.VsockCID,
	})
	if err != nil {
		return err
	}
	xmlPath := filepath.Join(spec.InstanceDir, domainXMLFileName)
	if err := os.WriteFile(xmlPath, xmlData, 0o600); err != nil {
		return fmt.Errorf("writing domain XML: %w", err)
	}

	if _, err := d.virsh(ctx, "define", xmlPath); err != nil {
		return fmt.Errorf("defining libvirt domain: %w", err)
	}
	return nil
}

// Start boots a defined domain.
func (d *Driver) Start(ctx context.Context, name string) error {
	if _, err := d.virsh(ctx, "start", name); err != nil {
		return fmt.Errorf("starting libvirt domain %q: %w", name, err)
	}
	return nil
}

// Stop shuts a domain down. Graceful stops request an ACPI shutdown and
// wait (ctx-bounded) until the domain is off; forced stops use virsh
// destroy. Stopping an already-off domain is a no-op.
func (d *Driver) Stop(ctx context.Context, name string, graceful bool) error {
	state, err := d.State(ctx, name)
	if err != nil {
		return err
	}
	switch state {
	case vm.StateAbsent:
		return fmt.Errorf("libvirt domain %q does not exist", name)
	case vm.StateStopped:
		return nil
	}
	if graceful {
		if _, err := d.virsh(ctx, "shutdown", name); err != nil {
			return fmt.Errorf("requesting shutdown of libvirt domain %q: %w", name, err)
		}
	} else {
		if output, err := d.virsh(ctx, "destroy", name); err != nil && !isNotRunningOutput(output, err) {
			return fmt.Errorf("destroying libvirt domain %q: %w", name, err)
		}
	}
	return d.waitForOff(ctx, name)
}

func (d *Driver) waitForOff(ctx context.Context, name string) error {
	for {
		state, err := d.State(ctx, name)
		if err != nil {
			return err
		}
		if state == vm.StateStopped || state == vm.StateAbsent {
			return nil
		}
		timer := time.NewTimer(statePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("libvirt domain %q shutdown not confirmed: %w", name, ctx.Err())
		case <-timer.C:
		}
	}
}

// Destroy force-stops the domain if needed and removes its definition
// (including managed NVRAM). Destroying an absent domain returns nil.
func (d *Driver) Destroy(ctx context.Context, name string) error {
	state, err := d.State(ctx, name)
	if err != nil {
		return err
	}
	if state == vm.StateAbsent {
		return nil
	}
	if state != vm.StateStopped {
		if output, err := d.virsh(ctx, "destroy", name); err != nil && !isNotRunningOutput(output, err) {
			return fmt.Errorf("destroying libvirt domain %q: %w", name, err)
		}
	}
	if output, err := d.virsh(ctx, "undefine", name, "--nvram"); err != nil {
		if isAbsentOutput(output, err) {
			return nil
		}
		return fmt.Errorf("undefining libvirt domain %q: %w", name, err)
	}
	return nil
}

// State maps virsh domstate output to the coarse instance states.
func (d *Driver) State(ctx context.Context, name string) (vm.InstanceState, error) {
	output, err := d.virsh(ctx, "domstate", name)
	if err != nil {
		if isAbsentOutput(output, err) {
			return vm.StateAbsent, nil
		}
		return vm.StateUnknown, fmt.Errorf("querying libvirt domain state for %q: %w", name, err)
	}
	switch strings.ToLower(strings.TrimSpace(string(output))) {
	case "running", "idle", "in shutdown":
		return vm.StateRunning, nil
	case "shut off":
		return vm.StateStopped, nil
	case "paused", "pmsuspended":
		return vm.StatePaused, nil
	case "crashed":
		return vm.StateCrashed, nil
	default:
		return vm.StateUnknown, nil
	}
}

// List returns defined domains whose name starts with prefix.
func (d *Driver) List(ctx context.Context, prefix string) ([]string, error) {
	output, err := d.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, fmt.Errorf("listing libvirt domains: %w", err)
	}
	var names []string
	for _, name := range strings.Fields(string(output)) {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

// ConsoleLogPath returns the serial console log file for an instance.
func (d *Driver) ConsoleLogPath(name string) (string, error) {
	if strings.TrimSpace(d.cfg.InstancesDir) == "" {
		return "", errors.New("libvirt driver has no instances directory configured")
	}
	return filepath.Join(d.instanceDir(name), consoleLogFileName), nil
}

// VsockDial is the guest-agent transport seam.
//
// TODO(bahia vm observe): implement AF_VSOCK dialing (host CID 2 ->
// instance CID from metadata) for the protocol-v2 guest health/metrics
// client. Until then Observe runs on hypervisor state alone.
func (d *Driver) VsockDial(ctx context.Context, name string, port uint32) (net.Conn, error) {
	return nil, fmt.Errorf("vsock guest-agent transport is not implemented yet for the libvirt driver (instance %q, port %d)", name, port)
}

// isAbsentOutput reports whether a virsh error indicates the domain does
// not exist.
func isAbsentOutput(output []byte, err error) bool {
	text := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(text, "failed to get domain") ||
		strings.Contains(text, "no domain with matching name") ||
		strings.Contains(text, "domain not found")
}

// isNotRunningOutput reports whether a virsh destroy error indicates the
// domain was already off.
func isNotRunningOutput(output []byte, err error) bool {
	text := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(text, "domain is not running") ||
		strings.Contains(text, "domain is not active")
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

// hostGuestArch maps the host GOARCH to the libvirt guest architecture
// used when the image manifest does not declare one.
func hostGuestArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	default:
		return "x86_64"
	}
}

var _ vm.Hypervisor = (*Driver)(nil)
