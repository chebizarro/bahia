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

func TestMLDelegationClaimRequiresCurrentSignedList(t *testing.T) {
	store := testDelegationStore(t)
	operator, agent, human := nostr.Generate(), nostr.Generate(), nostr.Generate()
	lists := NewDelegationLists(nil, store, []string{operator.Public().Hex()}, []string{agent.Public().Hex()}, nil)
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatal(err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{agent.Public().Hex()}}, nil, nil, signer, zap.NewNop(),
		WithDelegationLists(lists), WithControlPlanePublisher(capture),
		WithMLRegistry(service.NewMLRegistryService(nil, nil, zap.NewNop())),
		WithMLInferenceExecutor(mlParseTestExecutor{}))
	request := func(d string) *nostr.Event {
		return signedLLMRequest(t, agent.Hex(), KindMLInferenceRollbackRequest,
			`{"requested_by":"`+human.Public().Hex()+`"}`, nostr.Tags{{"d", d}})
	}
	reactor.handleMLInferenceRollbackRequest(context.Background(), request("before-list"))
	if got := tagValueNostr(capture.events[len(capture.events)-1].Tags, "result"); got != "delegation_unauthorized" {
		t.Fatalf("before signed list result = %q", got)
	}
	grant := signedDelegationList(t, operator, agent.Public(), 100, human.Public())
	if err := lists.Apply(&grant); err != nil {
		t.Fatal(err)
	}
	reactor.handleMLInferenceRollbackRequest(context.Background(), request("before-eose"))
	if got := tagValueNostr(capture.events[len(capture.events)-1].Tags, "result"); got != "delegation_unauthorized" {
		t.Fatalf("before EOSE result = %q", got)
	}
	lists.mu.Lock()
	lists.caughtUp = true
	lists.mu.Unlock()
	reactor.handleMLInferenceRollbackRequest(context.Background(), request("granted"))
	if got := tagValueNostr(capture.events[len(capture.events)-1].Tags, "result"); got != "endpoint_resolution_error" {
		t.Fatalf("signed grant did not pass delegation gate: result = %q", got)
	}
	revoke := signedDelegationList(t, operator, agent.Public(), 101)
	if err := lists.Apply(&revoke); err != nil {
		t.Fatal(err)
	}
	reactor.handleMLInferenceRollbackRequest(context.Background(), request("revoked"))
	if got := tagValueNostr(capture.events[len(capture.events)-1].Tags, "result"); got != "delegation_unauthorized" {
		t.Fatalf("after revocation result = %q", got)
	}
}
