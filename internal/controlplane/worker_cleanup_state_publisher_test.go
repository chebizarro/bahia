package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/events"
)

func TestWorkerCleanupStatePublisherPublishesCanonicalState(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewWorkerCleanupStatePublisher(capture, signer)
	started := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Minute)

	if err := publisher.Publish(ctx, events.WorkerCleanupEvent{
		WorkerPubKey:  "worker-pubkey-1",
		CleanupMode:   "reclaimable_only",
		Reason:        "storage pressure",
		LoomJobID:     "loom-job-1",
		ProtectedRefs: []string{"relay:standby", "lnd:emergency"},
		TargetFreeGB:  40,
		Status:        "completed",
		StartedAt:     started,
		CompletedAt:   &completed,
	}); err != nil {
		t.Fatalf("publish cleanup state: %v", err)
	}

	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	event := capture.events[0]
	if event.Kind != KindCASControlState {
		t.Fatalf("expected kind %d, got %d", KindCASControlState, event.Kind)
	}
	if tagValueNostr(event.Tags, "schema") != workerCleanupStateSchema || tagValueNostr(event.Tags, "worker") != "worker-pubkey-1" || tagValueNostr(event.Tags, "status") != "completed" {
		t.Fatalf("unexpected cleanup state tags: %#v", event.Tags)
	}
	if tagValueNostr(event.Tags, "d") != "worker:cleanup:worker-pubkey-1:loom-job-1" {
		t.Fatalf("unexpected d tag: %#v", event.Tags)
	}

	var content map[string]any
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatalf("cleanup state content json: %v", err)
	}
	if content["cleanup_id"] != "worker:cleanup:worker-pubkey-1:loom-job-1" || content["worker_pubkey"] != "worker-pubkey-1" || content["status"] != "completed" {
		t.Fatalf("unexpected content identity/status: %#v", content)
	}
	if content["schema"] != nil {
		t.Fatalf("schema belongs in tags, not cleanup content: %#v", content)
	}
}

func TestWorkerCleanupStatePublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewWorkerCleanupStatePublisher(capture, signer)

	err = publisher.Publish(ctx, events.WorkerCleanupEvent{
		WorkerPubKey: "worker-pubkey-1",
		CleanupMode:  "reclaimable_only",
		Status:       "requested",
		StartedAt:    time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatalf("expected no relay acceptance error")
	}
}
