package readmodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// EnvironmentDeploymentUnitReader provides the deployment-unit reads needed to
// build the resolved environment details model.
type EnvironmentDeploymentUnitReader interface {
	ListByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error)
	ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error)
}

// EnvironmentResponse builds the environment details read model used by both
// REST and signed ContextVM reads.
func EnvironmentResponse(ctx context.Context, env *domain.Environment, units EnvironmentDeploymentUnitReader) (*dto.EnvironmentResponse, error) {
	if env == nil {
		return nil, fmt.Errorf("environment is nil")
	}
	var resolved []domain.DeploymentUnit
	if units != nil {
		var err error
		resolved, err = units.ListByEnvironment(ctx, env.ID)
		if err != nil {
			return nil, fmt.Errorf("list deployment units for environment %s: %w", env.ID, err)
		}
	}
	if len(resolved) == 0 {
		var (
			implicit *domain.DeploymentUnit
			err      error
		)
		if units != nil {
			implicit, err = units.ResolveDefault(ctx, env)
		} else {
			envCopy := *env
			domain.NormalizeEnvironmentTargeting(&envCopy)
			if envCopy.Targeting.DefaultUnitKey != domain.DefaultDeploymentUnitKey {
				err = fmt.Errorf("configured default deployment unit %q cannot be resolved without a deployment unit repository: %w", envCopy.Targeting.DefaultUnitKey, repository.ErrConflict)
			} else {
				implicit, err = domain.NewImplicitDefaultDeploymentUnit(&envCopy)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("resolve default deployment unit for environment %s: %w", env.ID, err)
		}
		if implicit == nil {
			return nil, fmt.Errorf("default deployment unit was not found for environment %s", env.ID)
		}
		resolved = []domain.DeploymentUnit{*implicit}
	}
	return &dto.EnvironmentResponse{Environment: *env, DeploymentUnits: resolved}, nil
}
