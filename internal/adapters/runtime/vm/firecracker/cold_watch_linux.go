//go:build linux

package firecracker

import (
	"errors"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
	"golang.org/x/sys/unix"
)

type kernelColdWatch struct {
	fd      int
	invalid bool
}

func newColdFileWatch(paths []string) (coldFileWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	w := &kernelColdWatch{fd: fd}
	for _, path := range paths {
		if _, err = unix.InotifyAddWatch(fd, path, unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF); err != nil {
			w.Close()
			return nil, err
		}
	}
	return w, nil
}
func (w *kernelColdWatch) Close() error { return unix.Close(w.fd) }
func (w *kernelColdWatch) Check() error {
	var buf [4096]byte
	for {
		n, err := unix.Read(w.fd, buf[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n > 0 {
			w.invalid = true
		}
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		if n == 0 {
			return vm.ProviderError(domain.VMErrorUnconfirmed, nil)
		}
	}
	if w.invalid {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	return nil
}
