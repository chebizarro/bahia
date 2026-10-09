package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func canonicalRunRecord(t *testing.T, run domain.DeploymentRun, author string, deleted bool) *repository.NostrEventRecord {
	t.Helper()
	content, err := json.Marshal(run)
	require.NoError(t, err)
	tags, err := json.Marshal(nostr.Tags{
		{"d", run.ID.String()}, {"t", kinds.CPStateTopicDeploymentRun},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
		{kinds.CASControlStateTagLegacyKind, kinds.CPStateFamily(kinds.DeploymentRunRegistry).TagValue()},
		{kinds.CASControlStateTagDeleted, map[bool]string{true: "true", false: "false"}[deleted]},
		{"run", run.ID.String()}, {"status", string(run.Status)},
	})
	require.NoError(t, err)
	return &repository.NostrEventRecord{ID: uuid.NewString(), Kind: kinds.CASControlState, PubKey: author, Tags: tags, Content: string(content), CreatedAt: run.UpdatedAt}
}

func TestCanonicalRunHealthSourceUsesServiceAuthoredLocalRecords(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	run := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "loom-canonical", Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	foreign := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "loom-foreign", Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, canonicalRunRecord(t, run, "service", false))
	require.NoError(t, err)
	_, err = repo.Record(ctx, canonicalRunRecord(t, foreign, "other", false))
	require.NoError(t, err)
	source := NewCanonicalRunHealthSource(repo, "service")
	runs, err := source.ListNonTerminal(ctx)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, run.ID, runs[0].ID)
	got, err := source.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, run.LoomJobID, got.LoomJobID)
	got, err = source.GetByID(ctx, foreign.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestCanonicalRunHealthSourceRejectsMalformedServiceRecord(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC()
	run := domain.DeploymentRun{ID: uuid.New(), Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	rec := canonicalRunRecord(t, run, "service", false)
	rec.Content = `{"id":"` + uuid.NewString() + `","status":"running","created_at":"` + at.Format(time.RFC3339) + `"}`
	_, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	_, err = NewCanonicalRunHealthSource(repo, "service").ListNonTerminal(ctx)
	require.ErrorContains(t, err, "payload")
}

func TestStaleRunDetectorFailsClosedWithoutLoomCatchup(t *testing.T) {
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC()
	run := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "loom-old", Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, canonicalRunRecord(t, run, "service", false))
	require.NoError(t, err)
	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(NewCanonicalRunHealthSource(repo, "service"), repo, published, time.Minute, zap.NewNop())
	ready := make(chan struct{})
	close(ready)
	detector.SetReadiness(ready)
	err = detector.Run(ctx)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "Loom kind-30100 catch-up"))
	require.Empty(t, published.snapshot())
}

func TestStaleRunDetectorDoesNotPublishBeforeLoomEOSE(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	at := time.Now().Add(-time.Hour).UTC()
	run := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "loom-old", Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, canonicalRunRecord(t, run, "service", false))
	require.NoError(t, err)
	published := &staleRunPublisherFake{}
	detector := NewStaleRunDetector(NewCanonicalRunHealthSource(repo, "service"), repo, published, time.Minute, zap.NewNop())
	ready := make(chan struct{})
	close(ready)
	detector.SetReadiness(ready)
	detector.SetLoomStatusReadiness(make(chan struct{}))
	done := make(chan error, 1)
	go func() { done <- detector.Run(ctx) }()
	cancel()
	require.NoError(t, <-done)
	require.Empty(t, published.snapshot())
}

type staleRunPublishSignal struct{ published chan nostr.Event }

func (p staleRunPublishSignal) PublishSignedEvent(_ context.Context, event *nostr.Event) error {
	p.published <- *event
	return nil
}

func TestStaleRunDetectorRunsFromCanonicalRecordAfterBothCatchups(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	at := time.Now().Add(-time.Hour).UTC()
	run := domain.DeploymentRun{ID: uuid.New(), LoomJobID: "loom-old", Status: domain.RunStatusRunning, CreatedAt: at, UpdatedAt: at}
	repo := repositorytest.NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, canonicalRunRecord(t, run, "service", false))
	require.NoError(t, err)
	published := make(chan nostr.Event, 1)
	detector := NewStaleRunDetector(NewCanonicalRunHealthSource(repo, "service"), repo, staleRunPublishSignal{published}, time.Minute, zap.NewNop())
	runReady := make(chan struct{})
	loomReady := make(chan struct{})
	close(runReady)
	close(loomReady)
	detector.SetReadiness(runReady)
	detector.SetLoomStatusReadiness(loomReady)
	done := make(chan error, 1)
	go func() { done <- detector.Run(ctx) }()
	event := <-published
	assertStaleRunHealthEvent(t, event, run, "stale", "loom_status_missing")
	cancel()
	require.NoError(t, <-done)
}
