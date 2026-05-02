package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgWorkerRepository_GetByPubKeyScansRuntimeCapabilities(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectQuery("FROM workers WHERE pubkey = \\$1").
		WithArgs("worker-pubkey").
		WillReturnRows(pgxmock.NewRows([]string{"pubkey", "name", "description", "architecture", "max_concurrent_jobs", "current_queue_depth", "software", "pricing", "resources", "accelerators", "runtime_target", "min_duration_secs", "max_duration_secs", "geohash", "preferred_relays", "last_advertisement_at", "status", "created_at", "updated_at"}).
			AddRow("worker-pubkey", "gpu-worker", "", "linux/amd64", 2, 1, []byte(`[]`), []byte(`[{"mint_url":"mint","price_per_second":1,"unit":"sat"}]`), []byte(`{"cpu_cores":32,"memory_gb":256,"disk_gb":1000}`), []byte(`[{"vendor":"nvidia","model":"L40S","count":1,"memory_gb":48,"driver":"cuda"}]`), []byte(`{"type":"compose","endpoint_ref":"gpu-a","compose_dir":"/srv/llm","public_base_url":"http://gpu-a"}`), 0, 0, "", []byte(`["wss://relay.example"]`), now, "online", now, now))

	worker, err := repo.GetByPubKey(ctx, "worker-pubkey")
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.Equal(t, 256, worker.Resources.MemoryGB)
	require.Len(t, worker.Accelerators, 1)
	require.Equal(t, "L40S", worker.Accelerators[0].Model)
	require.Equal(t, domain.RuntimeTypeCompose, worker.RuntimeTarget.Type)
	require.Equal(t, "gpu-a", worker.RuntimeTarget.EndpointRef)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgWorkerRepository_GetByPubKeyNormalizesEmptyCapabilityDefaults(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectQuery("FROM workers WHERE pubkey = \\$1").
		WithArgs("legacy-worker").
		WillReturnRows(pgxmock.NewRows([]string{"pubkey", "name", "description", "architecture", "max_concurrent_jobs", "current_queue_depth", "software", "pricing", "resources", "accelerators", "runtime_target", "min_duration_secs", "max_duration_secs", "geohash", "preferred_relays", "last_advertisement_at", "status", "created_at", "updated_at"}).
			AddRow("legacy-worker", "legacy", "", "linux/amd64", 1, 0, []byte(`[]`), []byte(`[]`), []byte(`{}`), []byte(`[]`), []byte(`{}`), 0, 0, "", []byte(`[]`), now, "online", now, now))

	worker, err := repo.GetByPubKey(ctx, "legacy-worker")
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.Nil(t, worker.Resources)
	require.Empty(t, worker.Accelerators)
	require.Nil(t, worker.RuntimeTarget)
	require.NoError(t, mock.ExpectationsWereMet())
}
