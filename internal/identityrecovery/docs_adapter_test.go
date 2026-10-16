package identityrecovery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/docs"
)

func docsFixture(t *testing.T) (*DocsRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer, nostr.Event) {
	t.Helper()
	oldKey := signer(t)
	doc := docs.Document{Topic: docs.Topic{Topic: "getting-started", Title: "Getting Started", Category: "guide"}, Markdown: "# Bahia\n"}
	record := docs.DocsRecoveryRecord(doc, 100)
	old := signedRegistryHead(t, oldKey, int(record.Kind), record.Tags, record.Content, 100)
	base, relay, local, key := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	return &DocsRecoveryAdapter{OldPubkey: old.PubKey, Source: relay, Local: local, Ledger: base.Ledger}, relay, local, key, old
}

func TestDocsRecoveryRoundTripIdempotenceAndEquivalence(t *testing.T) {
	a, relay, local, key, old := docsFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != 1 || ids[0] != old.ID.Hex() {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 1 || local.writes != 1 {
		t.Fatalf("writes relay=%d local=%d", relay.publishes, local.writes)
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 2 {
		t.Fatalf("verify %v: %v", proof, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 1 || local.writes != 1 {
		t.Fatal("idempotent rerun wrote head")
	}
	bad := signedRegistryHead(t, key, int(old.Kind), old.Tags, "# Changed\n", time.Now().Unix()+100)
	relay.events = append(relay.events, bad)
	local.events = append(local.events, bad)
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent verify: %v", err)
	}
	if _, err := a.Reassert(ctx, key); err == nil || relay.publishes != 1 {
		t.Fatalf("divergent reassert: %v", err)
	}
}

func TestDocsRecoveryRejectsHashDriftAndEOSEFailure(t *testing.T) {
	a, relay, _, key, old := docsFixture(t)
	bad := old
	bad.Tags = slices.Clone(old.Tags)
	for i := range bad.Tags {
		if len(bad.Tags[i]) >= 2 && bad.Tags[i][0] == "content-hash" {
			bad.Tags[i] = nostr.Tag{"content-hash", "changed"}
		}
	}
	if _, _, err := docsRecord(bad); err == nil {
		t.Fatal("content hash drift accepted")
	}
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil {
		t.Fatal("EOSE failure accepted as absence")
	}
	if _, err := a.Reassert(context.Background(), key); err == nil || relay.publishes != 0 {
		t.Fatalf("EOSE failure published: %v", err)
	}
}
