package libvirt

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"go.uber.org/zap"
)

// fakeRunner scripts command results by matching a substring of the full
// command line, recording every invocation.
type fakeRunner struct {
	calls     []string
	responses []fakeResponse
}

type fakeResponse struct {
	match  string
	output string
	err    error
	// once responses are consumed in order when multiple match (for
	// state-transition sequences).
	used bool
	once bool
}

func (f *fakeRunner) run(_ context.Context, binary string, args ...string) ([]byte, error) {
	line := binary + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for i := range f.responses {
		r := &f.responses[i]
		if r.once && r.used {
			continue
		}
		if strings.Contains(line, r.match) {
			r.used = true
			return []byte(r.output), r.err
		}
	}
	return nil, nil
}

func (f *fakeRunner) callsMatching(sub string) []string {
	var out []string
	for _, call := range f.calls {
		if strings.Contains(call, sub) {
			out = append(out, call)
		}
	}
	return out
}

func newTestDriver(t *testing.T, runner *fakeRunner) (*Driver, string) {
	t.Helper()
	instancesDir := t.TempDir()
	driver := New(Config{
		URI:              "qemu:///session",
		InstancesDir:     instancesDir,
		FirmwareCodePath: "/fw/OVMF_CODE.fd",
		Runner:           runner.run,
	}, zap.NewNop())
	return driver, instancesDir
}

func qcow2Spec(t *testing.T, instancesDir, name string, uefi bool, cid uint32) vm.InstanceSpec {
	t.Helper()
	instanceDir := filepath.Join(instancesDir, name)
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	releaseDir := t.TempDir()
	diskPath := filepath.Join(releaseDir, "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := vm.InstanceSpec{
		Name:        name,
		InstanceDir: instanceDir,
		Image: vm.ImageSpec{
			Format:         vm.FormatQCOW2,
			Arch:           "x86_64",
			ReleaseDir:     releaseDir,
			DiskPath:       diskPath,
			ImageID:        "rel-001",
			ManifestDigest: "sha256:" + strings.Repeat("ab", 32),
		},
		VCPUs:    2,
		MemoryMB: 2048,
		VsockCID: cid,
	}
	if uefi {
		varsPath := filepath.Join(releaseDir, "uefi-vars.fd")
		if err := os.WriteFile(varsPath, []byte("vars"), 0o644); err != nil {
			t.Fatal(err)
		}
		spec.Image.UEFIVarsPath = varsPath
	}
	return spec
}

func absentResponse(name string) fakeResponse {
	return fakeResponse{
		match:  "domstate " + name,
		output: "error: failed to get domain '" + name + "'",
		err:    fmt.Errorf("virsh domstate %s: exit status 1", name),
	}
}

func TestCreateDefinesPersistentDomain(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("bahia-x-api")}}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 42)

	if err := driver.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	overlays := runner.callsMatching("qemu-img create")
	if len(overlays) != 1 {
		t.Fatalf("expected one qemu-img create, got %v", runner.calls)
	}
	if !strings.Contains(overlays[0], "-b "+spec.Image.DiskPath) {
		t.Errorf("overlay not backed by release disk: %s", overlays[0])
	}
	if !strings.Contains(overlays[0], filepath.Join(spec.InstanceDir, "disk.qcow2")) {
		t.Errorf("overlay not in instance dir: %s", overlays[0])
	}

	defines := runner.callsMatching("virsh -c qemu:///session define")
	if len(defines) != 1 {
		t.Fatalf("expected one virsh define, got %v", runner.calls)
	}

	xmlData, err := os.ReadFile(filepath.Join(spec.InstanceDir, "domain.xml"))
	if err != nil {
		t.Fatalf("domain.xml missing: %v", err)
	}
	xmlStr := string(xmlData)
	for _, want := range []string{
		"<name>bahia-x-api</name>",
		"<memory unit=\"MiB\">2048</memory>",
		"<vcpu placement=\"static\">2</vcpu>",
		"arch=\"x86_64\"",
		"machine=\"q35\"",
		"<on_reboot>restart</on_reboot>",
		filepath.Join(spec.InstanceDir, "console.log"),
		"<cid auto=\"no\" address=\"42\"/>",
	} {
		if !strings.Contains(xmlStr, want) {
			t.Errorf("domain XML missing %q:\n%s", want, xmlStr)
		}
	}
	if strings.Contains(xmlStr, "<loader") || strings.Contains(xmlStr, "<nvram>") {
		t.Errorf("non-UEFI domain should have no loader/nvram:\n%s", xmlStr)
	}
	if strings.Contains(xmlStr, "<interface") {
		t.Errorf("v1 domains must not attach network interfaces:\n%s", xmlStr)
	}
}

func TestCreateUEFIDomain(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("bahia-x-win")}}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-win", true, 0)

	if err := driver.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	nvram := filepath.Join(spec.InstanceDir, "nvram.fd")
	if _, err := os.Stat(nvram); err != nil {
		t.Fatalf("expected per-instance nvram copy: %v", err)
	}
	xmlData, _ := os.ReadFile(filepath.Join(spec.InstanceDir, "domain.xml"))
	xmlStr := string(xmlData)
	if !strings.Contains(xmlStr, "<loader readonly=\"yes\" type=\"pflash\">/fw/OVMF_CODE.fd</loader>") {
		t.Errorf("expected firmware loader in XML:\n%s", xmlStr)
	}
	if !strings.Contains(xmlStr, "<nvram>"+nvram+"</nvram>") {
		t.Errorf("expected nvram in XML:\n%s", xmlStr)
	}
	if strings.Contains(xmlStr, "<vsock") {
		t.Errorf("cid=0 should omit vsock device:\n%s", xmlStr)
	}
}

func TestCreateRejectsWrongFormat(t *testing.T) {
	runner := &fakeRunner{}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 0)
	spec.Image.Format = vm.FormatFirecrackerRootFS
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "qcow2") {
		t.Fatalf("expected format rejection, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("no commands expected, got %v", runner.calls)
	}
}

func TestCreateRejectsExistingDomain(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: "running\n"}}}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 0)
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestCreateRejectsMismatchedInstanceDir(t *testing.T) {
	runner := &fakeRunner{}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 0)
	spec.InstanceDir = t.TempDir()
	err := driver.Create(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected instance-dir mismatch error, got %v", err)
	}
}

func TestStartStopDestroy(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "domstate bahia-x-api", output: "running\n", once: true},
		{match: "domstate bahia-x-api", output: "shut off\n"},
	}}
	driver, _ := newTestDriver(t, runner)
	ctx := context.Background()

	if err := driver.Start(ctx, "bahia-x-api"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := runner.callsMatching("virsh -c qemu:///session start bahia-x-api"); len(got) != 1 {
		t.Errorf("expected virsh start, got %v", runner.calls)
	}

	// Graceful stop: running -> shutdown request -> shut off.
	if err := driver.Stop(ctx, "bahia-x-api", true); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := runner.callsMatching("shutdown bahia-x-api"); len(got) != 1 {
		t.Errorf("expected virsh shutdown, got %v", runner.calls)
	}

	// Destroy on a shut-off domain skips virsh destroy but undefines.
	if err := driver.Destroy(ctx, "bahia-x-api"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if got := runner.callsMatching("undefine bahia-x-api --nvram"); len(got) != 1 {
		t.Errorf("expected virsh undefine --nvram, got %v", runner.calls)
	}
	if got := runner.callsMatching("destroy bahia-x-api"); len(got) != 0 {
		t.Errorf("expected no virsh destroy for shut-off domain, got %v", got)
	}
}

func TestForcedStopDestroysRunningDomain(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{
		{match: "domstate bahia-x-api", output: "running\n", once: true},
		{match: "domstate bahia-x-api", output: "shut off\n"},
	}}
	driver, _ := newTestDriver(t, runner)
	if err := driver.Stop(context.Background(), "bahia-x-api", false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := runner.callsMatching("destroy bahia-x-api"); len(got) != 1 {
		t.Errorf("expected virsh destroy, got %v", runner.calls)
	}
}

func TestStopAbsentDomainErrors(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("ghost")}}
	driver, _ := newTestDriver(t, runner)
	err := driver.Stop(context.Background(), "ghost", true)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected absent-domain error, got %v", err)
	}
}

func TestStopAlreadyOffIsNoOp(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: "shut off\n"}}}
	driver, _ := newTestDriver(t, runner)
	if err := driver.Stop(context.Background(), "bahia-x-api", true); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
	if got := runner.callsMatching("shutdown"); len(got) != 0 {
		t.Errorf("no shutdown expected, got %v", got)
	}
}

func TestGracefulStopTimesOut(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: "running\n"}}}
	driver, _ := newTestDriver(t, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := driver.Stop(ctx, "bahia-x-api", true)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("expected shutdown-not-confirmed error, got %v", err)
	}
}

func TestDestroyAbsentIsIdempotent(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("ghost")}}
	driver, _ := newTestDriver(t, runner)
	if err := driver.Destroy(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected idempotent destroy, got %v", err)
	}
}

func TestStateMapping(t *testing.T) {
	cases := []struct {
		output string
		want   vm.InstanceState
	}{
		{"running\n", vm.StateRunning},
		{"idle\n", vm.StateRunning},
		{"in shutdown\n", vm.StateRunning},
		{"shut off\n", vm.StateStopped},
		{"paused\n", vm.StatePaused},
		{"pmsuspended\n", vm.StatePaused},
		{"crashed\n", vm.StateCrashed},
		{"weird-new-state\n", vm.StateUnknown},
	}
	for _, tc := range cases {
		runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: tc.output}}}
		driver, _ := newTestDriver(t, runner)
		state, err := driver.State(context.Background(), "bahia-x-api")
		if err != nil {
			t.Fatalf("State(%q): %v", tc.output, err)
		}
		if state != tc.want {
			t.Errorf("State(%q) = %s, want %s", tc.output, state, tc.want)
		}
	}
}

func TestStateAbsent(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("ghost")}}
	driver, _ := newTestDriver(t, runner)
	state, err := driver.State(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != vm.StateAbsent {
		t.Errorf("expected absent, got %s", state)
	}
}

func TestStateOtherErrorSurfaces(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{
		match: "domstate",
		err:   fmt.Errorf("virsh domstate x: exit status 1: cannot connect to hypervisor"),
	}}}
	driver, _ := newTestDriver(t, runner)
	_, err := driver.State(context.Background(), "bahia-x-api")
	if err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("expected connection error to surface, got %v", err)
	}
}

func TestListFiltersPrefix(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{
		match:  "list --all --name",
		output: "bahia-aaa-api\nbahia-bbb-worker\nother-domain\n\n",
	}}}
	driver, _ := newTestDriver(t, runner)
	names, err := driver.List(context.Background(), "bahia-")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "bahia-aaa-api" || names[1] != "bahia-bbb-worker" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestConsoleLogPath(t *testing.T) {
	runner := &fakeRunner{}
	driver, instancesDir := newTestDriver(t, runner)
	path, err := driver.ConsoleLogPath("bahia-x-api")
	if err != nil {
		t.Fatalf("ConsoleLogPath: %v", err)
	}
	if path != filepath.Join(instancesDir, "bahia-x-api", "console.log") {
		t.Errorf("unexpected console log path: %s", path)
	}
}

func TestCreateRecordsVsockCID(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("bahia-x-api")}}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 42)
	if err := driver.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cid, err := driver.readVsockRecord("bahia-x-api")
	if err != nil {
		t.Fatalf("readVsockRecord: %v", err)
	}
	if cid != 42 {
		t.Errorf("expected recorded CID 42, got %d", cid)
	}
}

func TestCreateWithoutVsockWritesNoRecord(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("bahia-x-api")}}
	driver, instancesDir := newTestDriver(t, runner)
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 0)
	if err := driver.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(instancesDir, "bahia-x-api", "vsock.json")); !os.IsNotExist(err) {
		t.Errorf("expected no vsock.json, stat err: %v", err)
	}
	if _, err := driver.VsockDial(context.Background(), "bahia-x-api", 1024); err == nil || !strings.Contains(err.Error(), "no vsock device") {
		t.Errorf("expected no-vsock-device error, got %v", err)
	}
}

func TestVsockDialUsesRecordedCID(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{absentResponse("bahia-x-api")}}
	instancesDir := t.TempDir()
	var gotCID, gotPort uint32
	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()
	driver := New(Config{
		URI:          "qemu:///session",
		InstancesDir: instancesDir,
		Runner:       runner.run,
		Dialer: func(_ context.Context, cid, port uint32) (net.Conn, error) {
			gotCID, gotPort = cid, port
			return host, nil
		},
	}, zap.NewNop())
	spec := qcow2Spec(t, instancesDir, "bahia-x-api", false, 77)
	if err := driver.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	conn, err := driver.VsockDial(context.Background(), "bahia-x-api", 5000)
	if err != nil {
		t.Fatalf("VsockDial: %v", err)
	}
	if conn != host {
		t.Error("expected the dialer's connection to be returned")
	}
	if gotCID != 77 || gotPort != 5000 {
		t.Errorf("expected dial(77, 5000), got dial(%d, %d)", gotCID, gotPort)
	}
}

func TestVsockDialUnknownInstanceErrors(t *testing.T) {
	runner := &fakeRunner{}
	driver, _ := newTestDriver(t, runner)
	_, err := driver.VsockDial(context.Background(), "ghost", 1024)
	if err == nil || !strings.Contains(err.Error(), "no vsock device") {
		t.Fatalf("expected no-vsock-device error, got %v", err)
	}
}

func TestVirshUsesConfiguredURI(t *testing.T) {
	runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: "running\n"}}}
	driver, _ := newTestDriver(t, runner)
	if _, err := driver.State(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(runner.calls[0], "virsh -c qemu:///session ") {
		t.Errorf("expected -c URI prefix, got %s", runner.calls[0])
	}
}
