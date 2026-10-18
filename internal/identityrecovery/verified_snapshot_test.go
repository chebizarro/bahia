package identityrecovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

// writeSnapshot uses ArtifactManager's gzip level, zero ModTime and raw-event JSONL layout.
func writeSnapshot(t *testing.T, lines ...[]byte) (SnapshotArtifact, []byte) {
	t.Helper()
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	zw.ModTime = time.Unix(0, 0).UTC()
	for _, line := range lines {
		if _, err := zw.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "verified.jsonl.gz")
	raw := out.Bytes()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return SnapshotArtifact{Path: path, SHA256: hex.EncodeToString(sum[:])}, append([]byte(nil), raw...)
}

func snapshotLine(t *testing.T, ev nostr.Event) []byte {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifiedSnapshotAdmission(t *testing.T) {
	ctx := context.Background()
	old := signer(t)
	approved := oldEvent(t, old)
	unapproved := nostr.Event{Kind: 1, CreatedAt: 101, Content: "unapproved"}
	if err := old.SignEvent(ctx, &unapproved); err != nil {
		t.Fatal(err)
	}
	artifact, raw := writeSnapshot(t, snapshotLine(t, approved), snapshotLine(t, unapproved))
	source := VerifiedSnapshotSource{Artifacts: []SnapshotArtifact{artifact}}
	found, receipts, err := source.ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[approved.ID.Hex()].ID != approved.ID || len(receipts) != 1 || receipts[0].EventCount != 2 || !slices.Equal(receipts[0].AdmittedIDs, []string{approved.ID.Hex()}) {
		t.Fatalf("admission: %v %v", found, receipts)
	}
	if source.Verification()[0].State != "verified" {
		t.Fatal("digest state absent")
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)/2] ^= 1
	if err := os.WriteFile(artifact.Path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, receipts, err := source.ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()}); err == nil || got != nil || receipts != nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tamper admitted: %v %v %v", got, receipts, err)
	}
	if source.Verification()[0].State != "mismatch" {
		t.Fatal("mismatch state absent")
	}
}

func TestVerifiedSnapshotAllArtifactsAtomic(t *testing.T) {
	ctx := context.Background()
	old := signer(t)
	approved := oldEvent(t, old)
	first, _ := writeSnapshot(t, snapshotLine(t, approved))
	second, raw := writeSnapshot(t, snapshotLine(t, approved))
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(second.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	found, receipts, err := (VerifiedSnapshotSource{Artifacts: []SnapshotArtifact{first, second}}).ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()})
	if err == nil || found != nil || receipts != nil {
		t.Fatalf("second artifact failed to revoke first: %v %v %v", found, receipts, err)
	}
}

func TestVerifiedSnapshotRejectsInvalidEventsAndJSONL(t *testing.T) {
	ctx := context.Background()
	old := signer(t)
	approved := oldEvent(t, old)
	other := oldEvent(t, signer(t))
	future := nostr.Event{Kind: 1, CreatedAt: 1001, Content: "future"}
	if err := old.SignEvent(ctx, &future); err != nil {
		t.Fatal(err)
	}
	badID := approved
	badID.Content = "changed"
	badSignature := approved
	badSignature.Sig = other.Sig
	for _, tc := range []struct {
		name string
		line []byte
	}{
		{"wrong author", snapshotLine(t, other)},
		{"after declaration", snapshotLine(t, future)},
		{"bad id", snapshotLine(t, badID)},
		{"bad signature", snapshotLine(t, badSignature)},
		{"malformed line", []byte(`{"id":`)},
		{"trailing json", append(snapshotLine(t, approved), []byte(` {}`)...)},
		{"unknown field", append(bytes.TrimSuffix(snapshotLine(t, approved), []byte("}")), []byte(`,"extra":true}`)...)},
		{"duplicate field", append(bytes.TrimSuffix(snapshotLine(t, approved), []byte("}")), []byte(`,"content":"duplicate"}`)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, _ := writeSnapshot(t, snapshotLine(t, approved), tc.line)
			got, receipts, err := (VerifiedSnapshotSource{Artifacts: []SnapshotArtifact{artifact}}).ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()})
			if err == nil || got != nil || receipts != nil {
				t.Fatalf("invalid artifact partially admitted: %v %v %v", got, receipts, err)
			}
		})
	}
	artifact, raw := writeSnapshot(t, snapshotLine(t, approved))
	// A valid JSON event without the JSONL delimiter is not an archive record.
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	if _, err := zw.Write(snapshotLine(t, approved)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	noNewline := plain.Bytes()
	if err := os.WriteFile(artifact.Path, noNewline, 0o600); err != nil {
		t.Fatal(err)
	}
	noNewlineSum := sha256.Sum256(noNewline)
	artifact.SHA256 = hex.EncodeToString(noNewlineSum[:])
	if got, receipts, err := (VerifiedSnapshotSource{Artifacts: []SnapshotArtifact{artifact}}).ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()}); err == nil || got != nil || receipts != nil {
		t.Fatalf("missing JSONL newline admitted: %v %v %v", got, receipts, err)
	}
	for _, suffix := range [][]byte{[]byte("garbage"), raw} {
		altered := append(append([]byte(nil), raw...), suffix...)
		if err := os.WriteFile(artifact.Path, altered, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(altered)
		artifact.SHA256 = hex.EncodeToString(sum[:])
		if got, receipts, err := (VerifiedSnapshotSource{Artifacts: []SnapshotArtifact{artifact}}).ReadMissing(ctx, pub(t, old), time.Unix(1000, 0).UTC().Format(time.RFC3339), []string{approved.ID.Hex()}); err == nil || got != nil || receipts != nil {
			t.Fatalf("trailing gzip data admitted: %v %v %v", got, receipts, err)
		}
	}
}

func TestInventorySnapshotFallbackReceiptAndMissing(t *testing.T) {
	ctx := context.Background()
	engine, source, _ := fixture(t)
	accepted := source.events[0]
	artifact, _ := writeSnapshot(t, snapshotLine(t, accepted))
	engine.Config.VerifiedSnapshots = []SnapshotArtifact{artifact}
	engine.Local = testLocal{}
	source.events = nil
	source.scanEvents = nil
	for _, phase := range []Phase{Declare, Contain, Inventory} {
		if err := engine.Apply(ctx, phase); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := state.Receipt(Inventory)
	if !ok || receipt.InventorySources[accepted.ID.Hex()] != "snapshot:"+artifact.SHA256[:12] || len(receipt.InventorySnapshots) != 1 || receipt.InventorySnapshots[0].EventCount != 1 || !slices.Equal(receipt.InventorySnapshots[0].AdmittedIDs, []string{accepted.ID.Hex()}) {
		t.Fatalf("snapshot receipt: %+v", receipt)
	}
	if state.InventoryDigest == "" || len(state.Inventory) != 1 {
		t.Fatalf("inventory: %+v", state.Inventory)
	}
}

func TestInventorySnapshotCannotIntroduceUnapprovedID(t *testing.T) {
	ctx := context.Background()
	engine, source, _ := fixture(t)
	old := source.events[0]
	artifact, _ := writeSnapshot(t, snapshotLine(t, old))
	engine.Config.VerifiedSnapshots = []SnapshotArtifact{artifact}
	engine.Config.AcceptedEventIDs = []string{strings.Repeat("a", 64)}
	engine.Local = testLocal{}
	source.events = nil
	source.scanEvents = nil
	for _, phase := range []Phase{Declare, Contain} {
		if err := engine.Apply(ctx, phase); err != nil {
			t.Fatal(err)
		}
	}
	err := engine.Apply(ctx, Inventory)
	if err == nil || !strings.Contains(err.Error(), engine.Config.AcceptedEventIDs[0]) {
		t.Fatalf("unapproved snapshot id filled missing inventory: %v", err)
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if state.InventoryDigest != "" || len(state.Inventory) != 0 {
		t.Fatal("unapproved snapshot advanced inventory")
	}
	if _, ok := state.Receipt(Inventory); ok {
		t.Fatal("unapproved snapshot got completion receipt")
	}
}
