package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// ExecutionPlaneAdmissionPolicy binds operator/RBAC, host trust policy and secret
// authorization to existing installation services. No missing verifier is permissive.
// VerifyProvenance must verify the referenced signed evidence against the host's
// TrustPolicyRef and exact digest; the caller-provided Verified bit is not proof.
type ExecutionPlaneAdmissionPolicy interface {
	AuthorizeExecutionPlane(context.Context, *auth.Principal, uuid.UUID) error
	VerifyPlaneProvenance(context.Context, *domain.VirtualizationHost, string, domain.VMProvenance) error
	// ValidatePlaneConfiguration resolves network/device references against the
	// host's approved configuration; typed UUIDs alone do not authorize access.
	ValidatePlaneConfiguration(context.Context, *domain.VirtualizationHost, domain.ExecutionPlaneConfiguration) error
	AuthorizePlaneSecret(context.Context, *auth.Principal, uuid.UUID, domain.SecretRef) error
}

type ExecutionPlaneService struct {
	repo   repository.VirtualizationRepository
	policy ExecutionPlaneAdmissionPolicy
}

func NewExecutionPlaneService(repo repository.VirtualizationRepository, policy ExecutionPlaneAdmissionPolicy) *ExecutionPlaneService {
	return &ExecutionPlaneService{repo: repo, policy: policy}
}

func (s *ExecutionPlaneService) authorize(ctx context.Context, principal *auth.Principal, org uuid.UUID) error {
	if s == nil || s.repo == nil || s.policy == nil || principal == nil || !principal.IsAuthenticated() || principal.PubKey == "" || org == uuid.Nil {
		return &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	return s.policy.AuthorizeExecutionPlane(ctx, principal, org)
}

// ExecutionPlaneConfigRevision hashes canonical safe configuration, never secret
// values. Empty arrays and order-insensitive reference sets have one encoding.
func ExecutionPlaneConfigRevision(configuration domain.ExecutionPlaneConfiguration) (string, error) {
	configuration.Revision = ""
	configuration.SecretBindings = append([]domain.VMBootstrapBinding{}, configuration.SecretBindings...)
	sort.Slice(configuration.SecretBindings, func(i, j int) bool {
		return configuration.SecretBindings[i].TargetKey < configuration.SecretBindings[j].TargetKey
	})
	configuration.Network.PassthroughDeviceRefs = append([]uuid.UUID{}, configuration.Network.PassthroughDeviceRefs...)
	sort.Slice(configuration.Network.PassthroughDeviceRefs, func(i, j int) bool {
		return configuration.Network.PassthroughDeviceRefs[i].String() < configuration.Network.PassthroughDeviceRefs[j].String()
	})
	if err := domain.ValidateVMBootstrapBindings(configuration.SecretBindings); err != nil {
		return "", err
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidateDesired verifies reference/trust boundaries on both admission and
// reconciliation. It never resolves a SecretRef to plaintext or invokes a job.
func (s *ExecutionPlaneService) ValidateDesired(ctx context.Context, p *domain.ExecutionPlaneDeployment) error {
	if s == nil || s.repo == nil || s.policy == nil || p == nil {
		return &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	copy := *p
	copy.Observation = nil
	copy.ObservationCursor = nil
	if err := domain.ValidateExecutionPlaneDeployment(&copy); err != nil {
		return err
	}
	revision, err := ExecutionPlaneConfigRevision(p.Desired.Configuration)
	if err != nil {
		return err
	}
	if revision != p.Desired.Configuration.Revision {
		return fmt.Errorf("%w: execution-plane configuration revision", domain.ErrInvalidValue)
	}
	host, err := s.repo.Hosts().Get(ctx, p.OrgID, p.HostID)
	if err != nil {
		return err
	}
	if host == nil || host.OrgID != p.OrgID || host.ID != p.HostID || !host.Enabled || !p.Desired.ReservedCapacity.Fits(host.Quota) {
		return fmt.Errorf("%w: plane host/quota", domain.ErrInvalidValue)
	}
	if err := s.policy.ValidatePlaneConfiguration(ctx, host, p.Desired.Configuration); err != nil {
		return err
	}
	if err := s.policy.VerifyPlaneProvenance(ctx, host, p.Desired.Package.Digest, p.Desired.Package.Provenance); err != nil {
		return err
	}
	for _, pin := range p.Desired.ImagePins {
		if !slices.Contains(host.LifecycleClasses, pin.LifecycleClass) {
			return fmt.Errorf("%w: plane host lifecycle", domain.ErrInvalidValue)
		}
		image, err := s.repo.Images().Get(ctx, p.OrgID, pin.ImageID)
		if err != nil {
			return err
		}
		if image == nil || domain.ValidateVMImage(image) != nil || image.ID != pin.ImageID || image.OrgID != p.OrgID || image.ManifestDigest != pin.ManifestDigest || image.Architecture != host.Architecture || image.OS != domain.VMOSLinux || !slices.Contains(image.LifecycleClasses, pin.LifecycleClass) ||
			(pin.LifecycleClass == domain.VMLifecycleLoomQEMU && image.Format != domain.VMImageQCOW2) ||
			(pin.LifecycleClass == domain.VMLifecycleLoomFirecracker && image.Format != domain.VMImageFirecrackerRootFS) {
			return fmt.Errorf("%w: plane image pin", domain.ErrInvalidValue)
		}
		if err := s.policy.VerifyPlaneProvenance(ctx, host, pin.ManifestDigest, image.Provenance); err != nil {
			return err
		}
		for _, capability := range p.Desired.ExpectedCapabilities {
			if capability.LifecycleClass == pin.LifecycleClass && (capability.Architecture != image.Architecture || capability.AgentProtocolVersion != image.AgentProtocolVersion) {
				return fmt.Errorf("%w: plane expected capability/image", domain.ErrInvalidValue)
			}
		}
	}
	return nil
}

func (s *ExecutionPlaneService) validateAdmission(ctx context.Context, principal *auth.Principal, p *domain.ExecutionPlaneDeployment) error {
	if err := s.ValidateDesired(ctx, p); err != nil {
		return err
	}
	for _, binding := range p.Desired.Configuration.SecretBindings {
		if err := s.policy.AuthorizePlaneSecret(ctx, principal, p.OrgID, binding.Ref); err != nil {
			return err
		}
	}
	return nil
}

func preparePlaneDesired(p *domain.ExecutionPlaneDeployment) error {
	if p.Observation != nil || p.ObservationCursor != nil {
		return fmt.Errorf("%w: observation is read-only", domain.ErrInvalidValue)
	}
	if p.Desired.ProbePolicy == (domain.ExecutionPlaneProbePolicy{}) {
		p.Desired.ProbePolicy = domain.DefaultExecutionPlaneProbePolicy()
	}
	return nil
}

// Create persists desired intent, then reserves capacity through the authoritative
// quota transaction. A non-nil resource alongside an error means intent persisted
// but capacity admission is pending; no apply is permitted without its reservation.
func (s *ExecutionPlaneService) Create(ctx context.Context, principal *auth.Principal, p domain.ExecutionPlaneDeployment) (*domain.ExecutionPlaneDeployment, error) {
	if err := s.authorize(ctx, principal, p.OrgID); err != nil {
		return nil, err
	}
	if p.Generation != 1 || p.ID == uuid.Nil {
		return nil, domain.ErrInvalidValue
	}
	p.CreatedBy = principal.PubKey
	if err := preparePlaneDesired(&p); err != nil {
		return nil, err
	}
	if err := s.validateAdmission(ctx, principal, &p); err != nil {
		return nil, err
	}
	if err := s.repo.ExecutionPlanes().Create(ctx, &p); err != nil {
		return nil, err
	}
	return &p, s.ReserveDesiredCapacity(ctx, &p)
}

func (s *ExecutionPlaneService) Update(ctx context.Context, principal *auth.Principal, p domain.ExecutionPlaneDeployment, expected int64) (*domain.ExecutionPlaneDeployment, error) {
	if err := s.authorize(ctx, principal, p.OrgID); err != nil {
		return nil, err
	}
	if err := preparePlaneDesired(&p); err != nil {
		return nil, err
	}
	if expected < 1 || p.Generation != expected+1 {
		return nil, domain.ErrInvalidValue
	}
	previous, err := s.repo.ExecutionPlanes().Get(ctx, p.OrgID, p.ID)
	if err != nil {
		return nil, err
	}
	if previous.Generation != expected {
		return nil, repository.ErrConflict
	}
	if p.HostID != previous.HostID || p.WorkerPubKey != previous.WorkerPubKey || !reflect.DeepEqual(p.Desired.LifecycleClasses, previous.Desired.LifecycleClasses) {
		return nil, domain.ErrInvalidValue
	}
	p.CreatedBy, p.CreatedAt = previous.CreatedBy, previous.CreatedAt
	if err := s.validateAdmission(ctx, principal, &p); err != nil {
		return nil, err
	}
	reservations, err := s.repo.ListReservations(ctx, p.OrgID, p.HostID)
	if err != nil {
		return nil, err
	}
	for _, reservation := range reservations {
		if reservation.ResourceKind == domain.ExecutionPlaneResource && reservation.ResourceID == p.ID && !reservation.Capacity.Fits(p.Desired.ReservedCapacity) {
			return nil, fmt.Errorf("%w: disable and verify drain before capacity reduction", repository.ErrConflict)
		}
	}
	if err := s.repo.ExecutionPlanes().Update(ctx, &p, expected); err != nil {
		return nil, err
	}
	return &p, s.ReserveDesiredCapacity(ctx, &p)
}

// ReserveDesiredCapacity charges the plane once, never its individual job domains.
// The stable reservation ID makes crash recovery and replay idempotent.
func (s *ExecutionPlaneService) ReserveDesiredCapacity(ctx context.Context, p *domain.ExecutionPlaneDeployment) error {
	if s == nil || s.repo == nil || p == nil {
		return &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	if p.Desired.State == domain.ExecutionPlaneDisabled {
		return nil
	}
	if len(p.Desired.LifecycleClasses) == 0 {
		return domain.ErrInvalidValue
	}
	return s.repo.ReserveCapacity(ctx, domain.VMCapacityReservation{
		ID: uuid.NewSHA1(p.ID, []byte("execution-plane-capacity")), OrgID: p.OrgID, HostID: p.HostID, ResourceID: p.ID,
		ResourceKind: domain.ExecutionPlaneResource, LifecycleClass: p.Desired.LifecycleClasses[0], Capacity: p.Desired.ReservedCapacity,
	})
}

func (s *ExecutionPlaneService) Get(ctx context.Context, principal *auth.Principal, org, id uuid.UUID) (*domain.ExecutionPlaneDeployment, error) {
	if err := s.authorize(ctx, principal, org); err != nil {
		return nil, err
	}
	return s.repo.ExecutionPlanes().Get(ctx, org, id)
}

func (s *ExecutionPlaneService) List(ctx context.Context, principal *auth.Principal, org uuid.UUID, limit, offset int) ([]domain.ExecutionPlaneDeployment, error) {
	if err := s.authorize(ctx, principal, org); err != nil {
		return nil, err
	}
	return s.repo.ExecutionPlanes().List(ctx, org, limit, offset)
}
