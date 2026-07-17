package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrivateKeyUsesEnvironmentOrFile(t *testing.T) {
	if got, err := loadPrivateKey("", "  env-secret  "); err != nil || got != "env-secret" {
		t.Fatalf("environment key = %q, err = %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if got, err := loadPrivateKey(path, ""); err != nil || got != "file-secret" {
		t.Fatalf("file key = %q, err = %v", got, err)
	}
}

func TestLoadPrivateKeyRejectsAmbiguousSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := loadPrivateKey(path, "env-secret"); err == nil {
		t.Fatal("expected ambiguous private-key source error")
	}
}
