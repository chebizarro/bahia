package mcp

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

type captureMLCommandPublisher struct {
	importCmd   *controlplane.MLCommandPayload
	recipeCmd   *controlplane.MLCommandPayload
	deployCmd   *controlplane.MLCommandPayload
	rollbackCmd *controlplane.MLCommandPayload
}

func (p *captureMLCommandPublisher) PublishMLModelImportRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.importCmd = &cmd
	return mlTestReceipt(controlplane.KindContextVMMessage, controlplane.KindCASControlState, cmd, map[string]int{"model_registry": controlplane.KindCASControlState}), nil
}
func (p *captureMLCommandPublisher) PublishMLRecipeRunRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.recipeCmd = &cmd
	return mlTestReceipt(controlplane.KindContextVMMessage, controlplane.KindCASControlState, cmd, map[string]int{"recipe_run_state": controlplane.KindCASControlState}), nil
}
func (p *captureMLCommandPublisher) PublishMLInferenceDeployRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.deployCmd = &cmd
	return mlTestReceipt(controlplane.KindContextVMMessage, controlplane.KindCASControlState, cmd, map[string]int{"endpoint_state": controlplane.KindCASControlState}), nil
}
func (p *captureMLCommandPublisher) PublishMLInferenceRollbackRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.rollbackCmd = &cmd
	return mlTestReceipt(controlplane.KindContextVMMessage, controlplane.KindCASControlState, cmd, map[string]int{"endpoint_state": controlplane.KindCASControlState}), nil
}

func mlTestReceipt(requestKind, resultKind int, cmd controlplane.MLCommandPayload, readModels map[string]int) *controlplane.MLCommandReceipt {
	return &controlplane.MLCommandReceipt{RequestEventID: "ml-event", RequestPubkey: "mcp-pubkey", RequestKind: requestKind, ResultKind: resultKind, ReadModelKinds: readModels, DTag: cmd.IdempotencyKey, PublishedRelays: 1, Endpoint: mlStringArg(cmd.Content, "endpoint"), ModelVersion: mlStringArg(cmd.Content, "model_version"), Recipe: mlStringArg(cmd.Content, "recipe")}
}

func mlStringArg(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func TestGetToolsIncludesMLTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{"bahia_ml_import_model": false, "bahia_ml_run_recipe": false, "bahia_ml_deploy": false, "bahia_ml_rollback": false, "bahia_ml_list_state": false, "bahia_ml_get_state": false, "bahia_ml_get_provenance": false}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing ML tool %s", name)
		}
	}
}

func TestMLMutatingToolsPublishNostrRequestsAndReturnCorrelation(t *testing.T) {
	ctx := context.Background()
	publisher := &captureMLCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{MLCommandPublisher: publisher})

	deployRes, err := server.CallTool(ctx, "bahia_ml_deploy", map[string]interface{}{"idempotency_key": "deploy:1", "endpoint": "endpoint:qwen:prod", "model_version": "model-version:qwen:v1", "runtime": "vllm"})
	if err != nil {
		t.Fatalf("deploy err: %v", err)
	}
	if deployRes.IsError {
		t.Fatalf("deploy returned error: %s", deployRes.Content[0].Text)
	}
	deployPayload := decodeResultMap(t, deployRes)
	if deployPayload["request_kind"].(float64) != float64(controlplane.KindContextVMMessage) || deployPayload["result_kind"].(float64) != float64(controlplane.KindCASControlState) || deployPayload["request_event_id"] != "ml-event" {
		t.Fatalf("unexpected deploy payload: %#v", deployPayload)
	}
	if publisher.deployCmd == nil || publisher.deployCmd.IdempotencyKey != "deploy:1" || publisher.deployCmd.Content["endpoint"] != "endpoint:qwen:prod" {
		t.Fatalf("unexpected captured deploy command: %#v", publisher.deployCmd)
	}

	for _, tt := range []struct{ tool, key string }{{"bahia_ml_import_model", "importCmd"}, {"bahia_ml_run_recipe", "recipeCmd"}, {"bahia_ml_rollback", "rollbackCmd"}} {
		res, err := server.CallTool(ctx, tt.tool, map[string]interface{}{"idempotency_key": tt.tool + ":1", "recipe": "recipe:hf-vllm:1", "endpoint": "endpoint:qwen:prod", "model": "model:qwen"})
		if err != nil {
			t.Fatalf("%s err: %v", tt.tool, err)
		}
		if res.IsError {
			t.Fatalf("%s returned error: %s", tt.tool, res.Content[0].Text)
		}
		payload := decodeResultMap(t, res)
		if payload["request_event_id"] != "ml-event" || payload["d_tag"] == "" {
			t.Fatalf("missing correlation metadata for %s: %#v", tt.tool, payload)
		}
	}
}

func TestMLMutatingToolsRequirePublisher(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(context.Background(), "bahia_ml_deploy", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected missing publisher error, got %#v", res)
	}
}
