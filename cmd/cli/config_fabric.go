package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func configCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Publish and inspect fleet config desired state"}

	publishCmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish a config-fabric desired-state event through the operator Signet signer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("file")
			body, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read config request: %w", err)
			}
			var request client.ConfigPublishRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return fmt.Errorf("decode config request: %w", err)
			}
			receipt, err := apiClient.PublishConfig(cmd.Context(), request)
			if err != nil {
				return err
			}
			return outputSingle(receipt)
		},
	}
	publishCmd.Flags().String("file", "", "JSON config publish request")
	_ = publishCmd.MarkFlagRequired("file")

	driftCmd := &cobra.Command{
		Use:   "drift",
		Short: "List desired-versus-applied config drift",
		RunE: func(cmd *cobra.Command, _ []string) error {
			drift, err := apiClient.ListConfigDrift(cmd.Context())
			if err != nil {
				return err
			}
			return output(drift, []string{"SERVICE", "POLICY", "SCOPE", "DESIRED", "APPLIED", "DRIFT", "LAST REJECTION"}, func(item client.ConfigDrift) []string {
				return []string{
					item.ServiceID,
					item.PolicyName,
					item.Scope,
					fmt.Sprintf("%d:%s", item.DesiredVersion, item.DesiredEventID),
					fmt.Sprintf("%d:%s", item.AppliedVersion, item.AppliedEventID),
					fmt.Sprintf("%t", item.Drift),
					item.LastRejectionReason,
				}
			})
		},
	}

	rollbackCmd := &cobra.Command{
		Use:   "rollback [event-id]",
		Short: "Republish a prior desired event at the next version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			receipt, err := apiClient.RollbackConfig(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return outputSingle(receipt)
		},
	}

	cmd.AddCommand(publishCmd, driftCmd, rollbackCmd)
	return cmd
}
