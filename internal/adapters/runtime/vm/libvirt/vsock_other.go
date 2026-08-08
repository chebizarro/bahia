//go:build !linux

package libvirt

import (
	"context"
	"fmt"
	"net"
	"runtime"
)

// dialVsock is unavailable off Linux: AF_VSOCK host sockets are a Linux
// kernel facility. Tests substitute Config.Dialer.
func dialVsock(_ context.Context, cid, port uint32) (net.Conn, error) {
	return nil, fmt.Errorf("libvirt vsock dialing (cid %d, port %d) requires linux, running on %s", cid, port, runtime.GOOS)
}
