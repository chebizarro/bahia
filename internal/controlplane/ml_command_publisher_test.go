package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestMLCommandPublisherPublishesAddressableDeployRequestWithCorrelation(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewMLCommandPublisher(capture, signer)

	receipt, err := publisher.PublishMLInferenceDeployRequest(ctx, MLCommandPayload{
		IdempotencyKey: "deploy:qwen-prod",
		Content: map[string]any{
			"endpoint":           "endpoint:qwen:prod",
			"model_version":      "model-version:qwen:v1",
			"runtime_preference": "vllm",
			"placement":          map[string]any{"accelerator": "gpu_nvidia_cuda"},
		},
		Tags: map[string]string{"runtime": "vllm", "accelerator": "gpu_nvidia_cuda"},
	})
	if err != nil {
		t.Fatalf("publish deploy: %v", err)
	}
	if receipt.RequestKind != KindMLInferenceDeployRequest || receipt.ResultKind != KindMLInferenceDeployResult || receipt.DTag != "deploy:qwen-prod" || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.ReadModelKinds["endpoint_state"] != KindMLInferenceEndpointState || receipt.Endpoint != "endpoint:qwen:prod" || receipt.ModelVersion != "model-version:qwen:v1" {
		t.Fatalf("missing correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindMLInferenceDeployRequest {
		t.Fatalf("expected kind %d, got %d", KindMLInferenceDeployRequest, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	assertReactorTag(t, ev.Tags, "d", "deploy:qwen-prod")
	assertReactorTag(t, ev.Tags, "endpoint", "endpoint:qwen:prod")
	assertReactorTag(t, ev.Tags, "environment", "prod")
	assertReactorTag(t, ev.Tags, "model_version", "model-version:qwen:v1")
	assertReactorTag(t, ev.Tags, "runtime", "vllm")
	assertReactorTag(t, ev.Tags, "accelerator", "gpu_nvidia_cuda")
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content["endpoint"] != "endpoint:qwen:prod" || content["model_version"] != "model-version:qwen:v1" {
		t.Fatalf("unexpected content: %#v", content)
	}
	if _, ok := content["idempotency_key"]; ok {
		t.Fatalf("idempotency key should be represented as d-tag, not content: %#v", content)
	}
}

func TestMLCommandPublisherPublishesImportRecipeAndRollbackKinds(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewMLCommandPublisher(capture, signer)
	calls := []struct {
		name       string
		publish    func(context.Context, MLCommandPayload) (*MLCommandReceipt, error)
		wantKind   int
		wantResult int
	}{
		{"import", publisher.PublishMLModelImportRequest, KindMLModelImportRequest, KindMLModelImportResult},
		{"recipe", publisher.PublishMLRecipeRunRequest, KindMLRecipeRunRequest, KindMLRecipeRunResult},
		{"rollback", publisher.PublishMLInferenceRollbackRequest, KindMLInferenceRollbackRequest, KindMLInferenceRollbackResult},
	}
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			capture.events = nil
			receipt, err := tt.publish(ctx, MLCommandPayload{IdempotencyKey: tt.name + ":1", Content: map[string]any{"endpoint": "endpoint:qwen:prod", "recipe": "recipe:hf-vllm:1", "model": "model:qwen"}})
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			if receipt.RequestKind != tt.wantKind || receipt.ResultKind != tt.wantResult || receipt.RequestEventID == "" {
				t.Fatalf("unexpected receipt: %#v", receipt)
			}
			if len(capture.events) != 1 || capture.events[0].Kind != tt.wantKind {
				t.Fatalf("unexpected event: %#v", capture.events)
			}
			assertReactorTag(t, capture.events[0].Tags, "d", tt.name+":1")
		})
	}
}

func TestMLCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewMLCommandPublisher(capture, signer)
	if _, err := publisher.PublishMLInferenceDeployRequest(ctx, MLCommandPayload{Content: map[string]any{"endpoint": "endpoint:qwen:prod"}}); err == nil {
		t.Fatalf("expected no relay acceptance error")
	}
}
