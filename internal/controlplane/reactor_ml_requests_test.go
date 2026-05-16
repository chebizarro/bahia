package controlplane

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"go.uber.org/zap"
)

func TestHandleMLPhase1RequestsPublishCorrelatedTerminalResults(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.GeneratePrivateKey()
	requestPubkey, err := nostr.GetPublicKey(requestKey)
	if err != nil {
		t.Fatalf("request pubkey: %v", err)
	}
	tests := []struct {
		name       string
		kind       int
		wantResult int
		handler    func(*Reactor, *nostr.Event)
	}{
		{"recipe", KindMLRecipeRunRequest, KindMLRecipeRunResult, func(r *Reactor, ev *nostr.Event) { r.handleMLRecipeRunRequest(ctx, ev) }},
		{"deploy", KindMLInferenceDeployRequest, KindMLInferenceDeployResult, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceDeployRequest(ctx, ev) }},
		{"approval", KindMLInferenceDeploymentApproval, KindMLInferenceApprovalResult, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceDeploymentApproval(ctx, ev) }},
		{"rollback", KindMLInferenceRollbackRequest, KindMLInferenceRollbackResult, func(r *Reactor, ev *nostr.Event) { r.handleMLInferenceRollbackRequest(ctx, ev) }},
		{"import", KindMLModelImportRequest, KindMLModelImportResult, func(r *Reactor, ev *nostr.Event) { r.handleMLModelImportRequest(ctx, ev) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
			if err != nil {
				t.Fatalf("create signer: %v", err)
			}
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
			request := signedLLMRequest(t, requestKey, tt.kind, `{}`, nostr.Tags{{"d", "req-" + tt.name}, {"endpoint", "endpoint:demo:prod"}, {"runtime", "vllm"}})

			tt.handler(reactor, request)

			if len(capture.events) != 1 {
				t.Fatalf("published events = %d, want 1", len(capture.events))
			}
			result := capture.events[0]
			if result.Kind != tt.wantResult {
				t.Fatalf("result kind = %d, want %d", result.Kind, tt.wantResult)
			}
			assertReactorTag(t, result.Tags, "d", "result:"+request.ID)
			assertReactorTag(t, result.Tags, "e", request.ID)
			assertReactorTag(t, result.Tags, "p", requestPubkey)
			assertReactorTag(t, result.Tags, "endpoint", "endpoint:demo:prod")
			assertReactorTag(t, result.Tags, "runtime", "vllm")
			assertSignedEvent(t, result)
		})
	}
}
