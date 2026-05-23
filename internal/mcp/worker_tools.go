package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// WorkerCommandPublisher emits signer-first worker-management Nostr requests.
type WorkerCommandPublisher interface {
	PublishWorkerCordonRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerUncordonRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerDrainRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerUndrainRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerMaintenanceEnterRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerMaintenanceExitRequest(ctx context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error)
	PublishWorkerLabelsUpdateRequest(ctx context.Context, cmd controlplane.WorkerLabelsUpdateCommand) (*controlplane.WorkerCommandReceipt, error)
}

func workerToolDefinitions() []Tool {
	object := func(props map[string]interface{}, required ...string) map[string]interface{} {
		schema := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	stringProp := map[string]interface{}{"type": "string"}
	lifecycleProps := map[string]interface{}{
		"worker_pubkey":     stringProp,
		"reason":            stringProp,
		"idempotency_key":   stringProp,
		"agent_id":          stringProp,
		"operator_metadata": map[string]interface{}{"type": "object"},
	}
	return []Tool{
		{Name: "bahia_worker_cordon", Description: "Publish a signer-first worker cordon request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_uncordon", Description: "Publish a signer-first worker uncordon request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_drain", Description: "Publish a signer-first worker drain request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_undrain", Description: "Publish a signer-first worker undrain request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_maintenance_enter", Description: "Publish a signer-first worker maintenance-entry request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_maintenance_exit", Description: "Publish a signer-first worker maintenance-exit request and return Nostr correlation metadata", InputSchema: object(lifecycleProps, "worker_pubkey")},
		{Name: "bahia_worker_labels_update", Description: "Publish a signer-first worker labels update request and return Nostr correlation metadata", InputSchema: object(map[string]interface{}{
			"worker_pubkey":     stringProp,
			"labels":            map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
			"reason":            stringProp,
			"idempotency_key":   stringProp,
			"agent_id":          stringProp,
			"operator_metadata": map[string]interface{}{"type": "object"},
		}, "worker_pubkey", "labels")},
		{Name: "bahia_worker_get_assignments", Description: "Get the worker assignment state read model", InputSchema: object(map[string]interface{}{"worker_pubkey": stringProp}, "worker_pubkey")},
		{Name: "bahia_worker_list_assignments", Description: "List worker assignment state read models", InputSchema: object(map[string]interface{}{})},
		{Name: "bahia_worker_get_drain_status", Description: "Get the worker drain status read model", InputSchema: object(map[string]interface{}{"worker_pubkey": stringProp}, "worker_pubkey")},
		{Name: "bahia_worker_list_drain_status", Description: "List worker drain status read models", InputSchema: object(map[string]interface{}{})},
		{Name: "bahia_worker_preview_eligibility", Description: "Preview worker eligibility/ranking for generic worker policy or inference placement", InputSchema: object(map[string]interface{}{
			"preview_id":           stringProp,
			"workload_type":        stringProp,
			"environment_id":       stringProp,
			"policy":               map[string]interface{}{"type": "object"},
			"runtime_kind":         stringProp,
			"task_kind":            stringProp,
			"artifact_formats":     map[string]interface{}{"type": "array", "items": stringProp},
			"accelerator":          stringProp,
			"min_vram_gb":          map[string]interface{}{"type": "integer"},
			"min_system_memory_gb": map[string]interface{}{"type": "integer"},
			"toolchains":           map[string]interface{}{"type": "array", "items": stringProp},
			"cached_artifact":      stringProp,
			"worker_selector":      map[string]interface{}{"type": "object"},
			"max_price":            map[string]interface{}{"type": "integer"},
			"pinned_worker":        stringProp,
			"label_selector":       map[string]interface{}{"type": "object", "additionalProperties": stringProp},
		})},
	}
}

func (s *Server) requireWorkerCommands() (WorkerCommandPublisher, *ToolResult) {
	if s.workerCommands == nil {
		return nil, errorResult("worker command publisher is not configured")
	}
	return s.workerCommands, nil
}

func (s *Server) handleWorkerLifecycleCommand(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireWorkerCommands()
	if errResult != nil {
		return errResult, nil
	}
	cmd := workerLifecycleCommandFromArgs(args)
	var receipt *controlplane.WorkerCommandReceipt
	var err error
	switch name {
	case "bahia_worker_cordon":
		receipt, err = publisher.PublishWorkerCordonRequest(ctx, cmd)
	case "bahia_worker_uncordon":
		receipt, err = publisher.PublishWorkerUncordonRequest(ctx, cmd)
	case "bahia_worker_drain":
		receipt, err = publisher.PublishWorkerDrainRequest(ctx, cmd)
	case "bahia_worker_undrain":
		receipt, err = publisher.PublishWorkerUndrainRequest(ctx, cmd)
	case "bahia_worker_maintenance_enter":
		receipt, err = publisher.PublishWorkerMaintenanceEnterRequest(ctx, cmd)
	case "bahia_worker_maintenance_exit":
		receipt, err = publisher.PublishWorkerMaintenanceExitRequest(ctx, cmd)
	default:
		return errorResult(fmt.Sprintf("unknown worker lifecycle tool: %s", name)), nil
	}
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish worker request: %v", err)), nil
	}
	return jsonResult(workerCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleWorkerLabelsUpdate(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireWorkerCommands()
	if errResult != nil {
		return errResult, nil
	}
	labels := stringMapFromArg(args["labels"])
	if labels == nil {
		return errorResult("labels are required"), nil
	}
	receipt, err := publisher.PublishWorkerLabelsUpdateRequest(ctx, controlplane.WorkerLabelsUpdateCommand{WorkerPubKey: strings.TrimSpace(stringArg(args, "worker_pubkey")), Labels: labels, Reason: strings.TrimSpace(stringArg(args, "reason")), OperatorMetadata: anyMapFromArg(args["operator_metadata"]), IdempotencyKey: strings.TrimSpace(stringArg(args, "idempotency_key")), AgentID: strings.TrimSpace(stringArg(args, "agent_id"))})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish worker labels update request: %v", err)), nil
	}
	return jsonResult(workerCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleWorkerGetAssignments(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	state, err := s.workerReadModels.GetAssignmentState(ctx, stringArg(args, "worker_pubkey"))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker assignments: %v", err)), nil
	}
	if state == nil {
		return errorResult("worker not found"), nil
	}
	return jsonResult(map[string]interface{}{"assignment_state": state, "read_model_kind": controlplane.KindWorkerAssignmentState})
}

func (s *Server) handleWorkerListAssignments(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	states, err := s.workerReadModels.ListAssignmentStates(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list worker assignments: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"assignment_states": states, "total": len(states), "read_model_kind": controlplane.KindWorkerAssignmentState})
}

func (s *Server) handleWorkerGetDrainStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	status, err := s.workerReadModels.GetDrainStatus(ctx, stringArg(args, "worker_pubkey"))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker drain status: %v", err)), nil
	}
	if status == nil {
		return errorResult("worker not found"), nil
	}
	return jsonResult(map[string]interface{}{"drain_status": status, "read_model_kind": controlplane.KindWorkerDrainStatus})
}

func (s *Server) handleWorkerListDrainStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	statuses, err := s.workerReadModels.ListDrainStatuses(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list worker drain statuses: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"drain_statuses": statuses, "total": len(statuses), "read_model_kind": controlplane.KindWorkerDrainStatus})
}

func (s *Server) handleWorkerPreviewEligibility(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workerReadModels == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	previewID := strings.TrimSpace(stringArg(args, "preview_id"))
	workloadType := strings.TrimSpace(stringArg(args, "workload_type"))
	policy := anyMapFromArg(args["policy"])
	if workloadType == "" || workloadType == "worker_policy" || workloadType == "service" || workloadType == "generic" {
		env, err := s.environmentForWorkerPreview(ctx, args, policy)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		preview, err := s.workerReadModels.PreviewWorkerPolicyEligibility(ctx, previewID, env, policy)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to preview worker eligibility: %v", err)), nil
		}
		return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindWorkerEligibilityPreview})
	}
	req := mlPlacementRequestFromArgs(args)
	preview, err := s.workerReadModels.PreviewMLEligibility(ctx, previewID, req, policy)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to preview ML worker eligibility: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindWorkerEligibilityPreview})
}

func (s *Server) environmentForWorkerPreview(ctx context.Context, args map[string]interface{}, policy map[string]any) (*domain.Environment, error) {
	if envIDRaw := strings.TrimSpace(stringArg(args, "environment_id")); envIDRaw != "" {
		if s.registry == nil {
			return nil, fmt.Errorf("registry is not configured")
		}
		envID, err := uuid.Parse(envIDRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid environment_id: %v", err)
		}
		env, err := s.registry.GetEnvironment(ctx, envID)
		if err != nil {
			return nil, fmt.Errorf("failed to get environment: %v", err)
		}
		if env == nil {
			return nil, fmt.Errorf("environment not found")
		}
		return env, nil
	}
	return &domain.Environment{ID: uuid.New(), RuntimeConfig: map[string]any{"worker_policy": policy}, LoomWorkerSelector: anyMapFromArg(args["worker_selector"])}, nil
}

func workerLifecycleCommandFromArgs(args map[string]interface{}) controlplane.WorkerLifecycleCommand {
	return controlplane.WorkerLifecycleCommand{WorkerPubKey: strings.TrimSpace(stringArg(args, "worker_pubkey")), Reason: strings.TrimSpace(stringArg(args, "reason")), OperatorMetadata: anyMapFromArg(args["operator_metadata"]), IdempotencyKey: strings.TrimSpace(stringArg(args, "idempotency_key")), AgentID: strings.TrimSpace(stringArg(args, "agent_id"))}
}

func workerCommandReceiptToMap(status string, receipt *controlplane.WorkerCommandReceipt) map[string]interface{} {
	result := map[string]interface{}{"status": status}
	if receipt == nil {
		return result
	}
	readModels := []int{receipt.StateKind, controlplane.KindWorkerAssignmentState, controlplane.KindWorkerDrainStatus}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	result["result_kind"] = receipt.ResultKind
	result["status_kinds"] = []int{receipt.StatusKind}
	result["result_kinds"] = []int{receipt.ResultKind}
	result["read_model_kinds"] = readModels
	result["d_tag"] = receipt.DTag
	result["published_relays"] = receipt.PublishedRelays
	result["worker_pubkey"] = receipt.WorkerPubKey
	result["command"] = receipt.Command
	result["correlation_tags"] = map[string]string{"d": receipt.DTag, "worker": receipt.WorkerPubKey, "command": receipt.Command, "request_event_id": receipt.RequestEventID}
	return result
}

func mlPlacementRequestFromArgs(args map[string]interface{}) service.MLPlacementRequest {
	return service.MLPlacementRequest{TaskKind: domain.MLTaskKind(stringArg(args, "task_kind")), RuntimeKind: domain.MLRuntimeKind(stringArg(args, "runtime_kind")), ArtifactFormats: mlArtifactFormatsFromArg(args["artifact_formats"]), Accelerator: strings.TrimSpace(stringArg(args, "accelerator")), MinVRAMGB: optionalIntArg(args, "min_vram_gb", 0), MinSystemMemoryGB: optionalIntArg(args, "min_system_memory_gb", 0), Toolchains: stringSliceFromArg(args["toolchains"]), CachedArtifact: strings.TrimSpace(stringArg(args, "cached_artifact")), WorkerSelector: anyMapFromArg(args["worker_selector"]), MaxPrice: optionalIntArg(args, "max_price", 0), PinnedWorker: strings.TrimSpace(stringArg(args, "pinned_worker")), LabelSelector: stringMapFromArg(args["label_selector"])}
}

func mlArtifactFormatsFromArg(raw interface{}) []domain.MLArtifactFormat {
	values := stringSliceFromArg(raw)
	formats := make([]domain.MLArtifactFormat, 0, len(values))
	for _, value := range values {
		formats = append(formats, domain.MLArtifactFormat(value))
	}
	return formats
}

func anyMapFromArg(raw interface{}) map[string]any {
	if raw == nil {
		return nil
	}
	if value, ok := raw.(map[string]any); ok {
		return value
	}
	return nil
}

func stringMapFromArg(raw interface{}) map[string]string {
	if raw == nil {
		return nil
	}
	out := map[string]string{}
	switch value := raw.(type) {
	case map[string]string:
		for k, v := range value {
			out[k] = v
		}
	case map[string]any:
		for k, v := range value {
			out[k] = fmt.Sprint(v)
		}
	default:
		return nil
	}
	return out
}

func stringSliceFromArg(raw interface{}) []string {
	switch value := raw.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
