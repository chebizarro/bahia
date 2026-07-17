package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/pkg/client"
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
			want: []string{"list", "get", "create"},
		},
		{
			name: "auth",
			cmd:  authCommands(),
			want: []string{"inspect"},
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

func TestResolveNIP98ProviderValidatesKey(t *testing.T) {
	resetNostrKeyGlobals(t)
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatalf("write invalid key file: %v", err)
	}
	cmd := newAuthFlagTestCommand()
	if err := cmd.PersistentFlags().Set("nostr-key-file", path); err != nil {
		t.Fatalf("set nostr-key-file flag: %v", err)
	}
	if _, err := resolveNIP98Provider(cmd); err == nil {
		t.Fatal("expected invalid key error")
	}

	cmd = newAuthFlagTestCommand()
	key := nostr.Generate().Hex()
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", key)
	provider, err := resolveNIP98Provider(cmd)
	if err != nil {
		t.Fatalf("resolveNIP98Provider() error = %v", err)
	}
	pubkey, err := provider.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey() error = %v", err)
	}
	secret, err := nostr.SecretKeyFromHex(key)
	if err != nil {
		t.Fatalf("SecretKeyFromHex() error = %v", err)
	}
	wantPubkey := secret.Public().Hex()
	if pubkey != wantPubkey {
		t.Fatalf("pubkey = %s, want %s", pubkey, wantPubkey)
	}
}

func TestConfigureClientAuthAllowsNoKeyForPublicEndpoints(t *testing.T) {
	resetNostrKeyGlobals(t)
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	if err := configureClientAuth(newAuthFlagTestCommand(), client.New("http://example.com")); err != nil {
		t.Fatalf("configureClientAuth() error = %v", err)
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
