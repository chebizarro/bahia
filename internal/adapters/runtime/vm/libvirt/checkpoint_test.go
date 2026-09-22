package libvirt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestConformanceCheckpointFlattensAllowedBackingChain(t *testing.T) {
	instances, images, destDir := t.TempDir(), t.TempDir(), t.TempDir()
	disk := filepath.Join(instances, "disk.qcow2")
	base := filepath.Join(images, "base.qcow2")
	dest := filepath.Join(destDir, "flattened.qcow2")
	for _, path := range []string{disk, base} {
		if err := os.WriteFile(path, []byte("qcow2 fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var calls [][]string
	runner := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded transfer")
		}
		calls = append(calls, append([]string{filepath.Base(binary)}, args...))
		if args[0] == "info" {
			return json.Marshal([]map[string]string{{"filename": disk, "format": "qcow2"}, {"filename": base, "format": "qcow2"}})
		}
		return nil, os.WriteFile(dest, []byte("independent qcow2 fixture"), 0600)
	}
	d := New(Config{InstancesDir: instances, ImageRoot: images, Runner: runner}, nil)
	if err := d.CopyPersistentComponent(context.Background(), domain.VMComponentDisk, disk, dest); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		for i, arg := range call {
			call[i] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(arg, instances, "$INSTANCES"), images, "$IMAGES"), destDir, "$DEST")
		}
	}
	data, err := os.ReadFile("../testdata/conformance/checkpoint.argv.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected [][]string
	if err = json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("checkpoint argv %#v", calls)
	}
	if _, size, err := vm.HashComponent(context.Background(), dest); err != nil || size == 0 {
		t.Fatal("flattened output absent")
	}
}
func TestTPMCheckpointRefusesActiveStateLock(t *testing.T) {
	if path := os.Getenv("BAHIA_TEST_TPM_LOCK"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		if err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stdout.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	root := t.TempDir()
	source := filepath.Join(root, "swtpm")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tpm2-00.permall"), []byte("live identity"), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTPMCheckpointRefusesActiveStateLock$")
	child.Env = append(os.Environ(), "BAHIA_TEST_TPM_LOCK="+filepath.Join(source, ".lock"))
	ready, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	hold, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		hold.Close()
		if err := child.Wait(); err != nil {
			t.Error(err)
		}
	}()
	var signal [1]byte
	if _, err = io.ReadFull(ready, signal[:]); err != nil {
		t.Fatal(err)
	}
	d := New(Config{InstancesDir: root}, nil)
	err = d.CopyPersistentComponent(context.Background(), domain.VMComponentSWTPM, source, filepath.Join(t.TempDir(), "state.tar"))
	var pe *domain.VMProviderError
	if !errors.As(err, &pe) || pe.Code != domain.VMErrorConflict {
		t.Fatalf("live swtpm-compatible lock not enforced: %v", err)
	}
}

func TestTPMCheckpointRequiresActualStateNotOnlyLock(t *testing.T) {
	instances := t.TempDir()
	source := filepath.Join(instances, "swtpm")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	d := New(Config{InstancesDir: instances}, nil)
	if err := d.CopyPersistentComponent(context.Background(), domain.VMComponentSWTPM, source, filepath.Join(t.TempDir(), "empty.tar")); err == nil {
		t.Fatal("empty TPM checkpoint accepted")
	}
	if err := os.WriteFile(filepath.Join(source, "tpm2-00.permall"), []byte("TPM identity"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "state.tar")
	if err := d.CopyPersistentComponent(context.Background(), domain.VMComponentSWTPM, source, dest); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := vm.UnpackTPM(context.Background(), dest, restored); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(restored, "tpm2-00.permall"))
	if err != nil || string(data) != "TPM identity" {
		t.Fatal("TPM state not restored")
	}
	if _, err = os.Stat(filepath.Join(restored, ".lock")); !os.IsNotExist(err) {
		t.Fatal("runtime lock copied as TPM state")
	}
}
