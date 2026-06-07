package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type captureNostrPublisher struct {
	events    []nostr.Event
	published int
}

func (p *captureNostrPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return p.published, nil
}

func assertContextVMCommand(t *testing.T, ev nostr.Event, method string) map[string]any {
	t.Helper()
	if ev.Kind != KindContextVMMessage {
		t.Fatalf("expected ContextVM command kind %d, got %d", KindContextVMMessage, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	assertReactorTag(t, ev.Tags, "method", method)
	assertReactorTag(t, ev.Tags, ContextVMRoutingTag, ContextVMWireVersion)
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(ev.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM command: %v", err)
	}
	if rpc.JSONRPC != "2.0" || rpc.Method != method {
		t.Fatalf("unexpected ContextVM command: %#v", rpc)
	}
	var params map[string]any
	if err := json.Unmarshal(rpc.Params, &params); err != nil {
		t.Fatalf("decode ContextVM params: %v", err)
	}
	return params
}

func TestLLMCommandPublisherPublishesCanonicalRouteCreateRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)

	receipt, err := publisher.PublishLLMRouteCreateRequest(ctx, LLMRouteCreateCommand{Name: " chat ", Description: "chat completions", Metadata: map[string]any{"owner": "operator"}, IdempotencyKey: "llm-route-create:chat"})
	if err != nil {
		t.Fatalf("publish route create: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.StatusKind != KindNIP38Status || receipt.ResultKind != KindContextVMMessage {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "llm/route-create")
	assertReactorTag(t, ev.Tags, "route", "chat")
	assertReactorTag(t, ev.Tags, "d", "llm-route-create:chat")
	if receipt.IdempotencyKey != "llm-route-create:chat" || receipt.PublishedRelays != 1 {
		t.Fatalf("unexpected route-create receipt metadata: %#v", receipt)
	}
	if content["name"] != "chat" || content["description"] != "chat completions" {
		t.Fatalf("unexpected route-create content: %#v", content)
	}
	metadata, ok := content["metadata"].(map[string]any)
	if !ok || metadata["owner"] != "operator" {
		t.Fatalf("unexpected route-create metadata: %#v", content)
	}
}

func TestLLMCommandPublisherRejectsEmptyRouteCreateName(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)

	_, err = publisher.PublishLLMRouteCreateRequest(ctx, LLMRouteCreateCommand{Name: "  "})
	if err == nil {
		t.Fatalf("expected empty route name error")
	}
	if len(capture.events) != 0 {
		t.Fatalf("empty route name must not publish events: %d", len(capture.events))
	}
}

func TestLLMCommandPublisherPublishesCanonicalReleaseRegisterRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)
	routeID := uuid.New()

	receipt, err := publisher.PublishLLMReleaseRegisterRequest(ctx, LLMReleaseRegisterCommand{RouteID: routeID, Version: "v1", ModelRef: "hf://example/model", ModelSource: string(domain.ModelSourceHuggingFace)})
	if err != nil {
		t.Fatalf("publish release register: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.StatusKind != KindNIP38Status || receipt.ResultKind != KindContextVMMessage {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "llm/release-register")
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	if content["route_id"] != routeID.String() || content["version"] != "v1" || content["model_ref"] != "hf://example/model" || content["model_source"] != string(domain.ModelSourceHuggingFace) {
		t.Fatalf("unexpected release-register content: %#v", content)
	}
}

func TestLLMCommandPublisherPublishesCanonicalRollbackRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)
	routeID := uuid.New()
	envID := uuid.New()

	receipt, err := publisher.PublishLLMRollbackRequest(ctx, LLMRollbackCommand{RouteID: routeID, EnvironmentID: envID, RequestedBy: "operator"})
	if err != nil {
		t.Fatalf("publish rollback: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.StatusKind != KindNIP38Status || receipt.ResultKind != KindContextVMMessage {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if receipt.RequestEventID == "" || receipt.RequestPubkey == "" {
		t.Fatalf("missing signed event correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "llm/rollback")
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	assertReactorTag(t, ev.Tags, "environment", envID.String())
	if content["route_id"] != routeID.String() || content["environment_id"] != envID.String() || content["requested_by"] != "operator" {
		t.Fatalf("unexpected rollback content: %#v", content)
	}
}

func TestLLMCommandPublisherPublishesCanonicalDeployRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()

	receipt, err := publisher.PublishLLMDeployRequest(ctx, LLMDeployCommand{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: "operator"})
	if err != nil {
		t.Fatalf("publish deploy: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.StatusKind != KindNIP38Status || receipt.ResultKind != KindContextVMMessage {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if receipt.RequestEventID == "" || receipt.RequestPubkey == "" {
		t.Fatalf("missing signed event correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "llm/deploy")
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	assertReactorTag(t, ev.Tags, "environment", envID.String())
	assertReactorTag(t, ev.Tags, "release", releaseID.String())
	if content["route_id"] != routeID.String() || content["environment_id"] != envID.String() || content["release_id"] != releaseID.String() || content["requested_by"] != "operator" {
		t.Fatalf("unexpected deploy content: %#v", content)
	}
}

func TestLLMCommandPublisherFailsWhenNoRelayAcceptsRouteCreateDeployOrRollback(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)

	_, err = publisher.PublishLLMRouteCreateRequest(ctx, LLMRouteCreateCommand{Name: "chat"})
	if err == nil {
		t.Fatalf("expected route create no relay acceptance error")
	}

	_, err = publisher.PublishLLMDeployRequest(ctx, LLMDeployCommand{RouteID: uuid.New(), EnvironmentID: uuid.New(), ReleaseID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatalf("expected deploy no relay acceptance error")
	}

	_, err = publisher.PublishLLMRollbackRequest(ctx, LLMRollbackCommand{RouteID: uuid.New(), EnvironmentID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatalf("expected rollback no relay acceptance error")
	}
}
