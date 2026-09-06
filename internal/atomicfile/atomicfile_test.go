package atomicfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCaptureAndRestoreExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned.conf")
	if err := os.WriteFile(path, []byte("previous\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	previous, err := Capture(path, 0o644)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !previous.Exists || string(previous.Data) != "previous\n" || previous.Mode != 0o640 {
		t.Fatalf("snapshot = %#v", previous)
	}
	if err := WriteFile(context.Background(), path, ".write-*.tmp", []byte("desired\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Restore(path, ".restore-*.tmp", previous); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous\n" || info.Mode().Perm() != 0o640 {
		t.Fatalf("restored data=%q mode=%#o", data, info.Mode().Perm())
	}
}

func TestRestoreAbsentSnapshotRemovesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.conf")
	previous, err := Capture(path, 0o644)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if previous.Exists || previous.Mode != 0o644 {
		t.Fatalf("missing snapshot = %#v", previous)
	}
	if err := WriteFile(context.Background(), path, ".write-*.tmp", []byte("desired\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Restore(path, ".restore-*.tmp", previous); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored absent path stat error = %v", err)
	}
}

func TestCanceledWriteDoesNotReplaceExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned.conf")
	if err := os.WriteFile(path, []byte("previous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteFile(ctx, path, ".write-*.tmp", []byte("desired\n"), 0o644); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFile error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous\n" {
		t.Fatalf("canceled write replaced file: %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("temp files leaked after cancellation: %#v", entries)
	}
}
