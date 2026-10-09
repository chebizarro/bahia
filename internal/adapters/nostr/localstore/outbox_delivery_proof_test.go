package localstore

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func backupConfigProofEvent(t *testing.T) nostr.Event {
	t.Helper()
	return signed(t, nostr.Generate(), nostr.Kind(kinds.CASControlState), nostr.Now(), nostr.Tags{
		{"d", "backup-recipe:1"}, {"t", kinds.CPStateTopicBackupRecipe}, {"domain", "backup"},
	}, `{"id":"1","deleted":false}`)
}

func TestBackupConfigDeliveryProofRequiresHistoricalTargetQuorum(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	ev := backupConfigProofEvent(t)
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
	require.NoError(t, err)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://a", "wss://b"}, Required: 2}
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.NoError(t, err)
	_, found, err := outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.False(t, found, "a Delivered bit and one OK cannot satisfy a two-relay policy")
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.ErrorContains(t, err, "lacks verified target quorum")
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPublished, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {Accepted: true}}})
	require.NoError(t, err)
	proof, found, err := outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, proof.ValidFor(ev, "control-plane"))
	require.Equal(t, policy, proof.Policy)
	require.Equal(t, map[string]bool{"wss://a": true, "wss://b": true}, proof.RelayOK)
	require.False(t, proof.ValidFor(ev, "default"), "the wrong target cannot borrow an ACK")

	for name, mutate := range map[string]func(*DeliveryProof){
		"signed payload":  func(p *DeliveryProof) { p.Event.Content = `{"id":"attacker"}` },
		"signature":       func(p *DeliveryProof) { p.Event.Sig = nostr.Event{}.Sig },
		"relay OK":        func(p *DeliveryProof) { p.RelayOK["wss://b"] = false },
		"policy quorum":   func(p *DeliveryProof) { p.Policy.Required = 3 },
		"policy relay":    func(p *DeliveryProof) { p.Policy.WriteRelays[1] = "wss://attacker" },
		"duplicate relay": func(p *DeliveryProof) { p.Policy.WriteRelays[1] = "wss://a" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proof
			changed.Policy.WriteRelays = append([]string(nil), proof.Policy.WriteRelays...)
			changed.RelayOK = map[string]bool{"wss://a": true, "wss://b": true}
			mutate(&changed)
			require.False(t, changed.ValidFor(ev, "control-plane"))
		})
	}
}

func TestBackupConfigDeliveryProofSurvivesPruneAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	outbox, err := OpenOutbox(path)
	require.NoError(t, err)
	ev := backupConfigProofEvent(t)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://a", "wss://b"}, Required: 1}
	_, err = outbox.EnqueuePublisherDelivery(OutboxEntry{Event: ev, Target: "control-plane", Delivered: true,
		Policy: policy, Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {LastError: "down"}}})
	require.NoError(t, err)
	_, found, err := outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.False(t, found, "pre-commit enqueue alone cannot mint proof")
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	pending, err := outbox.ListPending("control-plane", nil, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a crash before proof commit leaves the event retryable")
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.NoError(t, err)
	proof, found, err := outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.True(t, found, "only the publisher's verified round records proof")
	require.True(t, proof.ValidFor(ev, "control-plane"))
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", State: OutboxPublished, Delivered: true, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://b": {Accepted: true}}, At: time.Now().UTC().Add(-48 * time.Hour)})
	require.NoError(t, err)
	removed, err := outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, found, err = outbox.Get(ev.ID)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	defer outbox.Close()
	proof, found, err = outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, proof.ValidFor(ev, "control-plane"))

	proof.RelayOK["wss://a"] = false
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(outboxDeliveryProofsBucket).Put(ev.ID[:], raw)
	}))
	proof, found, err = outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, proof.ValidFor(ev, "control-plane"), "a tampered persistent receipt cannot prove quorum")
}

func TestBackupConfigDeliveryProofRefusesLegacyPolicyFreeRows(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	ev := backupConfigProofEvent(t)
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPublished,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.ErrorContains(t, err, "lacks verified target quorum")
	_, found, err := outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.False(t, found, "legacy rows do not identify the quorum policy and cannot be imported as proof")
}

func TestUnverifiedOutboxCallsCannotMintBackupConfigProof(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	ev := backupConfigProofEvent(t)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://a", "wss://b"}, Required: 2}
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane", Delivered: true, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {Accepted: true}}})
	require.ErrorContains(t, err, "unverified outbox enqueue")
	_, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, err = outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, OutboxRound{Delivered: true, Policy: policy, State: OutboxPending,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {Accepted: true}}})
	require.ErrorContains(t, err, "require the publisher path")
	_, found, err = outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.False(t, found, "generic CommitRound cannot record fabricated OKs")
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "default", Delivered: true, Policy: policy, State: OutboxPending,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {Accepted: true}}})
	require.ErrorContains(t, err, "differs from outbox target")
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, Policy: policy, State: OutboxPending,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.ErrorContains(t, err, "lacks verified target quorum")
	_, found, err = outbox.GetDeliveryProof(ev.ID)
	require.NoError(t, err)
	require.False(t, found, "stored untrusted OKs cannot augment a publisher round")
}
