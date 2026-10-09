package nostr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func localRepoRecord(t *testing.T, sk gonostr.SecretKey, kind gonostr.Kind, createdAt gonostr.Timestamp, tags gonostr.Tags) (*repository.NostrEventRecord, gonostr.Event) {
	t.Helper()
	ev := syncTestEvent(t, sk, kind, createdAt, tags, "{}")
	tagsJSON, err := json.Marshal(ev.Tags)
	require.NoError(t, err)
	return &repository.NostrEventRecord{
		ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON,
		Sig: eventSignatureHex(&ev), CreatedAt: ev.CreatedAt.Time(),
	}, ev
}

type admittedEvent struct {
	id, target, entityType string
}

// without PostgreSQL the nostr_events readers and writers use the local
// event store; it persists across a reopen (the old in-memory map did not).
func TestLocalEventRepositoryRecordsReadsAndPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "daemon.bolt")
	store := openTestLocalStore(t, path)
	repo := NewLocalEventRepository(store, nil)
	self, other := gonostr.Generate(), gonostr.Generate()
	now := gonostr.Now()

	audit, auditEv := localRepoRecord(t, self, KindCASAudit, now-30, gonostr.Tags{{"t", "deployment.run.health"}, {"legacy_kind", "31900"}})
	inserted, err := repo.Record(ctx, audit)
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = repo.Record(ctx, audit)
	require.NoError(t, err)
	require.False(t, inserted, "recording is idempotent by event id")
	foreign, _ := localRepoRecord(t, other, KindCASAudit, now-10, gonostr.Tags{{"t", "deployment.run.health"}})
	_, err = repo.Record(ctx, foreign)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, audit.ID)
	require.NoError(t, err)
	require.Equal(t, audit.ID, got.ID)
	byTag, err := repo.FindByTag(ctx, "t", "deployment.run.health", []int{KindCASAudit}, 10)
	require.NoError(t, err)
	require.Equal(t, []string{foreign.ID, audit.ID}, []string{byTag[0].ID, byTag[1].ID}, "newest first")
	byLongTag, err := repo.FindByTag(ctx, "legacy_kind", "31900", []int{KindCASAudit}, 10)
	require.NoError(t, err)
	require.Len(t, byLongTag, 1, "multi-letter tags match too")

	own, err := repo.Authored(self.Public().Hex()).ListByKind(ctx, KindCASAudit, 10)
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.Equal(t, audit.ID, own[0].ID, "an author-scoped view sees only that author")

	tampered := *audit
	tampered.Content = `{"tampered":true}`
	_, err = repo.Record(ctx, &tampered)
	require.Error(t, err, "only verifiable signed events are stored")

	require.NoError(t, store.Close())
	reopened := NewLocalEventRepository(openTestLocalStore(t, path), nil)
	latest, err := reopened.LatestCreatedAtForKindsAndAuthors(ctx, []int{KindCASAudit}, []string{self.Public().Hex()})
	require.NoError(t, err)
	require.Equal(t, auditEv.CreatedAt.Time(), *latest)
}

// A row recorded as pending delivery for a drained publish target is handed to
// that target's outbox; an archive row of the local outbox is only stored,
// because its producer publishes it through a Publisher itself.
func TestLocalEventRepositoryAdmitsPendingRowsToTheOutbox(t *testing.T) {
	ctx := context.Background()
	var admitted []admittedEvent
	repo := NewLocalEventRepository(openTestLocalStore(t, ""), func(_ context.Context, ev gonostr.Event, target, entityType string, _ *uuid.UUID) error {
		admitted = append(admitted, admittedEvent{id: ev.ID.Hex(), target: target, entityType: entityType})
		return nil
	})
	sk := gonostr.Generate()

	transactional, _ := localRepoRecord(t, sk, KindCASAudit, gonostr.Now(), nil)
	transactional.PublishState = repository.NostrPublishStatePending
	transactional.EntityType = "release_promotion"
	_, err := repo.Record(ctx, transactional)
	require.NoError(t, err)

	archive, _ := localRepoRecord(t, sk, KindCASAudit, gonostr.Now()-1, nil)
	archive.PublishState = repository.NostrPublishStatePending
	archive.PublishTarget = repository.LocalOutboxArchiveTarget(repository.NostrPublishTargetControlPlane)
	_, err = repo.Record(ctx, archive)
	require.NoError(t, err)

	require.Equal(t, []admittedEvent{{id: transactional.ID, target: repository.NostrPublishTargetDefault, entityType: "release_promotion"}}, admitted)

	unrouted := NewLocalEventRepository(openTestLocalStore(t, ""), nil)
	pending, _ := localRepoRecord(t, sk, KindCASAudit, gonostr.Now()-2, nil)
	pending.PublishState = repository.NostrPublishStatePending
	_, err = unrouted.Record(ctx, pending)
	require.ErrorContains(t, err, "no outbox", "a pending row is never silently left undelivered")
}

func TestLocalEventRepositoryLatestByCoordinateIsAuthorScopedAndReportsAbandonment(t *testing.T) {
	ctx := context.Background()
	store := openTestLocalStore(t, filepath.Join(t.TempDir(), "daemon.bolt"))
	repo := NewLocalEventRepository(store, nil)
	self, foreign := gonostr.Generate(), gonostr.Generate()
	d := "artifact:sbom-package:v2:coordinate-test"
	now := gonostr.Now()
	own, ownEvent := localRepoRecord(t, self, KindCASControlState, now-1, gonostr.Tags{{"d", d}, {"legacy_kind", strconv.Itoa(KindSBOMPackageRegistry)}})
	other, _ := localRepoRecord(t, foreign, KindCASControlState, now, gonostr.Tags{{"d", d}, {"legacy_kind", strconv.Itoa(KindSBOMPackageRegistry)}})
	_, err := repo.Record(ctx, own)
	require.NoError(t, err)
	_, err = repo.Record(ctx, other)
	require.NoError(t, err)

	scoped := repo.Authored(self.Public().Hex())
	got, err := scoped.LatestByCoordinate(ctx, KindCASControlState, d)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, own.ID, got.ID)
	absent, err := scoped.LatestByCoordinate(ctx, KindCASControlState, d+":absent")
	require.NoError(t, err)
	require.Nil(t, absent)

	_, err = store.MarkUndelivered(ownEvent, "blocked: refused", time.Now())
	require.NoError(t, err)
	got, err = scoped.LatestByCoordinate(ctx, KindCASControlState, d)
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStateFailed, got.PublishState)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = scoped.LatestByCoordinate(cancelled, KindCASControlState, d)
	require.ErrorIs(t, err, context.Canceled)
}
