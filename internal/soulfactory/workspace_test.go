package soulfactory

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestWorkspaceOpenClawConfigUsesConfiguredValues(t *testing.T) {
	controller := strings.Repeat("a", 64)
	agentPubkey := strings.Repeat("b", 64)
	manager := NewWorkspaceManager(WorkspaceConfig{
		GiteaURL:              "https://git.example",
		OpenClawRelays:        []string{" wss://relay-a.example/", "wss://relay-b.example"},
		OpenClawControllers:   []string{controller},
		OpenClawModel:         "operator/model-v1",
		OpenClawPrivateKeyRef: "secret://souls/scout/nostr-private-key",
		AgentMemoryMCPURLRef:  "config://souls/scout/agent-memory-mcp-url",
		NgitRelays:            []string{"wss://git-relay.example"},
		GatewayPort:           18781,
	}, nil)
	dir := t.TempDir()
	if err := manager.createWorkspaceFiles(dir, &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: agentPubkey}); err != nil {
		t.Fatalf("createWorkspaceFiles() error = %v", err)
	}

	raw := readTestFile(t, dir+"/config/openclaw.json")
	for _, forbidden := range []string{"wss://relay.sharegap.net", "wss://armada.sharegap.net", "anthropic/claude-sonnet-4-6", "__INJECTED_AT_RUNTIME_VIA_SIGNET__", "__AGENT_MEMORY_MCP_URL__", "cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("openclaw config contains forbidden placeholder/hardcoded value %q: %s", forbidden, raw)
		}
	}

	var parsed struct {
		Gateway struct {
			Port      int    `json:"port"`
			AgentName string `json:"agentName"`
		} `json:"gateway"`
		Model    string `json:"model"`
		Channels struct {
			Nostr struct {
				Relays         []string `json:"relays"`
				Pubkey         string   `json:"pubkey"`
				PrivateKeyRef  string   `json:"privateKeyRef"`
				AllowedPubkeys []string `json:"allowedPubkeys"`
			} `json:"nostr"`
		} `json:"channels"`
		MCPServers map[string]struct {
			Transport string `json:"transport"`
			URLRef    string `json:"urlRef"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("openclaw config JSON error = %v", err)
	}
	if parsed.Gateway.Port != 18781 || parsed.Gateway.AgentName != "Scout" || parsed.Model != "operator/model-v1" {
		t.Fatalf("openclaw scalar config = %+v model=%q", parsed.Gateway, parsed.Model)
	}
	if got := strings.Join(parsed.Channels.Nostr.Relays, ","); got != "wss://relay-a.example,wss://relay-b.example" {
		t.Fatalf("relays = %q", got)
	}
	if parsed.Channels.Nostr.Pubkey != agentPubkey || parsed.Channels.Nostr.PrivateKeyRef != "secret://souls/scout/nostr-private-key" {
		t.Fatalf("nostr identity config = %+v", parsed.Channels.Nostr)
	}
	if len(parsed.Channels.Nostr.AllowedPubkeys) != 1 || parsed.Channels.Nostr.AllowedPubkeys[0] != controller {
		t.Fatalf("controllers = %#v", parsed.Channels.Nostr.AllowedPubkeys)
	}
	if parsed.MCPServers["agent-memory"].URLRef != "config://souls/scout/agent-memory-mcp-url" {
		t.Fatalf("agent-memory MCP config = %#v", parsed.MCPServers["agent-memory"])
	}
}

func TestWorkspaceOpenClawConfigRejectsInvalidOrMissingValues(t *testing.T) {
	validPubkey := strings.Repeat("a", 64)
	base := WorkspaceConfig{
		GiteaURL:              "https://git.example",
		OpenClawRelays:        []string{"wss://relay.example"},
		OpenClawControllers:   []string{validPubkey},
		OpenClawModel:         "operator/model-v1",
		OpenClawPrivateKeyRef: "secret://souls/scout/nostr-private-key",
		AgentMemoryMCPURLRef:  "config://souls/scout/agent-memory-mcp-url",
		NgitRelays:            []string{"wss://relay.example"},
	}
	tests := []struct {
		name string
		cfg  WorkspaceConfig
		soul *domain.AgentSoul
		want string
	}{
		{name: "short soul pubkey", cfg: base, soul: &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: "short"}, want: "soul Nostr pubkey is invalid"},
		{name: "short controller", cfg: func() WorkspaceConfig { c := base; c.OpenClawControllers = []string{"short"}; return c }(), soul: &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: validPubkey}, want: "controller pubkey"},
		{name: "missing relays", cfg: func() WorkspaceConfig { c := base; c.OpenClawRelays = nil; c.NgitRelays = nil; return c }(), soul: &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: validPubkey}, want: "relays are required"},
		{name: "missing secret ref", cfg: func() WorkspaceConfig { c := base; c.OpenClawPrivateKeyRef = ""; return c }(), soul: &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: validPubkey}, want: "private key secret reference is required"},
		{name: "invalid gateway port", cfg: func() WorkspaceConfig { c := base; c.GatewayPort = 70000; return c }(), soul: &domain.AgentSoul{AgentID: "scout", Name: "Scout", NostrPubkey: validPubkey}, want: "gateway port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewWorkspaceManager(tt.cfg, nil)
			err := manager.createWorkspaceFiles(t.TempDir(), tt.soul)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("createWorkspaceFiles() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
