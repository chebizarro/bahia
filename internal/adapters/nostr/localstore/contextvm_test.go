package localstore

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func openContextVMTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func claimContextVM(t *testing.T, store *Store, req ContextVMRequest, now time.Time) ContextVMClaim {
	t.Helper()
	claim, err := store.ClaimContextVMRequest(req, now)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestContextVMLedgerClaimsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.bolt")
	now := time.Unix(1_900_000_000, 0)
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	store := openContextVMTestStore(t, path)
	keyed := ContextVMRequest{DeliveryID: "wrap-1", RequestID: "inner-1", Key: "requester\x00method\x00key", Fingerprint: "fp", CreatedAt: nostr.Timestamp(now.Unix())}
	if claim := claimContextVM(t, store, keyed, now); claim.State != ContextVMClaimed {
		t.Fatalf("first claim = %v", claim.State)
	}
	if err := store.CompleteContextVMRequest("inner-1", response); err != nil {
		t.Fatal(err)
	}
	unkeyed := ContextVMRequest{DeliveryID: "plain-1", RequestID: "plain-1", CreatedAt: nostr.Timestamp(now.Unix())}
	if claim := claimContextVM(t, store, unkeyed, now); claim.State != ContextVMClaimed {
		t.Fatalf("unkeyed claim = %v", claim.State)
	}
	if err := store.CompleteContextVMRequest("plain-1", nil); err != nil {
		t.Fatal(err)
	}
	interrupted := ContextVMRequest{DeliveryID: "wrap-9", RequestID: "inner-9", CreatedAt: nostr.Timestamp(now.Unix())}
	if claim := claimContextVM(t, store, interrupted, now); claim.State != ContextVMClaimed {
		t.Fatalf("interrupted claim = %v", claim.State)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openContextVMTestStore(t, path)
	defer store.Close()
	for _, tc := range []struct {
		name     string
		req      ContextVMRequest
		want     ContextVMClaimState
		response string
	}{
		{"relay replay of a handled wrap", keyed, ContextVMRedelivered, ""},
		{"re-wrapped copy of the inner request", ContextVMRequest{DeliveryID: "wrap-2", RequestID: "inner-1", Key: keyed.Key, Fingerprint: "fp"}, ContextVMCompleted, string(response)},
		{"new request reusing the idempotency key", ContextVMRequest{DeliveryID: "wrap-3", RequestID: "inner-3", Key: keyed.Key, Fingerprint: "fp"}, ContextVMCompleted, string(response)},
		{"idempotency key with different params", ContextVMRequest{DeliveryID: "wrap-4", RequestID: "inner-4", Key: keyed.Key, Fingerprint: "other"}, ContextVMKeyConflict, ""},
		{"re-wrapped unkeyed request keeps no response", ContextVMRequest{DeliveryID: "wrap-5", RequestID: "plain-1"}, ContextVMCompleted, ""},
		{"interrupted claim is never handed out again", ContextVMRequest{DeliveryID: "wrap-10", RequestID: "inner-9"}, ContextVMPending, ""},
	} {
		claim := claimContextVM(t, store, tc.req, now)
		if claim.State != tc.want || string(claim.Response) != tc.response {
			t.Fatalf("%s: claim = %v %s, want %v %s", tc.name, claim.State, claim.Response, tc.want, tc.response)
		}
	}
	// A conflicting delivery is still recorded, so its replay is skipped.
	if claim := claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-4", RequestID: "inner-4"}, now); claim.State != ContextVMRedelivered {
		t.Fatalf("replayed conflicting delivery = %v", claim.State)
	}
}

func TestContextVMDeliveriesAreMarkedOnce(t *testing.T) {
	store := openContextVMTestStore(t, filepath.Join(t.TempDir(), "events.bolt"))
	defer store.Close()
	now := time.Unix(1_900_000_000, 0)
	if seen, err := store.ContextVMDelivered("response-1"); err != nil || seen {
		t.Fatalf("unseen delivery = %v, %v", seen, err)
	}
	if err := store.MarkContextVMDelivery("response-1", now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkContextVMDelivery("response-1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if seen, err := store.ContextVMDelivered("response-1"); err != nil || !seen {
		t.Fatalf("marked delivery = %v, %v", seen, err)
	}
	// Pruning by first-seen time: the second mark did not refresh it.
	if _, err := store.PruneContextVMLedger(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if seen, _ := store.ContextVMDelivered("response-1"); seen {
		t.Fatal("delivery first seen before the cutoff survived pruning")
	}
}

func TestContextVMLedgerEpochIsRecordedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.bolt")
	store := openContextVMTestStore(t, path)
	first := time.Unix(1_900_000_000, 0).UTC()
	if epoch, err := store.ContextVMLedgerEpoch(first); err != nil || !epoch.Equal(first) {
		t.Fatalf("new epoch = %v, %v", epoch, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openContextVMTestStore(t, path)
	defer store.Close()
	if epoch, err := store.ContextVMLedgerEpoch(first.Add(48 * time.Hour)); err != nil || !epoch.Equal(first) {
		t.Fatalf("reopened epoch = %v, %v", epoch, err)
	}
}

func TestContextVMLedgerPruneDropsOnlyExpiredEntries(t *testing.T) {
	store := openContextVMTestStore(t, filepath.Join(t.TempDir(), "events.bolt"))
	defer store.Close()
	old := time.Unix(1_900_000_000, 0)
	recent := old.Add(10 * 24 * time.Hour)
	cutoff := old.Add(24 * time.Hour)
	claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-old", RequestID: "inner-old", Key: "k-old", Fingerprint: "fp", CreatedAt: nostr.Timestamp(old.Unix())}, old)
	claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-new", RequestID: "inner-new", Key: "k-new", Fingerprint: "fp", CreatedAt: nostr.Timestamp(recent.Unix())}, recent)
	// Seen long ago but created in the future (client clock ahead): kept until
	// its created_at passes the cutoff too.
	claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-skew", RequestID: "inner-skew", CreatedAt: nostr.Timestamp(recent.Unix())}, old)
	removed, err := store.PruneContextVMLedger(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 { // inner-old, wrap-old, wrap-skew
		t.Fatalf("removed = %d, want 3", removed)
	}
	if claim := claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-old-2", RequestID: "inner-old-2", Key: "k-old", Fingerprint: "other"}, recent); claim.State != ContextVMClaimed {
		t.Fatalf("pruned key still bound: %v", claim.State)
	}
	if claim := claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-new-2", RequestID: "inner-new-3", Key: "k-new", Fingerprint: "other"}, recent); claim.State != ContextVMKeyConflict {
		t.Fatalf("recent key lost: %v", claim.State)
	}
	if claim := claimContextVM(t, store, ContextVMRequest{DeliveryID: "wrap-skew-2", RequestID: "inner-skew"}, recent); claim.State != ContextVMPending {
		t.Fatalf("future-dated request pruned early: %v", claim.State)
	}
}

func TestContextVMCursorsArePerRelayAndService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.bolt")
	store := openContextVMTestStore(t, path)
	for _, step := range []struct {
		relay, service string
		to             nostr.Timestamp
	}{{"wss://a", "svc", 100}, {"wss://b", "svc", 200}, {"wss://a", "svc", 50}, {"wss://a", "other", 300}} {
		if err := store.AdvanceContextVMCursor(step.relay, step.service, step.to); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openContextVMTestStore(t, path)
	defer store.Close()
	for _, tc := range []struct {
		relay, service string
		want           nostr.Timestamp
	}{{"wss://a", "svc", 100}, {"wss://b", "svc", 200}, {"wss://c", "svc", 0}, {"wss://a", "other", 300}} {
		got, err := store.ContextVMCursor(tc.relay, tc.service)
		if err != nil || got != tc.want {
			t.Fatalf("cursor %s/%s = %d, %v; want %d", tc.relay, tc.service, got, err, tc.want)
		}
	}
	// The ContextVM cursor is a distinct filter identity from generic cursors.
	if got, _ := store.Cursor("wss://a", "svc"); got != 0 {
		t.Fatalf("generic cursor moved: %d", got)
	}
}
