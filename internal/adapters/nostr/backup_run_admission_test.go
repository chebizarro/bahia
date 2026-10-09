package nostr

import (
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupRunAdmissionStagesCanonicalStateWithoutPublishingInline(t *testing.T) {
	dir := t.TempDir()
	events, err := localstore.Open(filepath.Join(dir, "events.db"))
	require.NoError(t, err)
	defer events.Close()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	serviceKey := nostr.Generate()
	projector := &Projector{enabled: true, privateKey: serviceKey.Hex(), logger: zap.NewNop()}
	publisher := &Publisher{localOutbox: outbox, ownEvents: events, target: repository.NostrPublishTargetControlPlane,
		now: time.Now, logger: zap.NewNop(), wake: make(chan struct{}, 1)}
	canonical := NewBackupCanonicalPublisher(projector, zap.NewNop())
	canonical.SetRunAdmissionPublisher(publisher)
	runID, err := uuid.NewV7()
	require.NoError(t, err)
	requestID := nostr.Generate().Public().Hex()
	coordinate := BackupRunDTag(runID)
	now := time.Now().UTC()
	run := &domain.BackupRun{ID: runID, RecipeID: uuid.New(), RepositoryID: uuid.New(), RequestedBy: nostr.Generate().Public().Hex(),
		RequestEventID: requestID, RequestKind: kinds.CASControlState, RequestDTag: coordinate, Status: domain.RunStatusQueued,
		Backend: domain.BackupBackendKopia, TargetRef: "/data", VerificationMode: domain.BackupVerificationNone,
		VerificationStatus: domain.BackupVerificationPending, CreatedAt: now, UpdatedAt: now}
	stateID, err := canonical.StageRunAdmission(t.Context(), "intent-1", requestID, run)
	require.NoError(t, err)
	id, err := nostr.IDFromHex(stateID)
	require.NoError(t, err)
	entry, found, err := outbox.Get(id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Equal(t, repository.NostrPublishTargetControlPlane, entry.Target)
	require.Equal(t, nostr.Kind(kinds.CASControlState), entry.Event.Kind)
	require.Equal(t, serviceKey.Public(), entry.Event.PubKey)
	require.True(t, entry.Event.CheckID())
	require.True(t, entry.Event.VerifySignature())
	require.Equal(t, coordinate, tagValue(entry.Event.Tags, "d"))
	require.Equal(t, kinds.CPStateTopicBackupRun, tagValue(entry.Event.Tags, "t"))
	require.Equal(t, "31996", tagValue(entry.Event.Tags, "legacy_kind"))
	require.Contains(t, entry.Event.Content, requestID)
	lookupID, found, delivered, err := canonical.LookupRunAdmission(t.Context(), "intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, delivered)
	require.Equal(t, stateID, lookupID)
	replayID, err := canonical.StageRunAdmission(t.Context(), "intent-1", requestID, run)
	require.NoError(t, err)
	require.Equal(t, stateID, replayID, "re-signing on replay cannot create a second canonical event")
	_, err = canonical.StageRunAdmission(t.Context(), "intent-2", requestID, run)
	require.ErrorContains(t, err, "coordinate already belongs")
}
