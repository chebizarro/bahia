package main

import (
	"context"
	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestRootCommandExposesOperatorFlags(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newRootCommand()
	if flag := cmd.PersistentFlags().Lookup("relay"); flag == nil {
		t.Fatal("root command missing --relay")
	}
	if flag := cmd.PersistentFlags().Lookup("bootstrap-relay"); flag == nil {
		t.Fatal("root command missing --bootstrap-relay")
	}
	if flag := cmd.PersistentFlags().Lookup("service-pubkey"); flag == nil {
		t.Fatal("root command missing --service-pubkey")
	}
	if flag := cmd.PersistentFlags().Lookup("trusted-service-pubkey"); flag == nil {
		t.Fatal("root command missing --trusted-service-pubkey")
	}
	for _, name := range []string{"server", "http-fallback"} {
		if cmd.PersistentFlags().Lookup(name) != nil {
			t.Fatalf("root command still exposes --%s", name)
		}
	}
	for _, name := range []string{"encrypted", "result-timeout", "result-retries"} {
		if flag := cmd.PersistentFlags().Lookup(name); flag == nil {
			t.Fatalf("root command missing --%s", name)
		}
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-bunker-file"); flag == nil {
		t.Fatal("root command missing --nostr-bunker-file")
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-client-key-file"); flag == nil {
		t.Fatal("root command missing --nostr-client-key-file")
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-bunker-relay"); flag == nil {
		t.Fatal("root command missing --nostr-bunker-relay")
	}
}

func TestResolveOperatorRelaysPrecedence(t *testing.T) {
	t.Run("flag beats env and discovery", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env.example")
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("relay", "wss://flag.example,wss://flag.example/"); err != nil {
			t.Fatalf("set relay flag: %v", err)
		}
		discoveryCalls := 0
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			discoveryCalls++
			return []string{"wss://discovered.example"}, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://flag.example" {
			t.Fatalf("relays = %#v, want flag relay only", relays)
		}
		if discoveryCalls != 0 {
			t.Fatalf("discoveryCalls = %d, want no discovery when --relay is explicit", discoveryCalls)
		}
	})

	t.Run("env is used when flag is absent", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env1.example, wss://env2.example")
		cmd := newOperatorFlagTestCommand(t)
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			t.Fatal("discovery must not run when BAHIA_NOSTR_RELAYS is configured")
			return nil, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://env1.example,wss://env2.example" {
			t.Fatalf("relays = %#v, want env relays", relays)
		}
	})

	t.Run("trusted bootstrap discovery runs only after final relay config is absent", func(t *testing.T) {
		resetOperatorGlobals(t)
		servicePubkey := nostr.Generate().Public().Hex()
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("bootstrap-relay", "wss://bootstrap.example"); err != nil {
			t.Fatalf("set bootstrap relay: %v", err)
		}
		if err := cmd.Root().PersistentFlags().Set("trusted-service-pubkey", servicePubkey); err != nil {
			t.Fatalf("set trusted service pubkey: %v", err)
		}
		discoveryCalls := 0
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			discoveryCalls++
			if strings.Join(cfg.BootstrapRelays, ",") != "wss://bootstrap.example" {
				t.Fatalf("BootstrapRelays = %#v", cfg.BootstrapRelays)
			}
			if strings.Join(cfg.TrustedServicePubkeys, ",") != servicePubkey {
				t.Fatalf("TrustedServicePubkeys = %#v", cfg.TrustedServicePubkeys)
			}
			return []string{"wss://contextvm.example"}, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if discoveryCalls != 1 || strings.Join(relays, ",") != "wss://contextvm.example" {
			t.Fatalf("discoveryCalls=%d relays=%#v", discoveryCalls, relays)
		}
	})

	t.Run("service pubkey can provide single-service discovery trust", func(t *testing.T) {
		resetOperatorGlobals(t)
		servicePubkey := nostr.Generate().Public().Hex()
		t.Setenv("BAHIA_NOSTR_BOOTSTRAP_RELAYS", "wss://bootstrap.example")
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("service-pubkey", servicePubkey); err != nil {
			t.Fatalf("set service pubkey: %v", err)
		}
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			if strings.Join(cfg.TrustedServicePubkeys, ",") != servicePubkey {
				t.Fatalf("TrustedServicePubkeys = %#v", cfg.TrustedServicePubkeys)
			}
			return []string{"wss://contextvm.example"}, nil
		})
		defer restoreDiscovery()
		if relays, err := resolveOperatorRelays(cmd); err != nil || strings.Join(relays, ",") != "wss://contextvm.example" {
			t.Fatalf("relays=%#v error=%v", relays, err)
		}
	})

	t.Run("missing final and bootstrap configuration fails deterministically", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_ORG_ID", "")
		t.Setenv("BAHIA_NOSTR_RELAYS", "")
		cmd := newOperatorFlagTestCommand(t)
		relays, err := resolveOperatorRelays(cmd)
		if err == nil || !strings.Contains(err.Error(), "no operator relays configured") {
			t.Fatalf("resolveOperatorRelays() error = %v, want explicit relay failure", err)
		}
		if relays != nil {
			t.Fatalf("relays = %#v, want nil on missing explicit config", relays)
		}
	})

	t.Run("partial bootstrap discovery configuration fails deterministically", func(t *testing.T) {
		resetOperatorGlobals(t)
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("bootstrap-relay", "wss://bootstrap.example"); err != nil {
			t.Fatalf("set bootstrap relay: %v", err)
		}
		if _, err := resolveOperatorRelays(cmd); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
			t.Fatalf("bootstrap-only error = %v, want trusted pubkey requirement", err)
		}
	})
}

func newOperatorFlagTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "bahia"}
	root.PersistentFlags().StringArrayVar(&operatorRelays, "relay", nil, "")
	root.PersistentFlags().StringArrayVar(&operatorBootstrapRelays, "bootstrap-relay", nil, "")
	root.PersistentFlags().StringVar(&operatorServicePubkey, "service-pubkey", "", "")
	root.PersistentFlags().StringArrayVar(&operatorTrustedServicePubkeys, "trusted-service-pubkey", nil, "")
	root.PersistentFlags().BoolVar(&operatorEncrypted, "encrypted", false, "")
	root.PersistentFlags().DurationVar(&operatorResultTimeout, "result-timeout", client.DefaultOperatorResultTimeout, "")
	root.PersistentFlags().IntVar(&operatorResultRetries, "result-retries", client.DefaultOperatorResultRetries, "")
	root.PersistentFlags().StringVar(&nostrKeyFile, "nostr-key-file", "", "")
	root.PersistentFlags().StringVar(&nostrBunkerFile, "nostr-bunker-file", "", "")
	root.PersistentFlags().StringArrayVar(&nostrBunkerRelays, "nostr-bunker-relay", nil, "")
	root.PersistentFlags().StringVar(&nostrClientKeyFile, "nostr-client-key-file", "", "")
	cmd := &cobra.Command{Use: "test"}
	root.AddCommand(cmd)
	return cmd
}

func replaceOperatorDiscovery(discover func(context.Context, client.OperatorRelayDiscoveryConfig) ([]string, error)) func() {
	previous := discoverOperatorRelaysForCLI
	discoverOperatorRelaysForCLI = discover
	return func() { discoverOperatorRelaysForCLI = previous }
}

func resetOperatorGlobals(t *testing.T) {
	t.Helper()
	outputFormat = "table"
	nostrKeyFile = ""
	nostrBunkerFile = ""
	nostrBunkerRelays = nil
	nostrClientKeyFile = ""
	operatorRelays = nil
	operatorBootstrapRelays = nil
	operatorServicePubkey = ""
	operatorTrustedServicePubkeys = nil
	operatorEncrypted = false
	operatorResultTimeout = client.DefaultOperatorResultTimeout
	operatorResultRetries = client.DefaultOperatorResultRetries
	operatorIntentOrg = ""
	t.Setenv("BAHIA_ORG_ID", "")
	t.Setenv("BAHIA_NOSTR_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_BOOTSTRAP_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_SERVICE_PUBKEY", "")
	t.Setenv("BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS", "")
	t.Setenv("BAHIA_NOSTR_KEY_FILE", "")
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_FILE", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_CLIENT_KEY_FILE", "")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", "")
	t.Cleanup(func() {
		outputFormat = "table"
		nostrKeyFile = ""
		nostrBunkerFile = ""
		nostrBunkerRelays = nil
		nostrClientKeyFile = ""
		operatorRelays = nil
		operatorBootstrapRelays = nil
		operatorServicePubkey = ""
		operatorTrustedServicePubkeys = nil
		operatorEncrypted = false
		operatorResultTimeout = client.DefaultOperatorResultTimeout
		operatorResultRetries = client.DefaultOperatorResultRetries
		operatorIntentOrg = ""
	})
}
