//go:build darwin

package firecracker

import (
	"errors"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
	"golang.org/x/sys/unix"
)

type kernelColdWatch struct {
	fd      int
	files   []int
	invalid bool
}

func newColdFileWatch(paths []string) (coldFileWatch, error) {
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	w := &kernelColdWatch{fd: fd}
	for _, path := range paths {
		f, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, errors.Join(err, w.Close())
		}
		w.files = append(w.files, f)
		changes := []unix.Kevent_t{{Ident: uint64(f), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB | unix.NOTE_REVOKE}}
		if _, err = unix.Kevent(fd, changes, nil, nil); err != nil {
			return nil, errors.Join(err, w.Close())
		}
	}
	return w, nil
}
func (w *kernelColdWatch) Close() error {
	err := unix.Close(w.fd)
	for _, f := range w.files {
		err = errors.Join(err, unix.Close(f))
	}
	return err
}
func (w *kernelColdWatch) Check() error {
	var events [8]unix.Kevent_t
	for {
		n, err := unix.Kevent(w.fd, nil, events[:], &unix.Timespec{})
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		if n > 0 {
			w.invalid = true
		}
		if n == 0 {
			break
		}
	}
	if w.invalid {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	return nil
}
