package controlplane

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestBackupCanonicalOwnershipIsImmutableAndDBLess(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	serviceKey := nostr.Generate()
	owner := uuid.New()
	repo := domain.BackupRepository{ID: uuid.New(), OrgID: owner, Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/archive", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	tags, content := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	ev := configSnapshotEvent(t, serviceKey, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), tags, content)
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	reader, err := NewLocalBackupOwnershipReader(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	got, found, err := reader.OwnerOf(t.Context(), kinds.BackupRepositoryRegistry, repo.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, owner, got)
	actor := &Intent{OrgID: owner}
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{OwnerReader: reader})
	require.NoError(t, handler.requireOwner(t.Context(), actor, kinds.BackupRepositoryRegistry, repo.ID, owner))
	require.ErrorContains(t, handler.requireOwner(t.Context(), &Intent{OrgID: uuid.New()}, kinds.BackupRepositoryRegistry, repo.ID, uuid.New()), "owner must match")
	foreign := uuid.New()
	require.ErrorContains(t, handler.requireOwner(t.Context(), &Intent{OrgID: foreign}, kinds.BackupRepositoryRegistry, repo.ID, foreign), "ownership is immutable")
	require.ErrorContains(t, handler.requireReferenceOwner(t.Context(), kinds.BackupRepositoryRegistry, repo.ID, foreign), "no canonical state owned")
	registry := newFakeBackupIntentRegistry()
	publisher := &fakeBackupIntentPublisher{}
	handler = NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, Publisher: publisher, OwnerReader: reader, ConfigReader: reader.(BackupConfigReader), ConfigIndex: registry})
	intent := &Intent{Op: "repository-register", OrgID: foreign, Content: map[string]any{"id": repo.ID.String(), "org_id": foreign.String(), "name": "archive", "backend": "kopia", "repository_uri": "file:/archive"}}
	require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "ownership is immutable")
	require.Empty(t, registry.repositories, "SQL index cannot confer cross-org authority")
	require.Zero(t, publisher.repositoryCount)
	registry.repositories[repo.ID] = &repo
	deleteIntent := &Intent{Op: "delete", OrgID: foreign, Content: map[string]any{"entity_type": "repository", "entity_id": repo.ID.String()}}
	require.ErrorContains(t, handler.HandleIntent(t.Context(), deleteIntent), "no canonical state owned")
	require.Zero(t, publisher.deletedCount)
}

func TestBackupOwnerlessLegacyRecordRequiresCanonicalRepublish(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	serviceKey := nostr.Generate()
	repo := domain.BackupRepository{ID: uuid.New(), Name: "legacy", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/archive"}
	tags, content := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	ev := configSnapshotEvent(t, serviceKey, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), tags, content)
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	reader, err := NewLocalBackupOwnershipReader(store, serviceKey.Public().Hex())
	require.NoError(t, err)
	_, found, err := reader.OwnerOf(t.Context(), kinds.BackupRepositoryRegistry, repo.ID)
	require.True(t, found)
	require.ErrorContains(t, err, "operator must republish owner-attested canonical state")
}

func TestBackupCanonicalOwnerRejectsMalformedHead(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	key := nostr.Generate()
	repo := domain.BackupRepository{ID: uuid.New(), OrgID: uuid.New(), Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/archive"}
	tags, content := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	event := configSnapshotEvent(t, key, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), tags, content)
	validTags := event.Tags
	event.Tags = nil
	for _, tag := range validTags {
		if tag[0] != "deleted" {
			event.Tags = append(event.Tags, tag)
		}
	}
	require.NoError(t, event.Sign(key))
	_, err = store.SaveEvent(event)
	require.NoError(t, err)
	reader, err := NewLocalBackupOwnershipReader(store, key.Public().Hex())
	require.NoError(t, err)
	_, found, err := reader.OwnerOf(t.Context(), kinds.BackupRepositoryRegistry, repo.ID)
	require.True(t, found)
	require.ErrorContains(t, err, "invalid canonical envelope")
}

func TestBackupConfigApplyAndReplayWithoutPostgres(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	key := nostr.Generate()
	orgID := uuid.New()
	repo := domain.BackupRepository{ID: uuid.New(), OrgID: orgID, Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/archive"}
	tags, content := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	event := configSnapshotEvent(t, key, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), tags, content)
	_, err = store.SaveEvent(event)
	require.NoError(t, err)
	owner, err := NewLocalBackupOwnershipReader(store, key.Public().Hex())
	require.NoError(t, err)
	publisher := &fakeBackupIntentPublisher{}
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{SQLRepositoryUnavailable: true, OwnerReader: owner, ConfigReader: owner.(BackupConfigReader), Publisher: publisher})
	recipeID := uuid.New()
	intent := &Intent{Op: "recipe-apply", OrgID: orgID, Content: map[string]any{
		"id": recipeID.String(), "org_id": orgID.String(), "name": "daily", "version": "v1",
		"backend": "kopia", "repository_id": repo.ID.String(), "target_ref": "fs:/data",
	}}
	require.NoError(t, handler.HandleIntent(t.Context(), intent), "configuration must not need SQL")
	require.Equal(t, 1, publisher.recipeCount)
	index := newFakeBackupIntentRegistry()
	staleID := uuid.New()
	index.repositories[staleID] = &domain.BackupRepository{ID: staleID, Name: "SQL only"}
	require.NoError(t, owner.(BackupConfigRebuilder).RebuildConfigIndex(t.Context(), index))
	require.Equal(t, orgID, index.repositories[repo.ID].OrgID)
	require.True(t, index.deleted[staleID], "SQL-only row must not survive canonical replay")
	require.False(t, index.deleted[repo.ID])
	// A later signed tombstone wins and cannot be used as a reference.
	tags, content = nostradapter.BackupRepositoryRegistryRecord(&repo, true)
	tombstone := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: event.CreatedAt + 1,
		Tags: append(nostr.Tags{{"d", nostradapter.BackupRepositoryDTag(repo.ID)}, {"t", kinds.CPStateTopicBackupRepository},
			{"domain", "backup"}, {"schema", kinds.CASControlStateSchema}, {"legacy_kind", strconv.Itoa(kinds.BackupRepositoryRegistry)}, {"deleted", "true"}}, tags...), Content: content}
	require.NoError(t, tombstone.Sign(key))
	_, err = store.SaveEvent(tombstone)
	require.NoError(t, err)
	require.ErrorIs(t, handler.HandleIntent(t.Context(), intent), ErrBackupConfigDeleted)
	require.NoError(t, owner.(BackupConfigRebuilder).RebuildConfigIndex(t.Context(), index))
	require.True(t, index.deleted[repo.ID])
}
