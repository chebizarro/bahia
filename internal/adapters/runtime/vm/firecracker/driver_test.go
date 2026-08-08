package firecracker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"go.uber.org/zap"
)

// fakeProc is one simulated VMM process.
type fakeProc struct {
	id     VMMIdentity
	marker string
	alive  bool
}

// fakeProcs is an in-memory ProcessManager. Identity semantics mirror the
// real one: Alive/Kill require PID, start time, and marker to all match.
type fakeProcs struct {
	mu       sync.Mutex
	nextPID  int
	procs    map[int]*fakeProc
	starts   []StartVMMRequest
	kills    int
	startErr error
}

func newFakeProcs() *fakeProcs {
	return &fakeProcs{nextPID: 1000, procs: map[int]*fakeProc{}}
}

func (f *fakeProcs) Start(_ context.Context, req StartVMMRequest) (VMMIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	if f.startErr != nil {
		return VMMIdentity{}, f.startErr
	}
	// Mimic the real manager: the console log exists after a start.
	file, err := os.OpenFile(req.ConsoleLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return VMMIdentity{}, err
	}
	file.Close()
	marker := ""
	for i, arg := range req.Args {
		if arg == "--api-sock" && i+1 < len(req.Args) {
			marker = req.Args[i+1]
		}
	}
	f.nextPID++
	id := VMMIdentity{PID: f.nextPID, StartTime: uint64(f.nextPID) * 7}
	f.procs[id.PID] = &fakeProc{id: id, marker: marker, alive: true}
	return id, nil
}

func (f *fakeProcs) Alive(id VMMIdentity, marker string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	proc, ok := f.procs[id.PID]
	return ok && proc.alive && proc.id.StartTime == id.StartTime && proc.marker == marker
}

func (f *fakeProcs) Kill(id VMMIdentity, marker string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if proc, ok := f.procs[id.PID]; ok && proc.id.StartTime == id.StartTime && proc.marker == marker {
		if proc.alive {
			proc.alive = false
			f.kills++
		}
	}
	return nil
}

// exitByMarker simulates a VMM exiting on its own (e.g. after a guest
// shutdown), without counting as a kill.
func (f *fakeProcs) exitByMarker(marker string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, proc := range f.procs {
		if proc.marker == marker {
			proc.alive = false
		}
	}
}

func (f *fakeProcs) killCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kills
}

// shortTempDir returns a short-path temp dir; unix socket paths (macOS
// limit: 104 bytes) derived from t.TempDir() test names can exceed it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fcdrv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newTestDriver(t *testing.T, procs *fakeProcs) (*Driver, string) {
	t.Helper()
	instancesDir := shortTempDir(t)
	driver := New(Config{
		InstancesDir:    instancesDir,
		Binary:          "firecracker",
		ShutdownTimeout: 150 * time.Millisecond,
		Processes:       procs,
	}, zap.NewNop())
	return driver, instancesDir
}

func fcSpec(t *testing.T, instancesDir, name string, cid uint32) vm.InstanceSpec {
	t.Helper()
	instanceDir := filepath.Join(instancesDir, name)
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	releaseDir := t.TempDir()
	kernelPath := filepath.Join(releaseDir, "kernel")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootfsPath := filepath.Join(releaseDir, "rootfs.ext4")
	if err := os.WriteFile(rootfsPath, []byte("rootfs-base-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	return vm.InstanceSpec{
		Name:        name,
		InstanceDir: instanceDir,
		Image: vm.ImageSpec{
			Format:         vm.FormatFirecrackerRootFS,
			Arch:           "x86_64",
			ReleaseDir:     releaseDir,
			KernelPath:     kernelPath,
			RootFSPath:     rootfsPath,
			ImageID:        "rel-001",
			ManifestDigest: "sha256:" + strings.Repeat("ab", 32),
		},
		VCPUs:    2,
		MemoryMB: 1024,
		VsockCID: cid,
	}
}

func mustCreate(t *testing.T, d *Driver, spec vm.InstanceSpec) {
	t.Helper()
	if err := d.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func mustStart(t *testing.T, d *Driver, name string) {
	t.Helper()
	if err := d.Start(context.Background(), name); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func readVMConfigFile(t *testing.T, instanceDir string) vmConfigFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(instanceDir, "vmconfig.json"))
	if err != nil {
		t.Fatalf("vmconfig.json missing: %v", err)
	}
	var cfg vmConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decoding vmconfig.json: %v", err)
	}
	return cfg
}

func TestCreateWritesConfigAndRootfsCopy(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 42)
	mustCreate(t, driver, spec)

	rootfs := filepath.Join(spec.InstanceDir, "rootfs.ext4")
	data, err := os.ReadFile(rootfs)
	if err != nil {
		t.Fatalf("writable rootfs copy missing: %v", err)
	}
	if string(data) != "rootfs-base-content" {
		t.Errorf("rootfs copy content mismatch: %q", data)
	}

	cfg := readVMConfigFile(t, spec.InstanceDir)
	if cfg.BootSource.KernelImagePath != spec.Image.KernelPath {
		t.Errorf("kernel should be referenced from the release dir, got %s", cfg.BootSource.KernelImagePath)
	}
	if !strings.Contains(cfg.BootSource.BootArgs, "console=ttyS0") {
		t.Errorf("expected default kernel args, got %q", cfg.BootSource.BootArgs)
	}
	if len(cfg.Drives) != 1 || cfg.Drives[0].PathOnHost != rootfs || !cfg.Drives[0].IsRootDevice || cfg.Drives[0].IsReadOnly {
		t.Errorf("unexpected drives: %+v", cfg.Drives)
	}
	if cfg.MachineConfig.VcpuCount != 2 || cfg.MachineConfig.MemSizeMib != 1024 || cfg.MachineConfig.Smt {
		t.Errorf("unexpected machine config: %+v", cfg.MachineConfig)
	}
	if cfg.Vsock == nil || cfg.Vsock.GuestCID != 42 || cfg.Vsock.UDSPath != filepath.Join(spec.InstanceDir, "vsock.sock") {
		t.Errorf("unexpected vsock config: %+v", cfg.Vsock)
	}

	state, err := driver.State(context.Background(), "i1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != vm.StateStopped {
		t.Errorf("created-but-unstarted instance should be stopped, got %s", state)
	}
}

func TestCreateWithoutCIDOmitsVsock(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	cfg := readVMConfigFile(t, spec.InstanceDir)
	if cfg.Vsock != nil {
		t.Errorf("cid=0 should omit the vsock device, got %+v", cfg.Vsock)
	}
}

func TestCreateRejectsWrongFormat(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	spec.Image.Format = vm.FormatQCOW2
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "firecracker-rootfs") {
		t.Fatalf("expected format rejection, got %v", err)
	}
}

func TestCreateRejectsMismatchedInstanceDir(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	spec.InstanceDir = t.TempDir()
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected instance-dir mismatch error, got %v", err)
	}
}

func TestCreateRejectsNetworkProfile(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	spec.NetworkProfile = "bridged"
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "vsock+console only") {
		t.Fatalf("expected network-profile rejection, got %v", err)
	}
}

func TestCreateRejectsExistingInstance(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestStartLaunchesDetachedVMM(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	if len(procs.starts) != 1 {
		t.Fatalf("expected one VMM start, got %d", len(procs.starts))
	}
	req := procs.starts[0]
	if req.Binary != "firecracker" {
		t.Errorf("unexpected binary %q", req.Binary)
	}
	args := strings.Join(req.Args, " ")
	apiSocket := filepath.Join(spec.InstanceDir, "api.socket")
	for _, want := range []string{
		"--id i1",
		"--api-sock " + apiSocket,
		"--config-file " + filepath.Join(spec.InstanceDir, "vmconfig.json"),
	} {
		if !strings.Contains(args, want) {
			t.Errorf("VMM args missing %q: %s", want, args)
		}
	}
	if req.ConsoleLogPath != filepath.Join(spec.InstanceDir, "console.log") {
		t.Errorf("unexpected console log path %q", req.ConsoleLogPath)
	}

	// The pidfile record must identify the process for later adoption.
	data, err := os.ReadFile(filepath.Join(spec.InstanceDir, "vmm.json"))
	if err != nil {
		t.Fatalf("vmm.json missing: %v", err)
	}
	var record vmmRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decoding vmm.json: %v", err)
	}
	if record.PID == 0 || record.StartTime == 0 || record.Marker != apiSocket {
		t.Errorf("unexpected vmm record: %+v", record)
	}

	state, err := driver.State(context.Background(), "i1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != vm.StateRunning {
		t.Errorf("expected running, got %s", state)
	}

	if err := driver.Start(context.Background(), "i1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected already-running error, got %v", err)
	}
}

func TestStartAbsentInstanceErrors(t *testing.T) {
	driver, _ := newTestDriver(t, newFakeProcs())
	err := driver.Start(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected absent-instance error, got %v", err)
	}
}

func TestStartClearsStaleSocketsAndRestartsAfterCrash(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	// Simulate a VMM crash that leaves the socket files behind.
	procs.exitByMarker(filepath.Join(spec.InstanceDir, "api.socket"))
	for _, stale := range []string{"api.socket", "vsock.sock"} {
		if err := os.WriteFile(filepath.Join(spec.InstanceDir, stale), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := driver.State(context.Background(), "i1")
	if state != vm.StateStopped {
		t.Fatalf("dead VMM should read as stopped, got %s", state)
	}

	mustStart(t, driver, "i1")
	if len(procs.starts) != 2 {
		t.Fatalf("expected a second VMM start, got %d", len(procs.starts))
	}
	state, _ = driver.State(context.Background(), "i1")
	if state != vm.StateRunning {
		t.Errorf("expected running after restart, got %s", state)
	}
}

// TestStopGracefulViaAPISocket runs a real HTTP server on the instance's
// API unix socket and verifies the graceful path: SendCtrlAltDel arrives,
// the guest "shuts down" (fake process exits), and no SIGKILL is sent.
func TestStopGracefulViaAPISocket(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	apiSocket := filepath.Join(spec.InstanceDir, "api.socket")
	listener, err := net.Listen("unix", apiSocket)
	if err != nil {
		t.Fatalf("listening on api socket: %v", err)
	}
	defer listener.Close()
	var gotRequest struct {
		sync.Mutex
		method, path, body string
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		scanner := bufio.NewScanner(r.Body)
		for scanner.Scan() {
			buf.WriteString(scanner.Text())
		}
		gotRequest.Lock()
		gotRequest.method, gotRequest.path, gotRequest.body = r.Method, r.URL.Path, buf.String()
		gotRequest.Unlock()
		procs.exitByMarker(apiSocket)
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.Stop(ctx, "i1", true); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	gotRequest.Lock()
	defer gotRequest.Unlock()
	if gotRequest.method != http.MethodPut || gotRequest.path != "/actions" || !strings.Contains(gotRequest.body, "SendCtrlAltDel") {
		t.Errorf("unexpected API request: %s %s %s", gotRequest.method, gotRequest.path, gotRequest.body)
	}
	if procs.killCount() != 0 {
		t.Errorf("graceful stop should not SIGKILL, got %d kills", procs.killCount())
	}
	state, _ := driver.State(context.Background(), "i1")
	if state != vm.StateStopped {
		t.Errorf("expected stopped, got %s", state)
	}
}

// TestStopGracefulFallsBackToKill covers the timeout path: the API socket
// is dead (no server), so after the shutdown timeout the VMM is SIGKILLed.
func TestStopGracefulFallsBackToKill(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.Stop(ctx, "i1", true); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if procs.killCount() != 1 {
		t.Errorf("expected SIGKILL fallback, got %d kills", procs.killCount())
	}
	state, _ := driver.State(context.Background(), "i1")
	if state != vm.StateStopped {
		t.Errorf("expected stopped, got %s", state)
	}
}

func TestForcedStopKillsImmediately(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	if err := driver.Stop(context.Background(), "i1", false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if procs.killCount() != 1 {
		t.Errorf("expected one SIGKILL, got %d", procs.killCount())
	}
}

func TestStopAbsentInstanceErrors(t *testing.T) {
	driver, _ := newTestDriver(t, newFakeProcs())
	err := driver.Stop(context.Background(), "ghost", true)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected absent-instance error, got %v", err)
	}
}

func TestStopAlreadyStoppedIsNoOp(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	if err := driver.Stop(context.Background(), "i1", true); err != nil {
		t.Fatalf("expected no-op stop, got %v", err)
	}
	if procs.killCount() != 0 {
		t.Errorf("no kill expected, got %d", procs.killCount())
	}
}

func TestDestroyKillsAndRemovesDefinition(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 7)
	mustCreate(t, driver, spec)
	mustStart(t, driver, "i1")

	if err := driver.Destroy(context.Background(), "i1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if procs.killCount() != 1 {
		t.Errorf("expected running VMM to be killed, got %d kills", procs.killCount())
	}
	state, err := driver.State(context.Background(), "i1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != vm.StateAbsent {
		t.Errorf("expected absent after destroy, got %s", state)
	}
	for _, file := range []string{"vmconfig.json", "vmm.json"} {
		if _, err := os.Stat(filepath.Join(spec.InstanceDir, file)); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", file, err)
		}
	}
}

func TestDestroyAbsentIsIdempotent(t *testing.T) {
	driver, _ := newTestDriver(t, newFakeProcs())
	if err := driver.Destroy(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected idempotent destroy, got %v", err)
	}
}

func TestStateCorruptRecordSurfaces(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	if err := os.WriteFile(filepath.Join(spec.InstanceDir, "vmm.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := driver.State(context.Background(), "i1")
	if err == nil || !strings.Contains(err.Error(), "decoding VMM record") {
		t.Fatalf("expected corrupt-record error, got %v", err)
	}
}

func TestListFiltersDefinedInstancesByPrefix(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	mustCreate(t, driver, fcSpec(t, instancesDir, "bahia-aaa-api", 0))
	mustCreate(t, driver, fcSpec(t, instancesDir, "bahia-bbb-worker", 0))
	mustCreate(t, driver, fcSpec(t, instancesDir, "other-vm", 0))
	// A directory without a VM config (core metadata only) is not listed.
	if err := os.MkdirAll(filepath.Join(instancesDir, "bahia-ccc-empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	names, err := driver.List(context.Background(), "bahia-")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "bahia-aaa-api" || names[1] != "bahia-bbb-worker" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestListMissingInstancesDir(t *testing.T) {
	driver := New(Config{InstancesDir: filepath.Join(shortTempDir(t), "missing"), Processes: newFakeProcs()}, zap.NewNop())
	names, err := driver.List(context.Background(), "bahia-")
	if err != nil || names != nil {
		t.Fatalf("expected empty result, got %v / %v", names, err)
	}
}

func TestConsoleLogPath(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	path, err := driver.ConsoleLogPath("i1")
	if err != nil {
		t.Fatalf("ConsoleLogPath: %v", err)
	}
	if path != filepath.Join(instancesDir, "i1", "console.log") {
		t.Errorf("unexpected console log path: %s", path)
	}
}

// TestVsockDialHandshake runs a real listener on the instance's hybrid
// vsock unix socket and verifies the CONNECT handshake and that no guest
// payload bytes are swallowed by the handshake reader.
func TestVsockDialHandshake(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 9)
	mustCreate(t, driver, spec)

	udsPath := filepath.Join(spec.InstanceDir, "vsock.sock")
	listener, err := net.Listen("unix", udsPath)
	if err != nil {
		t.Fatalf("listening on vsock socket: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil || line != "CONNECT 1024\n" {
			return
		}
		// Reply and immediately push guest payload in the same write.
		_, _ = conn.Write([]byte("OK 5001\npayload\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := driver.VsockDial(ctx, "i1", 1024)
	if err != nil {
		t.Fatalf("VsockDial: %v", err)
	}
	defer conn.Close()
	payload, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading guest payload: %v", err)
	}
	if payload != "payload\n" {
		t.Errorf("unexpected payload %q", payload)
	}
}

func TestVsockDialRefused(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 9)
	mustCreate(t, driver, spec)

	udsPath := filepath.Join(spec.InstanceDir, "vsock.sock")
	listener, err := net.Listen("unix", udsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
		_, _ = conn.Write([]byte("KO\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = driver.VsockDial(ctx, "i1", 1024)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("expected refusal error, got %v", err)
	}
}

func TestVsockDialWithoutDevice(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	_, err := driver.VsockDial(context.Background(), "i1", 1024)
	if err == nil || !strings.Contains(err.Error(), "no vsock device") {
		t.Fatalf("expected no-vsock error, got %v", err)
	}
}

func TestAdoptOrphansAdoptsRunningAndReapsDead(t *testing.T) {
	procs := newFakeProcs()
	driver, instancesDir := newTestDriver(t, procs)
	live := fcSpec(t, instancesDir, "i-live", 0)
	mustCreate(t, driver, live)
	mustStart(t, driver, "i-live")
	dead := fcSpec(t, instancesDir, "i-dead", 0)
	mustCreate(t, driver, dead)
	mustStart(t, driver, "i-dead")

	// The dead instance's VMM exits while "bahia was down", leaving
	// stale socket files behind.
	procs.exitByMarker(filepath.Join(dead.InstanceDir, "api.socket"))
	for _, stale := range []string{"api.socket", "vsock.sock"} {
		if err := os.WriteFile(filepath.Join(dead.InstanceDir, stale), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A core-metadata-only directory must be left alone.
	foreignDir := filepath.Join(instancesDir, "i-core-only")
	if err := os.MkdirAll(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(foreignDir, "metadata.json")
	if err := os.WriteFile(foreignFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := driver.AdoptOrphans(context.Background()); err != nil {
		t.Fatalf("AdoptOrphans: %v", err)
	}

	state, err := driver.State(context.Background(), "i-live")
	if err != nil || state != vm.StateRunning {
		t.Errorf("live instance should stay running, got %s / %v", state, err)
	}
	for _, stale := range []string{"vmm.json", "api.socket", "vsock.sock"} {
		if _, err := os.Stat(filepath.Join(dead.InstanceDir, stale)); !os.IsNotExist(err) {
			t.Errorf("expected dead instance %s reaped, stat err=%v", stale, err)
		}
	}
	state, err = driver.State(context.Background(), "i-dead")
	if err != nil || state != vm.StateStopped {
		t.Errorf("dead instance should be stopped, got %s / %v", state, err)
	}
	if _, err := os.Stat(foreignFile); err != nil {
		t.Errorf("core-only directory should be untouched: %v", err)
	}

	// The reaped instance can be started again.
	mustStart(t, driver, "i-dead")
}

func TestAdoptOrphansReapsCorruptRecord(t *testing.T) {
	driver, instancesDir := newTestDriver(t, newFakeProcs())
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	if err := os.WriteFile(filepath.Join(spec.InstanceDir, "vmm.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driver.AdoptOrphans(context.Background()); err != nil {
		t.Fatalf("AdoptOrphans: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spec.InstanceDir, "vmm.json")); !os.IsNotExist(err) {
		t.Errorf("expected corrupt record reaped, stat err=%v", err)
	}
	state, err := driver.State(context.Background(), "i1")
	if err != nil || state != vm.StateStopped {
		t.Errorf("expected stopped after reap, got %s / %v", state, err)
	}
}

func TestAdoptOrphansMissingDirIsNoOp(t *testing.T) {
	driver := New(Config{InstancesDir: filepath.Join(shortTempDir(t), "missing"), Processes: newFakeProcs()}, zap.NewNop())
	if err := driver.AdoptOrphans(context.Background()); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestStartFailureSurfaces(t *testing.T) {
	procs := newFakeProcs()
	procs.startErr = fmt.Errorf("no KVM on this host")
	driver, instancesDir := newTestDriver(t, procs)
	spec := fcSpec(t, instancesDir, "i1", 0)
	mustCreate(t, driver, spec)
	err := driver.Start(context.Background(), "i1")
	if err == nil || !strings.Contains(err.Error(), "no KVM") {
		t.Fatalf("expected start failure to surface, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(spec.InstanceDir, "vmm.json")); !os.IsNotExist(statErr) {
		t.Errorf("no vmm record expected after failed start, stat err=%v", statErr)
	}
}
