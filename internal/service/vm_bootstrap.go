package service

import (
	"context"
	"reflect"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type VMAuditedSecretResolver interface {
	ResolveSecretWithAudit(context.Context, string, domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error)
}
type VMBootstrapService struct {
	secrets     repository.SecretRepository
	resolver    VMAuditedSecretResolver
	permissions VMPermissionChecker
}

func NewVMBootstrapService(secrets repository.SecretRepository, resolver VMAuditedSecretResolver, permissions VMPermissionChecker) (*VMBootstrapService, error) {
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
	for _, binding := range v.Bootstrap {
		value, manifest, err := d.service.resolver.ResolveSecretWithAudit(ctx, binding.Ref.ID.String(), domain.SecretResolveOptions{Actor: op.Actor, Reason: "persistent VM bootstrap", RequestID: op.ID.String(), Operation: domain.SecretAccessOperationRuntimeApply})
		if err != nil {
			return vmError(domain.VMErrorUnavailable)
		}
		if manifest.SecretID != binding.Ref.ID || manifest.ServiceID != *v.ServiceID || (manifest.EnvironmentID != nil && *manifest.EnvironmentID != *v.EnvironmentID) || manifest.VersionID == uuid.Nil || manifest.Version < 1 || manifest.Outcome != domain.SecretAccessOutcomeSuccess {
			return vmError(domain.VMErrorIntegrity)
		}
		payload := []byte(value)
		value = ""
		err = guest.ApplyBootstrap(ctx, binding.TargetKey, payload)
		clear(payload)
		audit := &domain.SecretAccessAudit{SecretID: manifest.SecretID, VersionID: manifest.VersionID, Version: manifest.Version, ServiceID: manifest.ServiceID, EnvironmentID: manifest.EnvironmentID, Operation: domain.SecretAccessOperationRuntimeApply, Outcome: domain.SecretAccessOutcomeSuccess, Actor: op.Actor, Reason: "persistent VM bootstrap delivery", RequestID: op.ID.String(), AccessedAt: manifest.AccessedAt}
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
