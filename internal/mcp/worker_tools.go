package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
)

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

func (s *Server) handleWorkerLifecycleCommand(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, name, args)
}

func (s *Server) handleWorkerLabelsUpdate(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_worker_labels_update", args)
}

func (s *Server) handleWorkerPreviewEligibility(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	workers := stateWorkerRepository{server: s}
	models := service.NewWorkerReadModelService(workers, nil, nil, service.NewWorkerPolicyService(workers, s.logger), service.NewMLPlacementService(workers, s.logger), s.logger)
	previewID := strings.TrimSpace(stringArg(args, "preview_id"))
	workloadType := strings.TrimSpace(stringArg(args, "workload_type"))
	policy := anyMapFromArg(args["policy"])
	if workloadType == "" || workloadType == "worker_policy" || workloadType == "service" || workloadType == "generic" {
		env, err := s.environmentForWorkerPreview(ctx, args, policy)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		preview, err := models.PreviewWorkerPolicyEligibility(ctx, previewID, env, policy)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to preview worker eligibility: %v", err)), nil
		}
		return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerEligibilityTopic})
	}
	req := mlPlacementRequestFromArgs(args)
	preview, err := models.PreviewMLEligibility(ctx, previewID, req, policy)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to preview ML worker eligibility: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerEligibilityTopic})
}

func (s *Server) environmentForWorkerPreview(ctx context.Context, args map[string]interface{}, policy map[string]any) (*domain.Environment, error) {
	if envIDRaw := strings.TrimSpace(stringArg(args, "environment_id")); envIDRaw != "" {
		envID, err := uuid.Parse(envIDRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid environment_id: %w", err)
		}
		var env *domain.Environment
		record, readErr := s.readStateOne(ctx, nostrpool.KindEnvironmentRegistry, "id", envID.String())
		if readErr != nil {
			return nil, readErr
		}
		if record != nil {
			env = &domain.Environment{}
			if err := json.Unmarshal(record.Content, env); err != nil {
				return nil, err
			}
		}
		if env == nil {
			return nil, fmt.Errorf("environment not found")
		}
		return env, nil
	}
	return &domain.Environment{ID: uuid.New(), RuntimeConfig: map[string]any{"worker_policy": policy}, LoomWorkerSelector: anyMapFromArg(args["worker_selector"])}, nil
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
