package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func signedStatePolicyRecord(t *testing.T, secret nostr.SecretKey, kind int, d string, deleted bool, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	_, envelope := nostradapter.ControlStateEnvelope(kind, d, deleted)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(time.Now().Unix()), Tags: append(envelope, tags...), Content: content}
	require.NoError(t, ev.Sign(secret))
	return ev
}

func TestDecodeStateProducerRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 123456000, time.UTC)
	serviceID, envID := uuid.New(), uuid.New()
	state := domain.EnvironmentServiceState{
		ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusDrifted,
		DesiredHash:                  "desired",
		ReconcileConsecutiveFailures: 2, ReconcileBackoffUntil: &now, LastReconciledAt: &now, UpdatedAt: now,
	}
	tags, content := nostradapter.RuntimeStateRecord(&state, nil)
	dTag := "service:" + serviceID.String() + ":environment:" + envID.String()
	ev := signedStatePolicyRecord(t, nostr.Generate(), kinds.ServiceState, dTag, false, tags, content)
	got, err := DecodeState(ev)
	require.NoError(t, err)
	require.Equal(t, &state, got)
	state.ReconcileFailureMetadata = map[string]any{"message": "credential-like runtime diagnostic"}
	_, publicContent := nostradapter.RuntimeStateRecord(&state, nil)
	require.NotContains(t, publicContent, "credential-like runtime diagnostic")
	var legacy map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &legacy))
	legacy["deployment_unit_id"] = ""
	legacyContent, err := json.Marshal(legacy)
	require.NoError(t, err)
	legacyEvent := signedStatePolicyRecord(t, nostr.Generate(), kinds.ServiceState, dTag, false, tags, string(legacyContent))
	legacyState, err := DecodeState(legacyEvent)
	require.NoError(t, err)
	require.Equal(t, got, legacyState)

	tombTags, tombContent := nostradapter.RuntimeStateTombstoneRecord(serviceID, envID)
	tomb := signedStatePolicyRecord(t, nostr.Generate(), kinds.ServiceState, dTag, true, tombTags, tombContent)
	got, err = DecodeState(tomb)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDecodePolicyProducerRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 123456000, time.UTC)
	envID := uuid.New()
	policy := domain.DeploymentPolicy{
		ID: uuid.New(), Name: "release", EnvironmentID: &envID,
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSBOM, Params: map[string]any{"format": "spdx"}}},
		Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	tags, content := controlplane.PolicyRegistryRecord(&policy, false)
	ev := signedStatePolicyRecord(t, nostr.Generate(), kinds.PolicyRegistry, policy.ID.String(), false, tags, content)
	got, err := DecodePolicy(ev)
	require.NoError(t, err)
	require.Equal(t, &policy, got)

	tombTags, tombContent := controlplane.PolicyRegistryRecord(&policy, true)
	tomb := signedStatePolicyRecord(t, nostr.Generate(), kinds.PolicyRegistry, policy.ID.String(), true, tombTags, tombContent)
	got, err = DecodePolicy(tomb)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestStateSyncRejectsTamperedEvent(t *testing.T) {
	sk := nostr.Generate()
	serviceID, envID := uuid.New(), uuid.New()
	state := domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusDrifted, UpdatedAt: time.Now().UTC().Truncate(time.Second)}
	tags, content := nostradapter.RuntimeStateRecord(&state, nil)
	dTag := "service:" + serviceID.String() + ":environment:" + envID.String()
	ev := signedStatePolicyRecord(t, sk, kinds.ServiceState, dTag, false, tags, content)
	ev.Content = `{"drift_status":"in_sync"}`
	pool := &fakePool{events: []*nostr.Event{&ev}}
	client, err := NewNostrClient(NostrClientConfig{StorePath: filepath.Join(t.TempDir(), "events.db"), ServicePubkey: nostr.GetPublicKey(sk).Hex(), Pool: pool})
	require.NoError(t, err)
	defer client.Close()
	events, result, err := client.SyncAndQuery(t.Context(), "state")
	require.NoError(t, err)
	require.True(t, result.Fresh)
	require.Empty(t, events)
}

type closedEOSEPool struct{}

func (closedEOSEPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (Subscription, error) {
	events := make(chan *nostr.Event)
	allEOSE := make(chan struct{})
	relayEOSE := make(chan RelayEOSEInfo)
	close(relayEOSE)
	return &fakeSubscription{events: events, endOfStoredEvents: allEOSE, relayEOSE: relayEOSE}, nil
}

func TestClosedRelayEOSEIsNotFresh(t *testing.T) {
	sk := nostr.Generate()
	client, err := NewNostrClient(NostrClientConfig{
		StorePath: filepath.Join(t.TempDir(), "events.db"), ServicePubkey: nostr.GetPublicKey(sk).Hex(),
		Pool: closedEOSEPool{}, EOSETimeout: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer client.Close()
	result, err := client.Sync(t.Context(), "state")
	require.NoError(t, err)
	require.False(t, result.Fresh)
}

func TestCancelledStateSyncReturnsError(t *testing.T) {
	sk := nostr.Generate()
	client, err := NewNostrClient(NostrClientConfig{
		StorePath: filepath.Join(t.TempDir(), "events.db"), ServicePubkey: nostr.GetPublicKey(sk).Hex(),
		Pool: closedEOSEPool{}, EOSETimeout: time.Second,
	})
	require.NoError(t, err)
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Sync(ctx, "state")
	require.ErrorIs(t, err, context.Canceled)
}
