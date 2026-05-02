package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
)

type captureNostrPublisher struct {
	events    []nostr.Event
	published int
}

func (p *captureNostrPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return p.published, nil
}

func TestLLMCommandPublisherPublishesCanonicalRollbackRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	publisher := NewLLMCommandPublisher(capture, nostr.GeneratePrivateKey(), nil)
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

func TestLLMCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	publisher := NewLLMCommandPublisher(capture, nostr.GeneratePrivateKey(), nil)

	_, err := publisher.PublishLLMDeployRequest(ctx, LLMDeployCommand{RouteID: uuid.New(), EnvironmentID: uuid.New(), ReleaseID: uuid.New(), RequestedBy: "operator"})
	if err == nil {
		t.Fatalf("expected no relay acceptance error")
	}
}
