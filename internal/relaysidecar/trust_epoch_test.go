package relaysidecar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSidecarTrustEpochReplacesReadWriteAdmissionAndRecordsAck(t *testing.T) {
	var fixture struct {
		RootPubkey string      `json:"root_pubkey"`
		OldPubkey  string      `json:"old_pubkey"`
		NewPubkey  string      `json:"new_pubkey"`
		Epoch      nostr.Event `json:"epoch"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "trust-epoch-envelopes.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	cfg := sidecarTestConfig(t)
	cfg.ServiceID = "deployment-a"
	cfg.OperatorRootPubkey = fixture.RootPubkey
	cfg.PublicKey = fixture.OldPubkey
	ackKey := nostr.MustSecretKeyFromHex("4444444444444444444444444444444444444444444444444444444444444444")
	cfg.Sidecar.AckPrivateKeyFile = filepath.Join(t.TempDir(), "ack.key")
	require.NoError(t, os.WriteFile(cfg.Sidecar.AckPrivateKeyFile, []byte("4444444444444444444444444444444444444444444444444444444444444444"), 0o600))
	cfg.Sidecar.AdministratorPubkeys = []string{fixture.OldPubkey}
	seed, err := openEventStore(t.Context(), cfg.Sidecar.DataDir, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, seed.Save(t.Context(), fixture.Epoch))
	require.NoError(t, seed.Close())
	resolved, err := PreflightTrustEpoch(t.Context(), cfg, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, fixture.NewPubkey, resolved)
	server, err := New(t.Context(), cfg, localSigner(t, nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")), zap.NewNop())
	require.NoError(t, err)
	defer func() { require.NoError(t, server.Close()) }()
	require.False(t, server.readAuth.isAllowedReader(fixture.OldPubkey), "admin allowlist cannot restore old key")
	require.True(t, server.readAuth.isAllowedReader(fixture.NewPubkey))
	old := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "old"}
	require.NoError(t, old.Sign(nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")))
	reject, reason := server.admission.acceptEvent(t.Context(), old)
	require.True(t, reject)
	require.Contains(t, reason, "hostile service event")
	pubkey := ackKey.Public()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{pubkey},
		Tags: nostr.TagMap{"d": []string{"trust-epoch-ack:deployment-a:sidecar"}}}
	seen := false
	for ack := range server.store.Query(t.Context(), filter, 0) {
		body, parseErr := trustepoch.Parse(fixture.Epoch, "deployment-a", fixture.RootPubkey)
		require.NoError(t, parseErr)
		resolution := trustepoch.Resolution{ServiceID: "deployment-a", Pubkey: fixture.NewPubkey,
			Epoch: body.Epoch, Record: &body, EventID: fixture.Epoch.ID.Hex()}
		require.NoError(t, trustepoch.VerifyAcknowledgmentAuthority(ack, resolution, "sidecar", pubkey.Hex(), false))
		seen = true
	}
	require.True(t, seen)
	badHead := fixture.Epoch
	badHead.Content = "{}"
	reject, reason = server.admission.acceptEvent(t.Context(), badHead)
	require.True(t, reject)
	require.Contains(t, reason, "unverifiable")
	require.False(t, server.readAuth.isAllowedReader(fixture.NewPubkey), "malformed root head poisons service reads")
}

func TestSidecarOnlyExemptsExactVerifiedInventoryReplay(t *testing.T) {
	oldKey := nostr.MustSecretKeyFromHex("2222222222222222222222222222222222222222222222222222222222222222")
	newKey := nostr.MustSecretKeyFromHex("3333333333333333333333333333333333333333333333333333333333333333")
	old := nostr.Event{Kind: 1, CreatedAt: 100, Content: "inventoried"}
	require.NoError(t, old.Sign(oldKey))
	p := newPolicy(newKey.Public())
	p.trust = &sidecarTrustEpoch{verifier: &trustepoch.Verifier{LegacyPubkey: oldKey.Public().Hex()},
		resolution: trustepoch.Resolution{Pubkey: newKey.Public().Hex(),
			Record:    &trustepoch.Epoch{OldPubkey: oldKey.Public().Hex(), Activated: true, ActivationBarrier: 150},
			Inventory: map[string]struct{}{old.ID.Hex(): {}}}, logger: zap.NewNop()}
	p.now = func() nostr.Timestamp { return nostr.Timestamp(10 * 365 * 24 * 60 * 60) }
	reject, _ := p.acceptEvent(t.Context(), old)
	require.False(t, reject, "exact inventory replay bypasses ordinary event-age admission")
	unlisted := nostr.Event{Kind: 1, CreatedAt: 100, Content: "unlisted"}
	require.NoError(t, unlisted.Sign(oldKey))
	reject, _ = p.acceptEvent(t.Context(), unlisted)
	require.True(t, reject)
}
