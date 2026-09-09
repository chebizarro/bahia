package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	giteaAdapter "github.com/openagentsinc/bahia/internal/adapters/gitea"
	hiveciAdapter "github.com/openagentsinc/bahia/internal/adapters/hiveci"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const hiveCIDependencyResolveTimeout = 30 * time.Second

type hiveCIDependencyPinner interface {
	Resolve(context.Context, []giteaAdapter.BuildDependencySpec) ([]giteaAdapter.PinnedBuildDependency, error)
}

type hiveCIJobSubmitter interface {
	SubmitJob(context.Context, loom.JobRequest) (string, error)
}

type hiveCIRunDispatcher struct {
	policies  []config.HiveCIPolicyConfig
	pinner    hiveCIDependencyPinner
	submitter hiveCIJobSubmitter
	logger    *zap.Logger
}

func hiveCIHasBuildDependencies(policies []config.HiveCIPolicyConfig) bool {
	for _, policy := range policies {
		if (policy.Enabled == nil || *policy.Enabled) && len(policy.BuildDependencies) > 0 {
			return true
		}
	}
	return false
}

func hiveCIBuildDependencyAuthorizations(ctx context.Context, policies []config.HiveCIPolicyConfig, services repository.ServiceRepository) ([]giteaAdapter.BuildDependencyAuthorization, error) {
	if !hiveCIHasBuildDependencies(policies) {
		return nil, nil
	}
	authorizations := make([]giteaAdapter.BuildDependencyAuthorization, 0, len(policies))
	seen := make(map[string]struct{})
	for _, policy := range policies {
		if policy.Enabled != nil && !*policy.Enabled {
			continue
		}
		serviceName := strings.TrimSpace(policy.ServiceName)
		if _, duplicate := seen[serviceName]; duplicate {
			continue
		}
		seen[serviceName] = struct{}{}
		service, err := services.GetByName(ctx, serviceName)
		if err != nil {
			return nil, fmt.Errorf("resolve Hive-CI dependency service %q: %w", serviceName, err)
		}
		if service == nil {
			return nil, fmt.Errorf("resolve Hive-CI dependency service %q: service not found", serviceName)
		}
		dependencies := make([]giteaAdapter.BuildDependencySpec, 0, len(policy.BuildDependencies))
		for _, dependency := range policy.BuildDependencies {
			dependencies = append(dependencies, giteaAdapter.BuildDependencySpec{Name: dependency.Name, CloneURL: dependency.CloneURL})
		}
		authorizations = append(authorizations, giteaAdapter.BuildDependencyAuthorization{ServiceID: service.ID, Dependencies: dependencies})
	}
	return authorizations, nil
}

func newHiveCIRunDispatcher(policies []config.HiveCIPolicyConfig, pinner hiveCIDependencyPinner, submitter hiveCIJobSubmitter, logger *zap.Logger) *hiveCIRunDispatcher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &hiveCIRunDispatcher{policies: policies, pinner: pinner, submitter: submitter, logger: logger}
}

func (d *hiveCIRunDispatcher) Dispatch(ctx context.Context, run hiveciAdapter.WorkflowRunDispatch) {
	if !run.Release {
		return
	}
	if strings.TrimSpace(run.Repository) == "" || strings.TrimSpace(run.Ref) == "" {
		d.logger.Warn("release workflow run is missing repository/ref; refusing Loom dispatch",
			zap.String("reason", "release_run_missing_repo_ref"), zap.String("run_event_id", run.RunEventID))
		return
	}
	policy, err := d.authorizedPolicy(run)
	if err != nil {
		d.logger.Warn("release workflow run has no unambiguous fleet dependency policy; refusing Loom dispatch",
			zap.String("reason", "build_dependency_policy_denied"), zap.String("run_event_id", run.RunEventID), zap.Error(err))
		return
	}

	specs := make([]giteaAdapter.BuildDependencySpec, 0, len(policy.BuildDependencies))
	for _, dependency := range policy.BuildDependencies {
		specs = append(specs, giteaAdapter.BuildDependencySpec{Name: dependency.Name, CloneURL: dependency.CloneURL})
	}
	pins := make([]giteaAdapter.PinnedBuildDependency, 0, len(specs))
	if len(specs) > 0 {
		if d.pinner == nil {
			d.logger.Error("fleet build dependency resolver is unavailable; refusing Loom dispatch",
				zap.String("reason", "build_dependency_resolver_unavailable"), zap.String("run_event_id", run.RunEventID))
			return
		}
		resolveCtx, cancel := context.WithTimeout(ctx, hiveCIDependencyResolveTimeout)
		pins, err = d.pinner.Resolve(resolveCtx, specs)
		cancel()
		if err != nil {
			d.logger.Error("failed to pin fleet-authorized build dependencies; refusing Loom dispatch",
				zap.String("reason", "build_dependency_resolution_failed"), zap.String("run_event_id", run.RunEventID), zap.Error(err))
			return
		}
	}

	dependencies := make([]loom.BuildDependency, 0, len(pins))
	for _, pin := range pins {
		dependencies = append(dependencies, loom.BuildDependency{Name: pin.Name, CloneURL: pin.CloneURL, CommitSHA: pin.CommitSHA})
	}
	jobID, err := d.submitter.SubmitJob(ctx, loom.JobRequest{
		ID:                run.RunEventID,
		Type:              "build",
		RequiredSoftware:  []string{"git", "act", "docker"},
		BuildDependencies: dependencies,
		Params: map[string]string{
			"method":   "ci/workflow-run",
			"run":      run.RunEventID,
			"repo":     run.Repository,
			"ref":      run.Ref,
			"workflow": run.Workflow,
			"event":    "push",
		},
	})
	if err != nil {
		d.logger.Error("failed to dispatch release workflow to Loom",
			zap.String("reason", "loom_dispatch_failed"), zap.String("run_event_id", run.RunEventID), zap.Error(err))
		return
	}
	d.logger.Info("release workflow dispatched to Loom",
		zap.String("run_event_id", run.RunEventID), zap.String("loom_job_id", jobID),
		zap.String("ref", run.Ref), zap.String("commit", run.CommitSHA), zap.Int("build_dependencies", len(dependencies)))
}

func (d *hiveCIRunDispatcher) authorizedPolicy(run hiveciAdapter.WorkflowRunDispatch) (*config.HiveCIPolicyConfig, error) {
	var selected *config.HiveCIPolicyConfig
	var signature string
	for index := range d.policies {
		policy := &d.policies[index]
		if policy.Enabled != nil && !*policy.Enabled {
			continue
		}
		if strings.TrimSpace(policy.RepoCoordinate) != strings.TrimSpace(run.RepoCoordinate) ||
			strings.TrimSpace(policy.WorkflowPath) != strings.TrimSpace(run.Workflow) {
			continue
		}
		candidateSignature := dependencyPolicySignature(*policy)
		if selected != nil && candidateSignature != signature {
			return nil, fmt.Errorf("conflicting service dependency policies")
		}
		selected = policy
		signature = candidateSignature
	}
	if selected == nil {
		return nil, fmt.Errorf("repository and workflow are not configured")
	}
	return selected, nil
}

func dependencyPolicySignature(policy config.HiveCIPolicyConfig) string {
	dependencies := make([]string, 0, len(policy.BuildDependencies))
	for _, dependency := range policy.BuildDependencies {
		dependencies = append(dependencies, strings.TrimSpace(dependency.Name)+"\x00"+strings.TrimSpace(dependency.CloneURL))
	}
	sort.Strings(dependencies)
	return strings.TrimSpace(policy.ServiceName) + "\x00" + strings.Join(dependencies, "\x01")
}
