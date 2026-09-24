package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSignerFirstOperatorAllowlists(t *testing.T) {
	pubkey := strings.Repeat("abcdef01", 8)
	for _, surface := range []string{"adoption", "direct_runtime_actions"} {
		t.Run(surface, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				entries string
				wantErr string
			}{
				{name: "omitted", wantErr: "operator allowlist is required"},
				{name: "empty", entries: "  allowed_pubkeys: []\n", wantErr: "operator allowlist is required"},
				{name: "blank", entries: "  allowed_pubkeys: [\"  \", \"\"]\n", wantErr: "operator allowlist is required"},
				{name: "subject only", entries: "  allowed_subjects: [ops]\n", wantErr: "allowed_subjects and allowed_emails do not authorize signer-first requests"},
				{name: "email only", entries: "  allowed_emails: [ops@example.com]\n", wantErr: "allowed_subjects and allowed_emails do not authorize signer-first requests"},
				{name: "short pubkey", entries: "  allowed_pubkeys: [abc123]\n", wantErr: "must be 64 hex characters"},
				{name: "non hex pubkey", entries: fmt.Sprintf("  allowed_pubkeys: [%q]\n", strings.Repeat("g", 64)), wantErr: "must be valid hex"},
				{name: "normalized and deduplicated", entries: fmt.Sprintf("  allowed_pubkeys: [%q, %q, \" \"]\n", " "+strings.ToUpper(pubkey)+" ", pubkey)},
				{name: "pubkey with compatibility identities", entries: fmt.Sprintf("  allowed_pubkeys: [%q]\n  allowed_subjects: [ops]\n  allowed_emails: [ops@example.com]\n", pubkey)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.yaml")
					// A global allowlist is not a substitute for the surface list.
					content := fmt.Sprintf("auth:\n  enabled: true\nnostr:\n  private_key: test-secret-key\n  authorized_pubkeys: [%q]\n%s:\n  enabled: true\n%s", pubkey, surface, tc.entries)
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
					cfg, err := Load(path)
					if tc.wantErr != "" {
						if err == nil || !strings.Contains(err.Error(), surface) || !strings.Contains(err.Error(), tc.wantErr) {
							t.Fatalf("Load() error = %v, want %s: %s", err, surface, tc.wantErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					keys := cfg.Adoption.AllowedPubkeys
					if surface == "direct_runtime_actions" {
						keys = cfg.DirectRuntime.AllowedPubkeys
					}
					if len(keys) != 1 || keys[0] != pubkey {
						t.Fatalf("loaded pubkeys = %v, want [%s]", keys, pubkey)
					}
				})
			}
		})
	}
}

func TestDisabledOperatorSurfacesAllowEmptyAllowlists(t *testing.T) {
	cfg := Defaults()
	if cfg.Adoption.Enabled || cfg.DirectRuntime.Enabled {
		t.Fatal("operator surfaces must be opt-in")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled surfaces with empty allowlists: %v", err)
	}
}
