package libvirt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/digitalocean/go-libvirt/socket"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestLegacyMutationsRequirePositiveMarkerAndExactUUID(t *testing.T) {
	for _, change := range []string{"unmarked", "uuid", "name", "definition"} {
		for _, operation := range []string{"start", "stop", "destroy"} {
			t.Run(change+"/"+operation, func(t *testing.T) {
				runner := &fakeRunner{responses: []fakeResponse{{match: "domstate", output: "running"}}}
				d, _ := newTestDriver(t, runner)
				authorizeLegacyFixture(t, d, runner, false)
				switch change {
				case "unmarked":
					runner.xml = []byte("<domain><name>bahia-x-api</name><metadata/></domain>")
				case "uuid":
					runner.xml = bytes.Replace(runner.xml, []byte("<uuid>"), []byte("<uuid>0000"), 1)
				case "name":
					runner.xml = bytes.Replace(runner.xml, []byte("<name>bahia-x-api</name>"), []byte("<name>other</name>"), 1)
				case "definition":
					runner.xml = bytes.Replace(runner.xml, []byte("disk.qcow2"), []byte("foreign.qcow2"), 1)
				}
				var err error
				switch operation {
				case "start":
					err = d.Start(context.Background(), "bahia-x-api")
				case "stop":
					err = d.Stop(context.Background(), "bahia-x-api", true)
				case "destroy":
					err = d.Destroy(context.Background(), "bahia-x-api")
				}
				if err == nil {
					t.Fatal("unowned legacy definition mutated")
				}
				for _, call := range runner.calls {
					for _, mutation := range []string{" start ", " shutdown ", " destroy ", " undefine "} {
						if strings.Contains(call, mutation) {
							t.Fatalf("mutation: %s", call)
						}
					}
				}
			})
		}
	}
}

func TestPersistentDefinitionRejectsPoolPeerAndLostOwnerBeforeResize(t *testing.T) {
	for _, condition := range []string{"peer-disk", "missing-owner", "foreign-owner"} {
		t.Run(condition, func(t *testing.T) {
			root := t.TempDir()
			marker := conformanceMarker()
			dir := filepath.Join(root, marker.ProviderResourceID.String())
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(root, uuid.NewString(), "disk.qcow2")
			if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			doc, _ := os.ReadFile("../testdata/conformance/owned-domain.xml")
			host := &conformanceHost{xml: doc, state: "shut off"}
			d := New(Config{InstancesDir: root, Runner: host.run}, nil)
			current, err := d.InspectPersistent(context.Background(), marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			spec := vm.PersistentSpec{Marker: marker, Instance: vm.InstanceSpec{Name: marker.ProviderResourceID.String(), InstanceDir: dir}, Components: map[domain.VMComponentKind]string{domain.VMComponentDisk: other}}
			if condition == "missing-owner" {
				current.Marker = nil
			}
			if condition == "foreign-owner" {
				m := *current.Marker
				m.DeploymentID = uuid.New()
				current.Marker = &m
			}
			before := len(host.calls)
			if err := d.DefinePersistent(context.Background(), spec, current); err == nil {
				t.Fatal("foreign storage/ownership accepted")
			}
			for _, call := range host.calls[before:] {
				if call[0] == "qemu-img" {
					t.Fatalf("touched disk before ownership proof: %v", call)
				}
			}
		})
	}
}

type coldTestEvents struct{ changed bool }

func (e *coldTestEvents) Subscribe(context.Context, uuid.UUID, bool) (DomainSubscription, error) {
	return e, nil
}
func (e *coldTestEvents) Next(ctx context.Context) (DomainEvent, error) {
	<-ctx.Done()
	return DomainEvent{}, ctx.Err()
}
func (e *coldTestEvents) Close() error { return nil }
func (e *coldTestEvents) CheckCold(context.Context) error {
	if e.changed {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	return nil
}
func TestColdGuardHoldsTPMLockAcrossEntireCoordinatedCopy(t *testing.T) {
	if path := os.Getenv("BAHIA_TEST_CHECK_COLD_LOCK"); path != "" {
		f, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
		held := errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES)
		if held != (os.Getenv("BAHIA_TEST_EXPECT_HELD") == "yes") {
			t.Fatalf("lock held=%v, err=%v", held, err)
		}
		return
	}
	root := t.TempDir()
	marker := conformanceMarker()
	dir := filepath.Join(root, marker.ProviderResourceID.String())
	tpm := filepath.Join(dir, "swtpm")
	if err := os.MkdirAll(tpm, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tpm, "state"), []byte("TPM state"), 0600); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := domainXML(domainParams{Name: marker.ProviderResourceID.String(), Marker: &marker, MemoryMB: 512, VCPUs: 2, Overlay: disk, TPMState: tpm, ConsoleLog: filepath.Join(dir, "console")})
	if err != nil {
		t.Fatal(err)
	}
	host := &conformanceHost{xml: data, state: "shut off"}
	events := &coldTestEvents{}
	d := New(Config{InstancesDir: root, Runner: host.run, Events: events}, nil)
	r, err := d.InspectPersistent(context.Background(), marker.ProviderResourceID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := d.BeginColdCopy(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	check := func(held string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestColdGuardHoldsTPMLockAcrossEntireCoordinatedCopy$")
		cmd.Env = append(os.Environ(), "BAHIA_TEST_CHECK_COLD_LOCK="+filepath.Join(tpm, ".lock"), "BAHIA_TEST_EXPECT_HELD="+held)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("lock probe: %s %v", out, err)
		}
	}
	check("yes")
	if err = d.CopyPersistentComponent(guard.Context(), domain.VMComponentSWTPM, tpm, filepath.Join(t.TempDir(), "tpm.tar")); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	check("yes")
	events.changed = true
	if err = guard.Check(context.Background()); err == nil {
		guard.Close()
		t.Fatal("cold barrier ignored queued transition")
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	check("no")
}

type shortReadConn struct {
	net.Conn
	input *bytes.Reader
}

func (c shortReadConn) Read(p []byte) (int, error) { return c.input.Read(p[:1]) }
func TestColdBarrierCountsNotificationsBeforeAsyncDispatch(t *testing.T) {
	var packets bytes.Buffer
	for _, kind := range []uint32{socket.Message, socket.Message, socket.Reply} {
		_ = binary.Write(&packets, binary.BigEndian, uint32(28))
		_ = binary.Write(&packets, binary.BigEndian, socket.Header{Type: kind})
	}
	wire := &eventConn{Conn: shortReadConn{input: bytes.NewReader(packets.Bytes())}}
	data := make([]byte, packets.Len())
	if _, err := io.ReadFull(wire, data); err != nil {
		t.Fatal(err)
	}
	if wire.epoch.Load() != 2 {
		t.Fatal("start/stop notifications lost before reply barrier")
	}
}
