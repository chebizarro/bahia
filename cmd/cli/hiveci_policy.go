package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func hiveCIPolicyCommands() *cobra.Command {
	group := &cobra.Command{Use: "hiveci-policy", Short: "Manage relay-canonical Hive-CI pipeline policies"}
	apply := &cobra.Command{Use: "apply", Short: "Sign a complete pipeline-policy desired state from JSON", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("file")
			var content map[string]any
			if err := decodeJSONFile(path, &content, true); err != nil {
				return err
			}
			id, err := hiveCIPolicyID(content["id"])
			if err != nil {
				return err
			}
			key, _ := cmd.Flags().GetString("idempotency-key")
			intentID, err := intentUUID(key)
			if err != nil {
				return err
			}
			revision, _ := cmd.Flags().GetString("expected-updated-at")
			if revision != "" {
				content["expected_updated_at"] = revision
			}
			content["intent_id"] = intentID
			op, _ := cmd.Flags().GetString("op")
			if op != "create" && op != "update" {
				return fmt.Errorf("--op must be create or update")
			}
			request := hiveCIPolicyRequest(op, id, intentID, content)
			if err := publishCLIIntent(cmd, request); err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": intentID, "policy_id": id})
		}}
	apply.Flags().String("file", "", "Complete pipeline-policy JSON document")
	apply.Flags().String("op", "update", "Advisory operation: create or update")
	apply.Flags().String("idempotency-key", "", "UUIDv7 to reuse when retrying this exact intent")
	apply.Flags().String("expected-updated-at", "", "Canonical RFC3339 revision for optimistic conflict detection")
	_ = apply.MarkFlagRequired("file")
	remove := &cobra.Command{Use: "delete", Short: "Sign a pipeline-policy tombstone", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rawID, _ := cmd.Flags().GetString("id")
			id, err := hiveCIPolicyID(rawID)
			if err != nil {
				return err
			}
			key, _ := cmd.Flags().GetString("idempotency-key")
			intentID, err := intentUUID(key)
			if err != nil {
				return err
			}
			content := map[string]any{"id": id, "deleted": true, "intent_id": intentID}
			revision, _ := cmd.Flags().GetString("expected-updated-at")
			if revision != "" {
				content["expected_updated_at"] = revision
			}
			if err := publishCLIIntent(cmd, hiveCIPolicyRequest("delete", id, intentID, content)); err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": intentID, "policy_id": id})
		}}
	remove.Flags().String("id", "", "Author-minted UUIDv7 policy id")
	remove.Flags().String("idempotency-key", "", "UUIDv7 to reuse when retrying this exact intent")
	remove.Flags().String("expected-updated-at", "", "Canonical RFC3339 revision for optimistic conflict detection")
	_ = remove.MarkFlagRequired("id")
	group.AddCommand(apply, remove)
	return group
}

func hiveCIPolicyID(raw any) (string, error) {
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("policy id must be a canonical UUIDv7 string")
	}
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != 7 || id.String() != value {
		return "", fmt.Errorf("policy id must be a canonical UUIDv7 string")
	}
	return value, nil
}

func hiveCIPolicyRequest(op, id, intentID string, content map[string]any) client.PublishIntentRequest {
	return client.PublishIntentRequest{Domain: "hiveci-policy", Op: op, Coordinate: "hiveci:policy:" + id,
		Schema: "bahia.intent.hiveci-policy.v1", IntentID: intentID, Content: content}
}
