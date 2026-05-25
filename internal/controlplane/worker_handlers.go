package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type workerCommandRequest struct {
	WorkerPubKey     string            `json:"worker_pubkey,omitempty"`
	EnvironmentID    string            `json:"environment_id,omitempty"`
	WorkloadID       string            `json:"workload_id,omitempty"`
	WorkloadKind     string            `json:"workload_kind,omitempty"`
	Policy           map[string]any    `json:"policy,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	OperatorMetadata map[string]any    `json:"operator_metadata,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
}

type workerSchedulingStateUpdater interface {
	UpdateSchedulingState(ctx context.Context, pubkey string, state domain.WorkerSchedulingState, note string) error
}

type workerLabelsUpdater interface {
	UpdateLabels(ctx context.Context, pubkey string, labels map[string]string) error
}

func (r *Reactor) handleWorkerCordonRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandCordon, domain.WorkerSchedulingCordoned)
}

func (r *Reactor) handleWorkerUncordonRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandUncordon, domain.WorkerSchedulingActive)
}

func (r *Reactor) handleWorkerDrainRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandDrain, domain.WorkerSchedulingDraining)
}

func (r *Reactor) handleWorkerUndrainRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandUndrain, domain.WorkerSchedulingActive)
}

func (r *Reactor) handleWorkerMaintenanceEnterRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandMaintenanceEnter, domain.WorkerSchedulingMaintenance)
}

func (r *Reactor) handleWorkerMaintenanceExitRequest(ctx context.Context, event *nostr.Event) {
	r.handleWorkerSchedulingRequest(ctx, event, WorkerCommandMaintenanceExit, domain.WorkerSchedulingActive)
}

func (r *Reactor) handleWorkerSchedulingRequest(ctx context.Context, event *nostr.Event, command string, targetState domain.WorkerSchedulingState) {
	req, ok := r.decodeWorkerRequest(ctx, event, command, false)
	if !ok {
		return
	}
	_ = r.publishWorkerStatus(ctx, event, req, command, "running", "updating", "worker scheduling state update started")
	worker, err := r.workerRepo.GetByPubKey(ctx, req.WorkerPubKey)
	if err != nil {
		_ = r.publishWorkerResult(ctx, event, req, command, "failed", "lookup_error", err.Error(), nil)
		return
	}
	if worker == nil {
		_ = r.publishWorkerResult(ctx, event, req, command, "failed", "not_found", "worker not found", nil)
		return
	}
	if err := validateWorkerSchedulingTransition(command, worker.SchedulingState, targetState); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, command, "failed", "invalid_transition", err.Error(), worker)
		return
	}
	updater, ok := r.workerRepo.(workerSchedulingStateUpdater)
	if !ok {
		_ = r.publishWorkerResult(ctx, event, req, command, "failed", "worker_repository_unavailable", "worker repository cannot update worker scheduling state", worker)
		return
	}
	if err := updater.UpdateSchedulingState(ctx, req.WorkerPubKey, targetState, strings.TrimSpace(req.Reason)); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, command, "failed", "update_error", err.Error(), worker)
		return
	}
	if refreshed, err := r.workerRepo.GetByPubKey(ctx, req.WorkerPubKey); err == nil && refreshed != nil {
		worker = refreshed
	} else {
		worker.SchedulingState = targetState
		worker.SchedulingNote = strings.TrimSpace(req.Reason)
	}
	if err := r.publishWorkerState(ctx, worker); err != nil {
		r.logger.Warn("publish worker state read model failed", "worker", req.WorkerPubKey, "error", err)
	}
	_ = r.publishWorkerResult(ctx, event, req, command, "succeeded", string(targetState), "worker scheduling state updated", worker)
}

func (r *Reactor) handleWorkerLabelsUpdateRequest(ctx context.Context, event *nostr.Event) {
	req, ok := r.decodeWorkerRequest(ctx, event, WorkerCommandLabelsUpdate, true)
	if !ok {
		return
	}
	_ = r.publishWorkerStatus(ctx, event, req, WorkerCommandLabelsUpdate, "running", "updating", "worker labels update started")
	worker, err := r.workerRepo.GetByPubKey(ctx, req.WorkerPubKey)
	if err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkerCommandLabelsUpdate, "failed", "lookup_error", err.Error(), nil)
		return
	}
	if worker == nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkerCommandLabelsUpdate, "failed", "not_found", "worker not found", nil)
		return
	}
	labels := sanitizeWorkerLabels(req.Labels)
	updater, ok := r.workerRepo.(workerLabelsUpdater)
	if !ok {
		_ = r.publishWorkerResult(ctx, event, req, WorkerCommandLabelsUpdate, "failed", "worker_repository_unavailable", "worker repository cannot update worker labels", worker)
		return
	}
	if err := updater.UpdateLabels(ctx, req.WorkerPubKey, labels); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkerCommandLabelsUpdate, "failed", "update_error", err.Error(), worker)
		return
	}
	if refreshed, err := r.workerRepo.GetByPubKey(ctx, req.WorkerPubKey); err == nil && refreshed != nil {
		worker = refreshed
	} else {
		worker.Labels = labels
	}
	if err := r.publishWorkerState(ctx, worker); err != nil {
		r.logger.Warn("publish worker state read model failed", "worker", req.WorkerPubKey, "error", err)
	}
	_ = r.publishWorkerResult(ctx, event, req, WorkerCommandLabelsUpdate, "succeeded", "labels_updated", "worker labels updated", worker)
}

func (r *Reactor) handleWorkerPolicyApplyRequest(ctx context.Context, event *nostr.Event) {
	req, ok := r.decodePlacementPolicyRequest(ctx, event, WorkerPolicyApplyRequest, true, false)
	if !ok {
		return
	}
	_ = r.publishWorkerStatus(ctx, event, req, WorkerPolicyApplyRequest, "running", "updating", "worker placement policy update started")
	if err := r.validatePinnedWorkerExists(ctx, req); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkerPolicyApplyRequest, "failed", "validation_error", err.Error(), nil)
		return
	}
	policy := sanitizeWorkerPolicy(req.Policy)
	if err := r.applyEnvironmentWorkerPolicy(ctx, req.EnvironmentID, policy); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkerPolicyApplyRequest, "failed", "update_error", err.Error(), nil)
		return
	}
	_ = r.publishWorkerResult(ctx, event, req, WorkerPolicyApplyRequest, "succeeded", "policy_applied", "worker placement policy applied", nil)
}

func (r *Reactor) handleWorkloadPinRequest(ctx context.Context, event *nostr.Event) {
	req, ok := r.decodePlacementPolicyRequest(ctx, event, WorkloadPinRequest, false, true)
	if !ok {
		return
	}
	_ = r.publishWorkerStatus(ctx, event, req, WorkloadPinRequest, "running", "updating", "workload pin update started")
	if err := r.validatePinnedWorkerExists(ctx, req); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "failed", "validation_error", err.Error(), nil)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.WorkloadKind))
	if kind == "ml_inference" || kind == "inference_endpoint" {
		if err := r.applyMLInferencePin(ctx, req); err != nil {
			_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "failed", "update_error", err.Error(), nil)
			return
		}
		_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "succeeded", "workload_pinned", "ML inference workload pin applied", nil)
		return
	}
	if strings.TrimSpace(req.EnvironmentID) == "" {
		_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "failed", "validation_error", "environment_id is required when workload_kind is not ml_inference", nil)
		return
	}
	if err := r.applyEnvironmentWorkerPolicy(ctx, req.EnvironmentID, map[string]any{"pinned_worker": req.WorkerPubKey}); err != nil {
		_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "failed", "update_error", err.Error(), nil)
		return
	}
	_ = r.publishWorkerResult(ctx, event, req, WorkloadPinRequest, "succeeded", "workload_pinned", "environment workload pin applied", nil)
}

func (r *Reactor) decodeWorkerRequest(ctx context.Context, event *nostr.Event, command string, requireLabels bool) (*workerCommandRequest, bool) {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: workerPubKeyFromEvent(event), IdempotencyKey: workerIdempotencyKey(event)}, command, "rejected", "unauthorized", "requester not in authorized list", nil)
		return nil, false
	}
	if r.workerRepo == nil {
		_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: workerPubKeyFromEvent(event), IdempotencyKey: workerIdempotencyKey(event)}, command, "failed", "worker_repository_unavailable", "worker repository is not configured", nil)
		return nil, false
	}
	tagWorkerPubKey := workerPubKeyFromEvent(event)
	tagIdempotencyKey := workerIdempotencyKey(event)
	var req workerCommandRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: workerPubKeyFromEvent(event), IdempotencyKey: workerIdempotencyKey(event)}, command, "failed", "parse_error", err.Error(), nil)
			return nil, false
		}
	}
	contentWorkerPubKey := strings.TrimSpace(req.WorkerPubKey)
	contentIdempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if tagWorkerPubKey != "" && contentWorkerPubKey != "" && tagWorkerPubKey != contentWorkerPubKey {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker tag and worker_pubkey content must match", nil)
		return nil, false
	}
	if tagIdempotencyKey != "" && contentIdempotencyKey != "" && tagIdempotencyKey != contentIdempotencyKey {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "d tag and idempotency_key content must match", nil)
		return nil, false
	}
	if req.WorkerPubKey == "" {
		req.WorkerPubKey = tagWorkerPubKey
	}
	req.WorkerPubKey = strings.TrimSpace(req.WorkerPubKey)
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = tagIdempotencyKey
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.WorkerPubKey == "" {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker_pubkey is required", nil)
		return nil, false
	}
	if !isHexNostrPubKey(req.WorkerPubKey) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker_pubkey must be a 32-byte lowercase hex Nostr public key", nil)
		return nil, false
	}
	if req.IdempotencyKey == "" {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "idempotency key is required via d tag or idempotency_key content", nil)
		return nil, false
	}
	if requireLabels && req.Labels == nil {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "labels are required", nil)
		return nil, false
	}
	_ = r.publishWorkerStatus(ctx, event, &req, command, "accepted", "accepted", "worker command accepted")
	return &req, true
}

func (r *Reactor) decodePlacementPolicyRequest(ctx context.Context, event *nostr.Event, command string, requirePolicy bool, requireWorker bool) (*workerCommandRequest, bool) {
	if !r.isAuthorized(event.PubKey) {
		_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: workerPubKeyFromEvent(event), EnvironmentID: environmentIDFromEvent(event), WorkloadID: workloadIDFromEvent(event), WorkloadKind: workloadKindFromEvent(event), IdempotencyKey: workerIdempotencyKey(event)}, command, "rejected", "unauthorized", "requester not in authorized list", nil)
		return nil, false
	}
	if r.registry == nil && command == WorkerPolicyApplyRequest {
		_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: workerPubKeyFromEvent(event), EnvironmentID: environmentIDFromEvent(event), IdempotencyKey: workerIdempotencyKey(event)}, command, "failed", "registry_unavailable", "registry service is not configured", nil)
		return nil, false
	}
	tagWorkerPubKey := workerPubKeyFromEvent(event)
	tagEnvironmentID := environmentIDFromEvent(event)
	tagWorkloadID := workloadIDFromEvent(event)
	tagWorkloadKind := workloadKindFromEvent(event)
	tagIdempotencyKey := workerIdempotencyKey(event)
	var req workerCommandRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			_ = r.publishWorkerResult(ctx, event, &workerCommandRequest{WorkerPubKey: tagWorkerPubKey, EnvironmentID: tagEnvironmentID, WorkloadID: tagWorkloadID, WorkloadKind: tagWorkloadKind, IdempotencyKey: tagIdempotencyKey}, command, "failed", "parse_error", err.Error(), nil)
			return nil, false
		}
	}
	if !consistentOptionalTag(tagWorkerPubKey, req.WorkerPubKey) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker tag and worker_pubkey content must match", nil)
		return nil, false
	}
	if !consistentOptionalTag(tagEnvironmentID, req.EnvironmentID) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "environment tag and environment_id content must match", nil)
		return nil, false
	}
	if !consistentOptionalTag(tagWorkloadID, req.WorkloadID) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "workload tag and workload_id content must match", nil)
		return nil, false
	}
	if !consistentOptionalTag(tagWorkloadKind, req.WorkloadKind) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "workload_kind tag and workload_kind content must match", nil)
		return nil, false
	}
	if !consistentOptionalTag(tagIdempotencyKey, req.IdempotencyKey) {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "d tag and idempotency_key content must match", nil)
		return nil, false
	}
	if req.WorkerPubKey == "" {
		req.WorkerPubKey = tagWorkerPubKey
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = tagEnvironmentID
	}
	if req.WorkloadID == "" {
		req.WorkloadID = tagWorkloadID
	}
	if req.WorkloadKind == "" {
		req.WorkloadKind = tagWorkloadKind
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = tagIdempotencyKey
	}
	req.WorkerPubKey = strings.TrimSpace(req.WorkerPubKey)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.WorkloadID = strings.TrimSpace(req.WorkloadID)
	req.WorkloadKind = strings.TrimSpace(req.WorkloadKind)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey == "" {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "idempotency key is required via d tag or idempotency_key content", nil)
		return nil, false
	}
	if requireWorker {
		if req.WorkerPubKey == "" {
			_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker_pubkey is required", nil)
			return nil, false
		}
		if !isHexNostrPubKey(req.WorkerPubKey) {
			_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "worker_pubkey must be a 32-byte lowercase hex Nostr public key", nil)
			return nil, false
		}
	}
	if req.EnvironmentID != "" {
		if _, err := uuid.Parse(req.EnvironmentID); err != nil {
			_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", fmt.Sprintf("invalid environment_id: %v", err), nil)
			return nil, false
		}
	}
	if requirePolicy && len(req.Policy) == 0 {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "policy is required", nil)
		return nil, false
	}
	if requirePolicy && req.EnvironmentID == "" {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "environment_id is required", nil)
		return nil, false
	}
	if requireWorker && req.EnvironmentID == "" && req.WorkloadID == "" {
		_ = r.publishWorkerResult(ctx, event, &req, command, "failed", "validation_error", "environment_id or workload_id is required", nil)
		return nil, false
	}
	_ = r.publishWorkerStatus(ctx, event, &req, command, "accepted", "accepted", "placement policy command accepted")
	return &req, true
}

func workerPubKeyFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(tagValueNostr(event.Tags, "worker"))
}

func workerIdempotencyKey(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(tagValueNostr(event.Tags, "d"))
}

func environmentIDFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(tagValueNostr(event.Tags, "environment"))
}

func workloadIDFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(tagValueNostr(event.Tags, "workload"))
}

func workloadKindFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(tagValueNostr(event.Tags, "workload_kind"))
}

func consistentOptionalTag(tagValue, contentValue string) bool {
	tagValue = strings.TrimSpace(tagValue)
	contentValue = strings.TrimSpace(contentValue)
	return tagValue == "" || contentValue == "" || tagValue == contentValue
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

func (r *Reactor) validatePinnedWorkerExists(ctx context.Context, req *workerCommandRequest) error {
	pinned := strings.TrimSpace(req.WorkerPubKey)
	policyPinned := pinnedWorkerFromPolicy(req.Policy)
	if pinned != "" && policyPinned != "" && pinned != policyPinned {
		return fmt.Errorf("worker tag/content and policy.pinned_worker must match")
	}
	if policyPinned != "" {
		pinned = policyPinned
	}
	if pinned == "" {
		return nil
	}
	if !isHexNostrPubKey(pinned) {
		return fmt.Errorf("pinned_worker must be a 32-byte lowercase hex Nostr public key")
	}
	if r.workerRepo == nil {
		return fmt.Errorf("worker repository is not configured")
	}
	worker, err := r.workerRepo.GetByPubKey(ctx, pinned)
	if err != nil {
		return fmt.Errorf("lookup pinned worker: %w", err)
	}
	if worker == nil {
		return fmt.Errorf("pinned worker not found")
	}
	return nil
}

func (r *Reactor) applyEnvironmentWorkerPolicy(ctx context.Context, environmentID string, policy map[string]any) error {
	if r.registry == nil {
		return fmt.Errorf("registry service is not configured")
	}
	envID, err := uuid.Parse(strings.TrimSpace(environmentID))
	if err != nil {
		return fmt.Errorf("invalid environment_id: %w", err)
	}
	env, err := r.registry.GetEnvironment(ctx, envID)
	if err != nil {
		return fmt.Errorf("lookup environment: %w", err)
	}
	if env == nil {
		return fmt.Errorf("environment not found")
	}
	if env.RuntimeConfig == nil {
		env.RuntimeConfig = map[string]any{}
	}
	current := map[string]any{}
	if existing, ok := env.RuntimeConfig["worker_policy"].(map[string]any); ok {
		for key, value := range existing {
			current[key] = value
		}
	}
	for key, value := range policy {
		current[key] = value
	}
	env.RuntimeConfig["worker_policy"] = current
	if err := r.registry.UpdateEnvironment(ctx, env); err != nil {
		return fmt.Errorf("update environment worker policy: %w", err)
	}
	if err := r.PublishEnvironmentRegistry(ctx, env); err != nil {
		r.logger.Warn("publish environment registry after worker policy update failed", "environment_id", env.ID.String(), "error", err)
	}
	return nil
}

func (r *Reactor) applyMLInferencePin(ctx context.Context, req *workerCommandRequest) error {
	if r.mlRegistry == nil {
		return fmt.Errorf("ML registry is not configured")
	}
	endpointID, err := uuid.Parse(strings.TrimSpace(req.WorkloadID))
	if err != nil {
		return fmt.Errorf("invalid workload_id for ML inference endpoint: %w", err)
	}
	endpoint, err := r.mlRegistry.GetInferenceEndpoint(ctx, endpointID)
	if err != nil {
		return fmt.Errorf("lookup ML inference endpoint: %w", err)
	}
	if endpoint == nil {
		return fmt.Errorf("ML inference endpoint not found")
	}
	if req.EnvironmentID != "" && endpoint.EnvironmentID.String() != req.EnvironmentID {
		return fmt.Errorf("environment_id does not match ML inference endpoint environment")
	}
	if endpoint.PlacementPolicy == nil {
		endpoint.PlacementPolicy = map[string]any{}
	}
	endpoint.PlacementPolicy["pinned_worker"] = req.WorkerPubKey
	if err := r.mlRegistry.CreateOrUpdateInferenceEndpoint(ctx, endpoint); err != nil {
		return fmt.Errorf("update ML inference endpoint placement policy: %w", err)
	}
	return nil
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

func (r *Reactor) publishWorkerStatus(ctx context.Context, requestEvent *nostr.Event, req *workerCommandRequest, command, status, step, message string) error {
	content := map[string]any{
		"request_event_id": requestEvent.ID,
		"command":          command,
		"step":             step,
		"status":           status,
		"message":          message,
		"worker_pubkey":    req.WorkerPubKey,
		"environment_id":   req.EnvironmentID,
		"workload_id":      req.WorkloadID,
		"workload_kind":    req.WorkloadKind,
		"idempotency_key":  req.IdempotencyKey,
	}
	event := &nostr.Event{Kind: KindWorkerStatus, CreatedAt: nostr.Now(), Tags: workerReplyTags(requestEvent, req, command, status, step), Content: mustJSON(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign worker status: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) publishWorkerResult(ctx context.Context, requestEvent *nostr.Event, req *workerCommandRequest, command, status, code, message string, worker *domain.Worker) error {
	if req == nil {
		req = &workerCommandRequest{}
	}
	content := map[string]any{
		"request_event_id": requestEvent.ID,
		"command":          command,
		"status":           status,
		"message":          message,
		"worker_pubkey":    req.WorkerPubKey,
		"environment_id":   req.EnvironmentID,
		"workload_id":      req.WorkloadID,
		"workload_kind":    req.WorkloadKind,
		"idempotency_key":  req.IdempotencyKey,
	}
	if code != "" {
		content["code"] = code
	}
	if req.Reason != "" {
		content["reason"] = req.Reason
	}
	if len(req.OperatorMetadata) > 0 {
		content["operator_metadata"] = req.OperatorMetadata
	}
	if len(req.Policy) > 0 {
		content["policy"] = req.Policy
	}
	if worker != nil {
		content["worker"] = worker
		content["scheduling_state"] = string(worker.SchedulingState)
		content["scheduling_note"] = worker.SchedulingNote
		content["labels"] = worker.Labels
	}
	if status == "failed" || status == "rejected" {
		content["error"] = map[string]any{"code": code, "message": message}
	}
	tags := workerReplyTags(requestEvent, req, command, status, "result")
	if code != "" {
		tags = append(tags, nostr.Tag{"result", code})
	}
	event := &nostr.Event{Kind: KindWorkerResult, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: mustJSON(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign worker result: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func workerReplyTags(requestEvent *nostr.Event, req *workerCommandRequest, command, status, step string) nostr.Tags {
	workerPubKey := ""
	idempotencyKey := ""
	if req != nil {
		workerPubKey = req.WorkerPubKey
		idempotencyKey = req.IdempotencyKey
	}
	tags := nostr.Tags{{"e", requestEvent.ID, "", "reply"}, {"p", requestEvent.PubKey}, {"command", command}, {"status", status}, {"step", step}}
	if workerPubKey != "" {
		tags = append(tags, nostr.Tag{"worker", workerPubKey})
	}
	if req != nil {
		if req.EnvironmentID != "" {
			tags = append(tags, nostr.Tag{"environment", req.EnvironmentID})
		}
		if req.WorkloadID != "" {
			tags = append(tags, nostr.Tag{"workload", req.WorkloadID})
		}
		if req.WorkloadKind != "" {
			tags = append(tags, nostr.Tag{"workload_kind", req.WorkloadKind})
		}
	}
	if idempotencyKey != "" {
		tags = append(tags, nostr.Tag{"d", "result:" + idempotencyKey})
		tags = append(tags, nostr.Tag{"idempotency", idempotencyKey})
	}
	return tags
}

func (r *Reactor) publishWorkerState(ctx context.Context, worker *domain.Worker) error {
	if r.workerStatePublisher == nil {
		r.workerStatePublisher = NewWorkerStatePublisher(r.publisher, r.signer)
	}
	return r.workerStatePublisher.Publish(ctx, worker)
}
