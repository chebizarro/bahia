package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

type fakeMLRecipeDispatcher struct {
	mu      sync.Mutex
	calls   []MLRecipeJobDispatchRequest
	results []*MLRecipeJobDispatchResult
	errs    []error
}

func (d *fakeMLRecipeDispatcher) DispatchStep(_ context.Context, req MLRecipeJobDispatchRequest) (*MLRecipeJobDispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, req)
	idx := len(d.calls) - 1
	if idx < len(d.errs) && d.errs[idx] != nil {
		return nil, d.errs[idx]
	}
	if idx < len(d.results) && d.results[idx] != nil {
		return d.results[idx], nil
	}
	return &MLRecipeJobDispatchResult{Status: domain.RunStatusSucceeded}, nil
}

func (d *fakeMLRecipeDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

type captureMLRecipeResponder struct {
	statuses []string
	results  []string
	errors   []string
}

func (r *captureMLRecipeResponder) PublishRecipeRunStatus(_ context.Context, _ *domain.MLRecipe, _ *domain.MLRecipeRun, step, _ string) error {
	r.statuses = append(r.statuses, step)
	return nil
}
func (r *captureMLRecipeResponder) PublishRecipeRunResult(_ context.Context, _ *domain.MLRecipe, _ *domain.MLRecipeRun, status, _ string) error {
	r.results = append(r.results, status)
	return nil
}
func (r *captureMLRecipeResponder) PublishRecipeRunError(_ context.Context, _ *domain.MLRecipe, _ *domain.MLRecipeRun, step string, cause error) error {
	r.errors = append(r.errors, step+":"+cause.Error())
	return nil
}

func TestMLRecipeCoordinatorCheckpointedLinearExecution(t *testing.T) {
	ctx := context.Background()
	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	artifactID := uuid.New()
	recipe := &domain.MLRecipe{ID: uuid.New(), Name: "linear", Version: "1", Steps: []domain.MLRecipeStep{{Name: "fetch", Action: "fetch_source"}, {Name: "publish", Action: "publish_artifact"}}}
	run := &domain.MLRecipeRun{ID: uuid.New(), RecipeID: recipe.ID, RequestedBy: "operator", Status: domain.RunStatusQueued, Inputs: map[string]any{"source": "hf://demo"}, CreatedAt: time.Now().UTC()}
	if err := repo.UpsertRecipe(ctx, recipe); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertRecipeRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	dispatcher := &fakeMLRecipeDispatcher{results: []*MLRecipeJobDispatchResult{{Status: domain.RunStatusSucceeded}, {Status: domain.RunStatusSucceeded, OutputArtifacts: []uuid.UUID{artifactID}, Metadata: map[string]any{"job": "loom-1"}}}}
	responder := &captureMLRecipeResponder{}
	coordinator := NewMLRecipeCoordinator(registry, dispatcher, zap.NewNop(), WithMLRecipeResponder(responder))

	if err := coordinator.ProcessRecipeRun(ctx, run.ID); err != nil {
		t.Fatalf("process recipe: %v", err)
	}
	stored, err := registry.GetRecipeRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", stored.Status)
	}
	if len(stored.StepStates) != 2 || stored.StepStates[0].Status != string(domain.RunStatusSucceeded) || stored.StepStates[1].Status != string(domain.RunStatusSucceeded) {
		t.Fatalf("unexpected step states: %#v", stored.StepStates)
	}
	if len(stored.StepStates[1].OutputArtifacts) != 1 || stored.StepStates[1].OutputArtifacts[0] != artifactID {
		t.Fatalf("missing output artifact checkpoint: %#v", stored.StepStates[1].OutputArtifacts)
	}
	if dispatcher.callCount() != 2 {
		t.Fatalf("dispatch calls = %d, want 2", dispatcher.callCount())
	}
	if len(responder.results) != 1 || responder.results[0] != "succeeded" {
		t.Fatalf("terminal results = %#v", responder.results)
	}
}

func TestMLRecipeCoordinatorManualResumeRetainsSuccessfulOutputs(t *testing.T) {
	ctx := context.Background()
	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	artifactID := uuid.New()
	recipe := &domain.MLRecipe{ID: uuid.New(), Name: "resume", Version: "1", Steps: []domain.MLRecipeStep{{Action: "package_artifact"}, {Action: "convert_model"}}}
	run := &domain.MLRecipeRun{ID: uuid.New(), RecipeID: recipe.ID, RequestedBy: "operator", Status: domain.RunStatusQueued, CreatedAt: time.Now().UTC()}
	_ = repo.UpsertRecipe(ctx, recipe)
	_ = repo.UpsertRecipeRun(ctx, run)
	dispatcher := &fakeMLRecipeDispatcher{results: []*MLRecipeJobDispatchResult{{Status: domain.RunStatusSucceeded, OutputArtifacts: []uuid.UUID{artifactID}}}, errs: []error{nil, errors.New("converter unavailable")}}
	coordinator := NewMLRecipeCoordinator(registry, dispatcher, zap.NewNop())

	if err := coordinator.ProcessRecipeRun(ctx, run.ID); err == nil {
		t.Fatalf("expected initial failure")
	}
	failed, _ := registry.GetRecipeRun(ctx, run.ID)
	if failed.Status != domain.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", failed.Status)
	}
	if len(failed.StepStates[0].OutputArtifacts) != 1 || failed.StepStates[0].OutputArtifacts[0] != artifactID {
		t.Fatalf("successful output was not retained: %#v", failed.StepStates[0].OutputArtifacts)
	}

	dispatcher.errs = nil
	dispatcher.results = []*MLRecipeJobDispatchResult{{Status: domain.RunStatusSucceeded, OutputArtifacts: []uuid.UUID{artifactID}}, {Status: domain.RunStatusSucceeded}}
	if err := coordinator.ResumeRecipeRun(ctx, run.ID); err != nil {
		t.Fatalf("resume recipe: %v", err)
	}
	resumed, _ := registry.GetRecipeRun(ctx, run.ID)
	if resumed.Status != domain.RunStatusSucceeded {
		t.Fatalf("resumed status = %s, want succeeded", resumed.Status)
	}
	if dispatcher.callCount() != 3 {
		t.Fatalf("dispatch calls after resume = %d, want 3 (step 0 not rerun)", dispatcher.callCount())
	}
}

func TestMLRecipeCoordinatorExplicitRetryPolicy(t *testing.T) {
	ctx := context.Background()
	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	recipe := &domain.MLRecipe{ID: uuid.New(), Name: "retry", Version: "1", Steps: []domain.MLRecipeStep{{Action: "publish_artifact", RetryPolicy: map[string]any{"max_attempts": 2}}}}
	run := &domain.MLRecipeRun{ID: uuid.New(), RecipeID: recipe.ID, RequestedBy: "operator", Status: domain.RunStatusQueued, CreatedAt: time.Now().UTC()}
	_ = repo.UpsertRecipe(ctx, recipe)
	_ = repo.UpsertRecipeRun(ctx, run)
	dispatcher := &fakeMLRecipeDispatcher{errs: []error{errors.New("transient push failure")}, results: []*MLRecipeJobDispatchResult{nil, {Status: domain.RunStatusSucceeded}}}
	coordinator := NewMLRecipeCoordinator(registry, dispatcher, zap.NewNop())

	if err := coordinator.ProcessRecipeRun(ctx, run.ID); err != nil {
		t.Fatalf("process with explicit retry: %v", err)
	}
	stored, _ := registry.GetRecipeRun(ctx, run.ID)
	if stored.Status != domain.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", stored.Status)
	}
	if got := metadataInt(stored.StepStates[0].Metadata, "attempts"); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestMLRecipeCoordinatorLeaseRecoveryRequeuesRunningStep(t *testing.T) {
	ctx := context.Background()
	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
	recipe := &domain.MLRecipe{ID: uuid.New(), Name: "lease", Version: "1", Steps: []domain.MLRecipeStep{{Action: "convert_model"}}}
	old := time.Now().UTC().Add(-time.Hour)
	run := &domain.MLRecipeRun{ID: uuid.New(), RecipeID: recipe.ID, RequestedBy: "operator", Status: domain.RunStatusRunning, StepStates: []domain.MLRecipeRunStepState{{Index: 0, Action: "convert_model", Status: string(domain.RunStatusRunning), StartedAt: &old}}, CreatedAt: old, UpdatedAt: old}
	_ = repo.UpsertRecipe(ctx, recipe)
	_ = repo.UpsertRecipeRun(ctx, run)
	repo.mu.Lock()
	repo.recipeRuns[run.ID].UpdatedAt = old
	repo.recipeRuns[run.ID].StartedAt = &old
	repo.mu.Unlock()
	dispatcher := &fakeMLRecipeDispatcher{results: []*MLRecipeJobDispatchResult{{Status: domain.RunStatusSucceeded}}}
	coordinator := NewMLRecipeCoordinator(registry, dispatcher, zap.NewNop(), WithMLRecipeCoordinatorConfig(MLRecipeCoordinatorConfig{StaleRunTimeout: time.Second}))

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	stored, _ := registry.GetRecipeRun(ctx, run.ID)
	if stored.Status != domain.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", stored.Status)
	}
	if repo.requeued == 0 {
		t.Fatalf("expected stale lease to be requeued")
	}
	if !metadataBool(stored.StepStates[0].Metadata, "lease_recovered") {
		t.Fatalf("step metadata missing lease_recovered: %#v", stored.StepStates[0].Metadata)
	}
}
