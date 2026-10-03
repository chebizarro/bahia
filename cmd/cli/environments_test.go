package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentUnitJSONFileRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unit.json")
	if err := os.WriteFile(path, []byte(`{"key":"max","runtime_type":"compose","secret":"must-not-pass"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeploymentUnitFile(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want strict unknown-field rejection", err)
	}
}
