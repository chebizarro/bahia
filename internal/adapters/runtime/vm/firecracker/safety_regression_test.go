package firecracker

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func fcSafetyRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fcs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestMissingConfigDoesNotHideLiveOrUnverifiableProcess(t *testing.T) {
	for _, condition := range []string{"live", "corrupt", "socket", "dead"} {
		t.Run(condition, func(t *testing.T) {
			root := fcSafetyRoot(t)
			spec := fcPersistentSpec(t, root)
			procs := newFakeProcs()
			d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: procs}, nil)
			if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
				t.Fatal(err)
			}
			if condition == "live" || condition == "dead" {
				if err := d.startVMM(context.Background(), spec.Instance.Name); err != nil {
					t.Fatal(err)
				}
				if condition == "dead" {
					procs.exitByMarker(d.apiSocketPath(spec.Instance.Name))
				}
			}
			if condition == "corrupt" {
				if err := os.WriteFile(filepath.Join(spec.Instance.InstanceDir, vmmRecordFileName), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "socket" {
				if err := os.WriteFile(d.apiSocketPath(spec.Instance.Name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(spec.Instance.InstanceDir, vmConfigFileName)); err != nil {
				t.Fatal(err)
			}
			resource, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
			if condition == "dead" {
				if err != nil || resource.State != domain.VMRuntimeAbsent {
					t.Fatalf("confirmed dead: %+v %v", resource, err)
				}
			} else if err == nil {
				t.Fatal("missing config silently declared live/unknown process absent")
			}
			ids, err := d.ListPersistent(context.Background())
			if err != nil || len(ids) != 1 {
				t.Fatal("orphan supervision evidence omitted from inventory")
			}
		})
	}
}

func TestPersistentStartWaitsForDelayedSocketAndPreservesTimeoutEvidence(t *testing.T) {
	for _, outcome := range []string{"ready", "cancel", "exit"} {
		t.Run(outcome, func(t *testing.T) {
			root := fcSafetyRoot(t)
			spec := fcPersistentSpec(t, root)
			procs := newFakeProcs()
			procs.started = make(chan StartVMMRequest, 1)
			d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: procs}, nil)
			if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
				t.Fatal(err)
			}
			before, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- d.TransitionPersistent(ctx, before, domain.VMOperationStart, false) }()
			select {
			case <-procs.started:
			case <-ctx.Done():
				t.Fatal("launch never reached")
			}
			select {
			case err := <-result:
				t.Fatalf("startup completed before API readiness: %v", err)
			default:
			}
			switch outcome {
			case "ready":
				listener, err := net.Listen("unix", d.apiSocketPath(spec.Instance.Name))
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"state":"Running"}`)) })}
				done := make(chan struct{})
				go func() { defer close(done); _ = server.Serve(listener) }()
				defer func() {
					if err := server.Close(); err != nil {
						t.Error(err)
					}
					<-done
				}()
			case "cancel":
				cancel()
			case "exit":
				procs.exitByMarker(d.apiSocketPath(spec.Instance.Name))
			}
			err = <-result
			if (outcome == "ready") != (err == nil) {
				t.Fatalf("%s: %v", outcome, err)
			}
			if procs.killCount() != 0 {
				t.Fatal("readiness failure killed guest")
			}
			if record, err := d.readRecord(spec.Instance.Name); err != nil || record == nil {
				t.Fatal("readiness failure lost process evidence")
			}
		})
	}
}

func TestDefinitionCannotResizeAnotherResourcesDiskOrOverwriteForeignMarker(t *testing.T) {
	for _, condition := range []string{"disk", "marker"} {
		t.Run(condition, func(t *testing.T) {
			root := fcSafetyRoot(t)
			spec := fcPersistentSpec(t, root)
			d := New(Config{InstancesDir: root, Processes: newFakeProcs()}, nil)
			if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
				t.Fatal(err)
			}
			before, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			spec.Components = before.Components
			other := filepath.Join(root, "other-vm", "rootfs")
			if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			if condition == "disk" {
				spec.Components[domain.VMComponentRootFS] = other
			} else {
				before.Marker = nil
			}
			if err := d.DefinePersistent(context.Background(), spec, before); err == nil {
				t.Fatal("foreign storage/definition accepted")
			}
			if data, err := os.ReadFile(other); err != nil || string(data) != "foreign" {
				t.Fatal("foreign disk resized")
			}
		})
	}
}

func TestLegacyLaunchRejectsNonAllowlistedExecutable(t *testing.T) {
	for _, binary := range []string{"firecracker", "/bin/sh", "/usr/bin/../bin/firecracker"} {
		procs := newFakeProcs()
		d, root := newTestDriver(t, procs)
		spec := fcSpec(t, root, "i1", 0)
		mustCreate(t, d, spec)
		d.cfg.Binary = binary
		if err := d.Start(context.Background(), spec.Name); err == nil || len(procs.starts) != 0 {
			t.Fatalf("executed non-allowlisted binary %q", binary)
		}
	}
}

func TestColdCopyKernelBarrierDetectsImmediateLaunchCycleAndWrites(t *testing.T) {
	for _, event := range []string{"launch", "write", "unchanged"} {
		t.Run(event, func(t *testing.T) {
			root := fcSafetyRoot(t)
			spec := fcPersistentSpec(t, root)
			d := New(Config{InstancesDir: root, Processes: newFakeProcs()}, nil)
			if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent}); err != nil {
				t.Fatal(err)
			}
			r, err := d.InspectPersistent(context.Background(), spec.Marker.ProviderResourceID)
			if err != nil {
				t.Fatal(err)
			}
			guard, err := d.BeginColdCopy(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := guard.Close(); err != nil {
					t.Error(err)
				}
			}()
			switch event {
			case "launch":
				socket := d.apiSocketPath(spec.Instance.Name)
				if err := os.WriteFile(socket, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(socket); err != nil {
					t.Fatal(err)
				}
			case "write":
				if err := os.WriteFile(r.Components[domain.VMComponentRootFS], []byte("guest write"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = guard.Check(context.Background())
			if (event == "unchanged") != (err == nil) {
				t.Fatalf("kernel barrier lost %s: %v", event, err)
			}
		})
	}
}
