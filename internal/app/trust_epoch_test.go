package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/stretchr/testify/require"
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
