package firecracker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// TestWatchPersistentFiresChangedOnHealthTransition verifies that the
// fsnotify-based health re-probe fires changed when the API socket state
// transitions (e.g. the VMM pauses), not just when the process exits.
// This is the event-driven re-probe required by.40 item 5 /.63.
func TestWatchPersistentFiresChangedOnHealthTransition(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fcw-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	}()
	spec := fcPersistentSpec(t, root)

	procs := &persistentProcs{fakeProcs: newFakeProcs(), exit: make(chan struct{})}
	d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: procs}, nil)

	if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{
		ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.startVMM(context.Background(), spec.Instance.Name); err != nil {
		t.Fatal(err)
	}

	// Stand up a fake API socket that reports Running initially, then Paused
	// after a signal.
	socket := d.apiSocketPath(spec.Instance.Name)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var paused atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := "Running"
		if paused.Load() {
			state = "Paused"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": state})
	})}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-serveDone }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var changeCount atomic.Int32
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- d.WatchPersistent(ctx, spec.Marker.ProviderResourceID, func() {
			changeCount.Add(1)
		})
	}()

	// Wait for the initial changed call that WatchPersistent fires
	// right after setup.
	deadline := time.After(5 * time.Second)
	for changeCount.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for initial changed() call")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	initialCount := changeCount.Load()

	// Simulate a health transition: mark paused, then touch the API socket
	// to trigger an fsnotify event so the watcher re-probes.
	paused.Store(true)
	// Touch the console.log to generate an fsnotify event.
	consoleLog := filepath.Join(d.instanceDir(spec.Instance.Name), consoleLogFileName)
	if err := os.WriteFile(consoleLog, []byte("paused\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Wait for the health-transition changed call.
	deadline = time.After(5 * time.Second)
	for changeCount.Load() <= initialCount {
		select {
		case <-deadline:
			t.Fatalf("WatchPersistent did not fire changed() on health transition "+
				"(Running → Paused); changes=%d, expected > %d — "+
				"the watcher must re-probe on fsnotify events, not just process exit",
				changeCount.Load(), initialCount)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Clean up: cancel to unblock the watch loop.
	cancel()
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchPersistent did not exit after context cancellation")
	}
}

// TestWatchPersistentStillFiresOnProcessExit confirms that the enhanced
// watcher still fires changed when the process exits (regression guard).
func TestWatchPersistentStillFiresOnProcessExit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fcw-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	}()
	spec := fcPersistentSpec(t, root)

	procs := &persistentProcs{fakeProcs: newFakeProcs(), exit: make(chan struct{})}
	d := New(Config{InstancesDir: root, Binary: "/usr/bin/firecracker", Processes: procs}, nil)

	if err := d.DefinePersistent(context.Background(), spec, &vm.PersistentResource{
		ID: spec.Marker.ProviderResourceID, State: domain.VMRuntimeAbsent,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.startVMM(context.Background(), spec.Instance.Name); err != nil {
		t.Fatal(err)
	}

	// Stand up a fake API socket.
	socket := d.apiSocketPath(spec.Instance.Name)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "Running"})
	})}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-serveDone }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var changeCount atomic.Int32
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- d.WatchPersistent(ctx, spec.Marker.ProviderResourceID, func() {
			changeCount.Add(1)
		})
	}()

	// Wait for initial changed.
	deadline := time.After(5 * time.Second)
	for changeCount.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for initial changed()")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	initialCount := changeCount.Load()

	// Simulate process exit.
	procs.exitByMarker(socket)
	close(procs.exit)

	// Wait for exit-triggered changed.
	select {
	case err := <-watchDone:
		if err != nil {
			t.Logf("WatchPersistent returned (expected): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchPersistent did not exit after process exit")
	}

	if changeCount.Load() <= initialCount {
		t.Fatal("WatchPersistent did not fire changed() on process exit")
	}
}
