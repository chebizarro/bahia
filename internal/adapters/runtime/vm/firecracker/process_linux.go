//go:build linux

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// newOSProcessManager returns the Linux VMM process manager. Identity
// checks read /proc (start time + cmdline), mirroring cascadia-go's
// firecracker platform so PID reuse can never target the wrong process.
func newOSProcessManager() ProcessManager {
	return linuxProcessManager{}
}

type linuxProcessManager struct{}

func (linuxProcessManager) Start(ctx context.Context, req StartVMMRequest) (VMMIdentity, error) {
	if err := ctx.Err(); err != nil {
		return VMMIdentity{}, err
	}
	logFile, err := os.OpenFile(req.ConsoleLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return VMMIdentity{}, fmt.Errorf("opening console log: %w", err)
	}
	defer logFile.Close()

	// Deliberately not exec.CommandContext: the VMM must outlive the
	// deploy call and the bahia process itself.
	cmd := exec.Command(req.Binary, req.Args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := ctx.Err(); err != nil {
		return VMMIdentity{}, err
	}
	if err := cmd.Start(); err != nil {
		return VMMIdentity{}, fmt.Errorf("starting firecracker VMM: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap the child if it exits while bahia is still running so it does
	// not linger as a zombie. When bahia exits first, the VMM reparents
	// to init and is reaped there.
	go func() { _ = cmd.Wait() }()

	startTime, err := processStartTime(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return VMMIdentity{}, fmt.Errorf("reading VMM process identity: %w", err)
	}
	return VMMIdentity{PID: pid, StartTime: startTime}, nil
}

func (linuxProcessManager) Alive(id VMMIdentity, marker string) bool {
	return processMatches(id, marker)
}

func (m linuxProcessManager) Kill(id VMMIdentity, marker string) error {
	fd, err := unix.PidfdOpen(id.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	alive, err := m.InspectProcess(context.Background(), id, marker)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

// processStartTime reads field 22 (starttime) from /proc/<pid>/stat,
// skipping past the parenthesised comm field which may contain spaces.
func processStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) <= 19 {
		return 0, errors.New("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

// processMatches reports whether the PID still refers to the recorded VMM:
// same start time and a command line containing the instance marker. A
// zombie's cmdline is empty, so terminated-but-unreaped VMMs do not match.
func processMatches(id VMMIdentity, marker string) bool {
	if id.PID <= 0 {
		return false
	}
	actualStart, err := processStartTime(id.PID)
	if err != nil || (id.StartTime != 0 && actualStart != id.StartTime) {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(id.PID), "cmdline"))
	if err != nil {
		return false
	}
	command := strings.ReplaceAll(string(data), "\x00", " ")
	return strings.Contains(command, marker)
}
