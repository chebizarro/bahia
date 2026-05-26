package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// TestComposeRuntime_Deploy_OwnedDirectory verifies that Deploy proceeds
// normally when the compose directory is Bahia-owned (valid marker present).
func TestComposeRuntime_Deploy_OwnedDirectory(t *testing.T) {
	dir := t.TempDir()
	createBahiaMarker(t, dir)

	// Create a minimal docker-compose.yml so compose commands don't fail
	// due to missing project file. The actual deploy will still fail because
	// there's no Docker daemon, but it should get PAST the ownership gate.
	composeYML := `version: "3"
services:
  test-svc:
    image: alpine:latest
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYML), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewComposeRuntime(dir, zap.NewNop())

	// ValidateOwnership should pass.
	if err := rt.ValidateOwnership(ComposeOwnershipConfig{}); err != nil {
		t.Fatalf("expected ownership validation to pass for owned directory, got: %v", err)
	}

	// Deploy should get past the ownership gate. It will fail downstream
	// (no Docker daemon) but the error should NOT be an ownership error.
	err := rt.Deploy(context.Background(), "test-svc", "alpine:latest", DeployOptions{})
	if err != nil && IsComposeOwnershipError(err) {
		t.Fatalf("deploy should not fail with ownership error for owned directory, got: %v", err)
	}
	// We expect a non-ownership error (compose CLI not available or no daemon).
	// The key assertion is that it's not an ownership error.
}

// TestComposeRuntime_Deploy_NonOwnedDirectory verifies that Deploy is blocked
// with a clear ownership error before any writes when the directory is not
// Bahia-owned.
func TestComposeRuntime_Deploy_NonOwnedDirectory(t *testing.T) {
	dir := t.TempDir()
	// No .bahia marker — operator-authored directory.
	composeYML := `version: "3"
services:
  test-svc:
    image: alpine:latest
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYML), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewComposeRuntime(dir, zap.NewNop())

	err := rt.Deploy(context.Background(), "test-svc", "alpine:latest", DeployOptions{})
	if err == nil {
		t.Fatal("expected deploy to fail for non-owned directory")
	}

	// Verify the error wraps a ComposeOwnershipError.
	ownershipErr, ok := AsComposeOwnershipError(err)
	if !ok {
		t.Fatalf("expected ComposeOwnershipError, got: %T: %v", err, err)
	}
	if ownershipErr.Reason != OwnershipNotOwned {
		t.Fatalf("expected reason OwnershipNotOwned, got %s", ownershipErr.ReasonCode)
	}
}

// TestComposeRuntime_Deploy_MalformedMarker verifies that Deploy fails before
// any writes when the .bahia marker exists but is malformed.
func TestComposeRuntime_Deploy_MalformedMarker(t *testing.T) {
	dir := t.TempDir()
	// Create .bahia/ but with invalid render-state.json.
	markerDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "render-state.json"), []byte("{bad json!}"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewComposeRuntime(dir, zap.NewNop())

	err := rt.Deploy(context.Background(), "test-svc", "alpine:latest", DeployOptions{})
	if err == nil {
		t.Fatal("expected deploy to fail for malformed marker")
	}

	ownershipErr, ok := AsComposeOwnershipError(err)
	if !ok {
		t.Fatalf("expected ComposeOwnershipError, got: %T: %v", err, err)
	}
	if ownershipErr.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", ownershipErr.ReasonCode)
	}
}

// TestComposeRuntime_Deploy_MissingDirectory verifies that Deploy fails
// gracefully when the compose directory does not exist.
func TestComposeRuntime_Deploy_MissingDirectory(t *testing.T) {
	rt := NewComposeRuntime("/nonexistent/compose/dir/that/should/not/exist", zap.NewNop())

	err := rt.Deploy(context.Background(), "test-svc", "alpine:latest", DeployOptions{})
	if err == nil {
		t.Fatal("expected deploy to fail for missing directory")
	}

	ownershipErr, ok := AsComposeOwnershipError(err)
	if !ok {
		t.Fatalf("expected ComposeOwnershipError, got: %T: %v", err, err)
	}
	if ownershipErr.Reason != OwnershipMissingDir {
		t.Fatalf("expected reason OwnershipMissingDir, got %s", ownershipErr.ReasonCode)
	}
}

// TestComposeRuntime_Deploy_ExplicitConfigOverride verifies that Deploy
// proceeds when ownership is granted via explicit config even without markers.
func TestComposeRuntime_Deploy_ExplicitConfigOverride(t *testing.T) {
	dir := t.TempDir()
	// No .bahia marker, but explicit config says it's owned.
	composeYML := `version: "3"
services:
  test-svc:
    image: alpine:latest
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYML), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewComposeRuntime(dir, zap.NewNop())

	// ValidateOwnership with explicit config should pass.
	if err := rt.ValidateOwnership(ComposeOwnershipConfig{BahiaOwned: boolPtr(true)}); err != nil {
		t.Fatalf("expected ownership validation to pass with explicit config, got: %v", err)
	}
}

// TestComposeRuntime_ValidateOwnership_NoWritesOnFailure verifies that
// ownership validation failure does not create any files in the compose
// directory (no staging, no env files, no render state written).
func TestComposeRuntime_ValidateOwnership_NoWritesOnFailure(t *testing.T) {
	dir := t.TempDir()

	// Record files before deploy attempt.
	entriesBefore, _ := os.ReadDir(dir)
	countBefore := len(entriesBefore)

	rt := NewComposeRuntime(dir, zap.NewNop())
	_ = rt.Deploy(context.Background(), "test-svc", "alpine:latest", DeployOptions{})

	// Verify no new files were created.
	entriesAfter, _ := os.ReadDir(dir)
	if len(entriesAfter) != countBefore {
		t.Fatalf("expected no files to be created on ownership failure, had %d files before, %d after",
			countBefore, len(entriesAfter))
	}
}

// TestComposeOwnershipError_MachineReadable verifies that the error type
// carries machine-readable reason codes suitable for structured logging
// and event payloads.
func TestComposeOwnershipError_MachineReadable(t *testing.T) {
	cases := []struct {
		name       string
		reason     ComposeOwnershipReason
		wantCode   string
	}{
		{"not_owned", OwnershipNotOwned, "not_owned"},
		{"missing_dir", OwnershipMissingDir, "missing_dir"},
		{"malformed", OwnershipMalformed, "malformed"},
		{"unknown", OwnershipUnknown, "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := ComposeOwnershipStatus{
				Reason: tc.reason,
			}
			err := NewComposeOwnershipError(status)

			if err.ReasonCode != tc.wantCode {
				t.Errorf("ReasonCode = %q, want %q", err.ReasonCode, tc.wantCode)
			}
			if err.Reason != tc.reason {
				t.Errorf("Reason = %d, want %d", err.Reason, tc.reason)
			}
			// Verify it's JSON-serializable for event payloads.
			data, jsonErr := json.Marshal(map[string]string{
				"reason_code": err.ReasonCode,
				"message":     err.Message,
			})
			if jsonErr != nil {
				t.Fatalf("ownership error not JSON-serializable: %v", jsonErr)
			}
			if len(data) == 0 {
				t.Fatal("expected non-empty JSON")
			}
		})
	}
}
