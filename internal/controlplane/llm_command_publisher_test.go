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

func TestLLMCommandPublisherPublishesCanonicalRouteCreateRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)

	receipt, err := publisher.PublishLLMRouteCreateRequest(ctx, LLMRouteCreateCommand{Name: "chat", Description: "chat completions", Metadata: map[string]any{"owner": "operator"}})
	if err != nil {
		t.Fatalf("publish route create: %v", err)
	}
	if receipt.RequestKind != KindLLMRouteCreate || receipt.StatusKind != 0 || receipt.ResultKind != KindLLMRouteCreateResult {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindLLMRouteCreate {
		t.Fatalf("expected route-create kind %d, got %d", KindLLMRouteCreate, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	if len(ev.Tags) != 0 {
		t.Fatalf("route-create request should not emit correlation tags before a route exists: %#v", ev.Tags)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content["name"] != "chat" || content["description"] != "chat completions" {
		t.Fatalf("unexpected route-create content: %#v", content)
	}
	metadata, ok := content["metadata"].(map[string]any)
	if !ok || metadata["owner"] != "operator" {
		t.Fatalf("unexpected route-create metadata: %#v", content)
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
	if receipt.RequestKind != KindLLMReleaseRegister || receipt.StatusKind != 0 || receipt.ResultKind != KindLLMReleaseRegisterResult {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindLLMReleaseRegister {
		t.Fatalf("expected release-register kind %d, got %d", KindLLMReleaseRegister, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
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
	if receipt.RequestKind != KindLLMRollbackRequest || receipt.StatusKind != KindLLMDeploymentStatus || receipt.ResultKind != KindLLMDeploymentResult {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if receipt.RequestEventID == "" || receipt.RequestPubkey == "" {
		t.Fatalf("missing signed event correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindLLMRollbackRequest {
		t.Fatalf("expected rollback kind %d, got %d", KindLLMRollbackRequest, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	assertReactorTag(t, ev.Tags, "environment", envID.String())
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
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
	if receipt.RequestKind != KindLLMDeployRequest || receipt.StatusKind != KindLLMDeploymentStatus || receipt.ResultKind != KindLLMDeploymentResult {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if receipt.RequestEventID == "" || receipt.RequestPubkey == "" {
		t.Fatalf("missing signed event correlation metadata: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindLLMDeployRequest {
		t.Fatalf("expected deploy kind %d, got %d", KindLLMDeployRequest, ev.Kind)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
	assertReactorTag(t, ev.Tags, "route", routeID.String())
	assertReactorTag(t, ev.Tags, "environment", envID.String())
	assertReactorTag(t, ev.Tags, "release", releaseID.String())
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content["route_id"] != routeID.String() || content["environment_id"] != envID.String() || content["release_id"] != releaseID.String() || content["requested_by"] != "operator" {
		t.Fatalf("unexpected deploy content: %#v", content)
	}
}

func TestLLMCommandPublisherFailsWhenNoRelayAcceptsDeployOrRollback(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewLLMCommandPublisher(capture, signer)

	_, err = publisher.PublishLLMDeployRequest(ctx, LLMDeployCommand{RouteID: uuid.New(), EnvironmentID: uuid.New(), ReleaseID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatalf("expected deploy no relay acceptance error")
	}

	_, err = publisher.PublishLLMRollbackRequest(ctx, LLMRollbackCommand{RouteID: uuid.New(), EnvironmentID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatalf("expected rollback no relay acceptance error")
	}
}
