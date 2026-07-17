package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgWorkerRepositoryUpsertReportsRejectedStaleAdvertisement(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgWorkerRepository{pool: mock}
	worker := staleGuardWorker("worker-pubkey", "stale", time.Now().UTC(), 10)
	args := make([]any, 28)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO workers").WithArgs(args...).WillReturnResult(pgxmock.NewResult("INSERT", 0))

	err = repo.Upsert(context.Background(), &worker)
	require.ErrorIs(t, err, ErrStaleWrite)
	require.True(t, errors.Is(err, ErrStaleWrite))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestWorkerStaleWriteGuardSemanticsPreserveNewestAdvertisement(t *testing.T) {
	base := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		writes     []domain.Worker
		wantName   string
		wantMemory int64
	}{
		{
			name: "in order newer advertisement persists",
			writes: []domain.Worker{
				staleGuardWorker("worker", "t1", base, 10),
				staleGuardWorker("worker", "t2", base.Add(time.Second), 20),
			},
			wantName:   "t2",
			wantMemory: 20,
		},
		{
			name: "reordered older advertisement is rejected",
			writes: []domain.Worker{
				staleGuardWorker("worker", "t2", base.Add(time.Second), 20),
				staleGuardWorker("worker", "t1", base, 10),
			},
			wantName:   "t2",
			wantMemory: 20,
		},
		{
			name: "same timestamp accepts replacement",
			writes: []domain.Worker{
				staleGuardWorker("worker", "first", base, 10),
				staleGuardWorker("worker", "second", base, 30),
			},
			wantName:   "second",
			wantMemory: 30,
		},
		{
			name: "old advertisement cannot regress telemetry",
			writes: []domain.Worker{
				staleGuardWorker("worker", "fresh-telemetry", base.Add(2*time.Second), 64),
				staleGuardWorker("worker", "old-telemetry", base.Add(time.Second), 1),
			},
			wantName:   "fresh-telemetry",
			wantMemory: 64,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stored *domain.Worker
			for _, write := range tc.writes {
				stored = applyWorkerUpsertStaleWriteGuard(stored, write)
			}
			if stored == nil {
				t.Fatal("stored worker is nil")
			}
			if stored.Name != tc.wantName {
				t.Fatalf("stored name = %q, want %q", stored.Name, tc.wantName)
			}
			if stored.Telemetry == nil || stored.Telemetry.Memory == nil || stored.Telemetry.Memory.AvailableBytes != tc.wantMemory {
				t.Fatalf("stored telemetry = %#v, want available memory %d", stored.Telemetry, tc.wantMemory)
			}
		})
	}
}

func applyWorkerUpsertStaleWriteGuard(stored *domain.Worker, incoming domain.Worker) *domain.Worker {
	if stored != nil && incoming.LastAdvertisementAt.Before(stored.LastAdvertisementAt) {
		copy := *stored
		return &copy
	}
	copy := incoming
	return &copy
}

func staleGuardWorker(pubkey, name string, advertisedAt time.Time, availableMemory int64) domain.Worker {
	return domain.Worker{
		PubKey:              pubkey,
		Name:                name,
		LastAdvertisementAt: advertisedAt,
		Telemetry: &domain.WorkerTelemetry{
			SampledAt: advertisedAt,
			Memory: &domain.WorkerMemoryTelemetry{
				TotalBytes:     100,
				AvailableBytes: availableMemory,
			},
		},
	}
}
