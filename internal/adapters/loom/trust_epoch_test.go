package loom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

type loomEpochSource struct {
	event nostr.Event
	err   error
}

func (s *loomEpochSource) Head(context.Context, string, string) (*nostr.Event, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return &s.event, true, nil
}

func TestLoomJobAndResultWaitRequireCurrentTrustEpochSigner(t *testing.T) {
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	old := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
	newKey := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	digest := sha256.Sum256(nil)
	body := trustepoch.Epoch{IncidentID: "incident-1", Epoch: 1, OldPubkey: old.Public().Hex(),
		NewPubkey: newKey.Public().Hex(), ControlledRelays: []string{"wss://relay.example"},
		ActivationBarrier: 150, InventoryDigest: hex.EncodeToString(digest[:]), InventoryCount: 0,
		ConsumerRoster: trustepoch.ConsumerIDs(), Activated: true}
	raw, _ := json.Marshal(body)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: 100,
		Tags: nostr.Tags{{"d", "trust-epoch:svc"}, {"t", trustepoch.Topic}, {"domain", trustepoch.Domain},
			{"schema", trustepoch.Schema}, {"legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()},
			{"epoch", "1"}, {"deleted", "false"}}, Content: string(raw)}
	if err := ev.Sign(root); err != nil {
		t.Fatal(err)
	}
	state, err := trustepoch.OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
	})
	source := &loomEpochSource{event: ev}
	v := &trustepoch.Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(),
		Source: source, Persistence: state}
	client := &Client{serviceID: "svc", clientPubkey: old.Public().Hex(), trustVerifier: v}
	if err := client.requireCurrentServiceIdentity(t.Context()); err == nil {
		t.Fatal("old signer admitted")
	}
	client.clientPubkey = newKey.Public().Hex()
	if err := client.requireCurrentServiceIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	late := nostr.Event{Kind: nostr.Kind(KindJobResult), CreatedAt: 151, Content: "late"}
	if err := late.Sign(old); err != nil {
		t.Fatal(err)
	}
	if err := client.admitJobEvent(t.Context(), late); err == nil {
		t.Fatal("post-barrier old-key job result admitted")
	}
	if err := late.Sign(newKey); err != nil {
		t.Fatal(err)
	}
	if err := client.admitJobEvent(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	source.err = errors.New("epoch source unavailable")
	if err := client.requireCurrentServiceIdentity(t.Context()); err == nil {
		t.Fatal("unverifiable epoch admitted")
	}
}
