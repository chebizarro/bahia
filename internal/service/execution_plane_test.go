package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type planeResourceFixture[T any] struct {
	value            *T
	creates, updates int
}

func (f *planeResourceFixture[T]) Create(_ context.Context, value *T) error {
	f.creates++
	b, _ := json.Marshal(value)
	f.value = new(T)
	return json.Unmarshal(b, f.value)
}
func (f *planeResourceFixture[T]) Get(context.Context, uuid.UUID, uuid.UUID) (*T, error) {
	if f.value == nil {
		return nil, repository.ErrNotFound
	}
	b, _ := json.Marshal(f.value)
	v := new(T)
	err := json.Unmarshal(b, v)
	return v, err
}
func (f *planeResourceFixture[T]) List(context.Context, uuid.UUID, int, int) ([]T, error) {
	if f.value == nil {
		return nil, nil
	}
	return []T{*f.value}, nil
}
func (f *planeResourceFixture[T]) Update(ctx context.Context, value *T, _ int64) error {
	f.updates++
	before := f.creates
	err := f.Create(ctx, value)
	f.creates = before
	return err
}

type planeAdmissionRepo struct {
	repository.VirtualizationRepository
	host         planeResourceFixture[domain.VirtualizationHost]
	image        planeResourceFixture[domain.VMImage]
	plane        planeResourceFixture[domain.ExecutionPlaneDeployment]
	reservations []domain.VMCapacityReservation
	reserveErr   error
}

func (r *planeAdmissionRepo) Hosts() repository.VirtualizationHostRepository { return &r.host }
func (r *planeAdmissionRepo) Images() repository.VMImageRepository           { return &r.image }
func (r *planeAdmissionRepo) ExecutionPlanes() repository.ExecutionPlaneDeploymentRepository {
	return &r.plane
}
func (r *planeAdmissionRepo) ReserveCapacity(_ context.Context, v domain.VMCapacityReservation) error {
	if r.reserveErr != nil {
		return r.reserveErr
	}
	r.reservations = []domain.VMCapacityReservation{v}
	return nil
}
func (r *planeAdmissionRepo) ListReservations(context.Context, uuid.UUID, uuid.UUID) ([]domain.VMCapacityReservation, error) {
	return r.reservations, nil
}

type planeAdmissionPolicyFixture struct {
	deny, trustError, secretError, configError error
	digests                                    []string
	secrets                                    []domain.SecretRef
}

func (p *planeAdmissionPolicyFixture) AuthorizeExecutionPlane(context.Context, *auth.Principal, uuid.UUID) error {
	return p.deny
}
func (p *planeAdmissionPolicyFixture) VerifyPlaneProvenance(_ context.Context, _ *domain.VirtualizationHost, digest string, _ domain.VMProvenance) error {
	p.digests = append(p.digests, digest)
	return p.trustError
}
func (p *planeAdmissionPolicyFixture) ValidatePlaneConfiguration(context.Context, *domain.VirtualizationHost, domain.ExecutionPlaneConfiguration) error {
	return p.configError
}
func (p *planeAdmissionPolicyFixture) AuthorizePlaneSecret(_ context.Context, _ *auth.Principal, _ uuid.UUID, ref domain.SecretRef) error {
	p.secrets = append(p.secrets, ref)
	return p.secretError
}

func planeAdmissionFixture(t *testing.T) (*ExecutionPlaneService, *planeAdmissionRepo, *planeAdmissionPolicyFixture, domain.ExecutionPlaneDeployment, *auth.Principal) {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	author := strings.Repeat("e", 64)
	digest := "sha256:" + strings.Repeat("a", 64)
	org := uuid.New()
	meta := func() domain.VirtualizationResourceMeta {
		return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: org, Generation: 1, CreatedBy: author, CreatedAt: now, UpdatedAt: now}
	}
	provenance := domain.VMProvenance{EventID: strings.Repeat("b", 64), Signer: author, Verified: true, VerifiedAt: now}
	host := domain.VirtualizationHost{VirtualizationResourceMeta: meta(), InstallationID: uuid.New(), LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}, Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	image := domain.VMImage{VirtualizationResourceMeta: meta(), ManifestDigest: digest, Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: host.LifecycleClasses, Firmware: domain.VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "3", ReleaseRef: uuid.New(), Provenance: provenance, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: digest, SizeBytes: 10 << 30}}}
	p := domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: meta(), HostID: host.ID, WorkerPubKey: author, ManagementEndpointRef: uuid.New(), ManagementAuthor: author, Desired: domain.ExecutionPlaneDesired{LifecycleClasses: host.LifecycleClasses, Package: domain.ExecutionPlanePackagePin{Digest: digest, Version: "1", Provenance: provenance}, Configuration: domain.ExecutionPlaneConfiguration{Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, SecretBindings: []domain.VMBootstrapBinding{{TargetKey: "credential", Ref: domain.SecretRef{ID: uuid.New()}}}}, ImagePins: []domain.ExecutionPlaneImagePin{{LifecycleClass: domain.VMLifecycleLoomQEMU, ImageID: image.ID, ManifestDigest: digest}}, ReservedCapacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 4 << 30, DiskBytes: 10 << 30}, Concurrency: 2, ExpectedCapabilities: []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "3"}}, State: domain.ExecutionPlaneEnabled, ProbePolicy: domain.DefaultExecutionPlaneProbePolicy()}}
	var err error
	p.Desired.Configuration.Revision, err = ExecutionPlaneConfigRevision(p.Desired.Configuration)
	require.NoError(t, err)
	repo := &planeAdmissionRepo{host: planeResourceFixture[domain.VirtualizationHost]{value: &host}, image: planeResourceFixture[domain.VMImage]{value: &image}}
	policy := &planeAdmissionPolicyFixture{}
	s := NewExecutionPlaneService(repo, policy)
	principal := &auth.Principal{PubKey: author, Method: auth.MethodNIP98}
	require.NoError(t, domain.ValidateExecutionPlaneDeployment(&p))
	return s, repo, policy, p, principal
}

func TestExecutionPlaneAdmissionPinsSecretsAndCapacity(t *testing.T) {
	s, repo, policy, p, principal := planeAdmissionFixture(t)
	p.CreatedBy = "untrusted"
	p.Desired.ProbePolicy = domain.ExecutionPlaneProbePolicy{}
	created, err := s.Create(t.Context(), principal, p)
	require.NoError(t, err)
	require.Equal(t, principal.PubKey, created.CreatedBy)
	require.Equal(t, domain.DefaultExecutionPlaneProbePolicy(), created.Desired.ProbePolicy)
	require.Equal(t, []string{p.Desired.Package.Digest, p.Desired.ImagePins[0].ManifestDigest}, policy.digests)
	require.Len(t, policy.secrets, 1)
	require.Equal(t, domain.SecretRef{ID: p.Desired.Configuration.SecretBindings[0].Ref.ID}, policy.secrets[0])
	require.Len(t, repo.reservations, 1)
	require.Equal(t, p.Desired.ReservedCapacity, repo.reservations[0].Capacity)
	first := repo.reservations[0].ID
	require.NoError(t, s.ReserveDesiredCapacity(t.Context(), created))
	require.Equal(t, first, repo.reservations[0].ID)
	encoded, err := json.Marshal(created)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "password")
	require.NotContains(t, string(encoded), "encrypted_value")
}

func TestExecutionPlaneAdmissionFailsClosedBeforePersistence(t *testing.T) {
	for name, mutate := range map[string]func(*ExecutionPlaneService, *planeAdmissionRepo, *planeAdmissionPolicyFixture, *domain.ExecutionPlaneDeployment, *auth.Principal){
		"no policy": func(s *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			s.policy = nil
		},
		"unauthenticated": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, a *auth.Principal) {
			a.Method = auth.MethodNone
		},
		"not operator": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, v *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			v.deny = errors.New("denied")
		},
		"untrusted provenance": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, v *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			v.trustError = errors.New("signature")
		},
		"unapproved network/device configuration": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, v *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			v.configError = errors.New("configuration not authorized")
		},
		"secret unauthorized": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, v *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			v.secretError = errors.New("secret denied")
		},
		"revision mismatch": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.Desired.Configuration.Revision = "sha256:" + strings.Repeat("c", 64)
		},
		"cross tenant image": func(_ *ExecutionPlaneService, r *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			r.image.value.OrgID = uuid.New()
		},
		"wrong image digest": func(_ *ExecutionPlaneService, r *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			r.image.value.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
		},
		"wrong architecture": func(_ *ExecutionPlaneService, r *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, _ *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			r.image.value.Architecture = "arm64"
		},
		"windows": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.Desired.ExpectedCapabilities[0].OS = domain.VMOSWindows
		},
		"persistent": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.Desired.LifecycleClasses = []domain.VMLifecycleClass{domain.VMLifecyclePersistent}
		},
		"forged observation": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.Observation = &domain.ExecutionPlaneObservation{}
		},
		"forged cursor": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.ObservationCursor = &domain.VMObservationCursor{}
		},
		"agent mismatch": func(_ *ExecutionPlaneService, _ *planeAdmissionRepo, _ *planeAdmissionPolicyFixture, p *domain.ExecutionPlaneDeployment, _ *auth.Principal) {
			p.Desired.ExpectedCapabilities[0].AgentProtocolVersion = "2"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, v, p, a := planeAdmissionFixture(t)
			mutate(s, r, v, &p, a)
			_, err := s.Create(t.Context(), a, p)
			require.Error(t, err)
			require.Zero(t, r.plane.creates)
			require.Empty(t, r.reservations)
		})
	}
}

func TestExecutionPlaneQuotaFailureRetainsOnlyDesiredIntent(t *testing.T) {
	s, r, _, p, a := planeAdmissionFixture(t)
	r.reserveErr = repository.ErrConflict
	created, err := s.Create(t.Context(), a, p)
	require.ErrorIs(t, err, repository.ErrConflict)
	require.NotNil(t, created)
	require.NotNil(t, r.plane.value)
	require.Nil(t, r.plane.value.Observation)
	require.Empty(t, r.reservations)
}
func TestExecutionPlaneUpdateRejectsShrinkAndStaleGeneration(t *testing.T) {
	s, r, _, p, a := planeAdmissionFixture(t)
	created, err := s.Create(t.Context(), a, p)
	require.NoError(t, err)
	updated := *created
	updated.Generation = 2
	updated.Desired.ReservedCapacity.VCPU--
	_, err = s.Update(t.Context(), a, updated, 1)
	require.ErrorIs(t, err, repository.ErrConflict)
	require.Zero(t, r.plane.updates)
	updated = *created
	updated.Generation = 3
	_, err = s.Update(t.Context(), a, updated, 2)
	require.ErrorIs(t, err, repository.ErrConflict)
	updated = *created
	updated.Generation = 2
	updated.Desired.State = domain.ExecutionPlaneDisabled
	_, err = s.Update(t.Context(), a, updated, 1)
	require.NoError(t, err)
	require.Len(t, r.reservations, 1, "drain must be observed before release")
}
func TestExecutionPlaneConfigurationHashCanonicalizesReferences(t *testing.T) {
	_, _, _, p, _ := planeAdmissionFixture(t)
	c := p.Desired.Configuration
	c.SecretBindings = append(c.SecretBindings, domain.VMBootstrapBinding{TargetKey: "aaa", Ref: domain.SecretRef{ID: uuid.New()}})
	first, err := ExecutionPlaneConfigRevision(c)
	require.NoError(t, err)
	c.SecretBindings[0], c.SecretBindings[1] = c.SecretBindings[1], c.SecretBindings[0]
	c.Revision = "ignored"
	c.Network.PassthroughDeviceRefs = []uuid.UUID{}
	second, err := ExecutionPlaneConfigRevision(c)
	require.NoError(t, err)
	require.Equal(t, first, second)
}
