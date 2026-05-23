package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var workerRowColumns = []string{
	"pubkey", "name", "description", "architecture", "max_concurrent_jobs", "current_queue_depth",
	"software", "pricing", "resources", "accelerators", "ml_capabilities", "capabilities", "runtime_target",
	"min_duration_secs", "max_duration_secs", "geohash", "preferred_relays", "last_advertisement_at",
	"status", "scheduling_state", "scheduling_note", "labels", "created_at", "updated_at",
}

func TestPgWorkerRepository_GetByPubKeyScansRuntimeCapabilities(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectQuery("FROM workers WHERE pubkey = \\$1").
		WithArgs("worker-pubkey").
		WillReturnRows(pgxmock.NewRows(workerRowColumns).
			AddRow("worker-pubkey", "gpu-worker", "", "linux/amd64", 2, 1, []byte(`[]`), []byte(`[{"mint_url":"mint","price_per_second":1,"unit":"sat"}]`), []byte(`{"cpu_cores":32,"memory_gb":256,"disk_gb":1000}`), []byte(`[{"vendor":"nvidia","model":"L40S","count":1,"memory_gb":48,"driver":"cuda"}]`), []byte(`{"runtimes":["vllm"],"artifact_formats":["safetensors"],"tasks":["chat_completions"],"accelerators":["gpu_nvidia_cuda"]}`), []byte(`{"workload_kinds":["inference"],"runtimes":["vllm"],"artifact_formats":["safetensors"],"accelerators":["gpu_nvidia_cuda"],"toolchains":["cuda"],"features":["streaming"]}`), []byte(`{"type":"compose","endpoint_ref":"gpu-a","compose_dir":"/srv/llm","public_base_url":"http://gpu-a"}`), 0, 0, "", []byte(`["wss://relay.example"]`), now, "online", "cordoned", "maintenance window", []byte(`{"role":"inference","track":"canary"}`), now, now))

	worker, err := repo.GetByPubKey(ctx, "worker-pubkey")
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.Equal(t, 256, worker.Resources.MemoryGB)
	require.Len(t, worker.Accelerators, 1)
	require.Equal(t, "L40S", worker.Accelerators[0].Model)
	require.Equal(t, []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM}, worker.MLCapabilities.Runtimes)
	require.Equal(t, []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors}, worker.MLCapabilities.ArtifactFormats)
	require.Equal(t, []string{"inference"}, worker.Capabilities.WorkloadKinds)
	require.Equal(t, []string{"vllm"}, worker.Capabilities.Runtimes)
	require.Equal(t, []string{"streaming"}, worker.Capabilities.Features)
	require.Equal(t, domain.RuntimeTypeCompose, worker.RuntimeTarget.Type)
	require.Equal(t, "gpu-a", worker.RuntimeTarget.EndpointRef)
	require.Equal(t, domain.WorkerSchedulingCordoned, worker.SchedulingState)
	require.Equal(t, "maintenance window", worker.SchedulingNote)
	require.Equal(t, map[string]string{"role": "inference", "track": "canary"}, worker.Labels)
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
		WillReturnRows(pgxmock.NewRows(workerRowColumns).
			AddRow("legacy-worker", "legacy", "", "linux/amd64", 1, 0, []byte(`[]`), []byte(`[]`), []byte(`{}`), []byte(`[]`), []byte(`{}`), []byte(`{}`), []byte(`{}`), 0, 0, "", []byte(`[]`), now, "online", "", "", []byte(`{}`), now, now))

	worker, err := repo.GetByPubKey(ctx, "legacy-worker")
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.Nil(t, worker.Resources)
	require.Empty(t, worker.Accelerators)
	require.Empty(t, worker.Capabilities)
	require.Nil(t, worker.RuntimeTarget)
	require.Equal(t, domain.WorkerSchedulingActive, worker.SchedulingState)
	require.Empty(t, worker.Labels)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgWorkerRepository_ListByLabelsUsesJSONBContainment(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectQuery("WHERE labels @> \\$1::jsonb").
		WithArgs([]byte(`{"role":"inference"}`), 50).
		WillReturnRows(pgxmock.NewRows(workerRowColumns).
			AddRow("worker-pubkey", "gpu-worker", "", "linux/amd64", 2, 1, []byte(`[]`), []byte(`[]`), []byte(`{}`), []byte(`[]`), []byte(`{}`), []byte(`{"workload_kinds":["inference"]}`), []byte(`{}`), 0, 0, "", []byte(`[]`), now, "online", "active", "", []byte(`{"role":"inference"}`), now, now))

	workers, err := repo.ListByLabels(ctx, map[string]string{"role": "inference"}, 50)
	require.NoError(t, err)
	require.Len(t, workers, 1)
	require.Equal(t, "worker-pubkey", workers[0].PubKey)
	require.Equal(t, map[string]string{"role": "inference"}, workers[0].Labels)
	require.Equal(t, []string{"inference"}, workers[0].Capabilities.WorkloadKinds)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgWorkerRepository_UpsertWritesSchedulingLabelsAndCapabilities(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectExec("INSERT INTO workers").
		WithArgs(
			"worker-pubkey", "gpu-worker", "shared worker", "linux/amd64",
			2, 1,
			[]byte(`null`), []byte(`null`), []byte(`null`), []byte(`null`), []byte(`{}`), []byte(`{"workload_kinds":["inference"],"runtimes":["vllm"]}`), []byte(`null`),
			0, 0, "", []byte(`null`),
			now, "online", "draining", "operator requested drain", []byte(`{"role":"inference"}`), true, true,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = repo.Upsert(ctx, &domain.Worker{
		PubKey:              "worker-pubkey",
		Name:                "gpu-worker",
		Description:         "shared worker",
		Architecture:        "linux/amd64",
		MaxConcurrentJobs:   2,
		CurrentQueueDepth:   1,
		Capabilities:        domain.WorkerCapabilities{WorkloadKinds: []string{"inference"}, Runtimes: []string{"vllm"}},
		LastAdvertisementAt: now,
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingDraining,
		SchedulingNote:      "operator requested drain",
		Labels:              map[string]string{"role": "inference"},
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgWorkerRepository_UpsertPreservesSchedulingAndLabelsWhenOmitted(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	now := time.Now().UTC()
	mock.ExpectExec("scheduling_state = CASE WHEN \\$23 THEN EXCLUDED.scheduling_state ELSE workers.scheduling_state END").
		WithArgs(
			"worker-pubkey", "worker", "", "",
			0, 0,
			[]byte(`null`), []byte(`null`), []byte(`null`), []byte(`null`), []byte(`{}`), []byte(`{}`), []byte(`null`),
			0, 0, "", []byte(`null`),
			now, "online", "active", "", []byte(`{}`), false, false,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = repo.Upsert(ctx, &domain.Worker{
		PubKey:              "worker-pubkey",
		Name:                "worker",
		LastAdvertisementAt: now,
		Status:              domain.WorkerStatusOnline,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
