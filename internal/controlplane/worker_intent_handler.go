package controlplane

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// WorkerIntentHandler reconciles full desired scheduling and labels state per
// worker through the same repository and orchestration paths as operator actions.
// The latest retained worker intent is sufficient after an offline catch-up.
type WorkerIntentHandler struct{ reactor *Reactor }

func NewWorkerIntentHandler(reactor *Reactor) *WorkerIntentHandler {
	return &WorkerIntentHandler{reactor: reactor}
}
func (*WorkerIntentHandler) PermissionFor(string) domain.Permission { return domain.PermWriteServices }
func (*WorkerIntentHandler) IsFleetScoped() bool                    { return true }

func (h *WorkerIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if h.reactor == nil || h.reactor.workerRepo == nil {
		return fmt.Errorf("worker repository is not configured")
	}
	if !supportedWorkerIntentOp(intent.Op) {
		return fmt.Errorf("unsupported op: worker %s — no operator action path", intent.Op)
	}
	pubkey, _ := intent.Content["worker_pubkey"].(string)
	pubkey = strings.TrimSpace(pubkey)
	if !isHexNostrPubKey(pubkey) || intent.Coordinate != "worker:"+pubkey {
		return fmt.Errorf("worker intent requires a matching worker:<pubkey> coordinate")
	}
	desired, _ := intent.Content["scheduling_state"].(string)
	target := domain.WorkerSchedulingState(desired)
	if !validWorkerIntentState(target) {
		return fmt.Errorf("worker intent requires scheduling_state=active|cordoned|draining|maintenance")
	}
	labels, err := workerIntentLabels(intent.Content)
	if err != nil {
		return err
	}
	labels = sanitizeWorkerLabels(labels)
	if want := workerOpTarget(intent.Op); want != "" && target != want {
		return fmt.Errorf("worker %s intent requires scheduling_state=%s", intent.Op, want)
	}
	worker, err := h.reactor.workerRepo.GetByPubKey(ctx, pubkey)
	if err != nil {
		return err
	}
	if worker == nil {
		return fmt.Errorf("worker %s not found", pubkey)
	}
	if err := checkWorkerRevision(intent, worker); err != nil {
		return err
	}
	reason, _ := intent.Content["reason"].(string)
	current := worker.SchedulingState
	if current == "" {
		current = domain.WorkerSchedulingActive
	}
	if current != target {
		command, err := workerCommandForDesired(current, target)
		if err != nil {
			return err
		}
		worker, _, err = h.reactor.updateWorkerSchedulingState(ctx, pubkey, reason, command, target)
		if err != nil {
			return err
		}
	}
	if !maps.Equal(worker.Labels, labels) {
		updater, ok := h.reactor.workerRepo.(workerLabelsUpdater)
		if !ok {
			return fmt.Errorf("worker repository cannot update labels")
		}
		if err := updater.UpdateLabels(ctx, pubkey, labels); err != nil {
			return err
		}
		worker, err = h.reactor.workerRepo.GetByPubKey(ctx, pubkey)
		if err != nil {
			return err
		}
		if worker == nil {
			return fmt.Errorf("worker %s disappeared after label update", pubkey)
		}
	}
	if intent.Op == "cleanup" {
		if h.reactor.workerCleanupOrchestrator == nil {
			return fmt.Errorf("worker cleanup orchestrator is not configured")
		}
		mode, _ := intent.Content["cleanup_mode"].(string)
		if strings.TrimSpace(mode) == "" {
			mode = service.CleanupModeReclaimableOnly
		}
		if _, err := h.reactor.workerCleanupOrchestrator.RequestCleanup(ctx, pubkey, mode, reason); err != nil {
			return err
		}
	}
	if err := h.reactor.publishWorkerState(ctx, worker); err != nil {
		return err
	}
	h.reactor.publishWorkerReadModels(ctx, pubkey)
	return nil
}

func supportedWorkerIntentOp(op string) bool {
	switch op {
	case "cordon", "uncordon", "drain", "undrain", "maintenance-enter", "maintenance-exit", "labels-update", "cleanup":
		return true
	default:
		return false
	}
}

func validWorkerIntentState(state domain.WorkerSchedulingState) bool {
	switch state {
	case domain.WorkerSchedulingActive, domain.WorkerSchedulingCordoned, domain.WorkerSchedulingDraining, domain.WorkerSchedulingMaintenance:
		return true
	default:
		return false
	}
}

func workerOpTarget(op string) domain.WorkerSchedulingState {
	switch op {
	case "cordon":
		return domain.WorkerSchedulingCordoned
	case "drain":
		return domain.WorkerSchedulingDraining
	case "maintenance-enter":
		return domain.WorkerSchedulingMaintenance
	case "uncordon", "undrain", "maintenance-exit":
		return domain.WorkerSchedulingActive
	default:
		return ""
	}
}

func workerCommandForDesired(current, target domain.WorkerSchedulingState) (string, error) {
	switch target {
	case domain.WorkerSchedulingCordoned:
		if current == domain.WorkerSchedulingActive {
			return WorkerCommandCordon, nil
		}
	case domain.WorkerSchedulingDraining:
		if current == domain.WorkerSchedulingActive || current == domain.WorkerSchedulingCordoned {
			return WorkerCommandDrain, nil
		}
	case domain.WorkerSchedulingMaintenance:
		if current == domain.WorkerSchedulingActive || current == domain.WorkerSchedulingCordoned || current == domain.WorkerSchedulingDraining {
			return WorkerCommandMaintenanceEnter, nil
		}
	case domain.WorkerSchedulingActive:
		switch current {
		case domain.WorkerSchedulingCordoned:
			return WorkerCommandUncordon, nil
		case domain.WorkerSchedulingDraining:
			return WorkerCommandUndrain, nil
		case domain.WorkerSchedulingMaintenance:
			return WorkerCommandMaintenanceExit, nil
		}
	}
	return "", fmt.Errorf("worker desired scheduling state %s cannot be reached from %s", target, current)
}

func workerIntentLabels(content map[string]interface{}) (map[string]string, error) {
	raw, ok := content["labels"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("worker intent requires a full labels object")
	}
	labels := make(map[string]string, len(raw))
	for k, v := range raw {
		value, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("worker label %q must be a string", k)
		}
		labels[k] = value
	}
	return labels, nil
}

func checkWorkerRevision(intent *Intent, worker *domain.Worker) error {
	if intent.ExpectedUpdatedAt == nil {
		return nil
	}
	return checkMLRevision(intent, worker.PubKey, true, worker.UpdatedAt)
}
