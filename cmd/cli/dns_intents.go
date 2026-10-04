package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// dnsD72Commands expose the durable DNS mutation handler's remaining operations.
// Full desired state comes from a JSON document; deletes carry the identifying
// field and an explicit revision rather than reusing stale local state.
func dnsD72Commands() []*cobra.Command {
	return []*cobra.Command{
		dnsDocumentCommand("zone-update", "name", "zone:"),
		dnsDeleteCommand("zone-delete", "name", "zone:"),
		dnsDocumentCommand("endpoint-create", "coordinate", ""),
		dnsDocumentCommand("endpoint-update", "coordinate", ""),
		dnsDeleteCommand("endpoint-delete", "coordinate", ""),
		dnsDocumentCommand("backend-create", "ref", "dnsbackend:"),
		dnsDocumentCommand("backend-update", "ref", "dnsbackend:"),
		dnsDeleteCommand("backend-delete", "ref", "dnsbackend:"),
		dnsDocumentCommand("policy-update", "id", "dnspolicy:"),
		dnsDeleteCommand("policy-delete", "id", "dnspolicy:"),
	}
}

func dnsDocumentCommand(op, identity, prefix string) *cobra.Command {
	cmd := &cobra.Command{Use: op, Short: "Publish a DNS " + op + " intent from JSON", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("file")
			content := map[string]interface{}{}
			if err := decodeJSONFile(path, &content, true); err != nil {
				return err
			}
			value, ok := content[identity].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return fmt.Errorf("DNS %s document requires %s", op, identity)
			}
			if op == "zone-update" {
				value = strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), ".")
				content[identity] = value
			}
			if identity == "id" {
				if _, err := uuid.Parse(value); err != nil {
					return fmt.Errorf("invalid policy id: %w", err)
				}
			}
			if strings.HasSuffix(op, "-update") {
				if _, err := dnsRevision(content); err != nil {
					return err
				}
			}
			intentID, err := publishMutationIntent(cmd, "dns", op, prefix+value, "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": intentID, "coordinate": prefix + value})
		}}
	cmd.Flags().String("file", "", "Full desired DNS state as JSON")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func dnsDeleteCommand(op, identity, prefix string) *cobra.Command {
	cmd := &cobra.Command{Use: op, Short: "Publish a DNS " + op + " intent", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, _ := cmd.Flags().GetString(identity)
			if op == "zone-delete" {
				value = strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), ".")
			}
			if identity == "id" {
				if _, err := uuid.Parse(value); err != nil {
					return fmt.Errorf("invalid policy id: %w", err)
				}
			}
			revision, _ := cmd.Flags().GetString("expected-updated-at")
			if _, err := time.Parse(time.RFC3339Nano, revision); err != nil {
				return fmt.Errorf("invalid expected_updated_at: %w", err)
			}
			content := map[string]interface{}{identity: value, "expected_updated_at": revision}
			intentID, err := publishMutationIntent(cmd, "dns", op, prefix+value, "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": intentID, "coordinate": prefix + value})
		}}
	cmd.Flags().String(identity, "", "Entity identifier")
	cmd.Flags().String("expected-updated-at", "", "Canonical revision (RFC3339)")
	_ = cmd.MarkFlagRequired(identity)
	_ = cmd.MarkFlagRequired("expected-updated-at")
	return cmd
}

func dnsRevision(content map[string]interface{}) (time.Time, error) {
	raw, ok := content["expected_updated_at"].(string)
	if !ok {
		return time.Time{}, fmt.Errorf("DNS update requires expected_updated_at RFC3339 string")
	}
	revision, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid expected_updated_at: %w", err)
	}
	return revision, nil
}
