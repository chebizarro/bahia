package localstore

import (
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestBackupRunAdmissionCommitsEventAndIdentityAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := OpenOutbox(path)
	require.NoError(t, err)
	runID, err := uuid.NewV7()
	require.NoError(t, err)
	coordinate := "backup-run:" + runID.String()
	request := nostr.Generate()
	requestID := request.Public().Hex()
	key := nostr.Generate()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}, Content: `{"deleted":false}`}
	require.NoError(t, event.Sign(key))
	entry := OutboxEntry{Event: event, Target: "control-plane"}
	admission, inserted, err := outbox.EnqueueBackupRun(entry, "intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, event.ID.Hex(), admission.StateEventID)
	stored, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, OutboxPending, stored.State)

	// A concurrent retry may sign a different event, but the same request
	// still owns exactly the original staged state.
	second := event
	second.CreatedAt++
	require.NoError(t, second.Sign(key))
	admission, inserted, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, event.ID.Hex(), admission.StateEventID)
	_, found, err = outbox.Get(second.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-1", coordinate, nostr.Generate().Public().Hex())
	require.ErrorContains(t, err, "conflicts")
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-2", coordinate, requestID)
	require.ErrorContains(t, err, "coordinate already belongs")

	_, err = outbox.CommitRound(event.ID, OutboxRound{Rounds: 1, Delivered: false, State: OutboxFailed,
		Relays: map[string]RelayDelivery{"wss://relay.example": {Rejected: "blocked"}}})
	require.NoError(t, err)
	prior, err := outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, prior.Delivered, "relay refusal cannot accept a staged run")
	_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, found, err = outbox.Get(event.ID)
	require.NoError(t, err)
	require.False(t, found)
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), prior.StateEventID, "delivery pruning must not free the run identity")
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	defer outbox.Close()
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), prior.StateEventID, "restart must preserve exact request binding")
	require.False(t, prior.Delivered)
}

func TestBackupRunAdmissionRequiresQuorumAndRetainsACKAfterPrune(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	key := nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	requestID := nostr.Generate().Public().Hex()
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}}
	require.NoError(t, event.Sign(key))
	_, inserted, err := outbox.EnqueueBackupRun(OutboxEntry{Event: event, Target: "control-plane"}, "intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, inserted)
	_, err = outbox.CommitRound(event.ID, OutboxRound{Rounds: 1, Delivered: false, State: OutboxPending,
		Relays: map[string]RelayDelivery{"wss://a.example": {Accepted: true}}})
	require.NoError(t, err)
	prior, err := outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, prior.Delivered, "one relay OK below the configured quorum is not acceptance")
	_, err = outbox.CommitRound(event.ID, OutboxRound{Rounds: 2, Delivered: true, State: OutboxPending,
		Relays: map[string]RelayDelivery{"wss://b.example": {Accepted: true}}})
	require.NoError(t, err)
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, prior.Delivered, "the persisted quorum marker and accepted relay together admit the run")
	_, err = outbox.CommitRound(event.ID, OutboxRound{Rounds: 3, Delivered: true, State: OutboxPublished})
	require.NoError(t, err)
	_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, prior.Delivered)
}

func TestBackupRunAdmissionRejectsWrongEnvelopeWithoutMutation(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	key := nostr.Generate()
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "backup-run:bad"}, {"t", "other"}, {"legacy_kind", "31996"}, {"deleted", "false"}}}
	require.NoError(t, event.Sign(key))
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: event, Target: "control-plane"}, "intent-1", "backup-run:bad", nostr.Generate().Public().Hex())
	require.ErrorContains(t, err, "canonical run-state")
	prior, err := outbox.GetBackupRunAdmission("intent-1", "backup-run:bad", nostr.Generate().Public().Hex())
	require.NoError(t, err)
	require.Nil(t, prior)
	_, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.False(t, found)
}
