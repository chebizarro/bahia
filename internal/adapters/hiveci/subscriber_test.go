package hiveci

import (
	"context"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const hiveCITestPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

type testHiveRepo struct {
	runs    map[string]domain.HiveCIWorkflowRun
	results map[string]domain.HiveCIWorkflowResult
}

type fakeRelaySubscriber struct {
	subscriptions []*nostrAdapter.MergedSubscription
	filters       [][]nostr.Filter
	authCalls     []string
	authErr       error
}

func (f *fakeRelaySubscriber) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostrAdapter.MergedSubscription, error) {
	copied := append([]nostr.Filter(nil), filters...)
	f.filters = append(f.filters, copied)
	if len(f.subscriptions) == 0 {
		return nil, context.Canceled
	}
	next := f.subscriptions[0]
	f.subscriptions = f.subscriptions[1:]
	return next, nil
}

func (f *fakeRelaySubscriber) AuthenticateRelay(_ context.Context, relayURL string) error {
	f.authCalls = append(f.authCalls, relayURL)
	return f.authErr
}

func signedHiveCIEvent(t *testing.T, kind int, createdAt time.Time, tags nostr.Tags) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostr.Kind(kind),
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Content:   "{}",
		Tags:      tags,
	}
	require.NoError(t, nostrutil.SignEventWithHexKey(ev, hiveCITestPrivateKey))
	return ev
}

func hiveCITestPubkey(t *testing.T) string {
	t.Helper()
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(hiveCITestPrivateKey)
	require.NoError(t, err)
	return pubkey
}

func mergedWithClosed(closed nostrAdapter.RelayClosed) *nostrAdapter.MergedSubscription {
	closedCh := make(chan nostrAdapter.RelayClosed, 1)
	closedCh <- closed
	return &nostrAdapter.MergedSubscription{Closed: closedCh}
}

func mergedWithEvents(events ...*nostr.Event) *nostrAdapter.MergedSubscription {
	eventsCh := make(chan *nostr.Event, len(events))
	for _, ev := range events {
		eventsCh <- ev
	}
	close(eventsCh)
	return &nostrAdapter.MergedSubscription{Events: eventsCh}
}

func newTestHiveRepo() *testHiveRepo {
	return &testHiveRepo{runs: map[string]domain.HiveCIWorkflowRun{}, results: map[string]domain.HiveCIWorkflowResult{}}
}

func (r *testHiveRepo) UpsertWorkflowRun(_ context.Context, run domain.HiveCIWorkflowRun) error {
	r.runs[run.RunEventID] = run
	return nil
}
func (r *testHiveRepo) UpsertWorkflowResult(_ context.Context, result domain.HiveCIWorkflowResult) error {
	r.results[result.ResultEventID] = result
	return nil
}
func (r *testHiveRepo) GetRunByEventID(_ context.Context, eventID string) (*domain.HiveCIWorkflowRun, error) {
	run, ok := r.runs[eventID]
	if !ok {
		return nil, nil
	}
	return &run, nil
}
func (r *testHiveRepo) GetResultByEventID(_ context.Context, eventID string) (*domain.HiveCIWorkflowResult, error) {
	result, ok := r.results[eventID]
	if !ok {
		return nil, nil
	}
	return &result, nil
}
func (r *testHiveRepo) GetLatestResultByRunEventID(_ context.Context, runEventID string) (*domain.HiveCIWorkflowResult, error) {
	for _, result := range r.results {
		if result.RunEventID == runEventID {
			copy := result
			return &copy, nil
		}
	}
	return nil, nil
}
func (r *testHiveRepo) ListPendingResults(_ context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (r *testHiveRepo) ListOrphanedResultsByRun(_ context.Context, _ string) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (r *testHiveRepo) UpdateResultState(_ context.Context, _ string, _ domain.HiveCIProcessingState) error {
	return nil
}
func (r *testHiveRepo) IncrementResultRetry(_ context.Context, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (r *testHiveRepo) MarkResultFailed(_ context.Context, _, _ string) error {
	return nil
}
func (r *testHiveRepo) ListPolicies(_ context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return nil, nil
}
func (r *testHiveRepo) GetPolicyByRepoAndWorkflow(_ context.Context, _, _ string) (*domain.HiveCIPipelinePolicy, error) {
	return nil, nil
}
func (r *testHiveRepo) EnsurePipelinePolicy(_ context.Context, _ domain.HiveCIPipelinePolicy) error {
	return nil
}
func (r *testHiveRepo) LookupRepositoryCI(_ context.Context, _ []string, _ bool) ([]domain.RepositoryCILookup, error) {
	return nil, nil
}

func TestHandleEventDropsInvalidBeforePersistenceAndDispatch(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	valid := signedHiveCIEvent(t, kindWorkflowRun, now, nostr.Tags{
		{"a", "30618:pk:repo"},
		{"commit", "abc"},
		{"branch", "main"},
		{"workflow", ".github/workflows/ci.yml"},
		{"triggered-by", "user"},
		{"publisher", publisher},
	})
	invalid := *valid
	invalid.ID[0] ^= 0xff
	called := false
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), func(context.Context, string) {
		called = true
	})
	s.now = func() time.Time { return now }

	s.handleEvent(context.Background(), &invalid)

	require.Empty(t, repo.runs, "invalid event must not persist")
	require.Empty(t, repo.results, "invalid event must not persist result records")
	require.False(t, called, "invalid event must not dispatch callbacks")
}

func TestHandleEventIngestsValidWorkflowRunAndResult(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	called := ""
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), func(_ context.Context, resultEventID string) {
		called = resultEventID
	})
	s.now = func() time.Time { return now }

	run := signedHiveCIEvent(t, kindWorkflowRun, now, nostr.Tags{
		{"a", "30618:pk:repo"},
		{"commit", "abc"},
		{"branch", "main"},
		{"workflow", ".github/workflows/ci.yml"},
		{"triggered-by", "user"},
		{"publisher", publisher},
	})
	s.handleEvent(context.Background(), run)

	result := signedHiveCIEvent(t, kindWorkflowResult, now.Add(time.Second), nostr.Tags{
		{"e", nostrutil.EventIDHex(run)},
		{"log_url", "https://b.test/log"},
		{"status", "success"},
		{"exit_code", "0"},
		{"duration", "12"},
	})
	s.handleEvent(context.Background(), result)

	require.Contains(t, repo.runs, nostrutil.EventIDHex(run))
	require.Contains(t, repo.results, nostrutil.EventIDHex(result))
	require.Equal(t, nostrutil.EventIDHex(result), called)
}

func TestReleaseWorkflowRunDispatchPreservesRepositoryAndRef(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), nil)
	s.now = func() time.Time { return now }
	var dispatched WorkflowRunDispatch
	s.SetRunConsumer(func(_ context.Context, run WorkflowRunDispatch) { dispatched = run })

	run := signedHiveCIEvent(t, kindWorkflowRun, now, nostr.Tags{
		{"a", "30617:pk:bahia"}, {"commit", "abc"}, {"branch", "v0.2.0-rc.1"},
		{"ref", "refs/tags/v0.2.0-rc.1"}, {"repo", "https://git.example/bahia.git"},
		{"workflow", ".github/workflows/release.yml"}, {"triggered-by", "nip34-tag"},
		{"publisher", publisher}, {"release", "true"},
	})
	s.handleEvent(context.Background(), run)

	require.Equal(t, nostrutil.EventIDHex(run), dispatched.RunEventID)
	require.Equal(t, "https://git.example/bahia.git", dispatched.Repository)
	require.Equal(t, "refs/tags/v0.2.0-rc.1", dispatched.Ref)
	require.Equal(t, ".github/workflows/release.yml", dispatched.Workflow)
	require.True(t, dispatched.Release)
}

func TestSubscribeAuthRequiredClosedAuthenticatesAndRetriesImmediately(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	run := signedHiveCIEvent(t, kindWorkflowRun, now, nostr.Tags{
		{"a", "30618:pk:repo"},
		{"commit", "abc"},
		{"branch", "main"},
		{"workflow", ".github/workflows/ci.yml"},
		{"triggered-by", "user"},
		{"publisher", publisher},
	})
	pool := &fakeRelaySubscriber{subscriptions: []*nostrAdapter.MergedSubscription{
		mergedWithClosed(nostrAdapter.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-1", Reason: "auth-required: restricted"}),
		mergedWithEvents(run),
	}}
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), nil)
	s.pool = pool
	s.now = func() time.Time { return now }

	require.NoError(t, s.subscribe(context.Background()))

	require.Equal(t, []string{"wss://relay.example"}, pool.authCalls)
	require.Len(t, pool.filters, 2, "successful AUTH should cause an immediate resubscribe with the same filters")
	require.Equal(t, pool.filters[0], pool.filters[1])
	require.Contains(t, repo.runs, nostrutil.EventIDHex(run))
}

func TestConsumeSubscriptionHandlesEOSEAndNonAuthClosedDeterministically(t *testing.T) {
	relayEOSE := make(chan nostrAdapter.RelayEOSE, 1)
	relayEOSE <- nostrAdapter.RelayEOSE{RelayURL: "wss://relay.example", SubscriptionID: "sub-1"}
	close(relayEOSE)

	closed := make(chan nostrAdapter.RelayClosed, 1)
	closed <- nostrAdapter.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-1", Reason: "rate-limited: slow down"}
	close(closed)

	eose := make(chan struct{})
	close(eose)

	pool := &fakeRelaySubscriber{}
	s := NewSubscriber(nil, newTestHiveRepo(), []string{}, zap.NewNop(), nil)
	s.pool = pool
	retry, err := s.consumeSubscription(context.Background(), &nostrAdapter.MergedSubscription{
		EndOfStoredEvents: eose,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
	}, map[string]struct{}{})

	require.NoError(t, err)
	require.False(t, retry)
	require.Empty(t, pool.authCalls, "non-auth CLOSED must not trigger AUTH")
}

func TestRequiredTagParsing(t *testing.T) {
	ev := &nostr.Event{Tags: nostr.Tags{{"a", "30618:pk:repo"}, {"commit", "abc"}}}
	v, err := requiredTag(ev, "a")
	if err != nil {
		t.Fatalf("requiredTag error: %v", err)
	}
	if v != "30618:pk:repo" {
		t.Fatalf("unexpected tag value: %q", v)
	}
	if _, err := requiredTag(ev, "workflow"); err == nil {
		t.Fatal("expected missing workflow tag error")
	}
}

func TestTrustedDispatcherFiltering(t *testing.T) {
	repo := newTestHiveRepo()
	s := NewSubscriber(nil, repo, []string{"trusted"}, zap.NewNop(), nil)
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)

	ev := signedHiveCIEvent(t, kindWorkflowRun, now, nostr.Tags{
		{"a", "30618:pk:repo"}, {"commit", "abc"}, {"branch", "main"}, {"workflow", ".github/workflows/ci.yml"}, {"triggered-by", "user"}, {"publisher", publisher},
	})
	s.handleWorkflowRun(context.Background(), ev)
	if len(repo.runs) != 0 {
		t.Fatalf("expected run to be dropped for untrusted dispatcher")
	}
}

func TestPublisherValidation5401Equals5402Pubkey(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	runID := "run-1"
	repo.runs[runID] = domain.HiveCIWorkflowRun{
		RunEventID:      runID,
		PublisherPubkey: publisher,
		EventCreatedAt:  time.Now(),
	}
	called := false
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), func(_ context.Context, resultEventID string) {
		if resultEventID != "" {
			called = true
		}
	})

	bad := signedHiveCIEvent(t, kindWorkflowResult, now, nostr.Tags{
		{"e", runID}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
	})
	badPubkeyHex, err := nostrutil.PublicKeyHexFromPrivateKeyHex("2222222222222222222222222222222222222222222222222222222222222222")
	require.NoError(t, err)
	bad.PubKey, err = nostrutil.PubKeyFromHex(badPubkeyHex)
	require.NoError(t, err)
	s.handleWorkflowResult(context.Background(), bad)
	if len(repo.results) != 0 {
		t.Fatalf("expected result to be rejected for publisher mismatch")
	}

	good := signedHiveCIEvent(t, kindWorkflowResult, now.Add(time.Second), nostr.Tags{
		{"e", runID}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
	})
	s.handleWorkflowResult(context.Background(), good)
	if len(repo.results) != 1 {
		t.Fatalf("expected valid result to persist")
	}
	if !called {
		t.Fatalf("expected bridge consumer callback")
	}
}

func TestWorkflowResultContentMetadataAndCallbackUsesResultID(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	runID := "run-2"
	repo.runs[runID] = domain.HiveCIWorkflowRun{
		RunEventID:      runID,
		PublisherPubkey: publisher,
		EventCreatedAt:  time.Now(),
	}
	called := ""
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), func(_ context.Context, resultEventID string) {
		called = resultEventID
	})

	ev := signedHiveCIEvent(t, kindWorkflowResult, now, nostr.Tags{
		{"e", runID}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
	})
	ev.Content = `{"image_repo":"harbor.sharegap.net/cascadia/ddgs","image_tag":"pilot-v1","image_digest":"sha256:abc"}`
	s.handleWorkflowResult(context.Background(), ev)
	resultID := nostrutil.EventIDHex(ev)
	stored, ok := repo.results[resultID]
	if !ok {
		t.Fatalf("expected result to persist")
	}
	if stored.ImageRepo != "harbor.sharegap.net/cascadia/ddgs" || stored.ImageTag != "pilot-v1" || stored.ImageDigest != "sha256:abc" {
		t.Fatalf("unexpected persisted image metadata: %+v", stored)
	}
	if called != resultID {
		t.Fatalf("expected callback with result id, got %q", called)
	}
}

func TestWorkflowResultReplaySkipsTerminalResult(t *testing.T) {
	repo := newTestHiveRepo()
	now := time.Unix(1_700_000_000, 0).UTC()
	publisher := hiveCITestPubkey(t)
	runID := "run-terminal-replay"
	repo.runs[runID] = domain.HiveCIWorkflowRun{
		RunEventID:      runID,
		PublisherPubkey: publisher,
		EventCreatedAt:  now,
	}
	ev := signedHiveCIEvent(t, kindWorkflowResult, now, nostr.Tags{
		{"e", runID}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
	})
	resultID := nostrutil.EventIDHex(ev)
	repo.results[resultID] = domain.HiveCIWorkflowResult{
		ResultEventID:   resultID,
		RunEventID:      runID,
		ProcessingState: domain.HiveCIProcessingStateFailed,
		RetryCount:      10,
	}
	called := false
	s := NewSubscriber(nil, repo, []string{publisher}, zap.NewNop(), func(context.Context, string) {
		called = true
	})

	s.handleWorkflowResult(context.Background(), ev)

	if called {
		t.Fatal("terminal replay must not invoke the bridge callback")
	}
	if got := repo.results[resultID]; got.ProcessingState != domain.HiveCIProcessingStateFailed || got.RetryCount != 10 {
		t.Fatalf("terminal replay mutated persisted result: %+v", got)
	}
}
