package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type persistentProcs struct {
	*fakeProcs
	exit     chan struct{}
	watching atomic.Bool
	lost     bool
}

func (p *persistentProcs) InspectProcess(_ context.Context, id VMMIdentity, marker string) (bool, error) {
	return p.Alive(id, marker), nil
}
func (p *persistentProcs) WatchExit(context.Context, VMMIdentity, string) (ProcessExit, error) {
	p.watching.Store(true)
	return &testExit{p: p}, nil
}

type testExit struct{ p *persistentProcs }

func (e *testExit) Close() error { return nil }
func (e *testExit) Wait(ctx context.Context) error {
	if e.p.lost {
		return errors.New("process event lost")
	}
	select {
	case <-e.p.exit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fcPersistentSpec(t *testing.T, root string) vm.PersistentSpec {
	t.Helper()
	id := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	dir := filepath.Join(root, id.String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	image := t.TempDir()
	kernel := filepath.Join(image, "kernel")
	disk := filepath.Join(image, "rootfs.ext4")
	if err := os.WriteFile(kernel, []byte("kernel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("rootfs"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: domain.VMResourceIdentity{InstallationID: uuid.New(), OrgID: uuid.New(), HostID: uuid.New(), DeploymentID: uuid.New(), ProviderResourceID: id, Provider: domain.VMProviderFirecracker, LifecycleClass: domain.VMLifecyclePersistent}, AppliedGeneration: 1, OperationID: uuid.New(), ImageDigest: "sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64)}
	return vm.PersistentSpec{Marker: marker, Deployment: domain.PersistentVMDeployment{Firmware: domain.VMFirmwareNone, Allocation: domain.VMCapacity{DiskBytes: 4096}}, Instance: vm.InstanceSpec{Name: id.String(), InstanceDir: dir, VCPUs: 2, MemoryMB: 512, Image: vm.ImageSpec{Format: vm.FormatFirecrackerRootFS, KernelPath: kernel, RootFSPath: disk}}}
}
func TestConformanceFirecrackerDefinitionAndOwnership(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	}()
	spec := fcPersistentSpec(t, root)
	p := &persistentProcs{fakeProcs: newFakeProcs(), exit: make(chan struct{})}
	d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: p}, nil)
	if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
		t.Fatal(err)
	}
	observed, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
	if err != nil || observed.Marker == nil || !reflect.DeepEqual(*observed.Marker, spec.Marker) || observed.State != domain.VMRuntimeStopped {
		t.Fatalf("ownership/config inspect: %+v %v", observed, err)
	}
	data, err := os.ReadFile(filepath.Join(spec.Instance.InstanceDir, vmConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(string(data), spec.Instance.InstanceDir, "$INSTANCE"), filepath.Dir(spec.Instance.Image.KernelPath), "$IMAGE")
	fixture, err := os.ReadFile("../testdata/conformance/firecracker-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected any
	_ = json.Unmarshal([]byte(normalized), &actual)
	_ = json.Unmarshal(fixture, &expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("config transform: %s", normalized)
	}
}
func TestConformanceFirecrackerShutdownNeverFallsBackToKill(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit_event", true: "lost_event"}[lost], func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "fcp-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.RemoveAll(root); err != nil {
					t.Error(err)
				}
			}()
			spec := fcPersistentSpec(t, root)
			p := &persistentProcs{fakeProcs: newFakeProcs(), exit: make(chan struct{}), lost: lost}
			d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: p}, nil)
			if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
				t.Fatal(err)
			}
			// Launch the simulated supervised process before installing its API socket;
			// this exercises the real driver launch argv and identity-file transformation.
			if err := d.Start(context.Background(), spec.Instance.Name); err == nil {
				t.Fatal("legacy name-based start mutated a v2 resource")
			}
			if err := d.startVMM(context.Background(), spec.Instance.Name); err != nil {
				t.Fatal(err)
			}
			socket := d.apiSocketPath(spec.Instance.Name)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.Path != "/" {
						t.Errorf("unsupported Firecracker observation endpoint %s", r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"state":"Running"}`))
					return
				}
				if !p.watching.Load() {
					t.Error("shutdown requested before exit registration")
				}
				p.exitByMarker(socket)
				close(p.exit)
				w.WriteHeader(http.StatusNoContent)
			})}
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve(listener) }()
			defer func() {
				if err := server.Close(); err != nil {
					t.Error(err)
				}
				<-done
			}()
			observed, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			err = d.TransitionPersistent(context.Background(), observed, domain.VMOperationGracefulStop, false)
			if lost {
				var pe *domain.VMProviderError
				if !errors.As(err, &pe) || !pe.Unconfirmed {
					t.Fatalf("lost process event silently succeeded: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if p.killCount() != 0 {
				t.Fatal("graceful stop fell back to kill")
			}
			if _, err = os.Stat(filepath.Join(spec.Instance.InstanceDir, vmConfigFileName)); err != nil {
				t.Fatal("unconfirmed state files removed")
			}
		})
	}
}
func TestConformanceFirecrackerCorruptIdentityNeverReaped(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	}()
	spec := fcPersistentSpec(t, root)
	d := New(Config{InstancesDir: root, Processes: &persistentProcs{fakeProcs: newFakeProcs()}}, nil)
	if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spec.Instance.InstanceDir, vmmRecordFileName)
	if err := os.WriteFile(path, []byte(`{"pid":123,"start_time":0,"marker":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID); err == nil {
		t.Fatal("invalid process identity treated as stopped")
	}
	if err := d.AdoptOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("v2 identity record reaped by legacy scanning")
	}
}
