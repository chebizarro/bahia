package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"fiatjaf.com/nostr"
)

func TestPurgeChecklistRequiresSeparateEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purge.json")
	sink := FilePurgeReceipts{Path: path}
	if _, err := sink.Purge(context.Background(), nil); err == nil {
		t.Fatal("missing file accepted")
	}
	evidence := map[string]string{"archive-artifacts": "archive-sha256", "pg-backups": "backup-sha256", "wal": "wal-sha256"}
	write := func() {
		raw, _ := json.Marshal(evidence)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := sink.Purge(context.Background(), nil); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("missing re-encryption: %v", err)
	}
	evidence["controlled-re-encryption"] = "reencryption-sha256"
	write()
	receipts, err := sink.Purge(context.Background(), []nostr.ID{})
	if err != nil || validatePurgeEvidence(receipts) != nil {
		t.Fatalf("receipts: %v %+v", err, receipts)
	}
	if hasPurgeEvidence(Receipt{EvidenceIDs: append([]string{"deletion-complete"}, receipts...)}, []string{"missing-deletion"}, []string{"wss://relay.example"}) {
		t.Fatal("missing deletion relay OK accepted")
	}
	if slices.Contains(receipts, "purge:controlled-re-encryption:") {
		t.Fatal("evidence ID omitted")
	}
	evidence["wal"] = "nsec1unsafe"
	write()
	if _, err := sink.Purge(context.Background(), nil); err == nil {
		t.Fatal("unsafe evidence accepted")
	}
}

func TestRetireRequiresDeletionAndReencryptionReceipt(t *testing.T) {
	ctx := context.Background()
	engine, _, _ := fixture(t)
	for _, phase := range Phases[:9] {
		if err := engine.Apply(ctx, phase); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := state.Receipt(DeleteAndPurge)
	if !ok || !hasPurgeEvidence(receipt, state.DeletionIDs, engine.Config.ControlledRelays) {
		t.Fatal("phase receipt lacks deletion and purge evidence")
	}
	if err := engine.Apply(ctx, RetireOldIdentity); err != nil {
		t.Fatal(err)
	}
}
