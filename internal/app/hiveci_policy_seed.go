package app

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type hiveCIPolicyEntityView interface {
	ListServices(context.Context) ([]domain.Service, error)
	ListEnvironments(context.Context) ([]service.AdoptionEnvironment, error)
}

type hiveCIPolicyWriter interface {
	EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error
}

// ensureConfiguredHiveCIPipelinePolicies resolves config names only against
// relay-derived local records after catch-up. Resolution is completed before
// publishing any policy, so a missing or ambiguous entity fails closed.
func ensureConfiguredHiveCIPipelinePolicies(ctx context.Context, policies []config.HiveCIPolicyConfig, view hiveCIPolicyEntityView, writer hiveCIPolicyWriter) error {
	if len(policies) == 0 {
		return nil
	}
	if view == nil || writer == nil {
		return fmt.Errorf("Hive-CI policy canonical view or publisher unavailable")
	}
	services, err := view.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list canonical services for Hive-CI policies: %w", err)
	}
	environments, err := view.ListEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("list canonical environments for Hive-CI policies: %w", err)
	}
	resolved := make([]domain.HiveCIPipelinePolicy, 0, len(policies))
	seen := make(map[string]domain.HiveCIPipelinePolicy, len(policies))
	for i, pc := range policies {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(pc.RepoCoordinate) == "" || strings.TrimSpace(pc.WorkflowPath) == "" || strings.TrimSpace(pc.ServiceName) == "" || strings.TrimSpace(pc.EnvironmentName) == "" {
			return fmt.Errorf("Hive-CI policy %d has an incomplete repository, workflow, service or environment coordinate", i)
		}
		var svc *domain.Service
		for j := range services {
			if services[j].Name != pc.ServiceName {
				continue
			}
			if svc != nil {
				return fmt.Errorf("Hive-CI policy %d service name %q is ambiguous in canonical state", i, pc.ServiceName)
			}
			svc = &services[j]
		}
		var env *domain.Environment
		for j := range environments {
			if environments[j].Environment.Name != pc.EnvironmentName {
				continue
			}
			if env != nil {
				return fmt.Errorf("Hive-CI policy %d environment name %q is ambiguous in canonical state", i, pc.EnvironmentName)
			}
			env = &environments[j].Environment
		}
		if svc == nil || env == nil {
			return fmt.Errorf("Hive-CI policy %d references a service or environment absent from canonical state", i)
		}
		if svc.OrgID != uuid.Nil && env.OrgID != uuid.Nil && svc.OrgID != env.OrgID {
			return fmt.Errorf("Hive-CI policy %d service and environment belong to different organizations", i)
		}
		enabled := pc.Enabled == nil || *pc.Enabled
		policy := domain.HiveCIPipelinePolicy{
			RepoCoordinate: pc.RepoCoordinate, WorkflowPath: pc.WorkflowPath, BranchPattern: pc.BranchPattern,
			ServiceID: svc.ID, EnvironmentID: env.ID, Enabled: enabled, Metadata: pc.Metadata,
		}
		key := pc.RepoCoordinate + "\x00" + pc.WorkflowPath + "\x00" + pc.BranchPattern + "\x00" + svc.ID.String() + "\x00" + env.ID.String()
		if prior, ok := seen[key]; ok {
			if prior.Enabled != policy.Enabled || !reflect.DeepEqual(prior.Metadata, policy.Metadata) {
				return fmt.Errorf("Hive-CI policy %d conflicts with another configured policy for the same canonical coordinate", i)
			}
			continue
		}
		seen[key] = policy
		resolved = append(resolved, policy)
	}
	for i := range resolved {
		if err := writer.EnsurePipelinePolicy(ctx, resolved[i]); err != nil {
			return fmt.Errorf("publish configured Hive-CI policy %d: %w", i, err)
		}
	}
	return nil
}
