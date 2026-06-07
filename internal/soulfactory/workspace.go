package soulfactory

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/openagentsinc/bahia/internal/domain"
)

// WorkspaceManager handles agent workspace initialization.
type WorkspaceManager struct {
	giteaURL                 string
	templateDir              string
	openClawRelays           []string
	openClawControllerPubkey []string
	openClawModel            string
	openClawPrivateKeyRef    string
	agentMemoryMCPURLRef     string
	ngitRelays               []string
	gatewayPort              int
	logger                   *slog.Logger
}

// WorkspaceConfig holds workspace manager configuration.
type WorkspaceConfig struct {
	GiteaURL              string   // Gitea URL (e.g., https://git.sharegap.net)
	TemplateDir           string   // Directory containing workspace templates
	OpenClawRelays        []string // Agent runtime/control relays written to generated OpenClaw workspace config
	OpenClawControllers   []string // SoulFactory controller pubkeys trusted by the generated OpenClaw workspace
	OpenClawModel         string   // Runtime model identifier supplied by operator config
	OpenClawPrivateKeyRef string   // Secret reference resolved by the runtime, never inline private key material
	AgentMemoryMCPURLRef  string   // Secret/config reference for the agent-memory MCP URL
	NgitRelays            []string // NIP-34 repository publication relays; required separately from OpenClawRelays
	GatewayPort           int      // Local OpenClaw gateway port written into workspace config
}

// NewWorkspaceManager creates a new workspace manager.
func NewWorkspaceManager(config WorkspaceConfig, logger *slog.Logger) *WorkspaceManager {
	if logger == nil {
		logger = slog.Default()
	}

	return &WorkspaceManager{
		giteaURL:                 strings.TrimSpace(config.GiteaURL),
		templateDir:              strings.TrimSpace(config.TemplateDir),
		openClawRelays:           normalizeSoulRelays(config.OpenClawRelays),
		openClawControllerPubkey: normalizePubkeyHexList(config.OpenClawControllers),
		openClawModel:            strings.TrimSpace(config.OpenClawModel),
		openClawPrivateKeyRef:    strings.TrimSpace(config.OpenClawPrivateKeyRef),
		agentMemoryMCPURLRef:     strings.TrimSpace(config.AgentMemoryMCPURLRef),
		ngitRelays:               normalizeSoulRelays(config.NgitRelays),
		gatewayPort:              config.GatewayPort,
		logger:                   logger.With("component", "workspace"),
	}
}

// InitWorkspace creates and pushes an agent workspace repository.
func (m *WorkspaceManager) InitWorkspace(ctx context.Context, soul *domain.AgentSoul) (repoURL string, err error) {
	if m.giteaURL == "" {
		return "", ErrWorkspaceNotConfigured
	}
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
	if err := m.validateOpenClawWorkspaceConfig(soul); err != nil {
		return err
	}
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

	// Create config/openclaw.json from operator-supplied runtime configuration.
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	openClawConfig, err := m.renderOpenClawConfig(soul)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "openclaw.json"), openClawConfig, 0644); err != nil {
		return fmt.Errorf("write openclaw config: %w", err)
	}

	return nil
}

func (m *WorkspaceManager) validateOpenClawWorkspaceConfig(soul *domain.AgentSoul) error {
	if soul == nil {
		return fmt.Errorf("soul is required")
	}
	if strings.TrimSpace(soul.NostrPubkey) == "" {
		return fmt.Errorf("soul Nostr pubkey is required")
	}
	if err := validateHexPubkey(strings.TrimSpace(soul.NostrPubkey)); err != nil {
		return fmt.Errorf("soul Nostr pubkey is invalid: %w", err)
	}
	if len(m.openClawRelays) == 0 {
		return fmt.Errorf("OpenClaw workspace relays are required")
	}
	if len(m.openClawControllerPubkey) == 0 {
		return fmt.Errorf("OpenClaw workspace controller pubkeys are required")
	}
	for _, pubkey := range m.openClawControllerPubkey {
		if err := validateHexPubkey(pubkey); err != nil {
			return fmt.Errorf("OpenClaw workspace controller pubkey %q is invalid: %w", pubkey, err)
		}
	}
	if m.openClawModel == "" {
		return fmt.Errorf("OpenClaw workspace model is required")
	}
	if m.openClawPrivateKeyRef == "" {
		return fmt.Errorf("OpenClaw workspace private key secret reference is required")
	}
	if m.agentMemoryMCPURLRef == "" {
		return fmt.Errorf("OpenClaw workspace agent-memory MCP URL reference is required")
	}
	if len(m.ngitRelays) == 0 {
		return fmt.Errorf("workspace ngit relays are required")
	}
	if m.gatewayPort < 0 || m.gatewayPort > 65535 {
		return fmt.Errorf("OpenClaw workspace gateway port must be between 0 and 65535")
	}
	return nil
}

func (m *WorkspaceManager) renderOpenClawConfig(soul *domain.AgentSoul) ([]byte, error) {
	if err := m.validateOpenClawWorkspaceConfig(soul); err != nil {
		return nil, err
	}
	port := m.gatewayPort
	if port == 0 {
		port = 18780
	}
	config := map[string]interface{}{
		"gateway": map[string]interface{}{
			"port":      port,
			"agentName": soul.Name,
		},
		"model": m.openClawModel,
		"channels": map[string]interface{}{
			"nostr": map[string]interface{}{
				"enabled":          true,
				"relays":           m.openClawRelays,
				"pubkey":           strings.TrimSpace(soul.NostrPubkey),
				"privateKeyRef":    m.openClawPrivateKeyRef,
				"allowedPubkeys":   m.openClawControllerPubkey,
				"policy":           "allowlist",
				"controllerPolicy": "allowlist",
			},
		},
		"mcpServers": map[string]interface{}{
			"agent-memory": map[string]interface{}{
				"transport": "http",
				"urlRef":    m.agentMemoryMCPURLRef,
			},
		},
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal openclaw config: %w", err)
	}
	return append(out, '\n'), nil
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
	if soul == nil {
		return "", fmt.Errorf("soul is required")
	}
	if err := validateHexPubkey(strings.TrimSpace(soul.NostrPubkey)); err != nil {
		return "", fmt.Errorf("soul Nostr pubkey is invalid: %w", err)
	}
	if len(m.ngitRelays) == 0 {
		return "", fmt.Errorf("workspace ngit relays are required")
	}
	// Check if ngit is available
	if _, err := exec.LookPath("ngit"); err != nil {
		return "", fmt.Errorf("ngit not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ngit", ngitInitArgs(soul.AgentID, m.ngitRelays)...)
	cmd.Dir = dir

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ngit init: %s: %w", string(output), err)
	}

	// Push to nostr remote
	if err := m.runGit(ctx, dir, "push", "nostr", "HEAD:main"); err != nil {
		return "", fmt.Errorf("ngit push: %w", err)
	}

	return fmt.Sprintf("%s/%s/%s", m.giteaURL, soul.NostrPubkey[:20], soul.AgentID), nil
}

func normalizePubkeyHexList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateHexPubkey(pubkey string) error {
	pubkey = strings.TrimSpace(pubkey)
	if len(pubkey) != 64 {
		return fmt.Errorf("expected 64 hex characters")
	}
	if _, err := hex.DecodeString(pubkey); err != nil {
		return fmt.Errorf("expected valid hex: %w", err)
	}
	return nil
}

func ngitInitArgs(name string, relays []string) []string {
	args := []string{"init", "--name", name}
	for _, relay := range normalizeSoulRelays(relays) {
		args = append(args, "--relay", relay)
	}
	return args
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
