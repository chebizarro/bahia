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

func TestBackupRunAdmissionRequiresCrossProcessFence(t *testing.T) {
	dir := t.TempDir()
	events, err := localstore.Open(filepath.Join(dir, "events.db"))
	require.NoError(t, err)
	defer events.Close()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	serviceKey := nostr.Generate()
	projector := withTestServiceKey(&Projector{enabled: true, logger: zap.NewNop()}, serviceKey.Hex())
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
	require.Empty(t, stateID)
	require.ErrorContains(t, err, "cross-process service-key signer fence unavailable")
	_, found, _, err := canonical.LookupRunAdmission(t.Context(), "intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, found, "no service-signed state may exist without a writer fence")
}
