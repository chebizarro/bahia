package router_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

type captureMLRESTPublisher struct {
	lastKind int
	lastCmd  controlplane.MLCommandPayload
}

func (p *captureMLRESTPublisher) PublishMLModelImportRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.lastKind, p.lastCmd = controlplane.KindMLModelImportRequest, cmd
	return mlRESTReceipt(controlplane.KindMLModelImportRequest, controlplane.KindMLModelImportResult, cmd), nil
}
func (p *captureMLRESTPublisher) PublishMLRecipeRunRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.lastKind, p.lastCmd = controlplane.KindMLRecipeRunRequest, cmd
	return mlRESTReceipt(controlplane.KindMLRecipeRunRequest, controlplane.KindMLRecipeRunResult, cmd), nil
}
func (p *captureMLRESTPublisher) PublishMLInferenceDeployRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.lastKind, p.lastCmd = controlplane.KindMLInferenceDeployRequest, cmd
	return mlRESTReceipt(controlplane.KindMLInferenceDeployRequest, controlplane.KindMLInferenceDeployResult, cmd), nil
}
func (p *captureMLRESTPublisher) PublishMLInferenceRollbackRequest(_ context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	p.lastKind, p.lastCmd = controlplane.KindMLInferenceRollbackRequest, cmd
	return mlRESTReceipt(controlplane.KindMLInferenceRollbackRequest, controlplane.KindMLInferenceRollbackResult, cmd), nil
}

func mlRESTReceipt(requestKind, resultKind int, cmd controlplane.MLCommandPayload) *controlplane.MLCommandReceipt {
	return &controlplane.MLCommandReceipt{RequestEventID: "rest-ml-event", RequestPubkey: "rest-pubkey", RequestKind: requestKind, ResultKind: resultKind, DTag: cmd.IdempotencyKey, ReadModelKinds: map[string]int{"endpoint_state": controlplane.KindMLInferenceEndpointState}, PublishedRelays: 1}
}

func TestMLRESTAsyncRoutesReturnNostrCorrelationMetadata(t *testing.T) {
	publisher := &captureMLRESTPublisher{}
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), MLCommands: publisher})

	tests := []struct {
		path     string
		wantKind int
		body     string
	}{
		{"/api/v1/ml/imports", controlplane.KindMLModelImportRequest, `{"idempotency_key":"import:1","model":"model:qwen","source":"huggingface"}`},
		{"/api/v1/ml/recipes/runs", controlplane.KindMLRecipeRunRequest, `{"idempotency_key":"recipe:1","recipe":"recipe:hf-vllm:1"}`},
		{"/api/v1/ml/deployments", controlplane.KindMLInferenceDeployRequest, `{"idempotency_key":"deploy:1","endpoint":"endpoint:qwen:prod","model_version":"model-version:qwen:v1"}`},
		{"/api/v1/ml/rollback", controlplane.KindMLInferenceRollbackRequest, `{"idempotency_key":"rollback:1","endpoint":"endpoint:qwen:prod"}`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusAccepted {
				t.Fatalf("status=%d, want 202, body=%s", w.Code, w.Body.String())
			}
			if publisher.lastKind != tt.wantKind {
				t.Fatalf("published kind=%d, want %d", publisher.lastKind, tt.wantKind)
			}
			if publisher.lastCmd.IdempotencyKey == "" {
				t.Fatalf("missing captured idempotency key: %#v", publisher.lastCmd)
			}
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			data := resp["data"].(map[string]any)
			if data["request_event_id"] != "rest-ml-event" || data["request_kind"].(float64) != float64(tt.wantKind) || data["result_kind"].(float64) == 0 {
				t.Fatalf("missing Nostr correlation metadata: %#v", data)
			}
		})
	}
}

func TestMLRESTReadRouteMountedWithoutBreakingLLM(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), MLCommands: &captureMLRESTPublisher{}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/llm/routes", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("LLM routes should remain controlled by LLM registry presence, got status=%d body=%s", w.Code, w.Body.String())
	}
}
