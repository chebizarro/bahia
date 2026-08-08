package firecracker

import "context"

// VMMIdentity identifies a supervised firecracker VMM process. PID alone is
// not enough — PIDs are reused — so the process start time (from
// /proc/<pid>/stat on Linux) is recorded alongside it and both must match
// for the process to count as the same VMM.
type VMMIdentity struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"start_time"`
}

// StartVMMRequest describes the VMM process to launch.
type StartVMMRequest struct {
	// Binary is the firecracker binary path or name.
	Binary string
	// Args are the firecracker command-line arguments.
	Args []string
	// ConsoleLogPath is the file that receives the VMM's stdout and
	// stderr (the guest serial console), opened in append mode.
	ConsoleLogPath string
}

// ProcessManager is the driver's OS boundary for VMM process supervision.
// The real implementation is Linux-only (/proc-based identity checks);
// tests substitute a fake so the package runs anywhere.
type ProcessManager interface {
	// Start launches the VMM as a detached, session-leader process that
	// survives the calling process, returning its identity. The context
	// applies to launch preparation only — it must NOT bound the VMM's
	// lifetime.
	Start(ctx context.Context, req StartVMMRequest) (VMMIdentity, error)

	// Alive reports whether the identified process is still the same VMM:
	// the PID exists, its start time matches, and its command line
	// contains marker (the instance's API socket path).
	Alive(id VMMIdentity, marker string) bool

	// Kill force-terminates the process (SIGKILL) if it still matches the
	// identity and marker. Killing an already-gone process is a no-op.
	Kill(id VMMIdentity, marker string) error
}
