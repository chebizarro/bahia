package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func notificationMutationCommands() []*cobra.Command {
	create := notificationDocumentCommand("create")
	update := notificationDocumentCommand("update")
	deleteCmd := &cobra.Command{Use: "delete [id]", Short: "Delete a notification channel", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := uuid.Parse(args[0])
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("channel ID must be a non-nil UUID")
		}
		intentID, err := publishMutationIntent(cmd, "notification", "delete", id.String(), "", "", map[string]interface{}{"id": id.String()})
		if err != nil {
			return err
		}
		return outputSingle(map[string]string{"intent_id": intentID, "channel_id": id.String()})
	}}
	return []*cobra.Command{create, update, deleteCmd}
}

func notificationDocumentCommand(op string) *cobra.Command {
	cmd := &cobra.Command{Use: op, Short: "Publish a full notification channel " + op + " intent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		path, _ := cmd.Flags().GetString("file")
		content := map[string]interface{}{}
		if err := decodeJSONFile(path, &content, true); err != nil {
			return err
		}
		raw, _ := content["id"].(string)
		var id uuid.UUID
		var err error
		if strings.TrimSpace(raw) != "" {
			id, err = uuid.Parse(raw)
			if err != nil || id == uuid.Nil {
				return fmt.Errorf("channel id must be a non-nil UUID")
			}
		}
		if op == "create" && id == uuid.Nil {
			id, err = uuid.NewV7()
			if err != nil {
				return err
			}
			content["id"] = id.String()
		}
		if op == "update" {
			if id == uuid.Nil {
				return fmt.Errorf("notification update requires id")
			}
			revision, ok := content["expected_updated_at"].(string)
			if !ok {
				return fmt.Errorf("notification update requires expected_updated_at RFC3339 string")
			}
			if _, err := time.Parse(time.RFC3339Nano, revision); err != nil {
				return fmt.Errorf("invalid expected_updated_at: %w", err)
			}
		}
		intentID, err := publishMutationIntent(cmd, "notification", op, id.String(), "", "", content)
		if err != nil {
			return err
		}
		return outputSingle(map[string]string{"intent_id": intentID, "channel_id": id.String()})
	}}
	cmd.Flags().String("file", "", "Full channel JSON document, including confidential config")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
