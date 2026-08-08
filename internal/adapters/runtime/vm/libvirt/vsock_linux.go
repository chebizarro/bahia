//go:build linux

package libvirt

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// dialVsock opens a host-side AF_VSOCK stream connection to (cid, port)
// with non-blocking connect so ctx cancellation is honored. (Pattern:
// cascadia-go worker/vmexec/libvirt.)
func dialVsock(ctx context.Context, cid, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	closeFD := true
	defer func() {
		if closeFD {
			_ = unix.Close(fd)
		}
	}()
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
	if err != nil && !errors.Is(err, syscall.EINPROGRESS) {
		return nil, err
	}
	for err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		count, pollErr := unix.Poll(poll, 50)
		if pollErr != nil {
			if errors.Is(pollErr, syscall.EINTR) {
				continue
			}
			return nil, pollErr
		}
		if count == 0 {
			continue
		}
		if poll[0].Revents&(unix.POLLNVAL|unix.POLLHUP) != 0 {
			return nil, errors.New("libvirt: vsock connect poll failed")
		}
		socketErr, getErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if getErr != nil {
			return nil, getErr
		}
		if socketErr == 0 {
			err = nil
		} else if socketErr != int(syscall.EINPROGRESS) {
			return nil, syscall.Errno(socketErr)
		}
	}
	file := os.NewFile(uintptr(fd), "libvirt-vsock")
	if file == nil {
		return nil, errors.New("libvirt: wrap vsock file descriptor")
	}
	conn, err := net.FileConn(file)
	fileCloseErr := file.Close()
	closeFD = false
	if err != nil {
		return nil, errors.Join(err, fileCloseErr)
	}
	if fileCloseErr != nil {
		conn.Close()
		return nil, fileCloseErr
	}
	return conn, nil
}
