package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRelayPolicyProjectionViewRequiresRelayConfirmationForFreshness(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	projection := repository.RelayPolicyProjection{
		EventCreatedAt: now.Add(-time.Minute), EventAcceptedAt: now.Add(-time.Minute),
		LastSyncAt: now, SourceRelay: "wss://user:token@relay.example/path?token=secret",
	}
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Now: func() time.Time { return now }})
	cached := h.projectionView(projection)
	require.Equal(t, "stale", cached.Freshness)
	require.Empty(t, cached.RelayConfirmedAt)
	require.Equal(t, "wss://relay.example/path", cached.SourceRelay)

	confirmedAt := now.Add(-time.Minute)
	projection.RelayConfirmedAt = &confirmedAt
	confirmed := h.projectionView(projection)
	require.Equal(t, "fresh", confirmed.Freshness)
	require.Equal(t, confirmedAt.Format(time.RFC3339), confirmed.RelayConfirmedAt)
}

func TestRelayPolicySecretRedactionAcrossValidationLogsSignedEventAndBackup(t *testing.T) {
	const secret = "relay-policy-secret-sentinel"
	const authorURL = "wss://user:" + secret + "@relay.example/path"
	const queryURL = "wss://relay.example/path?token=" + secret
	for _, tc := range []struct {
		name  string
		state RelayPolicyState
	}{
		{"browser userinfo", RelayPolicyState{BrowserRelays: []string{authorURL}}},
		{"service query", RelayPolicyState{ServiceRelays: []string{queryURL}}},
		{"dm userinfo", RelayPolicyState{DMRelayLists: []RelayPolicyDMRelayList{{Enabled: true, Feature: config.DMRelayListFeatureNotifications, Identity: config.DMRelayListIdentityService, Relays: []string{authorURL}}}}},
		{"admin relay query", RelayPolicyState{RelayAdministration: RelayPolicyAdministration{Targets: []RelayPolicyAdminTarget{{Ref: "edge", RelayURL: queryURL}}}}},
		{"admin HTTP query", RelayPolicyState{RelayAdministration: RelayPolicyAdministration{Targets: []RelayPolicyAdminTarget{{Ref: "edge", RelayURL: "wss://relay.example", HTTPURL: "https://relay.example/admin?token=" + secret}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			err := normalizeAndValidateRelayPolicyForSettings(&state, false)
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
		})
	}

	author := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	store := &memoryRelayPolicyProjectionStore{}
	core, logs := observer.New(zap.InfoLevel)
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		Pool:          nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
		ServicePubkey: author, ProjectionStore: store, Logger: zap.New(core),
		Now: func() time.Time { return time.Unix(2_000, 0).UTC() },
	})
	bad := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{authorURL}})
	require.False(t, h.handleEventFromRelay(context.Background(), bad, queryURL))
	goodState := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://safe.example"}}
	require.NoError(t, normalizeAndValidateRelayPolicyForSettings(&goodState, false))
	good := signedRelaySettingsStateEvent(t, time.Unix(1_001, 0), goodState)
	require.True(t, h.handleEventFromRelay(context.Background(), good, "wss://user:"+secret+"@relay.example/path?token="+secret))
	projection, err := store.Get(context.Background(), author)
	require.NoError(t, err)
	require.NotNil(t, projection)
	require.Equal(t, "wss://relay.example/path", projection.SourceRelay)
	backup, err := repository.NewRelayPolicyProjectionBackup(*projection, time.Unix(2_000, 0).UTC())
	require.NoError(t, err)
	logJSON, err := json.Marshal(logs.All())
	require.NoError(t, err)
	backupJSON, err := json.Marshal(backup)
	require.NoError(t, err)
	for name, output := range map[string]string{
		"logs": string(logJSON), "signed event": string(projection.SignedEvent),
		"backup": string(backupJSON),
	} {
		require.False(t, strings.Contains(output, secret), "%s disclosed a relay credential", name)
	}
}
