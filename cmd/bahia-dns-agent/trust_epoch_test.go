package main

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
	"github.com/stretchr/testify/require"
)

type dnsEpochSource struct {
	event nostr.Event
	err   error
}

type dnsAckOutput struct{ event nostr.Event }

func (o *dnsAckOutput) Publish(_ context.Context, ev nostr.Event) (int, error) {
	o.event = ev
	return 1, nil
}

func (s *dnsEpochSource) Head(context.Context, string, string) (*nostr.Event, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return &s.event, true, nil
}

func TestDNSAdmissionRejectsPostBarrierOldKeyAndUnverifiableEpoch(t *testing.T) {
	root := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	old := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
	newKey := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	digest := sha256.Sum256(nil)
	body := trustepoch.Epoch{IncidentID: "incident-1", Epoch: 1, OldPubkey: old.Public().Hex(),
		NewPubkey: newKey.Public().Hex(), ControlledRelays: []string{"wss://relay.example"},
		ActivationBarrier: 150, InventoryDigest: hex.EncodeToString(digest[:]), InventoryCount: 0,
		ConsumerRoster: trustepoch.ConsumerIDs(), Activated: true}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	epoch := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: 100,
		Tags: nostr.Tags{{"d", "trust-epoch:svc"}, {"t", trustepoch.Topic}, {"domain", trustepoch.Domain},
			{"schema", trustepoch.Schema}, {"legacy_kind", kinds.CPStateFamilyTrustEpoch.TagValue()},
			{"epoch", "1"}, {"deleted", "false"}}, Content: string(raw)}
	require.NoError(t, epoch.Sign(root))
	state, err := trustepoch.OpenBolt(filepath.Join(t.TempDir(), "epoch.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	source := &dnsEpochSource{event: epoch}
	verifier := &trustepoch.Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(),
		Source: source, Persistence: state}
	resolved, err := verifier.ServiceIdentity(t.Context(), "svc")
	require.NoError(t, err)
	guard := &dnsTrustEpoch{verifier: verifier, resolution: resolved, healthy: true,
		updates: make(chan struct{}, 1)}
	ackSigner := nostr.MustSecretKeyFromHex(strings.Repeat("4", 64))
	output := &dnsAckOutput{}
	guard.ackSigner, guard.ackOutput = ackSigner, output
	require.NoError(t, guard.publishAck(t.Context(), resolved))
	require.NoError(t, trustepoch.VerifyAcknowledgmentAuthority(output.event, resolved,
		"dns", ackSigner.Public().Hex(), false))
	late := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: 151, Content: "late"}
	require.NoError(t, late.Sign(old))
	require.Error(t, guard.Judge(t.Context(), late))
	current := late
	require.NoError(t, current.Sign(newKey))
	require.NoError(t, guard.Judge(t.Context(), current))
	source.err = errors.New("epoch unavailable")
	require.Error(t, guard.Judge(t.Context(), current))
	require.Error(t, guard.Judge(t.Context(), late), "poisoned trust state must stay closed")
}
