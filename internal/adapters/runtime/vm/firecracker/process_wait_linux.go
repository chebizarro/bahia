//go:build linux

package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

func (linuxProcessManager) InspectProcess(ctx context.Context, id VMMIdentity, marker string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if id.PID <= 0 || id.StartTime == 0 || marker == "" {
		return false, errors.New("invalid VMM identity")
	}
	start, err := processStartTime(id.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if start != id.StartTime {
		return false, nil
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(id.PID), "cmdline"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	} // Exited zombie, still awaiting OS reaping.
	args := strings.Split(string(data), "\x00")
	for i, arg := range args {
		if arg == "--api-sock" && i+1 < len(args) && args[i+1] == marker {
			return true, nil
		}
	}
	return false, errors.New("VMM command-line identity mismatch")
}

type pidExit struct {
	fd   int
	once sync.Once
	err  error
}

func (p *pidExit) Close() error {
	p.once.Do(func() {
		if p.fd >= 0 {
			p.err = unix.Close(p.fd)
		}
	})
	return p.err
}
func (p *pidExit) Wait(ctx context.Context) error {
	if p.fd < 0 {
		return nil
	}
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(finished); _, _ = unix.Write(wake, []byte{1, 0, 0, 0, 0, 0, 0, 0}) })
	defer func() {
		if !stop() {
			<-finished
		}
		_ = unix.Close(wake)
	}()
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}, {Fd: int32(wake), Events: unix.POLLIN}}
	for {
		_, err = unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		return errors.New("process exit event transport failed")
	}
}
func (m linuxProcessManager) WatchExit(ctx context.Context, id VMMIdentity, marker string) (ProcessExit, error) {
	fd, err := unix.PidfdOpen(id.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return &pidExit{fd: -1}, nil
	}
	if err != nil {
		return nil, err
	}
	alive, err := m.InspectProcess(ctx, id, marker)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if !alive {
		_ = unix.Close(fd)
		return &pidExit{fd: -1}, nil
	}
	return &pidExit{fd: fd}, nil
}

var _ PersistentProcessManager = linuxProcessManager{}
