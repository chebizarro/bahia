package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/signing"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type virtualizationPolicy struct {
	config   config.VirtualizationConfig
	rbac     *auth.RBAC
	events   repository.NostrEventRepository
	secrets  repository.SecretRepository
	services repository.ServiceRepository
}

func (p *virtualizationPolicy) operator(_ context.Context, principal *auth.Principal) error {
	if principal == nil || !principal.IsAuthenticated() || principal.PubKey == "" || !slices.Contains(p.config.OperatorPubkeys, principal.PubKey) {
		return &auth.AccessDeniedError{Reason: "VM operator required"}
	}
	return nil
}
func (p *virtualizationPolicy) host(h *domain.VirtualizationHost) (*config.VirtualizationHostPolicy, error) {
	if h != nil && h.Enabled {
		for _, policy := range p.config.Hosts {
			if policy.HostID == h.ID && policy.OrgID == h.OrgID && policy.TrustPolicyRef == h.TrustPolicyRef {
				return &policy, nil
			}
		}
	}
	return nil, &domain.VMProviderError{Code: domain.VMErrorUnavailable}
}
func (p *virtualizationPolicy) AuthorizeExecutionPlane(ctx context.Context, principal *auth.Principal, org uuid.UUID) error {
	if err := p.operator(ctx, principal); err != nil {
		return err
	}
	if p.rbac == nil {
		return &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	return p.rbac.CheckPermission(ctx, principal, org, domain.PermWriteDeployments)
}
func (p *virtualizationPolicy) VerifyPlaneProvenance(ctx context.Context, host *domain.VirtualizationHost, digest string, provenance domain.VMProvenance) error {
	policy, err := p.host(host)
	if err != nil {
		return err
	}
	invalid := &domain.VMProviderError{Code: domain.VMErrorIntegrity}
	if p.events == nil || !slices.Contains(policy.TrustedSigners, provenance.Signer) {
		return invalid
	}
	record, err := p.events.GetByID(ctx, provenance.EventID)
	if err != nil || record == nil || record.ID != provenance.EventID || record.PubKey != provenance.Signer || record.CreatedAt.After(time.Now()) {
		return invalid
	}
	ev := nostr.Event{Kind: nostr.Kind(record.Kind), Content: record.Content, CreatedAt: nostr.Timestamp(record.CreatedAt.Unix())}
	ev.ID, err = nostr.IDFromHex(record.ID)
	if err != nil {
		return invalid
	}
	ev.PubKey, err = nostr.PubKeyFromHex(record.PubKey)
	if err != nil {
		return invalid
	}
	sig, err := hex.DecodeString(record.Sig)
	if err != nil || len(sig) != len(ev.Sig) {
		return invalid
	}
	copy(ev.Sig[:], sig)
	if json.Unmarshal(record.Tags, &ev.Tags) != nil {
		return invalid
	}
	// Reuse the existing signed artifact attestation contract; catalog Verified
	// and VerifiedAt fields are never authority. Verification failures stay opaque.
	verified, err := signing.NewNostrVerifier(policy.TrustedSigners, zap.NewNop()).VerifyEvent(ctx, &ev, &domain.Artifact{ImageDigest: digest})
	if err != nil || verified == nil || !verified.Verified {
		return invalid
	}
	return nil
}
func (p *virtualizationPolicy) ValidatePlaneConfiguration(_ context.Context, h *domain.VirtualizationHost, c domain.ExecutionPlaneConfiguration) error {
	policy, err := p.host(h)
	if err != nil {
		return err
	}
	for _, binding := range c.SecretBindings {
		if !slices.Contains(policy.PlaneSecretRefs, binding.Ref.ID) {
			return domain.ErrInvalidValue
		}
	}
	if len(c.Network.PassthroughDeviceRefs) != 0 {
		return &domain.VMProviderError{Code: domain.VMErrorApprovalRequired}
	}
	if c.Network.Mode == domain.VMNetworkIsolated && c.Network.NetworkRef == nil {
		return nil
	}
	// D has no destructive approval contract: do not enable bridged networking
	// through plane configuration as a bypass of C's destructive tier.
	if c.Network.Mode == domain.VMNetworkNAT && c.Network.NetworkRef != nil {
		for _, n := range policy.Networks {
			if n.Ref == *c.Network.NetworkRef && n.Mode == c.Network.Mode {
				return nil
			}
		}
	}
	return &domain.VMProviderError{Code: domain.VMErrorApprovalRequired}
}
func (p *virtualizationPolicy) AuthorizePlaneSecret(ctx context.Context, principal *auth.Principal, org uuid.UUID, ref domain.SecretRef) error {
	if p.secrets == nil || p.services == nil || p.rbac == nil {
		return &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	if err := p.rbac.CheckPermission(ctx, principal, org, domain.PermReadSecrets); err != nil {
		return err
	}
	allowed := false
	for _, h := range p.config.Hosts {
		if h.OrgID == org && slices.Contains(h.PlaneSecretRefs, ref.ID) {
			allowed = true
		}
	}
	if !allowed {
		return domain.ErrInvalidValue
	}
	secret, err := p.secrets.GetByID(ctx, ref.ID)
	if err != nil || secret == nil {
		return domain.ErrInvalidValue
	}
	svc, err := p.services.GetByID(ctx, secret.ServiceID)
	if err != nil || svc == nil || svc.OrgID != org {
		return domain.ErrInvalidValue
	}
	return nil
}
