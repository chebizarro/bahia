package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDaemonBootstrapAdoptsActivatedTrustEpochAndRejectsOldKey(t *testing.T) {
	restore := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restore()
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	cfg := startupTestConfig("emergency")
	cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "daemon.db")
	cfg.Nostr.PrivateKey = "3333333333333333333333333333333333333333333333333333333333333333"
	cfg.Nostr.PublicKey = fixture.OldPubkey
	cfg.Nostr.ServiceID = "deployment-a"
	cfg.Nostr.OperatorRootPubkey = fixture.RootPubkey
	seed, err := localstore.Open(cfg.Nostr.LocalStore.Path)
	require.NoError(t, err)
	_, err = seed.SaveEvent(fixture.Epoch)
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	app, err := New(cfg)
	require.NoError(t, err)
	require.Equal(t, fixture.NewPubkey, cfg.Nostr.PublicKey)
	old := nostr.Event{Kind: 1, CreatedAt: 140, Content: "not inventoried"}
	require.NoError(t, old.Sign(nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")))
	_, err = app.localEventStore.SaveEvent(old)
	require.ErrorContains(t, err, "hostile service event")
	newEvent := nostr.Event{Kind: 1, CreatedAt: 160, Content: "current"}
	require.NoError(t, newEvent.Sign(nostr.MustSecretKeyFromHex(cfg.Nostr.PrivateKey)))
	_, err = app.localEventStore.SaveEvent(newEvent)
	require.NoError(t, err)
	pubkey, err := nostr.PubKeyFromHex(fixture.NewPubkey)
	require.NoError(t, err)
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey},
		Tags: nostr.TagMap{"d": []string{"trust-epoch-ack:deployment-a:daemon"}}}
	seen := false
	for ack := range app.localEventStore.QueryEvents(filter) {
		body, parseErr := trustepoch.Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
		require.NoError(t, parseErr)
		resolved := trustepoch.Resolution{ServiceID: "deployment-a", Pubkey: fixture.NewPubkey,
			Epoch: body.Epoch, EventID: fixture.Epoch.ID.Hex(), Record: &body}
		require.NoError(t, trustepoch.VerifyAcknowledgment(ack, resolved, "daemon"))
		seen = true
	}
	require.True(t, seen, "daemon ack is durably recorded")
	badHead := fixture.Epoch
	badHead.Content = "{}"
	require.Error(t, app.localEventStore.ObserveRejected(badHead))
	newAfterPoison := nostr.Event{Kind: 1, CreatedAt: 161, Content: "fenced after malformed head"}
	require.NoError(t, newAfterPoison.Sign(nostr.MustSecretKeyFromHex(cfg.Nostr.PrivateKey)))
	_, err = app.localEventStore.SaveEvent(newAfterPoison)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, app.RunContext(ctx))
}

func TestDaemonHistoricalReplayRequiresExactInventoryAndBarrier(t *testing.T) {
	oldKey := nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")
	old := nostr.Event{Kind: 1, CreatedAt: 100, Content: "inventoried"}
	require.NoError(t, old.Sign(oldKey))
	admission := &trustEpochAdmission{resolution: trustepoch.Resolution{Pubkey: "current",
		Record:    &trustepoch.Epoch{OldPubkey: oldKey.Public().Hex(), Activated: true, ActivationBarrier: 150},
		Inventory: map[string]struct{}{old.ID.Hex(): {}}}}
	require.True(t, admission.allowsHistoricalReplay(old))
	unlisted := nostr.Event{Kind: 1, CreatedAt: 100, Content: "unlisted"}
	require.NoError(t, unlisted.Sign(oldKey))
	require.False(t, admission.allowsHistoricalReplay(unlisted))
	admission.resolution.Record.ActivationBarrier = 99
	require.False(t, admission.allowsHistoricalReplay(old))
}

func TestDaemonRetainsRetiredAuthorsAcrossRotations(t *testing.T) {
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	keys := []nostr.SecretKey{nostr.MustSecretKeyFromHex(strings.Repeat("2", 64)), nostr.MustSecretKeyFromHex(strings.Repeat("3", 64)), nostr.MustSecretKeyFromHex(strings.Repeat("4", 64)), nostr.MustSecretKeyFromHex(strings.Repeat("5", 64))}
	state, err := trustepoch.OpenBolt(filepath.Join(t.TempDir(), "epochs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	a := &trustEpochAdmission{verifier: &trustepoch.Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: keys[0].Public().Hex(), Persistence: state}, resolution: trustepoch.Resolution{ServiceID: "deployment-a", Pubkey: keys[0].Public().Hex()}, logger: zap.NewNop()}
	sum := sha256.Sum256(nil)
	for i := 1; i < len(keys); i++ {
		body := trustepoch.Epoch{IncidentID: "incident", Epoch: uint64(i), OldPubkey: keys[i-1].Public().Hex(), NewPubkey: keys[i].Public().Hex(), ControlledRelays: []string{}, ActivationBarrier: 100, InventoryDigest: hex.EncodeToString(sum[:]), ConsumerRoster: trustepoch.ConsumerIDs(), Activated: true}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(i), Content: string(raw), Tags: nostr.Tags{{"d", "trust-epoch:deployment-a"}, {"t", trustepoch.Topic}, {"domain", trustepoch.Domain}, {"schema", trustepoch.Schema}, {"legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()}, {"epoch", strconv.Itoa(i)}, {"deleted", "false"}}}
		require.NoError(t, ev.Sign(root))
		require.NoError(t, a.admit(ev))
		for _, old := range keys[:i] {
			note := nostr.Event{Kind: 1, CreatedAt: 200}
			require.NoError(t, note.Sign(old))
			require.Error(t, a.admit(note))
		}
	}
}
