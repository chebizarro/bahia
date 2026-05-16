package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/controlplane"
)

func mlToolDefinitions() []Tool {
	object := func(props map[string]interface{}, required ...string) map[string]interface{} {
		schema := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	return []Tool{
		{Name: "bahia_ml_import_model", Description: "Publish a generic ML model/model-version import request and return Nostr correlation metadata", InputSchema: object(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"model":           map[string]interface{}{"type": "string", "description": "Model coordinate such as model:<slug>"},
			"model_version":   map[string]interface{}{"type": "string", "description": "Model version coordinate such as model-version:<slug>:<version>"},
			"source":          map[string]interface{}{"type": "string", "description": "Source family, e.g. huggingface"},
			"uri":             map[string]interface{}{"type": "string", "description": "Source URI"},
			"revision":        map[string]interface{}{"type": "string"},
			"runtime":         map[string]interface{}{"type": "string"},
			"task":            map[string]interface{}{"type": "string"},
			"artifact":        map[string]interface{}{"type": "string"},
			"tags":            map[string]interface{}{"type": "object"},
		})},
		{Name: "bahia_ml_run_recipe", Description: "Publish a generic ML recipe run request and return Nostr correlation metadata", InputSchema: object(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"recipe":          map[string]interface{}{"type": "string", "description": "Recipe coordinate such as recipe:<name>:<version>"},
			"inputs":          map[string]interface{}{"type": "object"},
			"parameters":      map[string]interface{}{"type": "object"},
			"runtime":         map[string]interface{}{"type": "string"},
			"task":            map[string]interface{}{"type": "string"},
			"tags":            map[string]interface{}{"type": "object"},
		}, "recipe")},
		{Name: "bahia_ml_deploy", Description: "Publish a generic ML inference deployment request and return Nostr correlation metadata", InputSchema: object(map[string]interface{}{
			"idempotency_key":    map[string]interface{}{"type": "string"},
			"endpoint":           map[string]interface{}{"type": "string", "description": "Endpoint coordinate endpoint:<name>:<environment>"},
			"endpoint_id":        map[string]interface{}{"type": "string"},
			"model_version":      map[string]interface{}{"type": "string", "description": "Model version coordinate model-version:<slug>:<version>"},
			"model_version_id":   map[string]interface{}{"type": "string"},
			"runtime_preference": map[string]interface{}{"type": "string"},
			"runtime":            map[string]interface{}{"type": "string"},
			"placement":          map[string]interface{}{"type": "object"},
			"tags":               map[string]interface{}{"type": "object"},
		})},
		{Name: "bahia_ml_rollback", Description: "Publish a generic ML inference rollback request and return Nostr correlation metadata", InputSchema: object(map[string]interface{}{
			"idempotency_key": map[string]interface{}{"type": "string"},
			"endpoint":        map[string]interface{}{"type": "string"},
			"endpoint_id":     map[string]interface{}{"type": "string"},
			"requested_by":    map[string]interface{}{"type": "string"},
			"tags":            map[string]interface{}{"type": "object"},
		})},
		{Name: "bahia_ml_list_state", Description: "List generic ML inference endpoint state read models", InputSchema: object(map[string]interface{}{})},
		{Name: "bahia_ml_get_state", Description: "Get generic ML inference state for an endpoint/environment", InputSchema: object(map[string]interface{}{
			"endpoint_id":    map[string]interface{}{"type": "string"},
			"environment_id": map[string]interface{}{"type": "string"},
		}, "endpoint_id", "environment_id")},
		{Name: "bahia_ml_get_provenance", Description: "Get ML artifact provenance edges for an artifact ref", InputSchema: object(map[string]interface{}{
			"artifact_id": map[string]interface{}{"type": "string"},
		}, "artifact_id")},
	}
}

func (s *Server) requireMLCommands() (MLCommandPublisher, *ToolResult) {
	if s.mlCommands == nil {
		return nil, errorResult("ML command publisher is not configured")
	}
	return s.mlCommands, nil
}

func (s *Server) handleMLModelImport(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireMLCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishMLModelImportRequest(ctx, mlPayloadFromArgs(args))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish ML model import request: %v", err)), nil
	}
	return jsonResult(mlCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleMLRecipeRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireMLCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishMLRecipeRunRequest(ctx, mlPayloadFromArgs(args))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish ML recipe run request: %v", err)), nil
	}
	return jsonResult(mlCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleMLDeploy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireMLCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishMLInferenceDeployRequest(ctx, mlPayloadFromArgs(args))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish ML deploy request: %v", err)), nil
	}
	return jsonResult(mlCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleMLRollback(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireMLCommands()
	if errResult != nil {
		return errResult, nil
	}
	receipt, err := publisher.PublishMLInferenceRollbackRequest(ctx, mlPayloadFromArgs(args))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish ML rollback request: %v", err)), nil
	}
	return jsonResult(mlCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleMLListState(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.mlRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	states, err := s.mlRegistry.ListInferenceStates(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list ML state: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"states": states, "total": len(states), "read_model_kind": controlplane.KindMLInferenceEndpointState})
}

func (s *Server) handleMLGetState(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.mlRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	endpointID, err := parseRequiredUUIDArg(args, "endpoint_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	envID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	state, err := s.mlRegistry.GetInferenceState(ctx, endpointID, envID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get ML state: %v", err)), nil
	}
	if state == nil {
		return errorResult("ML state not found"), nil
	}
	return jsonResult(map[string]interface{}{"state": state, "read_model_kind": controlplane.KindMLInferenceEndpointState})
}

func (s *Server) handleMLGetProvenance(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.mlRegistry == nil {
		return errorResult("ML registry is not configured"), nil
	}
	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	artifact, err := s.mlRegistry.GetArtifactRef(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get ML artifact: %v", err)), nil
	}
	if artifact == nil {
		return errorResult("ML artifact not found"), nil
	}
	edges, err := s.mlRegistry.ListProvenanceEdgesByArtifact(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list ML provenance: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"artifact": artifact, "edges": edges, "read_model_kind": controlplane.KindMLArtifactProvenanceGraph})
}

func mlPayloadFromArgs(args map[string]interface{}) controlplane.MLCommandPayload {
	payload := controlplane.MLCommandPayload{Content: args, Tags: map[string]string{}}
	for _, key := range []string{"idempotency_key", "request_id", "d"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			payload.IdempotencyKey = strings.TrimSpace(value)
			break
		}
	}
	if raw, ok := args["tags"].(map[string]interface{}); ok {
		for k, v := range raw {
			payload.Tags[k] = fmt.Sprint(v)
		}
	}
	return payload
}

func mlReceiptResourceTags(receipt *controlplane.MLCommandReceipt) map[string]string {
	if receipt == nil {
		return nil
	}
	return map[string]string{"endpoint": receipt.Endpoint, "endpoint_id": receipt.EndpointID, "environment": receipt.Environment, "environment_id": receipt.EnvironmentID, "model_version": receipt.ModelVersion, "model_version_id": receipt.ModelVersionID, "model": receipt.Model, "recipe": receipt.Recipe, "run": receipt.Run, "artifact": receipt.Artifact, "runtime": receipt.Runtime}
}

func mlCommandReceiptToMap(status string, receipt *controlplane.MLCommandReceipt) map[string]interface{} {
	result := map[string]interface{}{"status": status}
	if receipt == nil {
		return result
	}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	result["result_kind"] = receipt.ResultKind
	result["status_kinds"] = []int{}
	result["result_kinds"] = []int{receipt.ResultKind}
	result["read_model_kinds"] = receipt.ReadModelKinds
	result["resource_tags"] = mlReceiptResourceTags(receipt)
	result["d_tag"] = receipt.DTag
	result["published_relays"] = receipt.PublishedRelays
	if receipt.Endpoint != "" {
		result["endpoint"] = receipt.Endpoint
	}
	if receipt.EndpointID != "" {
		result["endpoint_id"] = receipt.EndpointID
	}
	if receipt.Environment != "" {
		result["environment"] = receipt.Environment
	}
	if receipt.EnvironmentID != "" {
		result["environment_id"] = receipt.EnvironmentID
	}
	if receipt.ModelVersion != "" {
		result["model_version"] = receipt.ModelVersion
	}
	if receipt.ModelVersionID != "" {
		result["model_version_id"] = receipt.ModelVersionID
	}
	if receipt.Model != "" {
		result["model"] = receipt.Model
	}
	if receipt.Recipe != "" {
		result["recipe"] = receipt.Recipe
	}
	if receipt.Run != "" {
		result["run"] = receipt.Run
	}
	if receipt.Artifact != "" {
		result["artifact"] = receipt.Artifact
	}
	if receipt.Runtime != "" {
		result["runtime"] = receipt.Runtime
	}
	return result
}
