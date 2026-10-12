package service

import (
	"context"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// VMBootstrapService authorizes and delivers a persistent VM's bootstrap
// secrets. Values resolve through the secret store at delivery and reach the
// guest from the store's buffer, which is zeroized afterwards.
type VMBootstrapService struct {
	secrets     repository.SecretRepository
	resolver    SecretValues
	permissions VMPermissionChecker
}

func NewVMBootstrapService(secrets repository.SecretRepository, resolver SecretValues, permissions VMPermissionChecker) (*VMBootstrapService, error) {
	if secrets == nil || resolver == nil || permissions == nil {
		return nil, domain.ErrInvalidValue
	}
	return &VMBootstrapService{secrets, resolver, permissions}, nil
}
func (s *VMBootstrapService) Authorize(ctx context.Context, p *auth.Principal, v domain.PersistentVMDeployment) error {
	if err := domain.ValidateVMBootstrapBindings(v.Bootstrap); err != nil {
		return err
	}
	if len(v.Bootstrap) == 0 {
		return nil
	}
	if s == nil || s.secrets == nil || s.resolver == nil || s.permissions == nil || p == nil || !p.IsAuthenticated() || v.ServiceID == nil || v.EnvironmentID == nil {
		return vmError(domain.VMErrorInvalid)
	}
	if err := s.permissions.CheckPermission(ctx, p, v.OrgID, domain.PermReadSecrets); err != nil {
		return vmError(domain.VMErrorInvalid)
	}
	for _, binding := range v.Bootstrap {
		secret, err := s.secrets.GetByID(ctx, binding.Ref.ID)
		if err != nil || secret == nil || secret.ID != binding.Ref.ID || secret.ServiceID != *v.ServiceID || (secret.EnvironmentID != nil && *secret.EnvironmentID != *v.EnvironmentID) {
			return vmError(domain.VMErrorInvalid)
		}
		if secret.EncryptionMethod != domain.EncryptionStoreRef {
			return domain.LegacySecretValueError(secret.ID, secret.Name)
		}
	}
	return nil
}
func (s *VMBootstrapService) Delivery(v domain.PersistentVMDeployment, op domain.VMOperation) domain.VMBootstrapDelivery {
	return &vmBootstrapDelivery{service: s, deployment: v, operation: op}
}

type vmBootstrapDelivery struct {
	service    *VMBootstrapService
	deployment domain.PersistentVMDeployment
	operation  domain.VMOperation
}

func (*vmBootstrapDelivery) MarshalJSON() ([]byte, error) { return nil, domain.ErrInvalidValue }
func (d *vmBootstrapDelivery) Deliver(ctx context.Context, identity domain.VMResourceIdentity, guest domain.VMBootstrapGuest) error {
	v, op := d.deployment, d.operation
	if guest == nil || !reflect.DeepEqual(identity, v.Identity) || op.OrgID != v.OrgID || op.ResourceID != v.ID || op.ResourceGeneration != v.Generation || op.Phase != domain.VMOperationExecuting || v.BootstrapApplied {
		return vmError(domain.VMErrorInvalid)
	}
	p := vmExecutionPrincipal(op.Actor)
	if err := d.service.Authorize(ctx, p, v); err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(v.Bootstrap))
	for _, binding := range v.Bootstrap {
		ids = append(ids, binding.Ref.ID)
	}
	resolved, err := d.service.resolver.ResolveAll(ctx, ids)
	if err != nil || len(resolved) != len(ids) {
		secretstore.ZeroizeResolved(resolved)
		return vmError(domain.VMErrorUnavailable)
	}
	defer secretstore.ZeroizeResolved(resolved)
	for i, binding := range v.Bootstrap {
		secret := resolved[i].Secret
		if secret.ID != binding.Ref.ID || secret.ServiceID != *v.ServiceID || (secret.EnvironmentID != nil && *secret.EnvironmentID != *v.EnvironmentID) || secret.CredentialRef == nil {
			return vmError(domain.VMErrorIntegrity)
		}
		err := guest.ApplyBootstrap(ctx, binding.TargetKey, resolved[i].Value.Bytes())
		audit := &domain.SecretAccessAudit{SecretID: secret.ID, Version: secret.CredentialRef.Version, ServiceID: secret.ServiceID, EnvironmentID: secret.EnvironmentID, Operation: domain.SecretAccessOperationRuntimeApply, Outcome: domain.SecretAccessOutcomeSuccess, Actor: op.Actor, Reason: "persistent VM bootstrap delivery", RequestID: op.ID.String(), AccessedAt: time.Now().UTC()}
		if err != nil {
			audit.Outcome = domain.SecretAccessOutcomeFailure
			audit.Error = string(domain.VMErrorUnconfirmed)
		}
		if auditErr := d.service.secrets.RecordSecretAccessAudit(ctx, audit); auditErr != nil {
			return vmError(domain.VMErrorUnconfirmed)
		}
		if err != nil {
			return vmError(domain.VMErrorUnconfirmed)
		}
	}
	return nil
}
func vmExecutionPrincipal(actor string) *auth.Principal {
	return &auth.Principal{Subject: actor, PubKey: actor, Method: auth.MethodSystem}
}
