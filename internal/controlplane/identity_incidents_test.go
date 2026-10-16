package controlplane

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

func incidentEvent(t *testing.T, author nostr.SecretKey, old nostr.PubKey, id, phase string, aborted bool, created nostr.Timestamp) nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"incident_id": id, "old_pubkey": old.Hex(), "declared_at": time.Now().UTC().Format(time.RFC3339),
		"reason_code": "key_compromise", "controlled_relays": []string{"wss://relay.example"}, "phase": phase, "aborted": aborted,
	})
	require.NoError(t, err)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: created, Content: string(content), Tags: nostr.Tags{
		{"d", "identity-incident:" + id}, {"t", kinds.CPStateTopicIdentityIncident}, {"domain", "identity"},
		{"schema", "bahia.identity-incident.v1"}, {"legacy_kind", kinds.CPStateFamilyIdentityIncident.TagValue()},
		{"old", old.Hex()}, {"deleted", "false"},
	}}
	require.NoError(t, ev.Sign(author))
	return ev
}

func incidentFixture(t *testing.T) (*IdentityIncidents, *nostrout.Admission, *localstore.Store, *localstore.Outbox, nostr.SecretKey, nostr.SecretKey, string, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()); require.NoError(t, store.Close()) })
	admission := nostrout.New(nostrout.Config{})
	marker := filepath.Join(dir, "outbox.bolt.identity-containment")
	require.NoError(t, admission.ConfigureRuntimeKillSwitch(marker))
	root, old := nostr.Generate(), nostr.Generate()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	watcher, err := NewIdentityIncidents(&delegationFakePool{urls: []string{"wss://relay.example"}, filters: make(chan nostr.Filter, 1), events: make(chan *nostr.Event), eose: make(chan nostradapter.RelayEOSE), done: make(chan struct{})}, store, outbox, admission, []string{root.Public().Hex()}, old.Public().Hex(), nil)
	require.NoError(t, err)
	return watcher, admission, store, outbox, root, old, id.String(), marker
}

func TestIdentityIncidentRelayDeclarationContainsBeforeEOSEAndAbortWaitsForCatchup(t *testing.T) {
	watcher, admission, _, _, root, old, id, _ := incidentFixture(t)
	pool := watcher.pool.(*delegationFakePool)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watcher.subscribe(ctx) }()
	filter := <-pool.filters
	require.Equal(t, []nostr.Kind{nostr.Kind(kinds.CASControlState)}, filter.Kinds)
	require.Equal(t, []nostr.PubKey{root.Public()}, filter.Authors)
	require.Equal(t, []string{kinds.CPStateTopicIdentityIncident}, filter.Tags["t"])
	require.Empty(t, filter.Tags["old"], "multi-letter old tag is verified locally, not relay-indexed")
	start := incidentEvent(t, root, old.Public(), id, "declare", false, nostr.Now()-2)
	changed := admission.RuntimeKillSwitchSignal()
	pool.events <- &start
	<-changed
	require.True(t, admission.State().KillSwitchActive, "verified EVENT must contain before EOSE")
	abort := incidentEvent(t, root, old.Public(), id, "abort", true, nostr.Now()-1)
	require.NoError(t, watcher.Apply(&abort))
	require.True(t, admission.State().KillSwitchActive, "abort cannot lift before EOSE")
	released := admission.RuntimeKillSwitchSignal()
	pool.eose <- nostradapter.RelayEOSE{RelayURL: pool.urls[0]}
	close(pool.done)
	<-released
	require.True(t, watcher.CaughtUp())
	require.False(t, admission.State().KillSwitchActive)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestIdentityIncidentRestartUsesDurableHeadAndMarker(t *testing.T) {
	watcher, admission, store, outbox, root, old, id, marker := incidentFixture(t)
	start := incidentEvent(t, root, old.Public(), id, "contain", false, nostr.Now()-1)
	require.NoError(t, watcher.Apply(&start))
	require.True(t, admission.State().KillSwitchActive)
	// Reopen both bbolt files to prove the head and marker survive a restart.
	require.NoError(t, outbox.Close())
	require.NoError(t, store.Close())
	var err error
	store, err = localstore.Open(filepath.Join(filepath.Dir(marker), "events.bolt"))
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	outbox, err = localstore.OpenOutbox(filepath.Join(filepath.Dir(marker), "outbox.bolt"))
	require.NoError(t, err)
	defer func() { require.NoError(t, outbox.Close()) }()
	restarted := nostrout.New(nostrout.Config{})
	require.NoError(t, restarted.ConfigureRuntimeKillSwitch(marker))
	require.True(t, restarted.State().KillSwitchActive)
	fresh, err := NewIdentityIncidents(watcher.pool, store, outbox, restarted, []string{root.Public().Hex()}, old.Public().Hex(), nil)
	require.NoError(t, err)
	require.NoError(t, fresh.Load())
	require.True(t, restarted.State().KillSwitchActive)
	active, gotID := restarted.RuntimeKillSwitch()
	require.True(t, active)
	require.Equal(t, id, gotID)
}

func TestIdentityIncidentRejectsWrongAuthorOldAndMalformedRecords(t *testing.T) {
	watcher, admission, _, _, root, old, id, _ := incidentFixture(t)
	other := nostr.Generate()
	base := nostr.Now() - 1
	wrongAuthor := incidentEvent(t, other, old.Public(), id, "declare", false, base)
	require.Error(t, watcher.Apply(&wrongAuthor))
	wrongOld := incidentEvent(t, root, other.Public(), id, "declare", false, base)
	require.Error(t, watcher.Apply(&wrongOld))
	malformed := incidentEvent(t, root, old.Public(), id, "declare", false, base)
	malformed.Tags = append(malformed.Tags, nostr.Tag{"schema", "bad"})
	require.NoError(t, malformed.Sign(root))
	require.Error(t, watcher.Apply(&malformed))
	badSignature := incidentEvent(t, root, old.Public(), id, "declare", false, base)
	badSignature.Sig[0] ^= 1
	require.Error(t, watcher.Apply(&badSignature))
	require.False(t, admission.State().KillSwitchActive)
}

func TestIdentityIncidentCutoverCannotBeAborted(t *testing.T) {
	watcher, admission, _, _, root, old, id, _ := incidentFixture(t)
	cut := incidentEvent(t, root, old.Public(), id, "cut-activate-trust", false, nostr.Now()-2)
	require.NoError(t, watcher.Apply(&cut))
	abort := incidentEvent(t, root, old.Public(), id, "abort", true, nostr.Now()-1)
	require.Error(t, watcher.Apply(&abort))
	require.True(t, admission.State().KillSwitchActive)
}

func TestIdentityIncidentMalformedDurableHeadFailsClosed(t *testing.T) {
	watcher, admission, _, outbox, _, _, id, _ := incidentFixture(t)
	require.NoError(t, outbox.PutControlRecord(identityIncidentRecordFamily, "identity-incident:"+id, []byte("not-json")))
	require.Error(t, watcher.Load())
	require.True(t, admission.State().KillSwitchActive)
}

func TestIdentityIncidentDurableCoordinateMismatchFailsClosed(t *testing.T) {
	watcher, admission, _, outbox, root, old, id, _ := incidentFixture(t)
	ev := incidentEvent(t, root, old.Public(), id, "declare", false, nostr.Now()-1)
	raw, err := json.Marshal(identityIncidentHead{Event: ev})
	require.NoError(t, err)
	require.NoError(t, outbox.PutControlRecord(identityIncidentRecordFamily, "identity-incident:wrong", raw))
	require.ErrorContains(t, watcher.Load(), "coordinate")
	require.True(t, admission.State().KillSwitchActive)
}

func TestIdentityIncidentAbortDoesNotReleaseAnotherActiveIncident(t *testing.T) {
	watcher, admission, _, _, root, old, firstID, _ := incidentFixture(t)
	second, err := uuid.NewV7()
	require.NoError(t, err)
	secondID := second.String()
	base := nostr.Now() - 4
	first := incidentEvent(t, root, old.Public(), firstID, "declare", false, base)
	secondEvent := incidentEvent(t, root, old.Public(), secondID, "declare", false, base+1)
	require.NoError(t, watcher.Apply(&first))
	require.NoError(t, watcher.Apply(&secondEvent))
	watcher.mu.Lock()
	watcher.caughtUp = true
	watcher.mu.Unlock()
	abortSecond := incidentEvent(t, root, old.Public(), secondID, "abort", true, base+2)
	require.NoError(t, watcher.Apply(&abortSecond))
	active, id := admission.RuntimeKillSwitch()
	require.True(t, active)
	require.Equal(t, firstID, id)
	abortFirst := incidentEvent(t, root, old.Public(), firstID, "abort", true, base+3)
	require.NoError(t, watcher.Apply(&abortFirst))
	require.False(t, admission.State().KillSwitchActive)
}
