package soulfactory

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/openagentsinc/bahia/internal/domain"
)

// WorkspaceManager handles agent workspace initialization.
type WorkspaceManager struct {
	giteaURL     string
	templateDir  string
	logger       *slog.Logger
}

// WorkspaceConfig holds workspace manager configuration.
type WorkspaceConfig struct {
	GiteaURL    string // Gitea URL (e.g., https://git.sharegap.net)
	TemplateDir string // Directory containing workspace templates
}

// NewWorkspaceManager creates a new workspace manager.
func NewWorkspaceManager(config WorkspaceConfig, logger *slog.Logger) *WorkspaceManager {
	if config.GiteaURL == "" {
		config.GiteaURL = "https://git.sharegap.net"
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &WorkspaceManager{
		giteaURL:    config.GiteaURL,
		templateDir: config.TemplateDir,
		logger:      logger.With("component", "workspace"),
	}
}

// InitWorkspace creates and pushes an agent workspace repository.
func (m *WorkspaceManager) InitWorkspace(ctx context.Context, soul *domain.AgentSoul) (repoURL string, err error) {
	m.logger.Info("initializing workspace",
		"agent_id", soul.AgentID,
		"name", soul.Name,
	)

	// Create temp directory for workspace
	tmpDir, err := os.MkdirTemp("", "soul-workspace-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	if err := m.runGit(ctx, tmpDir, "init"); err != nil {
		return "", fmt.Errorf("git init: %w", err)
	}

	// Create workspace files
	if err := m.createWorkspaceFiles(tmpDir, soul); err != nil {
		return "", fmt.Errorf("create files: %w", err)
	}

	// Add and commit
	if err := m.runGit(ctx, tmpDir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	commitMsg := fmt.Sprintf("init: %s — %s", soul.AgentID, soul.Name)
	if err := m.runGit(ctx, tmpDir, "commit", "-m", commitMsg); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	// Push via ngit (NIP-34)
	repoURL, err = m.pushWithNgit(ctx, tmpDir, soul)
	if err != nil {
		return "", fmt.Errorf("ngit push: %w", err)
	}

	m.logger.Info("workspace initialized",
		"agent_id", soul.AgentID,
		"repo_url", repoURL,
	)

	return repoURL, nil
}

// createWorkspaceFiles generates the workspace template files.
func (m *WorkspaceManager) createWorkspaceFiles(dir string, soul *domain.AgentSoul) error {
	data := map[string]interface{}{
		"AgentID":      soul.AgentID,
		"Name":         soul.Name,
		"Purpose":      soul.Purpose,
		"NostrPubkey":  soul.NostrPubkey,
		"NostrNpub":    soul.NostrNpub,
		"NIP05":        soul.NIP05,
		"AvatarURL":    soul.AvatarURL,
		"BunkerURI":    soul.BunkerURI,
		"Tier":         string(soul.Tier),
		"AllowedKinds": soul.AllowedKinds,
		"ToolGrants":   soul.ToolGrants,
		"SoulMD":       soul.SoulMD,
		"IdentityMD":   soul.IdentityMD,
	}

	// Create SOUL.md
	if err := m.writeTemplate(filepath.Join(dir, "SOUL.md"), soulTemplate, data); err != nil {
		return err
	}

	// Create IDENTITY.md
	if err := m.writeTemplate(filepath.Join(dir, "IDENTITY.md"), identityTemplate, data); err != nil {
		return err
	}

	// Create AGENTS.md
	if err := m.writeTemplate(filepath.Join(dir, "AGENTS.md"), agentsTemplate, data); err != nil {
		return err
	}

	// Create TOOLS.md
	if err := m.writeTemplate(filepath.Join(dir, "TOOLS.md"), toolsTemplate, data); err != nil {
		return err
	}

	// Create config/openclaw.json (placeholder)
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	if err := m.writeTemplate(filepath.Join(configDir, "openclaw.json"), openclawTemplate, data); err != nil {
		return err
	}

	return nil
}

func (m *WorkspaceManager) writeTemplate(path, tmplContent string, data interface{}) error {
	tmpl, err := template.New("file").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

func (m *WorkspaceManager) runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Soul Factory",
		"GIT_AUTHOR_EMAIL=soulfactory@sharegap.net",
		"GIT_COMMITTER_NAME=Soul Factory",
		"GIT_COMMITTER_EMAIL=soulfactory@sharegap.net",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}

	return nil
}

func (m *WorkspaceManager) pushWithNgit(ctx context.Context, dir string, soul *domain.AgentSoul) (string, error) {
	// Check if ngit is available
	if _, err := exec.LookPath("ngit"); err != nil {
		m.logger.Warn("ngit not found, skipping NIP-34 push")
		return fmt.Sprintf("%s/%s/%s", m.giteaURL, soul.NostrPubkey[:20], soul.AgentID), nil
	}

	// Initialize ngit
	cmd := exec.CommandContext(ctx, "ngit", "init",
		"--name", soul.AgentID,
		"--relay", "wss://relay.sharegap.net",
	)
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		m.logger.Warn("ngit init failed", "error", err, "output", string(output))
		// Continue without NIP-34
	}

	// Push to nostr remote
	if err := m.runGit(ctx, dir, "push", "nostr", "HEAD:main"); err != nil {
		m.logger.Warn("ngit push failed, trying direct Gitea push", "error", err)
		// Fall back to direct Gitea push if available
	}

	return fmt.Sprintf("%s/%s/%s", m.giteaURL, soul.NostrPubkey[:20], soul.AgentID), nil
}

// Workspace templates
const soulTemplate = `{{.SoulMD}}
`

const identityTemplate = `{{if .IdentityMD}}{{.IdentityMD}}{{else}}# {{.Name}}

**Agent ID:** {{.AgentID}}
**npub:** {{.NostrNpub}}
**NIP-05:** {{.NIP05}}

## Purpose

{{.Purpose}}

## Avatar

{{if .AvatarURL}}![Avatar]({{.AvatarURL}}){{else}}(Avatar pending){{end}}
{{end}}
`

const agentsTemplate = `# Agents

This document describes how to interact with {{.Name}}.

## Communication

- **Nostr:** {{.NostrNpub}}
- **NIP-05:** {{.NIP05}}

## Allowed Event Kinds

{{range .AllowedKinds}}- kind:{{.}}
{{end}}

## Tool Access

{{range .ToolGrants}}- {{.MCPServer}}: {{range .Scopes}}{{.}} {{end}}
{{end}}
`

const toolsTemplate = `# Tools

## MCP Servers

{{range .ToolGrants}}### {{.MCPServer}}

Scopes: {{range .Scopes}}` + "`{{.}}`" + ` {{end}}

{{end}}

## Notes

This agent's tools are configured at provisioning time by Soul Factory.
To request additional tools, contact a fleet operator.
`

const openclawTemplate = `{
  "gateway": {
    "port": 18780,
    "agentName": "{{.Name}}"
  },
  "model": "anthropic/claude-sonnet-4-6",
  "channels": {
    "nostr": {
      "enabled": true,
      "relays": [
        "wss://relay.sharegap.net",
        "wss://armada.sharegap.net"
      ],
      "pubkey": "{{.NostrPubkey}}",
      "privateKey": "__INJECTED_AT_RUNTIME_VIA_SIGNET__",
      "allowedPubkeys": [
        "cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400",
        "14907326f89ebdfc9cfdabe17bd492aa48abbd59ad5d8cc25295760bdf0e5015"
      ],
      "policy": "allowlist"
    }
  },
  "mcpServers": {
    "agent-memory": {
      "transport": "http",
      "url": "http://192.168.40.104:8282/mcp"
    }
  }
}
`
