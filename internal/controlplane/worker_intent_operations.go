package controlplane

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

type workerSchedulingStateUpdater interface {
	UpdateSchedulingState(ctx context.Context, pubkey string, state domain.WorkerSchedulingState, note string) error
}

type workerLabelsUpdater interface {
	UpdateLabels(ctx context.Context, pubkey string, labels map[string]string) error
}

func (r *Reactor) updateWorkerSchedulingState(ctx context.Context, pubkey, reason, command string, target domain.WorkerSchedulingState) (*domain.Worker, string, error) {
	if r.workerRepo == nil {
		return nil, "worker_repository_unavailable", fmt.Errorf("worker repository is not configured")
	}
	worker, err := r.workerRepo.GetByPubKey(ctx, pubkey)
	if err != nil {
		return nil, "lookup_error", err
	}
	if worker == nil {
		return nil, "not_found", fmt.Errorf("worker not found")
	}
	if err := validateWorkerSchedulingTransition(command, worker.SchedulingState, target); err != nil {
		return worker, "invalid_transition", err
	}
	updater, ok := r.workerRepo.(workerSchedulingStateUpdater)
	if !ok {
		return worker, "worker_repository_unavailable", fmt.Errorf("worker repository cannot update worker scheduling state")
	}
	if err := updater.UpdateSchedulingState(ctx, pubkey, target, strings.TrimSpace(reason)); err != nil {
		return worker, "update_error", err
	}
	if refreshed, err := r.workerRepo.GetByPubKey(ctx, pubkey); err == nil && refreshed != nil {
		worker = refreshed
	} else {
		worker.SchedulingState = target
		worker.SchedulingNote = strings.TrimSpace(reason)
	}
	return worker, "", nil
}

func isHexNostrPubKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func validateWorkerSchedulingTransition(command string, current, target domain.WorkerSchedulingState) error {
	if current == "" {
		current = domain.WorkerSchedulingActive
	}
	if current == target {
		return nil
	}
	if current == domain.WorkerSchedulingDisabled {
		return fmt.Errorf("worker is disabled; use worker.enable.request before changing scheduling state")
	}
	sourceAllowed := func(states ...domain.WorkerSchedulingState) bool {
		for _, state := range states {
			if current == state {
				return true
			}
		}
		return false
	}
	switch command {
	case WorkerCommandCordon:
		if sourceAllowed(domain.WorkerSchedulingActive) {
			return nil
		}
	case WorkerCommandUncordon:
		if sourceAllowed(domain.WorkerSchedulingCordoned) {
			return nil
		}
	case WorkerCommandDrain:
		if sourceAllowed(domain.WorkerSchedulingActive, domain.WorkerSchedulingCordoned) {
			return nil
		}
	case WorkerCommandUndrain:
		if sourceAllowed(domain.WorkerSchedulingDraining) {
			return nil
		}
	case WorkerCommandMaintenanceEnter:
		if sourceAllowed(domain.WorkerSchedulingActive, domain.WorkerSchedulingCordoned, domain.WorkerSchedulingDraining) {
			return nil
		}
	case WorkerCommandMaintenanceExit:
		if sourceAllowed(domain.WorkerSchedulingMaintenance) {
			return nil
		}
	}
	return fmt.Errorf("%s cannot transition worker from %s to %s", command, current, target)
}

func sanitizeWorkerLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func pinnedWorkerFromPolicy(policy map[string]any) string {
	if policy == nil {
		return ""
	}
	value, ok := policy["pinned_worker"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func sanitizeWorkerPolicy(policy map[string]any) map[string]any {
	out := make(map[string]any, len(policy))
	for key, value := range policy {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		switch key {
		case "pinned_worker":
			if s, ok := value.(string); ok {
				out[key] = strings.TrimSpace(s)
			}
		case "label_selector":
			out[key] = sanitizeStringMapAny(value)
		case "rollout":
			out[key] = sanitizeRolloutPolicy(value)
		default:
			out[key] = value
		}
	}
	return out
}

func sanitizeStringMapAny(raw any) map[string]any {
	out := map[string]any{}
	switch values := raw.(type) {
	case map[string]string:
		for key, value := range values {
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = strings.TrimSpace(value)
			}
		}
	case map[string]any:
		for key, value := range values {
			key = strings.TrimSpace(key)
			if key != "" {
				out[key] = strings.TrimSpace(fmt.Sprintf("%v", value))
			}
		}
	}
	return out
}

func sanitizeRolloutPolicy(raw any) map[string]any {
	m, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	if labels := sanitizeStringMapAny(m["from_labels"]); len(labels) > 0 {
		out["from_labels"] = labels
	}
	if labels := sanitizeStringMapAny(m["to_labels"]); len(labels) > 0 {
		out["to_labels"] = labels
	}
	return out
}

func (r *Reactor) publishWorkerState(ctx context.Context, worker *domain.Worker) error {
	if r.workerStatePublisher == nil {
		r.workerStatePublisher = NewWorkerStatePublisher(r.publisher, r.signer)
	}
	return r.workerStatePublisher.Publish(ctx, worker)
}

// publishWorkerReadModels publishes assignment and drain read models directly
// from the mutation site.
func (r *Reactor) publishWorkerReadModels(ctx context.Context, workerPubKey string) {
	if r.workerReadModelPublisher != nil {
		r.workerReadModelPublisher.PublishForWorker(ctx, workerPubKey)
	}
}
