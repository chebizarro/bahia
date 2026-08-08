// Package vm implements the shared VM runtime core for the vm-firecracker
// and vm-qemu runtime types. The core owns instance naming, per-instance
// state directories, artifact (image release) resolution, and lifecycle
// orchestration, delegating hypervisor mechanics to a small Hypervisor
// driver interface (libvirt/QEMU today, Firecracker as a follow-up).
package vm

import (
	"context"
	"net"
)

// InstanceState is the coarse hypervisor-level state of a VM instance.
type InstanceState string

const (
	// StateAbsent means the hypervisor has no instance with that name.
	StateAbsent InstanceState = "absent"
	// StateRunning means the instance is running.
	StateRunning InstanceState = "running"
	// StateStopped means the instance exists but is shut off.
	StateStopped InstanceState = "stopped"
	// StatePaused means the instance is suspended/paused.
	StatePaused InstanceState = "paused"
	// StateCrashed means the instance terminated abnormally.
	StateCrashed InstanceState = "crashed"
	// StateUnknown means the hypervisor reported a state the driver does
	// not recognize.
	StateUnknown InstanceState = "unknown"
)

// ImageSpec describes the resolved, digest-verified image release files
// backing an instance.
type ImageSpec struct {
	// Format is the release manifest format ("qcow2" or "firecracker-rootfs").
	Format string
	// Arch is the guest architecture declared by the manifest (e.g.
	// "x86_64", "aarch64"). Empty means "host architecture".
	Arch string
	// ReleaseDir is the resolved hash-pinned release directory.
	ReleaseDir string
	// DiskPath is the verified base disk image (qcow2 format only).
	DiskPath string
	// UEFIVarsPath is the verified UEFI NVRAM vars template, when the
	// release ships one (Windows-capable qcow2 images). Empty otherwise.
	UEFIVarsPath string
	// ImageID is the release identifier from the manifest.
	ImageID string
	// ManifestDigest is "sha256:<hex>" over the canonical manifest.json
	// bytes — the same value bahia stores as the artifact image digest.
	ManifestDigest string
}

// InstanceSpec describes a VM instance for Hypervisor.Create.
type InstanceSpec struct {
	// Name is the hypervisor-visible instance name
	// (bahia-<envID-short>-<serviceName>).
	Name string
	// InstanceDir is the per-instance state directory owned by the core
	// (metadata.json) and shared with the driver (overlay, nvram, domain
	// definition, console log).
	InstanceDir string
	// Image is the resolved release backing this instance.
	Image ImageSpec
	// VCPUs is the vCPU count.
	VCPUs int
	// MemoryMB is the memory size in MiB.
	MemoryMB int
	// VsockCID is the guest vsock context ID for the guest-agent
	// transport. Zero means no vsock device.
	VsockCID uint32
	// NetworkProfile names the host network profile. V1 VM instances are
	// vsock+console only; drivers currently reject or ignore non-empty
	// profiles explicitly.
	NetworkProfile string
}

// Hypervisor is the driver seam between the shared VM runtime core and a
// concrete virtualization mechanism (libvirt/QEMU, Firecracker).
//
// Drivers are expected to be CGO-free and cheap to construct: no host
// probing in constructors, all failures surfaced at call time.
type Hypervisor interface {
	// Create prepares a persistent instance (overlay disk, definition,
	// console log wiring) without starting it. It fails if an instance
	// with the same name already exists.
	Create(ctx context.Context, spec InstanceSpec) error

	// Start boots a previously created instance.
	Start(ctx context.Context, name string) error

	// Stop shuts an instance down. When graceful is true the driver
	// requests a guest shutdown (e.g. ACPI) and waits, bounded by ctx,
	// until the instance is off; otherwise it force-stops. Stopping an
	// already stopped instance is a no-op.
	Stop(ctx context.Context, name string, graceful bool) error

	// Destroy force-stops the instance if needed and removes its
	// hypervisor definition. Destroy is idempotent: destroying an absent
	// instance returns nil. Per-instance state-directory cleanup is the
	// core's responsibility.
	Destroy(ctx context.Context, name string) error

	// State reports the coarse instance state. Absent instances yield
	// (StateAbsent, nil), not an error.
	State(ctx context.Context, name string) (InstanceState, error)

	// List returns the names of instances whose name starts with prefix.
	List(ctx context.Context, prefix string) ([]string, error)

	// ConsoleLogPath returns the host path of the instance's serial
	// console log file (for StreamLogs tailing).
	ConsoleLogPath(name string) (string, error)

	// VsockDial opens a vsock connection to the guest agent on the given
	// port. This is the transport seam for the guest health/metrics
	// client (protocol v2 ping/metrics); drivers may return a clear
	// not-implemented error until that lands.
	VsockDial(ctx context.Context, name string, port uint32) (net.Conn, error)
}
