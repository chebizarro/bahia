package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type enrollmentConfig struct {
	IdentityID                string `json:"identity_id"`
	ControllerPubkey          string `json:"controller_pubkey"`
	RuntimePubkey             string `json:"runtime_pubkey"`
	ManagedPubkey             string `json:"managed_pubkey"`
	ProvisionerPubkey         string `json:"provisioner_pubkey"`
	StateDir                  string `json:"state_dir"`
	ClientKeyDir              string `json:"client_key_dir"`
	SignetContainer           string `json:"signet_container"`
	SignetConfigPath          string `json:"signet_config_path"`
	ProvisionerCredentialFile string `json:"provisioner_credential_file"`
}

func main() {
	configPath := flag.String("config", "", "path to secret-free Metiq enrollment configuration")
	flag.Parse()
	if *configPath == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: metiq-signet-enrollment -config <path> enroll|reconcile|inspect|revoke|compensate")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *configPath, flag.Arg(0), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath, action string, stdout *os.File) error {
	cfg, err := loadEnrollmentConfig(configPath)
	if err != nil {
		return err
	}
	admin, err := soulfactory.NewContainerSignetctl(soulfactory.SignetctlConfig{
		Container: cfg.SignetContainer, ConfigPath: cfg.SignetConfigPath,
		ProvisionerCredentialFile: cfg.ProvisionerCredentialFile,
		CredentialOwnerUID:        os.Geteuid(),
	})
	if err != nil {
		return err
	}
	profile := soulfactory.MetiqRuntimeSignetEnrollmentProfile()
	manager, err := soulfactory.NewOpenClawSignetEnrollmentManager(soulfactory.OpenClawSignetEnrollmentConfig{
		StateDir: cfg.StateDir, ClientKeyDir: cfg.ClientKeyDir,
		FileOwnerUID: os.Geteuid(), PolicyAdmin: admin,
		Verifier: soulfactory.NIP46SigningConnectivityVerifier{}, Profile: &profile,
	})
	if err != nil {
		return err
	}
	req := soulfactory.OpenClawSignetEnrollmentRequest{
		AgentID: cfg.IdentityID, ControllerPubkey: cfg.ControllerPubkey,
		RuntimePubkey: cfg.RuntimePubkey, ManagedPubkey: cfg.ManagedPubkey,
		ProvisionerPubkey: cfg.ProvisionerPubkey,
	}

	var output interface{}
	switch action {
	case "enroll":
		existing, err := manager.Inspect(ctx, cfg.IdentityID)
		if err != nil {
			return err
		}
		if existing == nil {
			oneTimeBunkerURI, err := admin.Provision(ctx, cfg.IdentityID)
			if err != nil {
				return fmt.Errorf("provision dedicated Metiq Signet identity: %w", err)
			}
			if err := manager.StageHandoff(ctx, cfg.IdentityID, oneTimeBunkerURI); err != nil {
				return fmt.Errorf("stage protected one-time Metiq bunker handoff: %w", err)
			}
			oneTimeBunkerURI = ""
		}
		output, err = manager.Enroll(ctx, req)
		if err != nil {
			return err
		}
	case "reconcile":
		output, err = manager.Reconcile(ctx, req)
		if err != nil {
			return err
		}
	case "inspect":
		output, err = manager.Inspect(ctx, cfg.IdentityID)
		if err != nil {
			return err
		}
		if output == nil {
			return fmt.Errorf("Metiq Signet enrollment does not exist")
		}
	case "revoke", "compensate":
		if err := manager.Revoke(ctx, cfg.IdentityID); err != nil {
			return err
		}
		output = map[string]string{"identity_id": cfg.IdentityID, "status": "client_revoked"}
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func loadEnrollmentConfig(path string) (enrollmentConfig, error) {
	var cfg enrollmentConfig
	clean := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(clean) {
		return cfg, fmt.Errorf("enrollment config path must be absolute")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return cfg, fmt.Errorf("read enrollment config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse enrollment config: %w", err)
	}
	if err := validateEnrollmentConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validateEnrollmentConfig(cfg enrollmentConfig) error {
	if strings.TrimSpace(cfg.IdentityID) == "" {
		return errors.New("identity_id is required")
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.RuntimePubkey), strings.TrimSpace(cfg.ManagedPubkey)) {
		return errors.New("runtime_pubkey and managed_pubkey must identify the same dedicated Metiq identity")
	}
	if strings.EqualFold(strings.TrimSpace(cfg.RuntimePubkey), strings.TrimSpace(cfg.ControllerPubkey)) || strings.EqualFold(strings.TrimSpace(cfg.RuntimePubkey), strings.TrimSpace(cfg.ProvisionerPubkey)) {
		return errors.New("dedicated Metiq runtime identity must differ from controller and provisioner identities")
	}
	for name, value := range map[string]string{
		"controller_pubkey": cfg.ControllerPubkey, "runtime_pubkey": cfg.RuntimePubkey,
		"managed_pubkey": cfg.ManagedPubkey, "provisioner_pubkey": cfg.ProvisionerPubkey,
	} {
		decoded, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(value)))
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("%s must be a 64-character hex pubkey", name)
		}
	}
	for name, path := range map[string]string{
		"state_dir": cfg.StateDir, "client_key_dir": cfg.ClientKeyDir,
		"provisioner_credential_file": cfg.ProvisionerCredentialFile,
	} {
		if !filepath.IsAbs(strings.TrimSpace(path)) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if strings.TrimSpace(cfg.SignetContainer) == "" || strings.TrimSpace(cfg.SignetConfigPath) == "" {
		return errors.New("signet_container and signet_config_path are required")
	}
	return nil
}
