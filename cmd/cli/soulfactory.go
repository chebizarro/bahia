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
	Relays            []string `json:"relays" yaml:"relays"`
	SoulFactoryPubkey string   `json:"soul_factory_pubkey" yaml:"soul_factory_pubkey"`
}

// Soul represents a soul for CLI output.
type Soul struct {
	AgentID      string `json:"agent_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	DeployStatus string `json:"deploy_status,omitempty"`
	Tier         string `json:"tier"`
	Npub         string `json:"npub"`
	NIP05        string `json:"nip05,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	CreatedAt    string `json:"created_at"`
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
			_ = status
			_ = limit
			return soulFactoryUnavailableErr("list souls")
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
			return soulFactoryUnavailableErr("get soul")
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

			_ = follow
			_ = interactive
			return soulFactoryUnavailableErr("provision soul")
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

			_ = agentID
			_ = reason
			return soulFactoryUnavailableErr("suspend soul")
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

			_ = agentID
			return soulFactoryUnavailableErr("resume soul")
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
			_ = agentID
			_ = reason
			_ = force
			return soulFactoryUnavailableErr("revoke soul")

			if !force {
				fmt.Printf("⚠️  This will permanently revoke soul '%s' and cannot be undone.\n", agentID)
				fmt.Print("Type the agent ID to confirm: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != agentID {
					return fmt.Errorf("confirmation failed")
				}
			}

			_ = reason
			return soulFactoryUnavailableErr("revoke soul")
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

			_ = agentID
			return soulFactoryUnavailableErr("redeploy soul")
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

			_ = agentID
			_ = brief
			return soulFactoryUnavailableErr("regenerate soul")
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
			return soulFactoryUnavailableErr("list templates")
		},
	}

	getCmd := &cobra.Command{
		Use:   "get [identifier]",
		Short: "Get template details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return soulFactoryUnavailableErr("get template")
		},
	}

	cmd.AddCommand(listCmd, getCmd)
	return cmd
}

// Helper to build provisioning request event
func soulFactoryUnavailableErr(operation string) error {
	return fmt.Errorf("cannot %s: Soul Factory CLI does not yet have configured Nostr signing/publish/query support", operation)
}

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
