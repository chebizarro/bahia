package config

import (
	"strings"
	"testing"
)

func TestAssistantExtensionPathsRejectTraversal(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Assistant.Subagents = AssistantExtensionSourceConfig{Enabled: true, Paths: []string{"/etc/bahia/../../secrets"}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "parent traversal") {
		t.Fatalf("expected parent traversal rejection, got %v", err)
	}
}

func TestAssistantExtensionPathsRequiredWhenEnabled(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Assistant.Hooks = AssistantExtensionSourceConfig{Enabled: true}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "assistant.hooks.paths is required") {
		t.Fatalf("expected required-paths error, got %v", err)
	}
}

func TestAssistantExtensionPathsNormalizeAndAllowClean(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Assistant.Skills = AssistantExtensionSourceConfig{Enabled: true, Paths: []string{" /srv/bahia/skills/ ", "", "config/skills"}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("clean paths should validate: %v", err)
	}
	got := cfg.Assistant.Skills.Paths
	if len(got) != 2 || got[0] != "/srv/bahia/skills" || got[1] != "config/skills" {
		t.Fatalf("normalized paths = %#v", got)
	}
}
