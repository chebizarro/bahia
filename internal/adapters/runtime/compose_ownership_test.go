package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

// helper to create a valid .bahia/render-state.json marker in dir.
func createBahiaMarker(t *testing.T, dir string) {
	t.Helper()
	markerDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"schema_version": 1,
		"renderer":       "compose",
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(markerDir, "render-state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateComposeOwnership_BahiaMarker(t *testing.T) {
	dir := t.TempDir()
	createBahiaMarker(t, dir)

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if !status.Owned {
		t.Fatal("expected Owned=true for directory with .bahia marker")
	}
	if status.Reason != OwnershipBahiaMarker {
		t.Fatalf("expected reason OwnershipBahiaMarker, got %s", status.Reason)
	}
	if status.Error != nil {
		t.Fatalf("unexpected error: %v", status.Error)
	}
	if status.MarkerPath == "" {
		t.Fatal("expected MarkerPath to be set")
	}
	if status.ComposePath == "" {
		t.Fatal("expected ComposePath to be set")
	}
}

func TestValidateComposeOwnership_MissingDir(t *testing.T) {
	status := ValidateComposeOwnership("/nonexistent/path/that/should/not/exist", ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for missing directory")
	}
	if status.Reason != OwnershipMissingDir {
		t.Fatalf("expected reason OwnershipMissingDir, got %s", status.Reason)
	}
	if status.Error == nil {
		t.Fatal("expected an error for missing directory")
	}
}

func TestValidateComposeOwnership_EmptyDir(t *testing.T) {
	status := ValidateComposeOwnership("", ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for empty compose_dir")
	}
	if status.Reason != OwnershipMissingDir {
		t.Fatalf("expected reason OwnershipMissingDir, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_MalformedMarker_MissingRenderState(t *testing.T) {
	dir := t.TempDir()
	// Create .bahia/ directory but no render-state.json
	if err := os.MkdirAll(filepath.Join(dir, ".bahia"), 0o755); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for malformed marker")
	}
	if status.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", status.Reason)
	}
	if status.Error == nil {
		t.Fatal("expected an error for malformed marker")
	}
}

func TestValidateComposeOwnership_MalformedMarker_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "render-state.json"), []byte("{not valid json}"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for invalid JSON in render-state.json")
	}
	if status.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_MalformedMarker_EmptyJSON(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "render-state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for empty JSON object in render-state.json")
	}
	if status.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_MalformedMarker_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	// Create .bahia as a file, not a directory
	if err := os.WriteFile(filepath.Join(dir, ".bahia"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false when .bahia is a file")
	}
	if status.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_NotOwned(t *testing.T) {
	dir := t.TempDir()
	// Create a compose file but no .bahia/ marker — operator-authored
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("version: '3'\nservices: {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for directory without .bahia marker")
	}
	if status.Reason != OwnershipNotOwned {
		t.Fatalf("expected reason OwnershipNotOwned, got %s", status.Reason)
	}
	if status.Error != nil {
		t.Fatalf("unexpected error for not-owned dir: %v", status.Error)
	}
}

func TestValidateComposeOwnership_ExplicitConfig(t *testing.T) {
	// Explicit config should work even if the directory doesn't exist yet.
	status := ValidateComposeOwnership("/some/future/dir", ComposeOwnershipConfig{
		BahiaOwned: boolPtr(true),
	})

	if !status.Owned {
		t.Fatal("expected Owned=true with explicit config override")
	}
	if status.Reason != OwnershipExplicitConfig {
		t.Fatalf("expected reason OwnershipExplicitConfig, got %s", status.Reason)
	}
	if status.Error != nil {
		t.Fatalf("unexpected error: %v", status.Error)
	}
}

func TestValidateComposeOwnership_ExplicitConfigFalse_FallsThrough(t *testing.T) {
	dir := t.TempDir()
	createBahiaMarker(t, dir)

	// Explicit false should not override — marker detection still applies.
	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{
		BahiaOwned: boolPtr(false),
	})

	if !status.Owned {
		t.Fatal("expected Owned=true — explicit false should fall through to marker detection")
	}
	if status.Reason != OwnershipBahiaMarker {
		t.Fatalf("expected reason OwnershipBahiaMarker, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_ExplicitConfigNil_FallsThrough(t *testing.T) {
	dir := t.TempDir()

	// nil BahiaOwned should fall through to marker detection.
	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for dir without marker and nil config")
	}
	if status.Reason != OwnershipNotOwned {
		t.Fatalf("expected reason OwnershipNotOwned, got %s", status.Reason)
	}
}

func TestValidateComposeOwnership_ErrorsDoNotLeakPaths(t *testing.T) {
	// Verify that error messages are generic and don't include raw filesystem
	// details that could leak deployment topology.
	status := ValidateComposeOwnership("", ComposeOwnershipConfig{})
	if status.Error == nil {
		t.Fatal("expected error for empty path")
	}
	errMsg := status.Error.Error()
	if errMsg != "compose_dir is empty" {
		t.Fatalf("unexpected error message: %s", errMsg)
	}
}

func TestComposeOwnershipReason_String(t *testing.T) {
	cases := []struct {
		reason ComposeOwnershipReason
		want   string
	}{
		{OwnershipUnknown, "unknown"},
		{OwnershipBahiaMarker, "bahia_marker"},
		{OwnershipExplicitConfig, "explicit_config"},
		{OwnershipNotOwned, "not_owned"},
		{OwnershipMissingDir, "missing_dir"},
		{OwnershipMalformed, "malformed"},
	}
	for _, tc := range cases {
		if got := tc.reason.String(); got != tc.want {
			t.Errorf("ComposeOwnershipReason(%d).String() = %q, want %q", tc.reason, got, tc.want)
		}
	}
}

func TestValidateComposeOwnership_MalformedMarker_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "render-state.json"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	status := ValidateComposeOwnership(dir, ComposeOwnershipConfig{})

	if status.Owned {
		t.Fatal("expected Owned=false for empty render-state.json")
	}
	if status.Reason != OwnershipMalformed {
		t.Fatalf("expected reason OwnershipMalformed, got %s", status.Reason)
	}
}
