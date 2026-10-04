package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/spf13/cobra"
)

func TestCommandGroupsExposeExpectedSubcommands(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		want []string
	}{
		{
			name: "deployments",
			cmd:  deployCommands(),
			want: []string{"deploy", "rollback"},
		},
		{
			name: "services",
			cmd:  servicesCommands(),
			want: []string{"list", "get", "create", "actions"},
		},
		{
			name: "service actions",
			cmd:  serviceActionsCommands(),
			want: []string{"deploy", "restart", "stop"},
		},
		{
			name: "adopt",
			cmd:  adoptCommands(),
			want: []string{"scan", "import"},
		},
		{
			name: "environments",
			cmd:  environmentsCommands(),
			want: []string{"list", "get", "create", "update", "units"},
		},
		{
			name: "environment units",
			cmd:  newEnvironmentUnitsCommand(),
			want: []string{"list", "create", "update"},
		},
		{
			name: "auth",
			cmd:  authCommands(),
			want: []string{"inspect"},
		},
		{
			name: "config",
			cmd:  configCommands(),
			want: []string{"publish", "drift", "rollback"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range tt.want {
				if findDirectChild(tt.cmd, name) == nil {
					t.Fatalf("%s command missing child %q", tt.cmd.Use, name)
				}
			}
		})
	}
}

func TestRootCommandRegistersConfigGroup(t *testing.T) {
	root := newRootCommand()
	configCmd := findDirectChild(root, "config")
	if configCmd == nil {
		t.Fatal("root command missing config command group")
	}
	for _, name := range []string{"publish", "drift", "rollback"} {
		if findDirectChild(configCmd, name) == nil {
			t.Fatalf("bahia config missing child %q", name)
		}
	}

	found, _, err := root.Find([]string{"config", "drift"})
	if err != nil {
		t.Fatalf("resolve `bahia config drift`: %v", err)
	}
	if found.Name() != "drift" || found.Parent() != configCmd {
		t.Fatalf("`bahia config drift` resolved to %q", found.CommandPath())
	}
}

func TestConfigDriftRequiresRelays(t *testing.T) {
	resetOperatorGlobals(t)
	root := newRootCommand()
	root.SilenceUsage = true
	root.SilenceErrors = true
	root.SetArgs([]string{"--output", "json", "config", "drift"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "no operator relays configured") {
		t.Fatalf("bahia config drift error = %v, want missing-relay error", err)
	}
}

func TestRemovedHTTPFlagsAreUnknown(t *testing.T) {
	for _, flag := range []string{"--server", "--http-fallback"} {
		t.Run(flag, func(t *testing.T) {
			root := newRootCommand()
			root.SilenceUsage = true
			root.SilenceErrors = true
			root.SetArgs([]string{"services", "list", flag})
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("%s error = %v, want unknown flag", flag, err)
			}
		})
	}
}

func TestRootCommandDoesNotExposeRawPrivateKeyFlags(t *testing.T) {
	resetNostrKeyGlobals(t)
	flags := newRootCommand().PersistentFlags()
	if flags.Lookup("nsec") != nil || flags.Lookup("privkey") != nil {
		t.Fatal("raw Nostr private-key flags must not be registered")
	}
	if flags.Lookup("nostr-key-file") == nil {
		t.Fatal("nostr-key-file flag is missing")
	}
}

func TestResolveNostrPrivateKeyInputFromFileAndStdin(t *testing.T) {
	resetNostrKeyGlobals(t)
	key := nostr.Generate().Hex()
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("  "+key+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	cmd := newAuthFlagTestCommand()
	if err := cmd.PersistentFlags().Set("nostr-key-file", path); err != nil {
		t.Fatalf("set nostr-key-file flag: %v", err)
	}
	got, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		t.Fatalf("resolveNostrPrivateKeyInput() file error = %v", err)
	}
	if got != key {
		t.Fatalf("file key = %q, want configured key", got)
	}

	cmd = newAuthFlagTestCommand()
	cmd.SetIn(strings.NewReader(key + "\n"))
	if err := cmd.PersistentFlags().Set("nostr-key-file", "-"); err != nil {
		t.Fatalf("set stdin key flag: %v", err)
	}
	got, err = resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		t.Fatalf("resolveNostrPrivateKeyInput() stdin error = %v", err)
	}
	if got != key {
		t.Fatalf("stdin key = %q, want configured key", got)
	}
}

func TestResolveNostrPrivateKeyInputRejectsAmbiguousEnvironment(t *testing.T) {
	resetNostrKeyGlobals(t)
	t.Setenv("BAHIA_NOSTR_NSEC", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_KEY_FILE", filepath.Join(t.TempDir(), "nostr.key"))
	if _, err := resolveNostrPrivateKeyInput(newAuthFlagTestCommand()); err == nil {
		t.Fatal("expected ambiguous key source error")
	}
}

func TestAuthInspectUsesNostrSigner(t *testing.T) {
	resetNostrKeyGlobals(t)
	key := nostr.Generate().Hex()
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", key)
	signer, closeSigner, err := newCLIReadSigner(newAuthFlagTestCommand())
	if err != nil {
		t.Fatal(err)
	}
	if closeSigner != nil {
		defer closeSigner()
	}
	pubkey, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := nostr.SecretKeyFromHex(key)
	if err != nil {
		t.Fatal(err)
	}
	if pubkey != secret.Public() {
		t.Fatalf("pubkey = %s", pubkey.Hex())
	}
}

func TestParseAdoptionTargets(t *testing.T) {
	targets, err := parseAdoptionTargets([]string{"local", "prod=prod-docker"}, nil, []string{"local=dev", "prod=prod"})
	if err != nil {
		t.Fatalf("parseAdoptionTargets returned error: %v", err)
	}
	if len(targets) != 2 || targets[0].Name != "local" || targets[0].EndpointRef != "local" || targets[0].EnvironmentName != "dev" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if targets[1].EndpointRef != "prod-docker" {
		t.Fatalf("endpoint_ref not parsed: %#v", targets[1])
	}

	if _, err := parseAdoptionTargets([]string{"local", "local=other"}, nil, nil); err == nil {
		t.Fatal("expected duplicate alias error")
	}
	if _, err := parseAdoptionTargets([]string{"Local", "local=other"}, nil, nil); err == nil {
		t.Fatal("expected normalized duplicate alias error")
	}
	if _, err := parseAdoptionTargets([]string{"local"}, nil, []string{"missing=prod"}); err == nil {
		t.Fatal("expected unmatched environment alias error")
	}
	raw, err := parseAdoptionTargets(nil, []string{"local=unix:///docker.sock"}, nil)
	if err != nil {
		t.Fatalf("parse raw targets returned error: %v", err)
	}
	if len(raw) != 1 || raw[0].DockerHost != "unix:///docker.sock" || raw[0].EndpointRef != "" {
		t.Fatalf("unexpected raw targets: %#v", raw)
	}
}

func TestParseAdoptionSelections(t *testing.T) {
	selections, err := parseAdoptionSelections([]string{"local/abc123", "prod/def456=api-prod"})
	if err != nil {
		t.Fatalf("parseAdoptionSelections returned error: %v", err)
	}
	if len(selections) != 2 || selections[1].TargetName != "prod" || selections[1].ContainerID != "def456" || selections[1].ServiceNameOverride != "api-prod" {
		t.Fatalf("unexpected selections: %#v", selections)
	}
	if _, err := parseAdoptionSelections([]string{"local/abc123", "local/abc123"}); err == nil {
		t.Fatal("expected duplicate selection error")
	}
	if _, err := parseAdoptionSelections([]string{"bad"}); err == nil {
		t.Fatal("expected malformed selection error")
	}
	if _, err := parseAdoptionSelections([]string{"local/abc123=___"}); err == nil {
		t.Fatal("expected invalid service override error")
	}
}

func findDirectChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func newAuthFlagTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.PersistentFlags().StringVar(&nostrKeyFile, "nostr-key-file", "", "")
	return cmd
}

func resetNostrKeyGlobals(t *testing.T) {
	t.Helper()
	nostrKeyFile = ""
	t.Setenv("BAHIA_NOSTR_KEY_FILE", "")
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Cleanup(func() {
		nostrKeyFile = ""
	})
}
