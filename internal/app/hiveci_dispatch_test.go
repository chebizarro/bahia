package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	giteaAdapter "github.com/openagentsinc/bahia/internal/adapters/gitea"
	hiveciAdapter "github.com/openagentsinc/bahia/internal/adapters/hiveci"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type dispatchPinner struct {
	pins  []giteaAdapter.PinnedBuildDependency
	err   error
	specs []giteaAdapter.BuildDependencySpec
}

func (p *dispatchPinner) Resolve(_ context.Context, specs []giteaAdapter.BuildDependencySpec) ([]giteaAdapter.PinnedBuildDependency, error) {
	p.specs = append([]giteaAdapter.BuildDependencySpec(nil), specs...)
	return p.pins, p.err
}

type dispatchSubmitter struct {
	jobs []loom.JobRequest
}

func (s *dispatchSubmitter) SubmitJob(_ context.Context, job loom.JobRequest) (string, error) {
	s.jobs = append(s.jobs, job)
	return "loom-job-id", nil
}

func astilleroDispatchPolicy() config.HiveCIPolicyConfig {
	return config.HiveCIPolicyConfig{
		RepoCoordinate: "30617:owner:astillero", WorkflowPath: ".gitea/workflows/release.yml",
		ServiceName: "astillero", EnvironmentName: "edge-01-production",
		BuildDependencies: []config.HiveCIBuildDependencyConfig{
			{Name: "cascadia-go", CloneURL: "https://git.sharegap.net/cascadia/cascadia-go.git"},
			{Name: "drydock", CloneURL: "https://git.sharegap.net/cascadia/drydock.git"},
		},
	}
}

func astilleroWorkflowDispatch() hiveciAdapter.WorkflowRunDispatch {
	return hiveciAdapter.WorkflowRunDispatch{
		RunEventID: "5401-event-id", RepoCoordinate: "30617:owner:astillero",
		Repository: "https://git.sharegap.net/cascadia/astillero.git", Ref: strings.Repeat("c", 40),
		Workflow: ".gitea/workflows/release.yml", CommitSHA: strings.Repeat("c", 40), Release: true,
	}
}

func TestHiveCIRunDispatcherSubmitsOnlyFleetAuthorizedImmutableDependencies(t *testing.T) {
	pinner := &dispatchPinner{pins: []giteaAdapter.PinnedBuildDependency{
		{Name: "cascadia-go", CloneURL: "https://git.sharegap.net/cascadia/cascadia-go.git", CommitSHA: strings.Repeat("a", 40)},
		{Name: "drydock", CloneURL: "https://git.sharegap.net/cascadia/drydock.git", CommitSHA: strings.Repeat("b", 40)},
	}}
	submitter := &dispatchSubmitter{}
	dispatcher := newHiveCIRunDispatcher([]config.HiveCIPolicyConfig{astilleroDispatchPolicy()}, pinner, submitter, zap.NewNop())
	dispatcher.Dispatch(t.Context(), astilleroWorkflowDispatch())
	if len(submitter.jobs) != 1 {
		t.Fatalf("submitted jobs = %d, want 1", len(submitter.jobs))
	}
	job := submitter.jobs[0]
	if len(pinner.specs) != 2 || len(job.BuildDependencies) != 2 {
		t.Fatalf("resolved specs=%#v job deps=%#v", pinner.specs, job.BuildDependencies)
	}
	for _, dependency := range job.BuildDependencies {
		if len(dependency.CommitSHA) != 40 {
			t.Fatalf("dependency is not immutable: %#v", dependency)
		}
	}
	if job.ID != "5401-event-id" || job.PaymentToken != "" || fmt.Sprint(job.RequiredSoftware) != "[git act docker]" {
		t.Fatalf("existing dispatch invariants changed: %#v", job)
	}
}

func TestHiveCIRunDispatcherResolutionFailurePreventsPublish(t *testing.T) {
	pinner := &dispatchPinner{err: fmt.Errorf("dependency default branch unavailable")}
	submitter := &dispatchSubmitter{}
	dispatcher := newHiveCIRunDispatcher([]config.HiveCIPolicyConfig{astilleroDispatchPolicy()}, pinner, submitter, zap.NewNop())
	dispatcher.Dispatch(t.Context(), astilleroWorkflowDispatch())
	if len(submitter.jobs) != 0 {
		t.Fatalf("unresolved dependency submitted %d jobs", len(submitter.jobs))
	}
}

func TestHiveCIRunDispatcherRequiresMatchingFleetServicePolicy(t *testing.T) {
	submitter := &dispatchSubmitter{}
	dispatcher := newHiveCIRunDispatcher([]config.HiveCIPolicyConfig{astilleroDispatchPolicy()}, &dispatchPinner{}, submitter, zap.NewNop())
	run := astilleroWorkflowDispatch()
	run.RepoCoordinate = "30617:attacker:other"
	dispatcher.Dispatch(t.Context(), run)
	if len(submitter.jobs) != 0 {
		t.Fatalf("unconfigured repository submitted %d jobs", len(submitter.jobs))
	}
}

type unsafeDependencyRepoClient struct{}

func (unsafeDependencyRepoClient) GetRepo(context.Context, string, string) (*giteaAdapter.RepoInfo, error) {
	return &giteaAdapter.RepoInfo{DefaultBranch: "main"}, nil
}

func (unsafeDependencyRepoClient) ResolveRef(context.Context, string, string, string) (string, error) {
	return strings.Repeat("a", 40), nil
}

func TestHiveCIRunDispatcherDoesNotLogCredentialBearingDependencyURL(t *testing.T) {
	secret := "dependency-password-must-not-leak"
	policy := astilleroDispatchPolicy()
	policy.BuildDependencies[0].CloneURL = "https://user:" + secret + "@git.sharegap.net/cascadia/cascadia-go.git"
	pinner, err := giteaAdapter.NewBuildDependencyResolver(unsafeDependencyRepoClient{}, "https://git.sharegap.net")
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	core, logs := observer.New(zap.DebugLevel)
	submitter := &dispatchSubmitter{}
	dispatcher := newHiveCIRunDispatcher([]config.HiveCIPolicyConfig{policy}, pinner, submitter, zap.New(core))
	dispatcher.Dispatch(t.Context(), astilleroWorkflowDispatch())
	if len(submitter.jobs) != 0 {
		t.Fatal("unsafe dependency was submitted")
	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message+fmt.Sprint(entry.ContextMap()), secret) {
			t.Fatal("dependency credential reached dispatcher logs")
		}
	}
}
