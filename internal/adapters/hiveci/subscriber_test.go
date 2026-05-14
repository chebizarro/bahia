package hiveci

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type testHiveRepo struct {
	runs    map[string]domain.HiveCIWorkflowRun
	results map[string]domain.HiveCIWorkflowResult
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
func (r *testHiveRepo) GetResultByEventID(_ context.Context, _ string) (*domain.HiveCIWorkflowResult, error) {
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
func (r *testHiveRepo) LookupRepositoryCI(_ context.Context, _ []string, _ bool) ([]domain.RepositoryCILookup, error) {
	return nil, nil
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

	ev := &nostr.Event{
		ID:        "run-1",
		Kind:      kindWorkflowRun,
		PubKey:    "untrusted",
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"a", "30618:pk:repo"}, {"commit", "abc"}, {"branch", "main"}, {"workflow", ".github/workflows/ci.yml"}, {"triggered-by", "user"}, {"publisher", "ephemeral"},
		},
	}
	s.handleWorkflowRun(context.Background(), ev)
	if len(repo.runs) != 0 {
		t.Fatalf("expected run to be dropped for untrusted dispatcher")
	}
}

func TestPublisherValidation5401Equals5402Pubkey(t *testing.T) {
	repo := newTestHiveRepo()
	repo.runs["run-1"] = domain.HiveCIWorkflowRun{
		RunEventID:      "run-1",
		PublisherPubkey: "expected-ephemeral",
		EventCreatedAt:  time.Now(),
	}
	called := false
	s := NewSubscriber(nil, repo, []string{"trusted"}, zap.NewNop(), func(_ context.Context, resultEventID string) {
		if resultEventID == "res-good" {
			called = true
		}
	})

	bad := &nostr.Event{
		ID:        "res-bad",
		Kind:      kindWorkflowResult,
		PubKey:    "different-pubkey",
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", "run-1"}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
		},
	}
	s.handleWorkflowResult(context.Background(), bad)
	if len(repo.results) != 0 {
		t.Fatalf("expected result to be rejected for publisher mismatch")
	}

	good := &nostr.Event{
		ID:        "res-good",
		Kind:      kindWorkflowResult,
		PubKey:    "expected-ephemeral",
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", "run-1"}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
		},
	}
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
	repo.runs["run-2"] = domain.HiveCIWorkflowRun{
		RunEventID:      "run-2",
		PublisherPubkey: "expected-ephemeral",
		EventCreatedAt:  time.Now(),
	}
	called := ""
	s := NewSubscriber(nil, repo, []string{"trusted"}, zap.NewNop(), func(_ context.Context, resultEventID string) {
		called = resultEventID
	})

	ev := &nostr.Event{
		ID:        "res-meta",
		Kind:      kindWorkflowResult,
		PubKey:    "expected-ephemeral",
		CreatedAt: nostr.Now(),
		Content:   `{"image_repo":"harbor.sharegap.net/cascadia/ddgs","image_tag":"pilot-v1","image_digest":"sha256:abc"}`,
		Tags: nostr.Tags{
			{"e", "run-2"}, {"log_url", "https://b.test/log"}, {"status", "success"}, {"exit_code", "0"}, {"duration", "12"},
		},
	}
	s.handleWorkflowResult(context.Background(), ev)
	stored, ok := repo.results["res-meta"]
	if !ok {
		t.Fatalf("expected result to persist")
	}
	if stored.ImageRepo != "harbor.sharegap.net/cascadia/ddgs" || stored.ImageTag != "pilot-v1" || stored.ImageDigest != "sha256:abc" {
		t.Fatalf("unexpected persisted image metadata: %+v", stored)
	}
	if called != "res-meta" {
		t.Fatalf("expected callback with result id, got %q", called)
	}
}
