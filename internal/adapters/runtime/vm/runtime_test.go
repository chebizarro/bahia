package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// fakeHypervisor is an in-memory Hypervisor for core unit tests.
type fakeHypervisor struct {
	mu        sync.Mutex
	instances map[string]InstanceState
	specs     map[string]InstanceSpec
	calls     []string

	createErr  error
	startErr   error
	stopErr    error
	destroyErr error
	stateErr   error

	consoleDir string
}

func newFakeHypervisor(consoleDir string) *fakeHypervisor {
	return &fakeHypervisor{
		instances:  make(map[string]InstanceState),
		specs:      make(map[string]InstanceSpec),
		consoleDir: consoleDir,
	}
}

func (f *fakeHypervisor) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeHypervisor) Create(_ context.Context, spec InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("create:" + spec.Name)
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.instances[spec.Name]; ok {
		return fmt.Errorf("instance %s already exists", spec.Name)
	}
	f.instances[spec.Name] = StateStopped
	f.specs[spec.Name] = spec
	return nil
}

func (f *fakeHypervisor) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("start:" + name)
	if f.startErr != nil {
		return f.startErr
	}
	if _, ok := f.instances[name]; !ok {
		return fmt.Errorf("instance %s does not exist", name)
	}
	f.instances[name] = StateRunning
	return nil
}

func (f *fakeHypervisor) Stop(_ context.Context, name string, graceful bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("stop:%s:graceful=%t", name, graceful))
	if f.stopErr != nil {
		return f.stopErr
	}
	if _, ok := f.instances[name]; !ok {
		return fmt.Errorf("instance %s does not exist", name)
	}
	f.instances[name] = StateStopped
	return nil
}

func (f *fakeHypervisor) Destroy(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("destroy:" + name)
	if f.destroyErr != nil {
		return f.destroyErr
	}
	delete(f.instances, name)
	delete(f.specs, name)
	return nil
}

func (f *fakeHypervisor) State(_ context.Context, name string) (InstanceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateErr != nil {
		return StateUnknown, f.stateErr
	}
	state, ok := f.instances[name]
	if !ok {
		return StateAbsent, nil
	}
	return state, nil
}

func (f *fakeHypervisor) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.instances {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (f *fakeHypervisor) ConsoleLogPath(name string) (string, error) {
	return filepath.Join(f.consoleDir, name+".log"), nil
}

func (f *fakeHypervisor) VsockDial(context.Context, string, uint32) (net.Conn, error) {
	return nil, fmt.Errorf("not implemented in fake")
}

type coreFixture struct {
	rt     *Runtime
	hv     *fakeHypervisor
	root   string
	digest string
}

func newCoreFixture(t *testing.T, runtimeType domain.RuntimeType) *coreFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := t.TempDir()
	format := FormatQCOW2
	if runtimeType == domain.RuntimeTypeVMFirecracker {
		format = FormatFirecrackerRootFS
	}
	digest := writeRelease(t, root, "vm/base", "rel-001", format, false)
	hv := newFakeHypervisor(t.TempDir())
	rt, err := NewRuntime(Config{
		RuntimeType: runtimeType,
		StateDir:    stateDir,
		ImageRoot:   root,
	}, hv, zap.NewNop())
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return &coreFixture{rt: rt, hv: hv, root: root, digest: digest}
}

func TestNewRuntimeValidation(t *testing.T) {
	hv := newFakeHypervisor(t.TempDir())
	if _, err := NewRuntime(Config{RuntimeType: domain.RuntimeTypeDocker, StateDir: "a", ImageRoot: "b"}, hv, nil); err == nil {
		t.Error("expected rejection of non-VM runtime type")
	}
	if _, err := NewRuntime(Config{RuntimeType: domain.RuntimeTypeVMQEMU, ImageRoot: "b"}, hv, nil); err == nil || !strings.Contains(err.Error(), "state_dir") {
		t.Errorf("expected state_dir error, got %v", err)
	}
	if _, err := NewRuntime(Config{RuntimeType: domain.RuntimeTypeVMQEMU, StateDir: "a"}, hv, nil); err == nil || !strings.Contains(err.Error(), "image_root") {
		t.Errorf("expected image_root error, got %v", err)
	}
	if _, err := NewRuntime(Config{RuntimeType: domain.RuntimeTypeVMQEMU, StateDir: "a", ImageRoot: "b"}, nil, nil); err == nil || !strings.Contains(err.Error(), "hypervisor") {
		t.Errorf("expected hypervisor error, got %v", err)
	}
}

func TestDeployHappyPath(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	envID := uuid.New()

	err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: envID.String()},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	short := strings.ReplaceAll(envID.String(), "-", "")[:8]
	wantName := "bahia-" + short + "-api"
	if state := fx.hv.instances[wantName]; state != StateRunning {
		t.Fatalf("expected %s running, got %q (instances: %v)", wantName, state, fx.hv.instances)
	}
	spec := fx.hv.specs[wantName]
	if spec.Image.ManifestDigest != fx.digest {
		t.Errorf("spec manifest digest mismatch: %s", spec.Image.ManifestDigest)
	}
	if spec.Image.DiskPath == "" || spec.Image.Format != FormatQCOW2 {
		t.Errorf("unexpected image spec: %+v", spec.Image)
	}
	if spec.VCPUs != DefaultVCPUs || spec.MemoryMB != DefaultMemoryMB {
		t.Errorf("expected resource defaults, got %d/%d", spec.VCPUs, spec.MemoryMB)
	}

	md, err := ReadInstanceMetadata(filepath.Join(InstancesDir(fx.rt.cfg.StateDir), wantName))
	if err != nil || md == nil {
		t.Fatalf("metadata missing: %v", err)
	}
	if md.ServiceName != "api" || md.ImageDigest != fx.digest || md.EnvironmentID != envID.String() {
		t.Errorf("unexpected metadata: %+v", md)
	}
	if md.SpecHash == "" || !strings.HasPrefix(md.SpecHash, "sha256:") {
		t.Errorf("expected spec hash, got %q", md.SpecHash)
	}
	if md.ImageID != "rel-001" {
		t.Errorf("expected image_id rel-001, got %q", md.ImageID)
	}
}

func TestDeployWithoutEnvLabelUsesHashFallback(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	if err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(fx.hv.instances) != 1 {
		t.Fatalf("expected one instance, got %v", fx.hv.instances)
	}
	for name := range fx.hv.instances {
		if !strings.HasPrefix(name, "bahia-") || !strings.HasSuffix(name, "-api") {
			t.Errorf("unexpected instance name %q", name)
		}
	}
}

func TestDeployReplacesExistingInstance(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	envA := uuid.New()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: envA.String()},
	}); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	oldName := "bahia-" + strings.ReplaceAll(envA.String(), "-", "")[:8] + "-api"

	envB := uuid.New()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: envB.String()},
	}); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	if _, ok := fx.hv.instances[oldName]; ok {
		t.Errorf("old instance %s should have been destroyed", oldName)
	}
	if len(fx.hv.instances) != 1 {
		t.Errorf("expected exactly one instance, got %v", fx.hv.instances)
	}
	found := false
	for _, call := range fx.hv.calls {
		if call == "destroy:"+oldName {
			found = true
		}
	}
	if !found {
		t.Errorf("expected destroy call for %s, calls: %v", oldName, fx.hv.calls)
	}
}

func TestDeployRejectsTagOnlyRef(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	err := fx.rt.Deploy(context.Background(), "api", "vm/base:v3", DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "tag-only") {
		t.Fatalf("expected tag-only rejection, got %v", err)
	}
	if len(fx.hv.calls) != 0 {
		t.Errorf("no hypervisor calls expected, got %v", fx.hv.calls)
	}
}

func TestDeployRejectsUnsupportedOptions(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	cases := []DeployOptions{
		{Ports: []string{"8080:80"}},
		{Volumes: []string{"/a:/b"}},
		{Command: []string{"sh"}},
		{Entrypoint: []string{"sh"}},
		{WorkingDir: "/srv"},
		{NetworkMode: "host"},
		{Environment: map[string]string{"A": "b"}},
		{Restart: "always"},
		{PullAlways: true},
	}
	for i, opts := range cases {
		err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, opts)
		if err == nil || !strings.Contains(err.Error(), "not supported by VM runtimes") {
			t.Errorf("case %d: expected unsupported-options rejection, got %v", i, err)
		}
	}
}

func TestDeployRejectsInvalidEnvLabel(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: "not-a-uuid"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid UUID") {
		t.Fatalf("expected invalid UUID error, got %v", err)
	}
}

func TestDeployFailedStartCleansUp(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	fx.hv.startErr = fmt.Errorf("boot failure")
	err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "boot failure") {
		t.Fatalf("expected start failure, got %v", err)
	}
	if len(fx.hv.instances) != 0 {
		t.Errorf("expected instance destroyed after failed start, got %v", fx.hv.instances)
	}
	entries, _ := os.ReadDir(InstancesDir(fx.rt.cfg.StateDir))
	if len(entries) != 0 {
		t.Errorf("expected instance dir cleaned up, got %v", entries)
	}
}

func TestUndeploy(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := fx.rt.Undeploy(ctx, "api"); err != nil {
		t.Fatalf("Undeploy: %v", err)
	}
	if len(fx.hv.instances) != 0 {
		t.Errorf("expected no instances, got %v", fx.hv.instances)
	}
	entries, _ := os.ReadDir(InstancesDir(fx.rt.cfg.StateDir))
	if len(entries) != 0 {
		t.Errorf("expected no instance dirs, got %d", len(entries))
	}
}

func TestUndeployAbsentIsNoOp(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	if err := fx.rt.Undeploy(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestObserveNoInstance(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	obs, err := fx.rt.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.HealthStatus != domain.HealthStatusStopped {
		t.Errorf("expected stopped, got %s", obs.HealthStatus)
	}
	if obs.ObservedImageDigest != "" {
		t.Errorf("expected empty digest, got %s", obs.ObservedImageDigest)
	}
	if obs.Source != "vm-qemu" {
		t.Errorf("expected source vm-qemu, got %s", obs.Source)
	}
}

func TestObserveStates(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: envID.String()},
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	name := "bahia-" + strings.ReplaceAll(envID.String(), "-", "")[:8] + "-api"

	cases := []struct {
		state InstanceState
		want  domain.HealthStatus
	}{
		{StateRunning, domain.HealthStatusHealthy},
		{StateStopped, domain.HealthStatusStopped},
		{StateCrashed, domain.HealthStatusUnhealthy},
		{StatePaused, domain.HealthStatusUnhealthy},
		{StateUnknown, domain.HealthStatusUnknown},
	}
	for _, tc := range cases {
		fx.hv.mu.Lock()
		fx.hv.instances[name] = tc.state
		fx.hv.mu.Unlock()
		obs, err := fx.rt.Observe(ctx, serviceID, envID, "api")
		if err != nil {
			t.Fatalf("Observe(%s): %v", tc.state, err)
		}
		if obs.HealthStatus != tc.want {
			t.Errorf("state %s: expected %s, got %s", tc.state, tc.want, obs.HealthStatus)
		}
		if obs.ObservedImageDigest != fx.digest {
			t.Errorf("state %s: expected digest %s, got %s", tc.state, fx.digest, obs.ObservedImageDigest)
		}
		if obs.ObservedContainerID != name {
			t.Errorf("state %s: expected instance name %s, got %s", tc.state, name, obs.ObservedContainerID)
		}
		if obs.Metadata["hypervisor_state"] != string(tc.state) {
			t.Errorf("state %s: metadata mismatch %v", tc.state, obs.Metadata)
		}
		if obs.Metadata["spec_hash"] == "" {
			t.Errorf("state %s: expected spec_hash metadata", tc.state)
		}
	}
}

func TestRestartAndStop(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if err := fx.rt.Restart(ctx, "api"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	var sawGracefulStop, sawSecondStart bool
	starts := 0
	for _, call := range fx.hv.calls {
		if strings.HasPrefix(call, "stop:") && strings.HasSuffix(call, "graceful=true") {
			sawGracefulStop = true
		}
		if strings.HasPrefix(call, "start:") {
			starts++
			if starts == 2 {
				sawSecondStart = true
			}
		}
	}
	if !sawGracefulStop || !sawSecondStart {
		t.Errorf("expected graceful stop + restart, calls: %v", fx.hv.calls)
	}

	if err := fx.rt.Stop(ctx, "api"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for name, state := range fx.hv.instances {
		if state != StateStopped {
			t.Errorf("expected %s stopped, got %s", name, state)
		}
	}

	if err := fx.rt.Restart(ctx, "ghost"); err == nil {
		t.Error("expected restart error for unknown target")
	}
	if err := fx.rt.Stop(ctx, "ghost"); err == nil {
		t.Error("expected stop error for unknown target")
	}
}

func TestStreamLogsTail(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	envID := uuid.New()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{
		Labels: map[string]string{LabelEnvironmentID: envID.String()},
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	name := "bahia-" + strings.ReplaceAll(envID.String(), "-", "")[:8] + "-api"
	logPath, _ := fx.hv.ConsoleLogPath(name)
	if err := os.WriteFile(logPath, []byte("boot line 1\nboot line 2\nboot line 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch, err := fx.rt.StreamLogs(ctx, "api", LogOptions{Tail: 2})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	var lines []string
	for entry := range ch {
		lines = append(lines, entry.Message)
	}
	if len(lines) != 2 || lines[0] != "boot line 2" || lines[1] != "boot line 3" {
		t.Errorf("unexpected tail lines: %v", lines)
	}
}

func TestStreamLogsFollow(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	var name string
	for n := range fx.hv.instances {
		name = n
	}
	logPath, _ := fx.hv.ConsoleLogPath(name)
	if err := os.WriteFile(logPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch, err := fx.rt.StreamLogs(ctx, "api", LogOptions{Follow: true})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	entry := <-ch
	if entry.Message != "first" {
		t.Fatalf("expected first line, got %q", entry.Message)
	}
	// Append a line and expect it via follow polling.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	select {
	case entry = <-ch:
		if entry.Message != "second" {
			t.Errorf("expected second line, got %q", entry.Message)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for followed line")
	}
	cancel()
}

func TestStreamLogsMissingConsole(t *testing.T) {
	fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
	ctx := context.Background()
	if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	_, err := fx.rt.StreamLogs(ctx, "api", LogOptions{})
	if err == nil || !strings.Contains(err.Error(), "console log not available") {
		t.Fatalf("expected console-log error, got %v", err)
	}
}

func TestInstanceNameSanitization(t *testing.T) {
	envID := uuid.MustParse("aabbccdd-0000-0000-0000-000000000000")
	name := InstanceName(envID, "My_Weird  Service!!")
	if name != "bahia-aabbccdd-my-weird-service" {
		t.Errorf("unexpected instance name %q", name)
	}
	long := InstanceName(envID, strings.Repeat("x", 100))
	if len(long) > len("bahia-aabbccdd-")+maxServiceNamePart {
		t.Errorf("instance name too long: %q", long)
	}
}

func TestComputeSpecHashDeterministic(t *testing.T) {
	a := ComputeSpecHash("vm-qemu", "sha256:abc", 2, 2048, "")
	b := ComputeSpecHash("vm-qemu", "sha256:abc", 2, 2048, "")
	if a != b {
		t.Error("spec hash should be deterministic")
	}
	c := ComputeSpecHash("vm-qemu", "sha256:abc", 4, 2048, "")
	if a == c {
		t.Error("spec hash should change with resources")
	}
}
