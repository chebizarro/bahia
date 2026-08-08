package vm

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"git.sharegap.net/cascadia/cascadia-go/worker/vmexec/protocol"
)

const (
	// guestProbeTimeout bounds the whole guest-agent probe (dial,
	// handshake, ping, metrics) so a wedged guest cannot stall Observe.
	guestProbeTimeout = 5 * time.Second
	// maxGuestFrameBytes bounds a single guest frame. Health and metrics
	// frames are tiny; anything larger indicates a broken peer.
	maxGuestFrameBytes = 1 << 20
)

// guestPingSeq correlates ping/pong pairs across probes.
var guestPingSeq atomic.Int64

// guestProbe is the outcome of a successful guest-agent health probe
// (the agent answered the ping).
type guestProbe struct {
	// Metrics is the guest metrics snapshot. It is nil when metrics could
	// not be collected even though the ping succeeded; MetricsErr says why.
	Metrics    *protocol.MetricsReport
	MetricsErr error
}

// probeGuestAgent dials the guest agent through the hypervisor's vsock
// seam and runs the protocol-v2 health probe: hello handshake, ping, and a
// best-effort metrics request. A nil error means the guest agent answered
// the ping — the instance is guest-healthy. Metrics failures after a
// successful ping are reported in the result, not as a probe failure.
func probeGuestAgent(ctx context.Context, hv Hypervisor, name, imageID string, port uint32) (*guestProbe, error) {
	probeCtx, cancel := context.WithTimeout(ctx, guestProbeTimeout)
	defer cancel()

	conn, err := hv.VsockDial(probeCtx, name, port)
	if err != nil {
		return nil, fmt.Errorf("dialing guest agent: %w", err)
	}
	defer conn.Close()
	// Closing the connection on timeout/cancellation unblocks a pending
	// Receive; SetDeadline covers transports that honor deadlines.
	stop := context.AfterFunc(probeCtx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := probeCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	codec := protocol.NewCodecWithMaxFrameBytes(conn, maxGuestFrameBytes)
	if err := codec.Send(protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ImageID:         imageID,
		JobID:           "bahia-observe",
	}); err != nil {
		return nil, fmt.Errorf("sending hello: %w", err)
	}
	frame, err := codec.Receive()
	if err != nil {
		return nil, fmt.Errorf("receiving hello_ack: %w", err)
	}
	ack, ok := frame.(protocol.HelloAck)
	if !ok {
		return nil, guestFrameError("hello_ack", frame)
	}
	if err := protocol.ValidateHelloAck(ack, protocol.CurrentVersion, imageID); err != nil {
		return nil, err
	}

	seq := guestPingSeq.Add(1)
	if err := codec.Send(protocol.Ping{Seq: seq}); err != nil {
		return nil, fmt.Errorf("sending ping: %w", err)
	}
	frame, err = codec.Receive()
	if err != nil {
		return nil, fmt.Errorf("receiving pong: %w", err)
	}
	pong, ok := frame.(protocol.Pong)
	if !ok {
		return nil, guestFrameError("pong", frame)
	}
	if pong.Seq != seq {
		return nil, fmt.Errorf("guest agent answered ping %d with pong %d", seq, pong.Seq)
	}

	probe := &guestProbe{}
	if err := codec.Send(protocol.MetricsRequest{}); err != nil {
		probe.MetricsErr = fmt.Errorf("sending metrics_request: %w", err)
		return probe, nil
	}
	frame, err = codec.Receive()
	if err != nil {
		probe.MetricsErr = fmt.Errorf("receiving metrics_report: %w", err)
		return probe, nil
	}
	report, ok := frame.(protocol.MetricsReport)
	if !ok {
		probe.MetricsErr = guestFrameError("metrics_report", frame)
		return probe, nil
	}
	probe.Metrics = &report
	return probe, nil
}

// guestFrameError turns an unexpected frame into a descriptive error,
// surfacing guest-agent error frames verbatim.
func guestFrameError(want string, frame protocol.Frame) error {
	if errFrame, ok := frame.(protocol.ErrorFrame); ok {
		return fmt.Errorf("guest agent error: %s", errFrame.Message)
	}
	return fmt.Errorf("expected %s frame, got %s", want, protocol.TypeOf(frame))
}

// metricsMetadata renders a guest metrics snapshot into observation
// metadata values.
func metricsMetadata(m *protocol.MetricsReport) map[string]any {
	return map[string]any{
		"cpu_percent":        m.CPUPercent,
		"memory_used_bytes":  m.MemoryUsedBytes,
		"memory_total_bytes": m.MemoryTotalBytes,
		"disk_used_bytes":    m.DiskUsedBytes,
		"disk_total_bytes":   m.DiskTotalBytes,
		"uptime_seconds":     m.UptimeSeconds,
	}
}
