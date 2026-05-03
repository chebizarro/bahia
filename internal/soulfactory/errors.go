package soulfactory

import (
	"context"
	"errors"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrSoulFactoryUnavailable is returned when a surface cannot perform a real
// event-driven Soul Factory operation in the current build/configuration.
var ErrSoulFactoryUnavailable = errors.New("soul factory unavailable: real event transport/provisioning engine is not configured")

// ErrLifecycleUnsupported is returned for lifecycle paths that would otherwise
// acknowledge a no-op as success.
var ErrLifecycleUnsupported = errors.New("soul lifecycle action unsupported by configured provisioning engine")

// ErrDeployableArtifactRequired is returned when deployment registration would
// require inventing an artifact image/digest.
var ErrDeployableArtifactRequired = errors.New("deployable artifact image reference is required")

// ErrWorkspaceNotConfigured is returned when workspace creation cannot push to
// a real remote repository.
var ErrWorkspaceNotConfigured = errors.New("workspace remote is not configured")

type unavailableProvisioningEngine struct{}

func (unavailableProvisioningEngine) Provision(context.Context, *domain.ProvisioningRequest, *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	return nil, ErrSoulFactoryUnavailable
}

func (unavailableProvisioningEngine) SuspendSoul(context.Context, string, string) error {
	return ErrSoulFactoryUnavailable
}

func (unavailableProvisioningEngine) ResumeSoul(context.Context, string) error {
	return ErrSoulFactoryUnavailable
}

func (unavailableProvisioningEngine) RevokeSoul(context.Context, string, string) error {
	return ErrSoulFactoryUnavailable
}

func (unavailableProvisioningEngine) RegenerateSoul(context.Context, string, string) error {
	return ErrSoulFactoryUnavailable
}

func (unavailableProvisioningEngine) RedeploySoul(context.Context, string) error {
	return ErrSoulFactoryUnavailable
}
