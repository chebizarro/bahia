package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type vmSecretRepo struct {
	repository.SecretRepository
	secret     domain.ServiceSecret
	audits     []domain.SecretAccessAudit
	auditError error
}

func (r *vmSecretRepo) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	v := r.secret
	return &v, nil
}
func (r *vmSecretRepo) RecordSecretAccessAudit(_ context.Context, a *domain.SecretAccessAudit) error {
	r.audits = append(r.audits, *a)
	return r.auditError
}

type vmSecretResolver struct {
	repo          *vmSecretRepo
	calls         int
	err           error
	wrongManifest bool
}

func (r *vmSecretResolver) ResolveSecretWithAudit(_ context.Context, id string, opts domain.SecretResolveOptions) (string, domain.SecretAccessManifest, error) {
	r.calls++
	s := r.repo.secret
	m := domain.SecretAccessManifest{SecretID: s.ID, VersionID: uuid.New(), Version: 1, ServiceID: s.ServiceID, EnvironmentID: s.EnvironmentID, Operation: opts.Operation, Outcome: domain.SecretAccessOutcomeSuccess, AccessedAt: vmTestNow}
	if r.wrongManifest {
		m.ServiceID = uuid.New()
	}
	return "SECRET-never-public", m, r.err
}

type vmBootstrapGuest struct {
	calls    int
	received string
	borrowed []byte
	err      error
}

func (g *vmBootstrapGuest) ApplyBootstrap(_ context.Context, key string, value []byte) error {
	g.calls++
	g.received = key + ":" + string(value)
	g.borrowed = value
	return g.err
}
func TestVMBootstrapAuditedSecretRefDeliveryAndRedaction(t *testing.T) {
	_, _, _, v, principal := vmFixture(t)
	serviceID, env := uuid.New(), uuid.New()
	v.ServiceID = &serviceID
	v.EnvironmentID = &env
	secret := domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: &env, Version: 1}
	v.Bootstrap = []domain.VMBootstrapBinding{{TargetKey: "ssh", Ref: domain.SecretRef{ID: secret.ID}}}
	secrets := &vmSecretRepo{secret: secret}
	resolver := &vmSecretResolver{repo: secrets}
	bootstrap, err := NewVMBootstrapService(secrets, resolver, &vmPermissionFake{})
	require.NoError(t, err)
	require.NoError(t, bootstrap.Authorize(context.Background(), principal, v))
	require.Zero(t, resolver.calls, "admission never decrypts")
	op := domain.VMOperation{VirtualizationResourceMeta: vmTestMeta(v.OrgID), ResourceID: v.ID, ResourceGeneration: v.Generation, Actor: principal.PubKey, Phase: domain.VMOperationExecuting}
	delivery := bootstrap.Delivery(v, op)
	_, err = json.Marshal(delivery)
	require.Error(t, err)
	guest := &vmBootstrapGuest{}
	require.NoError(t, delivery.Deliver(context.Background(), v.Identity, guest))
	require.Equal(t, "ssh:SECRET-never-public", guest.received)
	require.Equal(t, make([]byte, len(guest.borrowed)), guest.borrowed)
	require.Len(t, secrets.audits, 1)
	encoded, err := json.Marshal(secrets.audits)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SECRET-never-public")
	guest.err = errors.New("SECRET-never-public provider failure")
	err = delivery.Deliver(context.Background(), v.Identity, guest)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET-never-public")
	encoded, err = json.Marshal(secrets.audits)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SECRET-never-public")
	require.Equal(t, domain.SecretAccessOutcomeFailure, secrets.audits[1].Outcome)
}
func TestVMBootstrapRejectsSpoofedRefsAndWrongGuest(t *testing.T) {
	_, _, _, v, principal := vmFixture(t)
	svc, env := uuid.New(), uuid.New()
	v.ServiceID = &svc
	v.EnvironmentID = &env
	secret := domain.ServiceSecret{ID: uuid.New(), ServiceID: svc, EnvironmentID: &env}
	v.Bootstrap = []domain.VMBootstrapBinding{{TargetKey: "rdp", Ref: domain.SecretRef{ID: secret.ID}}}
	secrets := &vmSecretRepo{secret: secret}
	resolver := &vmSecretResolver{repo: secrets}
	bootstrap, err := NewVMBootstrapService(secrets, resolver, &vmPermissionFake{})
	require.NoError(t, err)
	bad := vmCopy(v)
	bad.Bootstrap[0].Ref.Name = "SECRET-never-public"
	require.Error(t, bootstrap.Authorize(context.Background(), principal, bad))
	secrets.secret.ServiceID = uuid.New()
	require.Error(t, bootstrap.Authorize(context.Background(), principal, v))
	require.Zero(t, resolver.calls)
	secrets.secret.ServiceID = svc
	op := domain.VMOperation{VirtualizationResourceMeta: vmTestMeta(v.OrgID), ResourceID: v.ID, ResourceGeneration: v.Generation, Actor: principal.PubKey, Phase: domain.VMOperationExecuting}
	guest := &vmBootstrapGuest{}
	delivery := bootstrap.Delivery(v, op)
	wrong := v.Identity
	wrong.ProviderResourceID = uuid.New()
	require.Error(t, delivery.Deliver(context.Background(), wrong, guest))
	require.Zero(t, resolver.calls)
	resolver.wrongManifest = true
	require.Error(t, delivery.Deliver(context.Background(), v.Identity, guest))
	require.Zero(t, guest.calls)
	resolver.wrongManifest = false
	resolver.err = errors.New("SECRET-never-public resolver failure")
	err = delivery.Deliver(context.Background(), v.Identity, guest)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET-never-public")
	require.Zero(t, guest.calls)
	resolver.err = nil
	secrets.auditError = errors.New("audit failure")
	require.Error(t, delivery.Deliver(context.Background(), v.Identity, guest))
}
