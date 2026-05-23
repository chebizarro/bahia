package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type workerCommandRequest struct {
	WorkerPubKey     string            `json:"worker_pubkey,omitempty"`
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

func (r *Reactor) publishWorkerStatus(ctx context.Context, requestEvent *nostr.Event, req *workerCommandRequest, command, status, step, message string) error {
	content := map[string]any{
		"request_event_id": requestEvent.ID,
		"command":          command,
		"step":             step,
		"status":           status,
		"message":          message,
		"worker_pubkey":    req.WorkerPubKey,
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
	if idempotencyKey != "" {
		tags = append(tags, nostr.Tag{"d", "result:" + idempotencyKey})
		tags = append(tags, nostr.Tag{"idempotency", idempotencyKey})
	}
	return tags
}

func (r *Reactor) publishWorkerState(ctx context.Context, worker *domain.Worker) error {
	if worker == nil {
		return fmt.Errorf("worker is nil")
	}
	if worker.SchedulingState == "" {
		worker.SchedulingState = domain.WorkerSchedulingActive
	}
	content := map[string]any{
		"deleted":               false,
		"pubkey":                worker.PubKey,
		"name":                  worker.Name,
		"description":           worker.Description,
		"architecture":          worker.Architecture,
		"max_concurrent_jobs":   worker.MaxConcurrentJobs,
		"current_queue_depth":   worker.CurrentQueueDepth,
		"status":                string(worker.Status),
		"scheduling_state":      string(worker.SchedulingState),
		"scheduling_note":       worker.SchedulingNote,
		"labels":                worker.Labels,
		"capabilities":          worker.Capabilities,
		"ml_capabilities":       worker.MLCapabilities,
		"runtime_target":        worker.RuntimeTarget,
		"resources":             worker.Resources,
		"accelerators":          worker.Accelerators,
		"last_advertisement_at": worker.LastAdvertisementAt.Format(time.RFC3339),
		"updated_at":            worker.UpdatedAt.Format(time.RFC3339),
	}
	event := &nostr.Event{Kind: KindWorkerState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", worker.PubKey}, {"worker", worker.PubKey}, {"deleted", "false"}, {"status", string(worker.Status)}, {"scheduling_state", string(worker.SchedulingState)}}, Content: mustJSON(content)}
	for key, value := range worker.Labels {
		if key != "" {
			event.Tags = append(event.Tags, nostr.Tag{"label", key, value})
		}
	}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign worker state: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}
