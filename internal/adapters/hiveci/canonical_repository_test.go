package hiveci

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// daemonKeyHex is the daemon's signing key in these tests; its pubkey is the
// author of every canonical record.
const daemonKeyHex = "2222222222222222222222222222222222222222222222222222222222222222"

// canonicalDaemon is the Hive-CI store wired the way app.go wires it: one
// daemon's local event store and publish outbox in dir, a real projector and
// a real fleet-OCK encryptor whose key envelopes are retained in the same
// store, and no relay (every publish is queued in the outbox). index is the
// optional SQL stand-in.
type canonicalDaemon struct {
	store     *localstore.Store
	outbox    *localstore.Outbox
	publisher *nostrAdapter.Publisher
	repo      *CanonicalRepository
	pubkey    string
}

func startCanonicalDaemon(t *testing.T, dir string, index repository.HiveCIRepository, trusted []string) *canonicalDaemon {
	t.Helper()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: daemonKeyHex, PublishEnabled: true}
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(daemonKeyHex)
	require.NoError(t, err)
	publisher := nostrAdapter.NewPublisher(cfg, nostrAdapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostrAdapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostrAdapter.WithLocalOutbox(outbox, store))
	history := nostrAdapter.NewLocalEventRepository(store, nil).Authored(pubkey)
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	projector := nostrAdapter.NewProjector(cfg, registry, publisher, history, zap.NewNop())
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	signer, err := controlplane.NewPrivateKeySigner(daemonKeyHex)
	require.NoError(t, err)
	ockManager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: pubkey, Publisher: projector,
		History: nostrAdapter.NewProjectorOCKEnvelopeHistory(history), Logger: zap.NewNop(),
	})
	encryptor := controlplane.NewConfidentialEncryptor(ockManager, zap.NewNop())
	canonical := nostrAdapter.NewHiveCICanonicalPublisher(projector, encryptor, zap.NewNop())
	d := &canonicalDaemon{store: store, outbox: outbox, publisher: publisher, pubkey: pubkey,
		repo: NewCanonicalRepository(store, canonical, index, trusted, zap.NewNop())}
	t.Cleanup(d.close)
	return d
}

func (d *canonicalDaemon) close() {
	d.publisher.Close()
	_ = d.outbox.Close()
	_ = d.store.Close()
}

// failingIndex is a SQL index whose every write fails.
type failingIndex struct {
	repository.HiveCIRepository
	mu     sync.Mutex
	writes int
}

func (f *failingIndex) count() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
}

func (f *failingIndex) UpsertWorkflowRun(context.Context, domain.HiveCIWorkflowRun) error {
	f.count()
	return errors.New("database unavailable")
}

func (f *failingIndex) UpsertWorkflowResult(context.Context, domain.HiveCIWorkflowResult) error {
	f.count()
	return errors.New("database unavailable")
}

func (f *failingIndex) UpdateResultState(context.Context, string, domain.HiveCIProcessingState) error {
	f.count()
	return errors.New("database unavailable")
}

func (f *failingIndex) IncrementResultRetry(context.Context, string, time.Time) (int, error) {
	f.count()
	return 0, errors.New("database unavailable")
}

func (f *failingIndex) EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error {
	f.count()
	return errors.New("database unavailable")
}

func (f *failingIndex) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return nil, errors.New("database unavailable")
}

// countingProcessor records each processing attempt and fails until told
// otherwise.
type countingProcessor struct {
	mu       sync.Mutex
	attempts []string
	fail     bool
}

func (p *countingProcessor) ProcessResult(_ context.Context, resultEventID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = append(p.attempts, resultEventID)
	if p.fail {
		return errors.New("registry unavailable")
	}
	return nil
}

func (p *countingProcessor) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.attempts)
}

func signedRunAndResult(t *testing.T, at time.Time) (*nostr.Event, *nostr.Event) {
	t.Helper()
	run := signedHiveCIEvent(t, kinds.HiveCIWorkflowRun, at, nostr.Tags{
		{"a", "30617:" + hiveCITestPubkey(t) + ":bahia"}, {"commit", "0123456789abcdef0123456789abcdef01234567"},
		{"branch", "main"}, {"workflow", ".hive-ci/build.yml"}, {"triggered-by", hiveCITestPubkey(t)},
		{"publisher", hiveCITestPubkey(t)}, {"t", "hive-ci"},
	})
	result := signedHiveCIEvent(t, kinds.HiveCIWorkflowResult, at.Add(time.Minute), nostr.Tags{
		{"e", run.ID.Hex()}, {"log_url", "https://logs.example/1"}, {"status", "success"},
		{"exit_code", "0"}, {"duration", "12"}, {"image_repo", "registry.example/app"},
		{"image_tag", "v1"}, {"image_digest", "sha256:" + hiveCITestPubkey(t)},
	})
	return run, result
}

func ingest(t *testing.T, d *canonicalDaemon, processor ResultProcessor, events ...*nostr.Event) {
	t.Helper()
	onResult := func(ctx context.Context, id string) { _ = processor.ProcessResult(ctx, id) }
	sub := NewSubscriber(nil, d.repo, []string{hiveCITestPubkey(t)}, zap.NewNop(), onResult)
	sub.SetEvidenceStore(d.store)
	sub.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	for _, ev := range events {
		sub.handleEvent(context.Background(), ev)
	}
}

// a result whose processing failed is resumed after a restart from the
// canonical result state in the local event store. There is no SQL row and no
// timer: the restarted daemon's one resume pass attempts it, counts the
// attempt, and the state reflects the outcome.
func TestPendingResultResumesAfterRestartWithoutSQL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	at := time.Unix(1_800_000_000, 0).UTC().Add(-time.Hour)
	run, result := signedRunAndResult(t, at)
	processor := &countingProcessor{fail: true}

	first := startCanonicalDaemon(t, dir, nil, []string{hiveCITestPubkey(t)})
	ingest(t, first, processor, run, result)
	require.Equal(t, 1, processor.count(), "arrival triggers processing")
	stored, err := first.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, domain.HiveCIProcessingStatePendingResult, stored.ProcessingState)
	require.Equal(t, 0, stored.RetryCount)
	first.close()

	processor.fail = false
	restarted := startCanonicalDaemon(t, dir, nil, []string{hiveCITestPubkey(t)})
	pending, err := restarted.repo.ListPendingResults(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, result.ID.Hex(), pending[0].ResultEventID)
	resumer := NewPendingResultResumer(restarted.repo, processor, 3, zap.NewNop())
	require.NoError(t, resumer.Resume(ctx))
	require.Equal(t, 2, processor.count(), "restart resumes the pending result once")
	resumed, err := restarted.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, 1, resumed.RetryCount)
	require.NotNil(t, resumed.LastRetryAt)

	// The bridge marks what it decided; the state is canonical and read back.
	require.NoError(t, restarted.repo.UpdateResultState(ctx, result.ID.Hex(), domain.HiveCIProcessingStateProcessed))
	pending, err = restarted.repo.ListPendingResults(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, resumer.Resume(ctx))
	require.Equal(t, 2, processor.count(), "a processed result is not attempted again")
}

// A result that keeps failing is attempted at most maxAttempts times across
// restarts, then marked failed instead of being attempted forever.
func TestPendingResultResumeBoundsAttempts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run, result := signedRunAndResult(t, time.Unix(1_800_000_000, 0).UTC().Add(-time.Hour))
	processor := &countingProcessor{fail: true}
	d := startCanonicalDaemon(t, dir, nil, []string{hiveCITestPubkey(t)})
	ingest(t, d, processor, run, result)
	resumer := NewPendingResultResumer(d.repo, processor, 2, zap.NewNop())
	require.Error(t, resumer.Resume(ctx), "the first attempt failed and will be retried")
	require.NoError(t, resumer.Resume(ctx), "the last attempt failed and was marked terminal")
	require.Equal(t, 3, processor.count())
	stored, err := d.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, domain.HiveCIProcessingStateFailed, stored.ProcessingState)
	require.Equal(t, "max retries exceeded", stored.ProcessingError)
	require.NoError(t, resumer.Resume(ctx))
	require.Equal(t, 3, processor.count(), "a failed result is terminal")

	// there is no retry timer behind the exhausted attempts; the
	// operator's signer-first build-result action is the contract, so the
	// bridge may finish a failed result through it.
	require.NoError(t, d.repo.UpdateResultState(ctx, result.ID.Hex(), domain.HiveCIProcessingStateProcessed))
	finished, err := d.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, domain.HiveCIProcessingStateProcessed, finished.ProcessingState)
	require.Error(t, d.repo.UpdateResultState(ctx, result.ID.Hex(), domain.HiveCIProcessingStatePendingResult), "a processed result never reopens")
}

// Evidence is read from the local event store: with the SQL index empty and
// refusing every write, runs, results and policies are still ingested and
// read back, and the index failures are only mirrored, never returned.
func TestIngestionDoesNotDependOnTheSQLIndex(t *testing.T) {
	ctx := context.Background()
	run, result := signedRunAndResult(t, time.Unix(1_800_000_000, 0).UTC().Add(-time.Hour))
	index := &failingIndex{}
	d := startCanonicalDaemon(t, t.TempDir(), index, []string{hiveCITestPubkey(t)})
	processor := &countingProcessor{}
	ingest(t, d, processor, run, result)
	require.Equal(t, 1, processor.count())

	loaded, err := d.repo.GetRunByEventID(ctx, run.ID.Hex())
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "main", loaded.Branch)
	found, err := d.repo.FindWorkflowRun(ctx, loaded.RepoCoordinate, loaded.CommitSHA, loaded.WorkflowPath)
	require.NoError(t, err)
	require.Equal(t, run.ID.Hex(), found.RunEventID)
	latest, err := d.repo.GetLatestResultByRunEventID(ctx, run.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, result.ID.Hex(), latest.ResultEventID)
	require.Equal(t, "registry.example/app", latest.ImageRepo)

	policy := domain.HiveCIPipelinePolicy{RepoCoordinate: loaded.RepoCoordinate, WorkflowPath: loaded.WorkflowPath,
		ServiceID: uuid.New(), EnvironmentID: uuid.New(), Enabled: true, Metadata: map[string]any{"workflow_digest": "abc"}}
	require.NoError(t, d.repo.EnsurePipelinePolicy(ctx, policy))
	require.NoError(t, d.repo.EnsurePipelinePolicy(ctx, policy), "re-ensuring is idempotent")
	policies, err := d.repo.ListPolicies(ctx)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.NotEqual(t, uuid.Nil, policies[0].ID)
	require.Equal(t, "abc", policies[0].Metadata["workflow_digest"])
	bound, err := d.repo.GetPolicyByRepoAndWorkflow(ctx, loaded.RepoCoordinate, loaded.WorkflowPath)
	require.NoError(t, err)
	require.Equal(t, policies[0].ID, bound.ID)
	require.Greater(t, index.writes, 0, "the index was attempted")

	// The release evidence reads the same policies and the same signed run.
	evidence := NewLocalReleaseEvidence(d.store, d.repo, nil, nil, service.WorkerPressureThresholds{})
	listed, err := evidence.ListPipelinePolicies(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	signedRun, err := evidence.GetWorkflowRunEvent(ctx, run.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, run.ID, signedRun.ID)
	require.True(t, signedRun.VerifySignature())
}

// A run signed by an untrusted key is not a run, even though the store may
// hold it, and a result for an unknown run waits as an orphan until the run
// arrives, when it is processed.
func TestOrphanedResultIsProcessedWhenItsRunArrives(t *testing.T) {
	ctx := context.Background()
	run, result := signedRunAndResult(t, time.Unix(1_800_000_000, 0).UTC().Add(-time.Hour))
	d := startCanonicalDaemon(t, t.TempDir(), nil, []string{hiveCITestPubkey(t)})
	processor := &countingProcessor{}
	ingest(t, d, processor, result)
	require.Equal(t, 0, processor.count(), "an orphan is not processed")
	orphans, err := d.repo.ListOrphanedResultsByRun(ctx, run.ID.Hex())
	require.NoError(t, err)
	require.Len(t, orphans, 1)

	untrusted := &nostr.Event{Kind: run.Kind, CreatedAt: run.CreatedAt, Tags: run.Tags, Content: run.Content}
	require.NoError(t, untrusted.Sign(nostr.Generate()))
	_, err = d.store.SaveEvent(*untrusted)
	require.NoError(t, err)
	loaded, err := d.repo.GetRunByEventID(ctx, untrusted.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, loaded, "an untrusted signer's run is not a run")

	ingest(t, d, processor, run)
	require.Equal(t, 1, processor.count(), "the run's arrival processes the orphan")
	stored, err := d.repo.GetResultByEventID(ctx, result.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, domain.HiveCIProcessingStatePendingResult, stored.ProcessingState)
}
