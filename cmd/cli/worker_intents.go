package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/spf13/cobra"
)

func workerIntentCommands() []*cobra.Command {
	ops := []string{"cordon", "uncordon", "drain", "undrain", "maintenance-enter", "maintenance-exit", "labels-update", "cleanup"}
	commands := make([]*cobra.Command, 0, len(ops))
	for _, operation := range ops {
		op := operation
		cmd := &cobra.Command{Use: op + " [pubkey]", Short: "Publish a worker " + op + " intent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			pubkey := strings.ToLower(args[0])
			if _, err := nostr.PubKeyFromHex(pubkey); err != nil {
				return fmt.Errorf("invalid worker pubkey: %w", err)
			}
			worker, err := getCLIWorker(cmd, pubkey)
			if err != nil {
				return err
			}
			state := worker.SchedulingState
			if state == "" {
				state = domain.WorkerSchedulingActive
			}
			switch op {
			case "cordon":
				state = domain.WorkerSchedulingCordoned
			case "drain":
				state = domain.WorkerSchedulingDraining
			case "maintenance-enter":
				state = domain.WorkerSchedulingMaintenance
			case "uncordon", "undrain", "maintenance-exit":
				state = domain.WorkerSchedulingActive
			}
			labels := make(map[string]interface{}, len(worker.Labels))
			for key, value := range worker.Labels {
				labels[key] = value
			}
			if op == "labels-update" {
				raw, _ := cmd.Flags().GetString("labels")
				if err := json.Unmarshal([]byte(raw), &labels); err != nil {
					return fmt.Errorf("invalid labels JSON: %w", err)
				}
			}
			content := map[string]interface{}{"worker_pubkey": pubkey, "scheduling_state": string(state), "labels": labels}
			reason, _ := cmd.Flags().GetString("reason")
			if reason != "" {
				content["reason"] = reason
			}
			if op == "cleanup" {
				mode, _ := cmd.Flags().GetString("mode")
				content["cleanup_mode"] = mode
			}
			if !worker.UpdatedAt.IsZero() {
				content["expected_updated_at"] = worker.UpdatedAt.UTC().Format(time.RFC3339Nano)
			}
			intentID, err := publishMutationIntent(cmd, "worker", op, "worker:"+pubkey, "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": intentID, "worker_pubkey": pubkey})
		}}
		cmd.Flags().String("reason", "", "Operator reason")
		if op == "labels-update" {
			cmd.Flags().String("labels", "{}", "Complete desired labels JSON object")
		}
		if op == "cleanup" {
			cmd.Flags().String("mode", "reclaimable_only", "Cleanup mode")
		}
		commands = append(commands, cmd)
	}
	return commands
}
