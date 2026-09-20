package controlplane

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestHandleMLInferenceRollbackRequestRejectsSelfDelegation(t *testing.T) {
	ctx := context.Background()
	requestKey := nostr.Generate().Hex()
	requestPubkey := testNostrPubKeyHexFromPrivateKey(t, requestKey)
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requestPubkey}}, nil, nil, signer, zap.NewNop(),
		WithControlPlanePublisher(capture),
		WithMLRegistry(service.NewMLRegistryService(nil, nil, zap.NewNop())),
		WithMLInferenceExecutor(mlParseTestExecutor{}),
	)
	request := signedLLMRequest(t, requestKey, KindMLInferenceRollbackRequest, `{"requested_by":"`+requestPubkey+`"}`, nostr.Tags{{"d", "ml-rollback-self"}})

	reactor.handleMLInferenceRollbackRequest(ctx, request)

	if len(capture.events) == 0 {
		t.Fatal("self-delegation did not publish a result event")
	}
	result := capture.events[len(capture.events)-1]
	if got := tagValueNostr(result.Tags, "result"); got != "delegation_unauthorized" {
		t.Fatalf("self-delegation result code = %q, want delegation_unauthorized", got)
	}
	if !strings.Contains(result.Content, "cannot supply requester authority") {
		t.Fatalf("self-delegation result content = %q, want self-delegation refusal", result.Content)
	}
}
