package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// SoulFactoryConfig holds Soul Factory CLI configuration.
type SoulFactoryConfig struct {
	Relays           []string `json:"relays" yaml:"relays"`
	SoulFactoryPubkey string  `json:"soul_factory_pubkey" yaml:"soul_factory_pubkey"`
}

// Soul represents a soul for CLI output.
type Soul struct {
	AgentID      string   `json:"agent_id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	DeployStatus string   `json:"deploy_status,omitempty"`
	Tier         string   `json:"tier"`
	Npub         string   `json:"npub"`
	NIP05        string   `json:"nip05,omitempty"`
	AvatarURL    string   `json:"avatar_url,omitempty"`
	CreatedAt    string   `json:"created_at"`
}

// Template represents a template for CLI output.
type Template struct {
	Identifier  string   `json:"identifier"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tier        string   `json:"tier"`
	Tags        []string `json:"tags,omitempty"`
}

var defaultRelays = []string{
	"wss://relay.sharegap.net",
	"wss://armada.sharegap.net",
}

func soulFactoryCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "souls",
		Short:   "Soul Factory agent provisioning",
		Aliases: []string{"soul", "sf"},
	}

	cmd.AddCommand(
		soulsListCommand(),
		soulsGetCommand(),
		soulsProvisionCommand(),
		soulsSuspendCommand(),
		soulsResumeCommand(),
		soulsRevokeCommand(),
		soulsRedeployCommand(),
		soulsRegenerateCommand(),
		templatesCommand(),
	)

	return cmd
}

func soulsListCommand() *cobra.Command {
	var status string
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent souls",
		RunE: func(cmd *cobra.Command, args []string) error {
			// For now, show mock data. In production, this would query Nostr relays.
			souls := []Soul{
				{
					AgentID:      "scout",
					Name:         "Scout",
					Status:       "active",
					DeployStatus: "healthy",
					Tier:         "standard",
					Npub:         "npub1abc...",
					NIP05:        "scout@agents.openagents.com",
					CreatedAt:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
				},
				{
					AgentID:      "codebot",
					Name:         "CodeBot",
					Status:       "active",
					DeployStatus: "deployed",
					Tier:         "heavy",
					Npub:         "npub1def...",
					NIP05:        "codebot@agents.openagents.com",
					CreatedAt:    time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
				},
			}

			// Filter by status
			if status != "" {
				filtered := make([]Soul, 0)
				for _, s := range souls {
					if s.Status == status {
						filtered = append(filtered, s)
					}
				}
				souls = filtered
			}

			return output(souls, []string{"AGENT_ID", "NAME", "STATUS", "DEPLOY", "TIER", "NPUB"}, func(s Soul) []string {
				return []string{s.AgentID, s.Name, s.Status, s.DeployStatus, s.Tier, truncate(s.Npub, 12)}
			})
		},
	}

	cmd.Flags().StringVarP(&status, "status", "s", "", "Filter by status (active, suspended, revoked)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of results")

	return cmd
}

func soulsGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get [agent-id]",
		Short: "Get soul details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			// Mock data - in production, query Nostr relays
			soul := map[string]interface{}{
				"agent_id":      agentID,
				"name":          strings.Title(agentID),
				"status":        "active",
				"deploy_status": "healthy",
				"tier":          "standard",
				"npub":          "npub1abc123...",
				"pubkey":        "abc123...",
				"nip05":         fmt.Sprintf("%s@agents.openagents.com", agentID),
				"avatar_url":    fmt.Sprintf("https://blossom.example.com/%s-avatar.png", agentID),
				"workspace":     fmt.Sprintf("https://gitea.example.com/agents/%s", agentID),
				"qdrant":        agentID,
				"allowed_kinds": []int{1, 4, 1950},
				"tools": []map[string]interface{}{
					{"server": "web-search", "scopes": []string{"search"}},
					{"server": "file-system", "scopes": []string{"read", "write"}},
				},
				"created_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			}

			return outputSingle(soul)
		},
	}
}

func soulsProvisionCommand() *cobra.Command {
	var (
		name        string
		tier        string
		template    string
		brief       string
		briefFile   string
		follow      bool
		interactive bool
	)

	cmd := &cobra.Command{
		Use:   "provision [agent-id]",
		Short: "Provision a new agent soul",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			// Load brief from file if specified
			if briefFile != "" {
				data, err := os.ReadFile(briefFile)
				if err != nil {
					return fmt.Errorf("reading brief file: %w", err)
				}
				brief = string(data)
			}

			// Validate
			if brief == "" && template == "" {
				return fmt.Errorf("must specify --brief or --template")
			}

			if name == "" {
				name = strings.Title(strings.ReplaceAll(agentID, "-", " "))
			}

			fmt.Printf("🚀 Provisioning soul: %s\n", agentID)
			fmt.Printf("   Name: %s\n", name)
			fmt.Printf("   Tier: %s\n", tier)
			if template != "" {
				fmt.Printf("   Template: %s\n", template)
			}
			fmt.Println()

			// In production, this would:
			// 1. Build the kind:5950 event
			// 2. Sign it with the user's key
			// 3. Publish to relays
			// 4. Subscribe to kind:6950/7950 for progress

			if follow {
				fmt.Println("Following provisioning progress...")
				steps := []string{"generate", "signet", "avatar", "profile", "qdrant", "memory", "workspace", "deploy"}
				for i, step := range steps {
					time.Sleep(500 * time.Millisecond)
					fmt.Printf("  [%d/%d] %s ✓\n", i+1, len(steps), step)
				}
				fmt.Println()
				fmt.Printf("✅ Soul provisioned successfully!\n")
				fmt.Printf("   npub: npub1%s...\n", agentID[:8])
				fmt.Printf("   View: bahia souls get %s\n", agentID)
			} else {
				fmt.Println("Provisioning request submitted.")
				fmt.Println("Use --follow to watch progress, or check status with: bahia souls get", agentID)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Agent name (default: derived from agent-id)")
	cmd.Flags().StringVarP(&tier, "tier", "t", "standard", "Resource tier: lightweight, standard, heavy")
	cmd.Flags().StringVar(&template, "template", "", "Template reference (e.g., 31950:pubkey:identifier)")
	cmd.Flags().StringVarP(&brief, "brief", "b", "", "Agent brief/description")
	cmd.Flags().StringVarP(&briefFile, "brief-file", "f", "", "Read brief from file")
	cmd.Flags().BoolVarP(&follow, "follow", "w", false, "Watch provisioning progress")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Interactive mode")

	return cmd
}

func soulsSuspendCommand() *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "suspend [agent-id]",
		Short: "Suspend an agent soul",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			fmt.Printf("⏸️  Suspending soul: %s\n", agentID)
			if reason != "" {
				fmt.Printf("   Reason: %s\n", reason)
			}

			// In production: publish kind:1950 with action=suspend
			fmt.Printf("✓ Soul suspended\n")
			return nil
		},
	}

	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Reason for suspension")

	return cmd
}

func soulsResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume [agent-id]",
		Short: "Resume a suspended soul",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			fmt.Printf("▶️  Resuming soul: %s\n", agentID)

			// In production: publish kind:1950 with action=resume
			fmt.Printf("✓ Soul resumed\n")
			return nil
		},
	}
}

func soulsRevokeCommand() *cobra.Command {
	var reason string
	var force bool

	cmd := &cobra.Command{
		Use:   "revoke [agent-id]",
		Short: "Permanently revoke an agent soul",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			if !force {
				fmt.Printf("⚠️  This will permanently revoke soul '%s' and cannot be undone.\n", agentID)
				fmt.Print("Type the agent ID to confirm: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != agentID {
					return fmt.Errorf("confirmation failed")
				}
			}

			fmt.Printf("🚫 Revoking soul: %s\n", agentID)
			if reason != "" {
				fmt.Printf("   Reason: %s\n", reason)
			}

			// In production: publish kind:1950 with action=revoke
			fmt.Printf("✓ Soul revoked\n")
			return nil
		},
	}

	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Reason for revocation")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

func soulsRedeployCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "redeploy [agent-id]",
		Short: "Trigger a fresh deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			fmt.Printf("🔄 Redeploying soul: %s\n", agentID)

			// In production: publish kind:1950 with action=redeploy
			fmt.Printf("✓ Redeployment triggered\n")
			return nil
		},
	}
}

func soulsRegenerateCommand() *cobra.Command {
	var brief string
	var briefFile string

	cmd := &cobra.Command{
		Use:   "regenerate [agent-id]",
		Short: "Regenerate soul with a new brief",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := args[0]

			// Load brief from file if specified
			if briefFile != "" {
				data, err := os.ReadFile(briefFile)
				if err != nil {
					return fmt.Errorf("reading brief file: %w", err)
				}
				brief = string(data)
			}

			if brief == "" {
				return fmt.Errorf("must specify --brief or --brief-file")
			}

			fmt.Printf("🔄 Regenerating soul: %s\n", agentID)
			fmt.Printf("   New brief: %s...\n", truncate(brief, 50))

			// In production: publish kind:1950 with action=regenerate
			fmt.Printf("✓ Regeneration triggered\n")
			return nil
		},
	}

	cmd.Flags().StringVarP(&brief, "brief", "b", "", "New brief for regeneration")
	cmd.Flags().StringVarP(&briefFile, "brief-file", "f", "", "Read new brief from file")

	return cmd
}

func templatesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage soul templates",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Mock data - in production, query Nostr relays for kind:31950
			templates := []Template{
				{
					Identifier:  "research-agent",
					Name:        "Research Agent",
					Description: "Investigates topics and synthesizes findings",
					Tier:        "standard",
					Tags:        []string{"research", "analysis"},
				},
				{
					Identifier:  "code-reviewer",
					Name:        "Code Reviewer",
					Description: "Reviews code for quality and security",
					Tier:        "standard",
					Tags:        []string{"code", "review", "security"},
				},
				{
					Identifier:  "monitor-agent",
					Name:        "Monitor Agent",
					Description: "Monitors systems and alerts on issues",
					Tier:        "lightweight",
					Tags:        []string{"monitoring", "alerts"},
				},
				{
					Identifier:  "builder-agent",
					Name:        "Builder Agent",
					Description: "Builds and deploys software",
					Tier:        "heavy",
					Tags:        []string{"build", "deploy", "ci"},
				},
			}

			return output(templates, []string{"IDENTIFIER", "NAME", "TIER", "TAGS"}, func(t Template) []string {
				return []string{t.Identifier, t.Name, t.Tier, strings.Join(t.Tags, ", ")}
			})
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [identifier]",
		Short: "Get template details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			// Mock data
			template := map[string]interface{}{
				"identifier":  identifier,
				"name":        strings.Title(strings.ReplaceAll(identifier, "-", " ")),
				"description": "Template description here",
				"tier":        "standard",
				"tags":        []string{"example", "template"},
				"base_prompt": "You are an AI agent that...",
				"default_kinds": []int{1, 4, 1950},
				"default_tools": []map[string]interface{}{
					{"server": "web-search", "scopes": []string{"search"}},
				},
			}

			return outputSingle(template)
		},
	}

	cmd.AddCommand(listCmd, getCmd)
	return cmd
}

// Helper to build provisioning request event
func buildProvisioningRequestEvent(agentID, name, tier, template, brief string) map[string]interface{} {
	tags := [][]string{
		{"agent-id", agentID},
		{"name", name},
		{"tier", tier},
		{"output", "application/json"},
	}

	if template != "" {
		tags = append(tags, []string{"template", template})
	}

	content := map[string]string{"brief": brief}
	contentJSON, _ := json.Marshal(content)

	return map[string]interface{}{
		"kind":       5950,
		"created_at": time.Now().Unix(),
		"tags":       tags,
		"content":    string(contentJSON),
	}
}
