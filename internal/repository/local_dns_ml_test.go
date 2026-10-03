package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLocalDNSMLRecordsSurviveReopenAndDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbox.db")
	store, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	zones := NewLocalDNSZoneRepository(store)
	backends := NewLocalDNSBackendRepository(store)
	models := NewLocalMLRegistryRepository(store, nil)
	zone := &domain.DNSZone{Name: "example.test", Visibility: domain.ZoneVisibilityInternal, BackendRef: "primary", TTL: 60}
	backend := &domain.DNSBackendState{Ref: "primary", Type: domain.DNSBackendTypeCoreDNS}
	model := &domain.MLModel{ID: uuid.New(), Slug: "sample", Name: "Sample"}
	require.NoError(t, zones.Create(ctx, zone))
	require.NoError(t, backends.SeedConfigured(ctx, backend))
	require.NoError(t, models.UpsertModel(ctx, model))
	require.False(t, zone.UpdatedAt.IsZero())
	require.False(t, model.UpdatedAt.IsZero())
	require.NoError(t, store.Close())

	store, err = localstore.OpenOutbox(path)
	require.NoError(t, err)
	zones = NewLocalDNSZoneRepository(store)
	backends = NewLocalDNSBackendRepository(store)
	models = NewLocalMLRegistryRepository(store, nil)
	loadedZone, err := zones.Get(ctx, zone.Name)
	require.NoError(t, err)
	require.Equal(t, zone, loadedZone)
	loadedBackend, err := backends.Get(ctx, backend.Ref)
	require.NoError(t, err)
	require.Equal(t, backend, loadedBackend)
	loadedModel, err := models.GetModel(ctx, model.ID)
	require.NoError(t, err)
	require.Equal(t, model, loadedModel)
	require.NoError(t, zones.Delete(ctx, zone.Name))
	require.NoError(t, backends.Delete(ctx, backend.Ref))
	require.NoError(t, models.DeleteModel(ctx, model.ID))
	require.NoError(t, store.Close())

	store, err = localstore.OpenOutbox(path)
	require.NoError(t, err)
	defer store.Close()
	loadedZone, err = NewLocalDNSZoneRepository(store).Get(ctx, zone.Name)
	require.NoError(t, err)
	require.Nil(t, loadedZone)
	require.NoError(t, NewLocalDNSZoneRepository(store).SeedConfigured(ctx, zone))
	loadedZone, err = NewLocalDNSZoneRepository(store).Get(ctx, zone.Name)
	require.NoError(t, err)
	require.Nil(t, loadedZone, "configured zone deletion must survive a daemon restart")
	require.NoError(t, NewLocalDNSBackendRepository(store).SeedConfigured(ctx, backend))
	loadedBackend, err = NewLocalDNSBackendRepository(store).Get(ctx, backend.Ref)
	require.NoError(t, err)
	require.Nil(t, loadedBackend, "configured backend deletion must survive a daemon restart")
	loadedModel, err = NewLocalMLRegistryRepository(store, nil).GetModel(ctx, model.ID)
	require.NoError(t, err)
	require.Nil(t, loadedModel)
}
