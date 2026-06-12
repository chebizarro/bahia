package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

type backupCaptureNostrPublisher struct {
	events    []nostr.Event
	published int
}

func (p *backupCaptureNostrPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return p.published, nil
}

func TestBackupCommandPublisherPublishesCanonicalRunRequest(t *testing.T) {
	ctx := context.Background()
	capture := &backupCaptureNostrPublisher{published: 1}
	signer, err := controlplane.NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewBackupCommandPublisher(capture, signer)

	receipt, err := publisher.PublishBackupRunRequest(ctx, BackupRunCommand{Recipe: "recipe:postgres:v1", IdempotencyKey: "backup-run:1", AgentID: "agent:ops"})
	if err != nil {
		t.Fatalf("publish backup run: %v", err)
	}
	if receipt.RequestKind != controlplane.KindBackupRunRequest || receipt.StatusKind != controlplane.KindBackupRunStatus || receipt.ResultKind != controlplane.KindBackupRunResult {
		t.Fatalf("unexpected receipt kinds: %#v", receipt)
	}
	if receipt.DTag != "backup-run:1" || receipt.Action != "backup_run" || receipt.PublishedRelays != 1 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != nostr.Kind(controlplane.KindBackupRunRequest) {
		t.Fatalf("expected kind %d, got %d", controlplane.KindBackupRunRequest, ev.Kind)
	}
	if !ev.VerifySignature() {
		t.Fatalf("published event signature invalid")
	}
	assertBackupTag(t, ev.Tags, "d", "backup-run:1")
	assertBackupTag(t, ev.Tags, "command", "backup_run")
	assertBackupTag(t, ev.Tags, "agent", "agent:ops")
	assertBackupTag(t, ev.Tags, "recipe", "recipe:postgres:v1")

	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content["recipe"] != "recipe:postgres:v1" || content["idempotency_key"] != "backup-run:1" || content["action"] != "backup_run" || content["agent_id"] != "agent:ops" {
		t.Fatalf("unexpected content: %#v", content)
	}
}

func assertBackupTag(t *testing.T, tags nostr.Tags, key, want string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == want {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %#v", key, want, tags)
}
