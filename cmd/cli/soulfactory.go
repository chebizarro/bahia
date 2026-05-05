package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
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

type cliSoulFactoryClient interface {
	Close()
	ListSouls(context.Context, int, string) ([]domain.AgentSoul, error)
	GetSoul(context.Context, string) (*domain.AgentSoul, error)
	ListTemplates(context.Context, int, string) ([]domain.SoulTemplate, error)
	PublishProvisionRequest(context.Context, domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error)
	AwaitProvisioningResult(context.Context, *soulfactory.SoulFactoryRequestReceipt, func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error)
	ExecuteSoulAction(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error)
}

var newCLISoulFactoryClient = func(relays []string, privateKey string) (cliSoulFactoryClient, error) {
	return soulfactory.NewNostrClientFromPrivateKey(relays, privateKey)
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
			cli, err := buildCLISoulFactoryClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()

			souls, err := cli.ListSouls(cmd.Context(), limit, status)
			if err != nil {
				return err
			}
			items := make([]Soul, 0, len(souls))
			for _, soul := range souls {
				items = append(items, soulToCLI(soul))
			}
			return output(items, []string{"AGENT ID", "NAME", "STATUS", "DEPLOY", "TIER", "NPUB"}, func(s Soul) []string {
				return []string{s.AgentID, s.Name, s.Status, firstNonEmpty(s.DeployStatus, "-"), s.Tier, truncate(s.Npub, 16)}
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
			cli, err := buildCLISoulFactoryClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()

			soul, err := cli.GetSoul(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if soul == nil {
				return fmt.Errorf("soul not found: %s", args[0])
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

			if briefFile != "" {
				data, err := os.ReadFile(briefFile)
				if err != nil {
					return fmt.Errorf("reading brief file: %w", err)
				}
				brief = string(data)
			}

			if brief == "" && template == "" {
				return fmt.Errorf("must specify --brief or --template")
			}
			if name == "" {
				name = strings.Title(strings.ReplaceAll(agentID, "-", " "))
			}
			if tier == "" {
				tier = string(domain.SoulTierStandard)
			}

			cli, err := buildCLISoulFactoryClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()

			receipt, err := cli.PublishProvisionRequest(cmd.Context(), domain.ProvisioningRequest{
				AgentID:     agentID,
				Name:        name,
				Tier:        domain.SoulTier(tier),
				TemplateRef: template,
				Brief:       brief,
			})
			if err != nil {
				return err
			}
			if !follow {
				return outputSingle(receipt)
			}

			run, err := cli.AwaitProvisioningResult(cmd.Context(), receipt, soulProvisionStatusCallback(cmd))
			if err != nil {
				return err
			}
			if outputFormat != "table" {
				if err := outputSingle(run); err != nil {
					return err
				}
			}
			if run.Status != domain.ProvisioningStatusCompleted {
				return fmt.Errorf("%s", firstNonEmpty(run.Error, "provisioning failed"))
			}
			if outputFormat == "table" {
				fmt.Fprintf(cmd.OutOrStdout(), "✓ Soul provisioned: %s\n", firstNonEmpty(run.AgentID, agentID))
			}
			_ = interactive
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
			return runSoulActionCommand(cmd, args[0], domain.SoulActionSuspend, reason, "")
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
			return runSoulActionCommand(cmd, args[0], domain.SoulActionResume, "", "")
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
				fmt.Fprintf(cmd.OutOrStdout(), "⚠️  This will permanently revoke soul '%s' and cannot be undone.\n", agentID)
				fmt.Fprint(cmd.OutOrStdout(), "Type the agent ID to confirm: ")
				var confirm string
				fmt.Fscanln(cmd.InOrStdin(), &confirm)
				if confirm != agentID {
					return fmt.Errorf("confirmation failed")
				}
			}
			return runSoulActionCommand(cmd, agentID, domain.SoulActionRevoke, reason, "")
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
			return runSoulActionCommand(cmd, args[0], domain.SoulActionRedeploy, "", "")
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
			return runSoulActionCommand(cmd, args[0], domain.SoulActionRegenerate, "", brief)
		},
	}

	cmd.Flags().StringVarP(&brief, "brief", "b", "", "New brief for regeneration")
	cmd.Flags().StringVarP(&briefFile, "brief-file", "f", "", "Read new brief from file")

	return cmd
}

func templatesCommand() *cobra.Command {
	var tier string
	var limit int
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage soul templates",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, err := buildCLISoulFactoryClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()

			templates, err := cli.ListTemplates(cmd.Context(), limit, tier)
			if err != nil {
				return err
			}
			items := make([]Template, 0, len(templates))
			for _, tmpl := range templates {
				items = append(items, templateToCLI(tmpl))
			}
			return output(items, []string{"IDENTIFIER", "NAME", "TIER", "DESCRIPTION"}, func(t Template) []string {
				return []string{t.Identifier, t.Name, t.Tier, truncate(t.Description, 48)}
			})
		},
	}
	listCmd.Flags().StringVarP(&tier, "tier", "t", "", "Filter by tier")
	listCmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of results")

	getCmd := &cobra.Command{
		Use:   "get [identifier]",
		Short: "Get template details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli, err := buildCLISoulFactoryClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()

			templates, err := cli.ListTemplates(cmd.Context(), 200, "")
			if err != nil {
				return err
			}
			for _, tmpl := range templates {
				if tmpl.Identifier == args[0] {
					return outputSingle(tmpl)
				}
			}
			return fmt.Errorf("template not found: %s", args[0])
		},
	}

	cmd.AddCommand(listCmd, getCmd)
	return cmd
}

func buildCLISoulFactoryClient(cmd *cobra.Command) (cliSoulFactoryClient, error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("provide --nsec, --privkey, BAHIA_NOSTR_NSEC, or BAHIA_NOSTR_PRIVATE_KEY for signer-first Soul Factory requests")
	}
	var discovery systemInfoClient
	if apiClient != nil {
		discovery = apiClient
	}
	relays, err := resolveOperatorRelays(cmd.Context(), cmd, discovery)
	if err != nil {
		return nil, err
	}
	return newCLISoulFactoryClient(relays, key)
}

func soulProvisionStatusCallback(cmd *cobra.Command) func(soulfactory.SoulFactoryStatusEvent) {
	if outputFormat != "table" {
		return nil
	}
	return func(status soulfactory.SoulFactoryStatusEvent) {
		message := strings.TrimSpace(status.Message)
		if message == "" {
			message = firstNonEmpty(status.Step, status.Status)
		}
		if message == "" {
			message = "status update"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "→ provisioning: %s\n", message)
	}
}

func runSoulActionCommand(cmd *cobra.Command, agentID string, action domain.SoulActionType, reason, newBrief string) error {
	cli, err := buildCLISoulFactoryClient(cmd)
	if err != nil {
		return err
	}
	defer cli.Close()

	soul, err := cli.GetSoul(cmd.Context(), agentID)
	if err != nil {
		return err
	}
	if soul == nil {
		return fmt.Errorf("soul not found: %s", agentID)
	}

	resultEvent, err := cli.ExecuteSoulAction(cmd.Context(), soul.AgentID, action, reason, newBrief)
	if err != nil {
		return err
	}
	if !actionResultAccepted(resultEvent) {
		return soulActionResultError(string(action), resultEvent)
	}
	return outputSingle(map[string]any{
		"action":   action,
		"agent_id": soul.AgentID,
		"event_id": resultEvent.ID,
		"status":   firstTagValue(resultEvent.Tags, "status"),
		"result":   decodeJSONContent(resultEvent.Content),
	})
}

func actionResultAccepted(event *nostr.Event) bool {
	return strings.EqualFold(firstTagValue(event.Tags, "status"), "completed") || strings.EqualFold(firstTagValue(event.Tags, "status"), "success")
}

func soulActionResultError(action string, event *nostr.Event) error {
	message := firstTagValue(event.Tags, "error")
	if message == "" {
		payload := decodeJSONContent(event.Content)
		if mapped, ok := payload.(map[string]any); ok {
			if v, ok := mapped["error"].(string); ok {
				message = v
			} else if v, ok := mapped["message"].(string); ok {
				message = v
			}
		}
	}
	message = firstNonEmpty(strings.TrimSpace(message), strings.TrimSpace(event.Content), "terminal result was not successful")
	return fmt.Errorf("%s failed: %s", action, message)
}

func decodeJSONContent(content string) any {
	content = strings.TrimSpace(content)
	if content == "" {
		return map[string]any{}
	}
	var payload any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return content
	}
	return payload
}

func firstTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func soulToCLI(soul domain.AgentSoul) Soul {
	return Soul{
		AgentID:      soul.AgentID,
		Name:         soul.Name,
		Status:       string(soul.Status),
		DeployStatus: soul.DeployStatus,
		Tier:         string(soul.Tier),
		Npub:         soul.NostrNpub,
		NIP05:        soul.NIP05,
		AvatarURL:    soul.AvatarURL,
		CreatedAt:    soul.CreatedAt.Format(time.RFC3339),
	}
}

func templateToCLI(template domain.SoulTemplate) Template {
	return Template{
		Identifier:  template.Identifier,
		Name:        template.Name,
		Description: template.Description,
		Tier:        string(template.Tier),
		Tags:        append([]string(nil), template.Tags...),
	}
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
