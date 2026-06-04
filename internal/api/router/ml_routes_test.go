package router_test

import (
	"context"
	"encoding/json"
	"errors"
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
	return &controlplane.MLCommandReceipt{RequestEventID: "rest-ml-event", RequestPubkey: "rest-pubkey", RequestKind: requestKind, ResultKind: resultKind, DTag: cmd.IdempotencyKey, ReadModelKinds: map[string]int{"endpoint_state": controlplane.KindMLInferenceEndpointState}, Status: "submitted", PublishedRelays: 1}
}

type failingMLRESTPublisher struct {
	err error
}

func (p *failingMLRESTPublisher) publishError() error {
	if p.err != nil {
		return p.err
	}
	return errors.New("relay publish unavailable")
}
func (p *failingMLRESTPublisher) PublishMLModelImportRequest(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	return nil, p.publishError()
}
func (p *failingMLRESTPublisher) PublishMLRecipeRunRequest(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	return nil, p.publishError()
}
func (p *failingMLRESTPublisher) PublishMLInferenceDeployRequest(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	return nil, p.publishError()
}
func (p *failingMLRESTPublisher) PublishMLInferenceRollbackRequest(context.Context, controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error) {
	return nil, p.publishError()
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
			if data["status"] != "submitted" || data["published_relays"].(float64) != 1 || data["timeout_seconds"].(float64) != 30 {
				t.Fatalf("missing Nostr command receipt status metadata: %#v", data)
			}
			if message, _ := data["message"].(string); !strings.Contains(message, "subscribe to Nostr result/read-model events") {
				t.Fatalf("receipt message must describe Nostr completion semantics: %#v", data)
			}
		})
	}
}

func TestMLRESTAsyncRoutePublishFailureDoesNotReturnSubmittedReceipt(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), MLCommands: &failingMLRESTPublisher{err: errors.New("relay publish unavailable")}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ml/deployments", strings.NewReader(`{"idempotency_key":"deploy:failure","endpoint":"endpoint:qwen:prod","model_version":"model-version:qwen:v1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "request_event_id") || strings.Contains(w.Body.String(), "submitted") {
		t.Fatalf("publish failure must not return submitted Nostr receipt metadata: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "relay publish unavailable") {
		t.Fatalf("publish failure response should preserve bridge error: %s", w.Body.String())
	}
}

func TestMLRESTReadRouteMountedWithoutBreakingLLM(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults(), MLCommands: &captureMLRESTPublisher{}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/llm/routes", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("removed LLM route creation should not be mounted, got status=%d body=%s", w.Code, w.Body.String())
	}
}
