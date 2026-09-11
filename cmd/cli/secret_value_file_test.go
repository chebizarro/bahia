package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSecretValueFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretValueFile(path)
	if err != nil {
		t.Fatalf("readSecretValueFile: %v", err)
	}
	if got != "secret-value" {
		t.Fatalf("value = %q", got)
	}
}

func TestReadSecretValueFileRejectsUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	unsafe := filepath.Join(dir, "unsafe")
	if err := os.WriteFile(unsafe, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretValueFile(unsafe); err == nil {
		t.Fatal("world-readable secret file accepted")
	}
	if _, err := readSecretValueFile("relative-token"); err == nil {
		t.Fatal("relative secret file accepted")
	}
}
