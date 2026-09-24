package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap/zaptest/observer"
	"net"
	"strings"
	"testing"

	"git.sharegap.net/cascadia/cascadia-go/worker/vmexec/protocol"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// newGuestFixture builds a core fixture whose release manifest declares the
// given agent protocol version and whose runtime has the given vsock guest
// port configured.
func newGuestFixture(t *testing.T, agentVersion, vsockPort int) *coreFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := t.TempDir()
	digest := writeReleaseAgent(t, root, "vm/base", "rel-001", FormatQCOW2, false, agentVersion)
	hv := newFakeHypervisor(t.TempDir())
	rt, err := NewRuntime(Config{
		RuntimeType:    domain.RuntimeTypeVMQEMU,
		StateDir:       stateDir,
		ImageRoot:      root,
		VsockGuestPort: vsockPort,
	}, hv, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return &coreFixture{rt: rt, hv: hv, root: root, digest: digest}
}

// fakeGuestAgent serves one protocol connection like a v2 service-mode
// guest agent: hello handshake, then pong every ping and answer metrics
// requests until the connection closes.
func fakeGuestAgent(t *testing.T, conn net.Conn, imageID string, metrics *protocol.MetricsReport, metricsErr string) {
	t.Helper()
	defer func() {
		checkTestError(t, conn.Close())
	}()
	codec := protocol.NewCodec(conn)
	frame, err := codec.Receive()
	if err != nil {
		return
	}
	hello, ok := frame.(protocol.Hello)
	if !ok {
		_ = codec.Send(protocol.ErrorFrame{Message: "expected hello"})
		return
	}
	version, err := protocol.NegotiateHello(hello, imageID)
	if err != nil {
		_ = codec.Send(protocol.ErrorFrame{Message: err.Error()})
		return
	}
	if err := codec.Send(protocol.HelloAck{ProtocolVersion: version, ImageID: imageID}); err != nil {
		return
	}
	for {
		frame, err = codec.Receive()
		if err != nil {
			return
		}
		switch request := frame.(type) {
		case protocol.Ping:
			if err := codec.Send(protocol.Pong(request)); err != nil {
				return
			}
		case protocol.MetricsRequest:
			if metricsErr != "" {
				_ = codec.Send(protocol.ErrorFrame{Message: metricsErr})
				return
			}
			if err := codec.Send(*metrics); err != nil {
				return
			}
		default:
			_ = codec.Send(protocol.ErrorFrame{Message: fmt.Sprintf("unexpected frame %s", protocol.TypeOf(frame))})
			return
		}
	}
}

func deployRunning(t *testing.T, fx *coreFixture) {
	t.Helper()
	if err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}

func TestObserveGuestAgentHealthyWithMetrics(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	metrics := &protocol.MetricsReport{
		CPUPercent:       12.5,
		MemoryUsedBytes:  512 << 20,
		MemoryTotalBytes: 2048 << 20,
		DiskUsedBytes:    1 << 30,
		DiskTotalBytes:   8 << 30,
		UptimeSeconds:    3600,
	}
	fx.hv.vsockDial = func(_ context.Context, name string, port uint32) (net.Conn, error) {
		if port != 5000 {
			t.Errorf("expected guest port 5000, got %d", port)
		}
		host, guest := net.Pipe()
		go fakeGuestAgent(t, guest, "rel-001", metrics, "")
		return host, nil
	}
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("expected healthy, got %s (metadata %v)", obs.HealthStatus, obs.Metadata)
	}
	if obs.Metadata["guest_agent"] != "ok" {
		t.Errorf("expected guest_agent ok, got %v", obs.Metadata["guest_agent"])
	}
	gm, ok := obs.Metadata["guest_metrics"].(map[string]any)
	if !ok {
		t.Fatalf("expected guest_metrics metadata, got %v", obs.Metadata)
	}
	if gm["cpu_percent"] != 12.5 {
		t.Errorf("unexpected cpu_percent: %v", gm["cpu_percent"])
	}
	if gm["memory_total_bytes"] != int64(2048<<20) {
		t.Errorf("unexpected memory_total_bytes: %v", gm["memory_total_bytes"])
	}
	if gm["uptime_seconds"] != float64(3600) {
		t.Errorf("unexpected uptime_seconds: %v", gm["uptime_seconds"])
	}
}

func TestGuestDiagnosticsNeverLeakToMetadataOrLogs(t *testing.T) {
	const secret = "password=super-secret-nsec-token"
	for _, phase := range []string{"hello", "metrics", "dial"} {
		t.Run(phase, func(t *testing.T) {
			fx := newGuestFixture(t, 2, 5000)
			logs, observed := observer.New(zap.DebugLevel)
			fx.rt.logger = zap.New(logs)
			fx.hv.vsockDial = func(context.Context, string, uint32) (net.Conn, error) {
				if phase == "dial" {
					return nil, fmt.Errorf("%s", secret)
				}
				host, guest := net.Pipe()
				go func() {
					defer func() {
						if err := guest.Close(); err != nil {
							t.Error(err)
						}
					}()
					if phase == "metrics" {
						fakeGuestAgent(t, guest, "rel-001", nil, secret)
						return
					}
					codec := protocol.NewCodec(guest)
					if _, err := codec.Receive(); err == nil {
						_ = codec.Send(protocol.ErrorFrame{Message: secret})
					}
				}()
				return host, nil
			}
			deployRunning(t, fx)
			result, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.Metadata)
			if strings.Contains(string(data), secret) {
				t.Fatal("guest secret leaked into observation")
			}
			for _, entry := range observed.All() {
				data, _ := json.Marshal(entry.ContextMap())
				if strings.Contains(entry.Message+string(data), secret) {
					t.Fatal("guest secret leaked into logs")
				}
			}
		})
	}
}

func TestObserveGuestAgentUnreachableDegrades(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	fx.hv.vsockDial = func(context.Context, string, uint32) (net.Conn, error) {
		return nil, fmt.Errorf("connection refused")
	}
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe must not fail on guest probe failure: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusUnhealthy {
		t.Errorf("expected unhealthy (degraded), got %s", obs.HealthStatus)
	}
	if obs.Metadata["guest_agent"] != "unreachable" {
		t.Errorf("expected guest_agent unreachable, got %v", obs.Metadata["guest_agent"])
	}
	if msg, _ := obs.Metadata["guest_agent_error"].(string); msg != "guest_probe_failed" {
		t.Errorf("expected dial error in metadata, got %v", obs.Metadata["guest_agent_error"])
	}
	if obs.Metadata["hypervisor_state"] != string(StateRunning) {
		t.Errorf("hypervisor state must still be reported, got %v", obs.Metadata["hypervisor_state"])
	}
}

func TestObserveGuestAgentErrorFrameDegrades(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	fx.hv.vsockDial = func(context.Context, string, uint32) (net.Conn, error) {
		host, guest := net.Pipe()
		go func() {
			defer func() {
				checkTestError(t, guest.Close())
			}()
			codec := protocol.NewCodec(guest)
			if _, err := codec.Receive(); err != nil {
				return
			}
			_ = codec.Send(protocol.ErrorFrame{Message: "image id mismatch"})
		}()
		return host, nil
	}
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusUnhealthy {
		t.Errorf("expected unhealthy, got %s", obs.HealthStatus)
	}
	if msg, _ := obs.Metadata["guest_agent_error"].(string); msg != "guest_probe_failed" {
		t.Errorf("expected guest error surfaced, got %v", obs.Metadata["guest_agent_error"])
	}
}

func TestObserveMetricsFailureStillHealthy(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	fx.hv.vsockDial = func(_ context.Context, name string, port uint32) (net.Conn, error) {
		host, guest := net.Pipe()
		go fakeGuestAgent(t, guest, "rel-001", nil, "metrics collection failed")
		return host, nil
	}
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("ping succeeded, expected healthy, got %s (metadata %v)", obs.HealthStatus, obs.Metadata)
	}
	if _, ok := obs.Metadata["guest_metrics"]; ok {
		t.Error("expected no guest_metrics after metrics failure")
	}
	if msg, _ := obs.Metadata["guest_metrics_error"].(string); msg != "guest_metrics_failed" {
		t.Errorf("expected metrics error in metadata, got %v", obs.Metadata["guest_metrics_error"])
	}
}

func TestObservePreV2AgentSkipsProbe(t *testing.T) {
	fx := newGuestFixture(t, 1, 5000)
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("hypervisor-running with pre-v2 agent must be healthy, got %s", obs.HealthStatus)
	}
	if fx.hv.vsockCalls != 0 {
		t.Errorf("expected no vsock dials for pre-v2 image, got %d", fx.hv.vsockCalls)
	}
	if _, ok := obs.Metadata["guest_agent"]; ok {
		t.Errorf("expected no guest_agent metadata, got %v", obs.Metadata)
	}
}

func TestObserveNoVsockConfiguredSkipsProbe(t *testing.T) {
	fx := newGuestFixture(t, 2, 0)
	deployRunning(t, fx)

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("hypervisor-running without vsock must be healthy, got %s", obs.HealthStatus)
	}
	if fx.hv.vsockCalls != 0 {
		t.Errorf("expected no vsock dials without a configured port, got %d", fx.hv.vsockCalls)
	}
}

func TestObserveStoppedInstanceSkipsProbe(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	deployRunning(t, fx)
	for name := range fx.hv.instances {
		fx.hv.instances[name] = StateStopped
	}

	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusStopped {
		t.Errorf("expected stopped, got %s", obs.HealthStatus)
	}
	if fx.hv.vsockCalls != 0 {
		t.Errorf("expected no vsock dials for stopped instance, got %d", fx.hv.vsockCalls)
	}
}

func TestDeployRecordsAgentProtocolVersion(t *testing.T) {
	fx := newGuestFixture(t, 2, 5000)
	deployRunning(t, fx)
	matches, err := FindInstancesByService(InstancesDir(fx.rt.cfg.StateDir), "api")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one instance, got %v (%v)", matches, err)
	}
	if matches[0].AgentProtocolVersion != 2 {
		t.Errorf("expected agent protocol version 2 recorded, got %d", matches[0].AgentProtocolVersion)
	}
	if matches[0].VsockCID < 3 {
		t.Errorf("expected derived vsock CID >= 3, got %d", matches[0].VsockCID)
	}
}
