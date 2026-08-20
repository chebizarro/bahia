package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnrollmentConfigIsSecretFreeAndBindsDedicatedIdentity(t *testing.T) {
	root := t.TempDir()
	pubkey := strings.Repeat("a", 64)
	path := filepath.Join(root, "config.json")
	content := `{
  "identity_id":"metiq-runtime",
  "controller_pubkey":"` + strings.Repeat("b", 64) + `",
  "runtime_pubkey":"` + pubkey + `",
  "managed_pubkey":"` + pubkey + `",
  "provisioner_pubkey":"` + strings.Repeat("c", 64) + `",
  "state_dir":"` + filepath.Join(root, "state") + `",
  "client_key_dir":"` + filepath.Join(root, "keys") + `",
  "signet_container":"signetd",
  "signet_config_path":"/etc/signet/signet.conf",
  "provisioner_credential_file":"` + filepath.Join(root, "provisioner.nsec") + `"
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadEnrollmentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityID != "metiq-runtime" || cfg.RuntimePubkey != cfg.ManagedPubkey {
		t.Fatalf("config = %+v", cfg)
	}
	if strings.Contains(content, "bunker://") || strings.Contains(content, "nsec1") {
		t.Fatal("enrollment config contains secret material")
	}
}

func TestValidateEnrollmentConfigRejectsSharedIdentity(t *testing.T) {
	cfg := enrollmentConfig{
		IdentityID: "metiq-runtime", RuntimePubkey: strings.Repeat("a", 64), ManagedPubkey: strings.Repeat("b", 64),
		StateDir: "/state", ClientKeyDir: "/keys", SignetContainer: "signetd",
		SignetConfigPath: "/etc/signet/signet.conf", ProvisionerCredentialFile: "/run/secrets/provisioner",
	}
	if err := validateEnrollmentConfig(cfg); err == nil || !strings.Contains(err.Error(), "same dedicated") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadEnrollmentConfigRejectsUnknownSecretFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"identity_id":"metiq-runtime","bunker_uri":"must-not-be-configured"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEnrollmentConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}
