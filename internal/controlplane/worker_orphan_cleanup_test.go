package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/kinds"
)

type orphanCleanupTestRelay struct {
	events    []*nostr.Event
	published []nostr.Event
}

func (r *orphanCleanupTestRelay) QuerySync(_ context.Context, _ nostr.Filter) ([]*nostr.Event, error) {
	return r.events, nil
}

func (r *orphanCleanupTestRelay) Publish(_ context.Context, event nostr.Event) error {
	r.published = append(r.published, event)
	return nil
}

func TestWorkerOrphanCleanupDryRun(t *testing.T) {
	ctx := context.Background()
	sk := nostr.Generate()

	orphanedPubkey := "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	orphanedEvent := &nostr.Event{
		ID: nostr.ID{0x01}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: sk.Public(), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", orphanedPubkey},
			{"domain", kinds.WorkerDomain},
			{"schema", kinds.CASControlStateSchema},
			{"legacy_kind", "32000"},
		},
		Content: `{"scheduling_state":"active"}`,
	}

	validEvent := &nostr.Event{
		ID: nostr.ID{0x02}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: sk.Public(), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "worker:state:" + orphanedPubkey},
			{"domain", kinds.WorkerDomain},
			{"schema", kinds.CASControlStateSchema},
			{"legacy_kind", "32000"},
			{"t", kinds.WorkerStateTopic},
		},
		Content: `{"scheduling_state":"active"}`,
	}

	// Non-worker record should be ignored.
	nonWorkerEvent := &nostr.Event{
		ID: nostr.ID{0x03}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: sk.Public(), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "some-service-id"},
			{"domain", "controlplane"},
			{"schema", kinds.CASControlStateSchema},
			{"legacy_kind", "5901"},
		},
		Content: `{}`,
	}

	relay := &orphanCleanupTestRelay{events: []*nostr.Event{orphanedEvent, validEvent, nonWorkerEvent}}

	results, err := cleanupOrphanedWorkerRecords(ctx, relay, sk, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("dry run: got %d results, want 1", len(results))
	}
	if results[0].DTag != orphanedPubkey {
		t.Errorf("dry run: d tag = %q, want %q", results[0].DTag, orphanedPubkey)
	}
	if results[0].DeletionID != "" {
		t.Error("dry run: expected empty deletion ID")
	}
	if len(relay.published) != 0 {
		t.Errorf("dry run: published %d events, want 0", len(relay.published))
	}
}

func TestWorkerOrphanCleanupLiveRun(t *testing.T) {
	ctx := context.Background()
	sk := nostr.Generate()

	orphanedPubkey := "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	orphanedEvent := &nostr.Event{
		ID: nostr.ID{0x01}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: sk.Public(), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", orphanedPubkey},
			{"domain", kinds.WorkerDomain},
			{"legacy_kind", "32001"},
		},
		Content: `{}`,
	}

	relay := &orphanCleanupTestRelay{events: []*nostr.Event{orphanedEvent}}

	results, err := cleanupOrphanedWorkerRecords(ctx, relay, sk, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("live run: got %d results, want 1", len(results))
	}
	if results[0].DeletionID == "" {
		t.Error("live run: expected non-empty deletion ID")
	}
	if len(relay.published) != 1 {
		t.Fatalf("live run: published %d events, want 1", len(relay.published))
	}
	deletion := relay.published[0]
	if deletion.Kind != nostr.KindDeletion {
		t.Errorf("deletion kind = %d, want %d", deletion.Kind, nostr.KindDeletion)
	}
	if workerOrphanTagValue(deletion.Tags, "a") == "" {
		t.Error("deletion missing 'a' tag")
	}
}

func TestWorkerOrphanCleanupNoOrphans(t *testing.T) {
	ctx := context.Background()
	sk := nostr.Generate()

	validEvent := &nostr.Event{
		ID: nostr.ID{0x01}, Kind: nostr.Kind(kinds.CASControlState),
		PubKey: sk.Public(), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "worker:state:somepubkey"},
			{"domain", kinds.WorkerDomain},
			{"legacy_kind", "32000"},
			{"t", kinds.WorkerStateTopic},
		},
	}

	relay := &orphanCleanupTestRelay{events: []*nostr.Event{validEvent}}
	results, err := cleanupOrphanedWorkerRecords(ctx, relay, sk, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 orphans, got %d", len(results))
	}
}
