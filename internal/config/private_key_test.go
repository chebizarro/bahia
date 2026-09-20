package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LoadPrivateKey is the single loader two binaries now share. Its two security
// properties - refusing key material from the environment, and bounding how
// much it will read - previously existed as duplicated code in each binary and
// had no test in either. These pin them.
func TestLoadPrivateKeyRefusesEnvironmentSuppliedKey(t *testing.T) {
	const envKey = "BAHIA_TEST_PRIVATE_KEY"
	t.Setenv(envKey, "nsec1shouldneverbeaccepted")

	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("nsec1fromfile"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	key, err := LoadPrivateKey(path, envKey)
	if err == nil {
		t.Fatalf("env-supplied key was accepted, returned %q", key)
	}
	if !strings.Contains(err.Error(), envKey) {
		t.Fatalf("error does not name the offending variable: %v", err)
	}
}

func TestLoadPrivateKeyRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxPrivateKeyFileBytes+1)), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	if _, err := LoadPrivateKey(path, "BAHIA_TEST_UNSET_PRIVATE_KEY"); err == nil {
		t.Fatal("oversized key file was accepted")
	}
}

func TestLoadPrivateKeyReadsTrimmedFileContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("  nsec1valid\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	key, err := LoadPrivateKey(path, "BAHIA_TEST_UNSET_PRIVATE_KEY")
	if err != nil {
		t.Fatalf("LoadPrivateKey: %v", err)
	}
	if key != "nsec1valid" {
		t.Fatalf("key = %q, want trimmed contents", key)
	}
}
