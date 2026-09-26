package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func assistantContextVMRequest(t *testing.T, key nostr.SecretKey, dTag, method string, params map[string]any) nostr.Event {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": dTag, "method": method, "params": json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: nostr.Kind(kinds.ContextVMMessage), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", dTag}, {"method", method}}, Content: string(content)}
	if err := ev.Sign(key); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestAssistantRequestEvidenceProvesExactSubmissionOnly(t *testing.T) {
	relay := newAssistantTestRelay()
	commandKey := nostr.Generate()
	resolver := &AssistantContextVMRequestEvidenceResolver{Subscriber: relay, RequestAuthor: nostr.GetPublicKey(commandKey).Hex(), Methods: map[string][]string{"mutate": {"service/deploy"}}}
	x := domain.AssistantExecution{SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative}
	w := domain.AssistantWorkItem{WorkID: "r:c", OriginID: "c", ToolName: "mutate", Arguments: map[string]any{"service_id": "svc", "replicas": json.Number("2")}, IdempotencyKey: "assistant-agent:s:r:c"}
	publish := func(ev nostr.Event) string {
		if _, err := relay.Publish(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		return ev.ID.Hex()
	}
	resolve := func(id string) (*domain.AsyncToolReceipt, error) {
		return resolver.ResolveAssistantRequestEvidence(context.Background(), id, x, w)
	}

	if _, err := resolve(strings.Repeat("ab", 32)); err == nil || !strings.Contains(err.Error(), "absence does not prove") {
		t.Fatalf("absent request: %v", err)
	}
	foreign := publish(assistantContextVMRequest(t, nostr.Generate(), w.IdempotencyKey, "service/deploy", map[string]any{"service_id": "svc", "replicas": 2}))
	if _, err := resolve(foreign); err == nil {
		t.Fatal("request signed by another identity accepted")
	}
	wrongKey := publish(assistantContextVMRequest(t, commandKey, "assistant-agent:s:r:other", "service/deploy", map[string]any{"service_id": "svc", "replicas": 2}))
	if _, err := resolve(wrongKey); err == nil {
		t.Fatal("request for another work item accepted")
	}
	wrongMethod := publish(assistantContextVMRequest(t, commandKey, w.IdempotencyKey, "service/rollback", map[string]any{"service_id": "svc", "replicas": 2}))
	if _, err := resolve(wrongMethod); err == nil {
		t.Fatal("request with another method accepted")
	}
	changed := publish(assistantContextVMRequest(t, commandKey, w.IdempotencyKey, "service/deploy", map[string]any{"service_id": "other", "replicas": 2}))
	if _, err := resolve(changed); err == nil {
		t.Fatal("request with different effective arguments accepted")
	}
	exact := publish(assistantContextVMRequest(t, commandKey, w.IdempotencyKey, "service/deploy", map[string]any{"service_id": "svc", "replicas": 2.0, "_meta": map[string]any{"progressToken": w.IdempotencyKey}}))
	receipt, err := resolve(exact)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RequestEventID != exact || receipt.IdempotencyKey != w.IdempotencyKey || receipt.ToolName != "mutate" || len(receipt.ResultKinds) != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	var none *AssistantContextVMRequestEvidenceResolver
	if _, err := none.ResolveAssistantRequestEvidence(context.Background(), exact, x, w); err == nil {
		t.Fatal("unconfigured resolver accepted evidence")
	}
}
