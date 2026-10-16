package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

func TestIdentityContainmentQuarantinesSignerAndPreservesLocalEvidence(t *testing.T) {
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	defer func() { require.NoError(t, outbox.Close()) }()
	admission := nostrout.New(nostrout.Config{})
	marker := filepath.Join(dir, "outbox.bolt.identity-containment")
	require.NoError(t, admission.ConfigureRuntimeKillSwitch(marker))
	containment := newIdentityContainment(admission, store, outbox)
	key := nostr.Generate()
	local, err := nostrutil.NewLocalKeyer(key.Hex())
	require.NoError(t, err)
	guarded := containmentKeyer{Keyer: local, admission: admission}
	oldEvent := nostr.Event{Kind: 1, CreatedAt: nostr.Timestamp(time.Now().Add(-time.Hour).Unix()), Content: "evidence"}
	require.NoError(t, oldEvent.Sign(key))
	_, err = store.SaveEvent(oldEvent)
	require.NoError(t, err)

	require.NoError(t, containment.ActivateRuntimeKillSwitch("019a0000-0000-7000-8000-000000000001"))
	require.ErrorIs(t, guarded.SignEvent(context.Background(), &nostr.Event{Kind: 1, CreatedAt: nostr.Now()}), ErrServiceSignerQuarantined)
	require.ErrorIs(t, admission.AdmitAuth(t.Context(), "wss://relay.example"), nostrout.ErrKillSwitch)
	removed, err := store.PruneRegularEvents(time.Now())
	require.NoError(t, err)
	require.Zero(t, removed)
	removed, err = outbox.Prune(time.Now(), time.Now())
	require.NoError(t, err)
	require.Zero(t, removed)
	require.NoError(t, outbox.CompactBackupRunAccepted("", "", "", nostr.Event{}, ""), "backup ledger compaction must be suspended before inspecting inputs")
	check := identityContainmentHealth(containment, nil)
	require.Equal(t, HealthStatusFail, check.Status)
	require.Equal(t, "019a0000-0000-7000-8000-000000000001", check.Details["incident_id"])

	require.NoError(t, containment.ReleaseRuntimeKillSwitch())
	newEvent := nostr.Event{Kind: 1, CreatedAt: nostr.Now()}
	require.NoError(t, guarded.SignEvent(context.Background(), &newEvent))
	require.True(t, newEvent.VerifySignature())
	removed, err = store.PruneRegularEvents(time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Equal(t, HealthStatusPass, identityContainmentHealth(containment, nil).Status)
}

func TestAppRestartWithContainmentMarkerStartsQuarantined(t *testing.T) {
	restoreDB := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDB()
	cfg := startupTestConfig("emergency")
	cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "daemon.bolt")
	cfg.Nostr.AuthorizedPubkeys = []string{nostr.Generate().Public().Hex()}
	marker := cfg.Nostr.LocalStore.ResolvedOutboxPath() + ".identity-containment"
	staging := nostrout.New(nostrout.Config{})
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0700))
	require.NoError(t, staging.ConfigureRuntimeKillSwitch(marker))
	id := "019a0000-0000-7000-8000-000000000001"
	require.NoError(t, staging.ActivateRuntimeKillSwitch(id))
	t.Cleanup(func() { _ = nostrout.Default().ReleaseRuntimeKillSwitch() })

	a, err := New(cfg)
	require.NoError(t, err)
	defer shutdown(t, a)
	require.NotNil(t, a.IdentityIncidents)
	check := identityContainmentHealth(newIdentityContainment(nostrout.Default(), a.localEventStore, a.localOutbox), a.IdentityIncidents)
	require.Equal(t, HealthStatusFail, check.Status)
	require.Equal(t, id, check.Details["incident_id"])
	require.ErrorIs(t, a.serviceSigner.Keyer().SignEvent(t.Context(), &nostr.Event{Kind: 1, CreatedAt: nostr.Now()}), ErrServiceSignerQuarantined)
}
