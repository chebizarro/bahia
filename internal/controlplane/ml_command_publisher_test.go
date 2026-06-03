package controlplane

import (
	"context"
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
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindContextVMMessage || receipt.DTag != "deploy:qwen-prod" || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.ReadModelKinds["endpoint_state"] != KindCASControlState || receipt.Endpoint != "endpoint:qwen:prod" || receipt.ModelVersion != "model-version:qwen:v1" {
		t.Fatalf("missing correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "ml/inference-deploy")
	assertReactorTag(t, ev.Tags, "d", "deploy:qwen-prod")
	assertReactorTag(t, ev.Tags, "endpoint", "endpoint:qwen:prod")
	assertReactorTag(t, ev.Tags, "environment", "prod")
	assertReactorTag(t, ev.Tags, "model_version", "model-version:qwen:v1")
	assertReactorTag(t, ev.Tags, "runtime", "vllm")
	assertReactorTag(t, ev.Tags, "accelerator", "gpu_nvidia_cuda")
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
		wantMethod string
		wantResult int
	}{
		{"import", publisher.PublishMLModelImportRequest, "ml/model-import", KindContextVMMessage},
		{"recipe", publisher.PublishMLRecipeRunRequest, ContextVMMethodMLRecipeRun, KindContextVMMessage},
		{"rollback", publisher.PublishMLInferenceRollbackRequest, "ml/inference-rollback", KindContextVMMessage},
	}
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			capture.events = nil
			receipt, err := tt.publish(ctx, MLCommandPayload{IdempotencyKey: tt.name + ":1", Content: map[string]any{"endpoint": "endpoint:qwen:prod", "recipe": "recipe:hf-vllm:1", "model": "model:qwen"}})
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != tt.wantResult || receipt.RequestEventID == "" {
				t.Fatalf("unexpected receipt: %#v", receipt)
			}
			if len(capture.events) != 1 {
				t.Fatalf("unexpected event: %#v", capture.events)
			}
			assertContextVMCommand(t, capture.events[0], tt.wantMethod)
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
	if _, err := publisher.PublishMLInferenceDeployRequest(ctx, MLCommandPayload{Content: map[string]any{"endpoint": "endpoint:qwen:prod", "model_version": "model-version:qwen:v1"}}); err == nil {
		t.Fatalf("expected no relay acceptance error")
	}
}
