package firecracker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// sendCtrlAltDel asks the VMM, over its API unix socket, to inject a
// Ctrl+Alt+Del into the guest (the firecracker graceful-shutdown
// mechanism: the guest's init handles it as a power-off request).
func sendCtrlAltDel(ctx context.Context, socketPath string) error {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/actions",
		strings.NewReader(`{"action_type": "SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker API SendCtrlAltDel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("firecracker API SendCtrlAltDel: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// dialHybridVsock connects to firecracker's host-side vsock unix socket
// and performs the hybrid-vsock handshake ("CONNECT <port>\n" -> "OK
// <hostport>\n"), returning a connection to the guest listener on port.
func dialHybridVsock(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, fmt.Errorf("dialing vsock unix socket: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT handshake write: %w", err)
	}
	line, err := readHandshakeLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT handshake read: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT to guest port %d refused: %q", port, line)
	}
	_ = conn.SetDeadline(time.Time{}) // clear the handshake deadline
	return conn, nil
}

// readHandshakeLine reads a single short newline-terminated line one byte
// at a time so no guest payload bytes past the handshake are consumed.
func readHandshakeLine(conn net.Conn) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 64 {
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(buf[0])
	}
	return "", fmt.Errorf("handshake response exceeds 64 bytes")
}
