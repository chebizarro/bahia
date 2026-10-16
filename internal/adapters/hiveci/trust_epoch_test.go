package hiveci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type hiveEpochSource struct {
	event nostr.Event
	err   error
}

func (s *hiveEpochSource) Head(context.Context, string, string) (*nostr.Event, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return &s.event, true, nil
}

func TestSubscriberAndCanonicalRepositoryRejectPostBarrierOldServiceRun(t *testing.T) {
	old := nostr.MustSecretKeyFromHex(hiveCITestPrivateKey)
	newKey := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	root := nostr.MustSecretKeyFromHex(strings.Repeat("4", 64))
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
	source := &hiveEpochSource{event: epoch}
	v := &trustepoch.Verifier{OperatorRoot: root.Public().Hex(), LegacyPubkey: old.Public().Hex(),
		Source: source, Persistence: state}
	run, _ := signedRunAndResult(t, time.Unix(1_800_000_000, 0).UTC())
	d := startCanonicalDaemon(t, t.TempDir(), nil, []string{old.Public().Hex()})
	_, err = d.store.SaveEvent(*run)
	require.NoError(t, err)
	d.repo.SetTrustEpoch(v, "svc")
	got, err := d.repo.GetRunByEventID(t.Context(), run.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, got)
	subscriberRepo := &testHiveRepo{runs: map[string]domain.HiveCIWorkflowRun{}}
	subscriber := NewSubscriber(nil, subscriberRepo, []string{old.Public().Hex()}, zap.NewNop(), nil)
	subscriber.SetTrustEpoch(v, "svc")
	subscriber.handleEvent(t.Context(), run)
	require.Empty(t, subscriberRepo.runs)
	source.err = errors.New("epoch source unavailable")
	_, err = d.repo.GetRunByEventID(t.Context(), run.ID.Hex())
	require.Error(t, err)
}
