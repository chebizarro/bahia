package workflow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type staleRunSourceFake struct {
	mu   sync.Mutex
	runs map[uuid.UUID]domain.DeploymentRun
}

func (f *staleRunSourceFake) ListNonTerminal(context.Context) ([]domain.DeploymentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var runs []domain.DeploymentRun
	for _, run := range f.runs {
		if run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusRunning {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

func (f *staleRunSourceFake) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return nil, nil
	}
	copy := run
	return &copy, nil
}

func (f *staleRunSourceFake) put(run domain.DeploymentRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[run.ID] = run
}

type staleRunPublisherFake struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (f *staleRunPublisherFake) PublishSignedEvent(_ context.Context, event *nostr.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, *event)
	return nil
}

func (f *staleRunPublisherFake) snapshot() []nostr.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nostr.Event(nil), f.events...)
}

func TestStaleRunDetectorPublishesReplaceableStaleAndRecoveredOnStatusResume(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	startedAt := now.Add(-10 * time.Minute)
	run := domain.DeploymentRun{
		ID:        uuid.New(),
		LoomJobID: "loom-job-event-id",
		Status:    domain.RunStatusRunning,
		StartedAt: &startedAt,
		CreatedAt: startedAt,
		UpdatedAt: startedAt,
	}
	runs := &staleRunSourceFake{runs: map[uuid.UUID]domain.DeploymentRun{run.ID: run}}
	audit := repository.NewInMemoryNostrEventRepository()
	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(runs, audit, published, 5*time.Minute, zap.NewNop())
	detector.now = func() time.Time { return now }

	require.NoError(t, detector.check(ctx))
	events := published.snapshot()
	require.Len(t, events, 1)
	assertStaleRunHealthEvent(t, events[0], run, "stale", "loom_status_missing")
	require.Equal(t, startedAt.Add(5*time.Minute).Unix(), events[0].CreatedAt.Time().Unix())

	// The active transition is not republished by each operational timer tick.
	require.NoError(t, detector.check(ctx))
	require.Len(t, published.snapshot(), 1)

	statusAt := now.Add(-time.Minute)
	tags, err := json.Marshal(nostr.Tags{{"d", run.LoomJobID}, {"e", run.LoomJobID}, {"status", "running"}})
	require.NoError(t, err)
	_, err = audit.Record(ctx, &repository.NostrEventRecord{
		ID:         "loom-status-1",
		Kind:       kinds.LoomJobStatusUpdate,
		Tags:       tags,
		CreatedAt:  statusAt,
		ReceivedAt: statusAt,
	})
	require.NoError(t, err)

	require.NoError(t, detector.check(ctx))
	events = published.snapshot()
	require.Len(t, events, 2)
	assertStaleRunHealthEvent(t, events[1], run, "recovered", "loom_status_resumed")
	require.Equal(t, statusAt.Unix(), events[1].CreatedAt.Time().Unix())
	require.Equal(t, tagValue(events[0], "d"), tagValue(events[1], "d"), "recovery must replace the stale status")

	require.NoError(t, detector.check(ctx))
	require.Len(t, published.snapshot(), 2)
}

func TestStaleRunDetectorPublishesRecoveredWhenRunBecomesTerminal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 30, 19, 0, 0, 0, time.UTC)
	startedAt := now.Add(-20 * time.Minute)
	run := domain.DeploymentRun{
		ID:        uuid.New(),
		LoomJobID: "loom-terminal-job",
		Status:    domain.RunStatusQueued,
		StartedAt: &startedAt,
		CreatedAt: startedAt,
		UpdatedAt: startedAt,
	}
	runs := &staleRunSourceFake{runs: map[uuid.UUID]domain.DeploymentRun{run.ID: run}}
	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(runs, repository.NewInMemoryNostrEventRepository(), published, 5*time.Minute, zap.NewNop())
	detector.now = func() time.Time { return now }

	require.NoError(t, detector.check(ctx))
	require.Len(t, published.snapshot(), 1)

	finishedAt := now.Add(time.Minute)
	run.Status = domain.RunStatusSucceeded
	run.FinishedAt = &finishedAt
	run.UpdatedAt = finishedAt
	runs.put(run)
	detector.now = func() time.Time { return finishedAt }

	require.NoError(t, detector.check(ctx))
	events := published.snapshot()
	require.Len(t, events, 2)
	assertStaleRunHealthEvent(t, events[1], run, "recovered", "run_terminal")
	require.Equal(t, finishedAt.Unix(), events[1].CreatedAt.Time().Unix())

	require.NoError(t, detector.check(ctx))
	require.Len(t, published.snapshot(), 2)
}

func TestStaleRunDetectorRecoversPersistedStaleSignalAfterRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 30, 19, 30, 0, 0, time.UTC)
	finishedAt := now.Add(-time.Minute)
	run := domain.DeploymentRun{
		ID:         uuid.New(),
		LoomJobID:  "persisted-stale-job",
		Status:     domain.RunStatusFailed,
		FinishedAt: &finishedAt,
		UpdatedAt:  finishedAt,
		CreatedAt:  now.Add(-20 * time.Minute),
	}
	runs := &staleRunSourceFake{runs: map[uuid.UUID]domain.DeploymentRun{run.ID: run}}
	audit := repository.NewInMemoryNostrEventRepository()
	content, err := json.Marshal(map[string]any{
		"schema":      staleRunHealthSchema,
		"run_id":      run.ID.String(),
		"loom_job_id": run.LoomJobID,
		"state":       "stale",
	})
	require.NoError(t, err)
	tags, err := json.Marshal(nostr.Tags{{"d", run.ID.String()}, {"e", run.LoomJobID}, {"t", "deployment.run.health"}})
	require.NoError(t, err)
	_, err = audit.Record(ctx, &repository.NostrEventRecord{
		ID:         "prior-stale-health",
		Kind:       kinds.NIP38Status,
		Content:    string(content),
		Tags:       tags,
		CreatedAt:  now.Add(-5 * time.Minute),
		ReceivedAt: now.Add(-5 * time.Minute),
	})
	require.NoError(t, err)

	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(runs, audit, published, 5*time.Minute, zap.NewNop())
	detector.now = func() time.Time { return now }
	require.NoError(t, detector.check(ctx))

	events := published.snapshot()
	require.Len(t, events, 1)
	assertStaleRunHealthEvent(t, events[0], run, "recovered", "run_terminal")
}

func TestStaleRunDetectorIgnoresFreshAndNonLoomRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 30, 20, 0, 0, 0, time.UTC)
	startedAt := now.Add(-10 * time.Minute)
	loomRun := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "fresh-job", Status: domain.RunStatusRunning, StartedAt: &startedAt, CreatedAt: startedAt}
	directRun := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "runtime:direct", Status: domain.RunStatusRunning, StartedAt: &startedAt, CreatedAt: startedAt}
	runs := &staleRunSourceFake{runs: map[uuid.UUID]domain.DeploymentRun{loomRun.ID: loomRun, directRun.ID: directRun}}
	audit := repository.NewInMemoryNostrEventRepository()
	statusAt := now.Add(-time.Minute)
	tags, err := json.Marshal(nostr.Tags{{"e", loomRun.LoomJobID}})
	require.NoError(t, err)
	_, err = audit.Record(ctx, &repository.NostrEventRecord{ID: "fresh-status", Kind: kinds.LoomJobStatusUpdate, Tags: tags, CreatedAt: statusAt, ReceivedAt: statusAt})
	require.NoError(t, err)
	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(runs, audit, published, 5*time.Minute, zap.NewNop())
	detector.now = func() time.Time { return now }

	require.NoError(t, detector.check(ctx))
	require.Empty(t, published.snapshot())
}

func TestStaleRunDetectorRunStopsWithContext(t *testing.T) {
	now := time.Now().UTC()
	run := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "job", Status: domain.RunStatusRunning, StartedAt: &now, CreatedAt: now}
	detector := NewStaleRunDetector(
		&staleRunSourceFake{runs: map[uuid.UUID]domain.DeploymentRun{run.ID: run}},
		repository.NewInMemoryNostrEventRepository(),
		&staleRunPublisherFake{},
		2*time.Second,
		zap.NewNop(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- detector.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("detector did not stop after context cancellation")
	}
}

func assertStaleRunHealthEvent(t *testing.T, event nostr.Event, run domain.DeploymentRun, state, reason string) {
	t.Helper()
	require.Equal(t, nostr.Kind(kinds.NIP38Status), event.Kind)
	require.Equal(t, run.ID.String(), tagValue(event, "d"))
	require.Equal(t, run.LoomJobID, tagValue(event, "e"))
	require.Equal(t, state, tagValue(event, "status"))
	require.Equal(t, "deployment.run.health", tagValue(event, "t"))
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(event.Content), &content))
	require.Equal(t, staleRunHealthSchema, content["schema"])
	require.Equal(t, run.ID.String(), content["run_id"])
	require.Equal(t, run.LoomJobID, content["loom_job_id"])
	require.Equal(t, state, content["state"])
	require.Equal(t, reason, content["reason"])
}

func tagValue(event nostr.Event, name string) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
