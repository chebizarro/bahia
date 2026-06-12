package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

func TestHandleMLPhase1RequestsPublishCorrelatedTerminalResults(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	tests := []struct {
		name    string
		kind    int
		handler func(*Reactor, *nostr.Event)
	}{
		{"recipe", KindMLRecipeRunRequest, func(r *Reactor, ev *nostr.Event) { r.handleMLRecipeRunRequest(ctx, ev) }},
		{"deploy", KindMLInferenceDeployRequest, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceDeployRequest(ctx, ev) }},
		{"approval", KindMLInferenceDeploymentApproval, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceDeploymentApproval(ctx, ev) }},
		{"rollback", KindMLInferenceRollbackRequest, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceRollbackRequest(ctx, ev) }},
		{"import", KindMLModelImportRequest, func(r *Reactor, ev *nostr.Event) { r.handleMLModelImportRequest(ctx, ev) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
			if err != nil {
				t.Fatalf("create signer: %v", err)
			}
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
			request := signedLLMRequest(t, requestKey, tt.kind, `{}`, nostr.Tags{
				{"d", "req-" + tt.name},
				{"endpoint", "endpoint:demo:prod"},
				{"deployment", "deployment:demo"},
				{"model_version", "model-version:qwen:v1"},
				{"recipe", "recipe:hf-vllm:1"},
				{"run", "run:demo"},
				{"artifact", "artifact:weights"},
				{"worker", "worker-pubkey"},
				{"runtime", "vllm"},
				{"task", "chat_completions"},
				{"accelerator", "gpu_nvidia_cuda"},
			})

			tt.handler(reactor, request)

			if len(capture.events) != 1 {
				t.Fatalf("published events = %d, want 1", len(capture.events))
			}
			result := capture.events[0]
			if result.Kind != KindContextVMMessage {
				t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
			}
			assertNoLegacyStatusResultEvents(t, capture.events)
			assertReactorTag(t, result.Tags, "d", "result:"+request.ID.Hex())
			assertReactorTag(t, result.Tags, "e", request.ID.Hex())
			assertReactorTag(t, result.Tags, "p", requestPubkey)
			assertReactorTag(t, result.Tags, "endpoint", "endpoint:demo:prod")
			assertReactorTag(t, result.Tags, "deployment", "deployment:demo")
			assertReactorTag(t, result.Tags, "model_version", "model-version:qwen:v1")
			assertReactorTag(t, result.Tags, "recipe", "recipe:hf-vllm:1")
			assertReactorTag(t, result.Tags, "run", "run:demo")
			assertReactorTag(t, result.Tags, "artifact", "artifact:weights")
			assertReactorTag(t, result.Tags, "worker", "worker-pubkey")
			assertReactorTag(t, result.Tags, "runtime", "vllm")
			assertReactorTag(t, result.Tags, "task", "chat_completions")
			assertReactorTag(t, result.Tags, "accelerator", "gpu_nvidia_cuda")
			assertSignedEvent(t, result)
		})
	}
}
