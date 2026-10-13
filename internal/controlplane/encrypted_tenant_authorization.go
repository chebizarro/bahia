package controlplane

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type encryptedServiceLoader interface {
	GetByID(context.Context, uuid.UUID) (*domain.Service, error)
}

type encryptedEnvironmentLoader interface {
	GetEnvironment(context.Context, uuid.UUID) (*domain.Environment, error)
}

type encryptedTenantAuthorizer struct {
	services     encryptedServiceLoader
	environments encryptedEnvironmentLoader
	rbac         *auth.RBAC
}

func (a encryptedTenantAuthorizer) authorizeOrg(ctx context.Context, event *nostr.Event, orgID uuid.UUID, permission domain.Permission) error {
	if orgID == uuid.Nil {
		return fmt.Errorf("organization is required")
	}
	if event == nil {
		return fmt.Errorf("signed ContextVM request event is required")
	}
	if a.rbac == nil {
		return fmt.Errorf("tenant RBAC is not configured")
	}
	return a.rbac.CheckPermission(ctx, requestPrincipal(EncryptedRequest{Event: event}), orgID, permission)
}

func (a encryptedTenantAuthorizer) authorizeService(ctx context.Context, event *nostr.Event, serviceID uuid.UUID, permission domain.Permission) (*domain.Service, error) {
	if serviceID == uuid.Nil {
		return nil, fmt.Errorf("service_id is required")
	}
	if a.services == nil {
		return nil, fmt.Errorf("service repository is not configured")
	}
	svc, err := a.services.GetByID(ctx, serviceID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("service not found")
		}
		return nil, fmt.Errorf("failed to fetch service")
	}
	if svc == nil {
		return nil, fmt.Errorf("service not found")
	}
	if err := a.authorizeOrg(ctx, event, svc.OrgID, permission); err != nil {
		return nil, err
	}
	return svc, nil
}

func (a encryptedTenantAuthorizer) authorizeEnvironment(ctx context.Context, event *nostr.Event, environmentID uuid.UUID, permission domain.Permission) (*domain.Environment, error) {
	if environmentID == uuid.Nil {
		return nil, fmt.Errorf("environment_id is required")
	}
	if a.environments == nil {
		return nil, fmt.Errorf("environment repository is not configured")
	}
	env, err := a.environments.GetEnvironment(ctx, environmentID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("environment not found")
		}
		return nil, fmt.Errorf("failed to fetch environment")
	}
	if env == nil {
		return nil, fmt.Errorf("environment not found")
	}
	if err := a.authorizeOrg(ctx, event, env.OrgID, permission); err != nil {
		return nil, err
	}
	return env, nil
}

func (a encryptedTenantAuthorizer) authorizeServiceEnvironment(
	ctx context.Context,
	event *nostr.Event,
	serviceID, environmentID uuid.UUID,
	servicePermission, environmentPermission domain.Permission,
) (*domain.Service, *domain.Environment, error) {
	svc, err := a.authorizeService(ctx, event, serviceID, servicePermission)
	if err != nil {
		return nil, nil, err
	}
	env, err := a.authorizeEnvironment(ctx, event, environmentID, environmentPermission)
	if err != nil {
		return nil, nil, err
	}
	if svc.OrgID == uuid.Nil || env.OrgID == uuid.Nil || svc.OrgID != env.OrgID {
		return nil, nil, fmt.Errorf("service and environment must belong to the same organization")
	}
	return svc, env, nil
}
