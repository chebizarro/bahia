package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

var vmTestNow = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func vmTestDigest(s string) string { return "sha256:" + strings.Repeat(s, 64) }
func vmTestMeta(org uuid.UUID) domain.VirtualizationResourceMeta {
	return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: org, Generation: 1, CreatedBy: "requester"}
}
func vmCopy[T any](v T) T {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}
func vmMetaFor(v any) *domain.VirtualizationResourceMeta {
	switch x := v.(type) {
	case *domain.VirtualizationHost:
		return &x.VirtualizationResourceMeta
	case *domain.VMImage:
		return &x.VirtualizationResourceMeta
	case *domain.PersistentVMDeployment:
		return &x.VirtualizationResourceMeta
	case *domain.VMCheckpoint:
		return &x.VirtualizationResourceMeta
	case *domain.VMExport:
		return &x.VirtualizationResourceMeta
	}
	panic("unexpected test resource")
}

type vmMemoryRepo struct {
	repository.VirtualizationRepository
	mu                  sync.Mutex
	hosts               map[uuid.UUID]domain.VirtualizationHost
	images              map[uuid.UUID]domain.VMImage
	deployments         map[uuid.UUID]domain.PersistentVMDeployment
	checkpoints         map[uuid.UUID]domain.VMCheckpoint
	exports             map[uuid.UUID]domain.VMExport
	ops                 map[uuid.UUID]domain.VMOperation
	approvals           map[uuid.UUID]domain.VMApproval
	reservations        map[uuid.UUID]domain.VMCapacityReservation
	locked              map[uuid.UUID]bool
	transitions         []domain.VMOperationPhase
	beforeTransition    func(repository.VMOperationTransition) error
	beforeDeploymentGet func()
	beforeAdmission     func(repository.VMOperationAdmission)
}

func vmNewMemory() *vmMemoryRepo {
	return &vmMemoryRepo{hosts: map[uuid.UUID]domain.VirtualizationHost{}, images: map[uuid.UUID]domain.VMImage{}, deployments: map[uuid.UUID]domain.PersistentVMDeployment{}, checkpoints: map[uuid.UUID]domain.VMCheckpoint{}, exports: map[uuid.UUID]domain.VMExport{}, ops: map[uuid.UUID]domain.VMOperation{}, approvals: map[uuid.UUID]domain.VMApproval{}, reservations: map[uuid.UUID]domain.VMCapacityReservation{}, locked: map[uuid.UUID]bool{}}
}

type vmResources[T any] struct {
	r      *vmMemoryRepo
	values map[uuid.UUID]T
}

func (r vmResources[T]) Create(ctx context.Context, v *T) error {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	m := vmMetaFor(v)
	if _, ok := r.values[m.ID]; ok {
		return repository.ErrConflict
	}
	m.CreatedAt = vmTestNow
	m.UpdatedAt = vmTestNow
	r.values[m.ID] = vmCopy(*v)
	return ctx.Err()
}
func (r vmResources[T]) Get(ctx context.Context, org, id uuid.UUID) (*T, error) {
	var zero T
	if _, ok := any(zero).(domain.PersistentVMDeployment); ok && r.r.beforeDeploymentGet != nil {
		r.r.beforeDeploymentGet()
	}
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	v, ok := r.values[id]
	if !ok || vmMetaFor(&v).OrgID != org {
		return nil, repository.ErrNotFound
	}
	out := vmCopy(v)
	return &out, ctx.Err()
}
func (r vmResources[T]) List(ctx context.Context, org uuid.UUID, limit, offset int) ([]T, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	var rows []T
	for _, v := range r.values {
		if vmMetaFor(&v).OrgID == org {
			rows = append(rows, vmCopy(v))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return vmMetaFor(&rows[i]).ID.String() < vmMetaFor(&rows[j]).ID.String() })
	if offset >= len(rows) {
		return nil, nil
	}
	return rows[offset:min(offset+limit, len(rows))], ctx.Err()
}
func (r vmResources[T]) Update(ctx context.Context, v *T, expected int64) error {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	m := vmMetaFor(v)
	old, ok := r.values[m.ID]
	if !ok {
		return repository.ErrNotFound
	}
	if vmMetaFor(&old).Generation != expected || m.Generation != expected+1 {
		return repository.ErrConflict
	}
	r.values[m.ID] = vmCopy(*v)
	return ctx.Err()
}
func (r *vmMemoryRepo) Hosts() repository.VirtualizationHostRepository {
	return vmResources[domain.VirtualizationHost]{r, r.hosts}
}
func (r *vmMemoryRepo) Images() repository.VMImageRepository {
	return vmResources[domain.VMImage]{r, r.images}
}
func (r *vmMemoryRepo) Deployments() repository.PersistentVMDeploymentRepository {
	return vmResources[domain.PersistentVMDeployment]{r, r.deployments}
}
func (r *vmMemoryRepo) Checkpoints() repository.VMCheckpointRepository {
	return vmResources[domain.VMCheckpoint]{r, r.checkpoints}
}
func (r *vmMemoryRepo) Exports() repository.VMExportRepository {
	return vmResources[domain.VMExport]{r, r.exports}
}
func (r *vmMemoryRepo) GetOperation(ctx context.Context, org, id uuid.UUID) (*domain.VMOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.ops[id]
	if !ok || o.OrgID != org {
		return nil, repository.ErrNotFound
	}
	c := vmCopy(o)
	return &c, ctx.Err()
}
func (r *vmMemoryRepo) ListOperations(ctx context.Context, org, id uuid.UUID, limit, offset int) ([]domain.VMOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []domain.VMOperation
	for _, o := range r.ops {
		if o.OrgID == org && (id == uuid.Nil || o.ResourceID == id) {
			rows = append(rows, vmCopy(o))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID.String() < rows[j].ID.String() })
	if offset >= len(rows) {
		return nil, nil
	}
	return rows[offset:min(offset+limit, len(rows))], ctx.Err()
}
func (r *vmMemoryRepo) ListReservations(ctx context.Context, org, host uuid.UUID) ([]domain.VMCapacityReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []domain.VMCapacityReservation
	for _, v := range r.reservations {
		if v.OrgID == org && v.HostID == host {
			rows = append(rows, v)
		}
	}
	return rows, ctx.Err()
}
func (r *vmMemoryRepo) ReleaseCapacity(ctx context.Context, org, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.reservations[id]
	if !ok || v.OrgID != org {
		return repository.ErrNotFound
	}
	delete(r.reservations, id)
	return ctx.Err()
}
func (r *vmMemoryRepo) GetApproval(ctx context.Context, org, id uuid.UUID) (*domain.VMApproval, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.approvals[id]
	if !ok || v.OrgID != org {
		return nil, repository.ErrNotFound
	}
	out := vmCopy(v)
	return &out, ctx.Err()
}
func (r *vmMemoryRepo) CreateApproval(ctx context.Context, a *domain.VMApproval) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := domain.ValidateVMApproval(a); err != nil {
		return err
	}
	v, ok := r.deployments[a.ResourceID]
	if !ok || v.OrgID != a.OrgID {
		return repository.ErrNotFound
	}
	if v.Generation != a.Generation {
		return repository.ErrConflict
	}
	r.approvals[a.ID] = vmCopy(*a)
	return ctx.Err()
}
func (r *vmMemoryRepo) consume(o *domain.VMOperation) error {
	if o.RequiredTier != domain.VMApprovalDestructive {
		return nil
	}
	if o.ApprovalID == nil {
		return repository.ErrConflict
	}
	a, ok := r.approvals[*o.ApprovalID]
	if !ok || a.ConsumedAt != nil || !a.ExpiresAt.After(vmTestNow) || a.Generation != o.ExpectedGeneration || a.AdoptionDigest != adoptionDigest(*o) || a.RequestHash != o.RequestHash || a.ResourceID != o.ResourceID || a.OrgID != o.OrgID || a.Requester != o.Actor || a.ProviderFingerprint != o.ProviderFingerprint {
		return repository.ErrConflict
	}
	now := vmTestNow
	a.ConsumedAt = &now
	r.approvals[a.ID] = a
	return nil
}
func (r *vmMemoryRepo) AdmitOperation(ctx context.Context, a repository.VMOperationAdmission) (*repository.VMOperationAdmissionResult, error) {
	if r.beforeAdmission != nil {
		r.beforeAdmission(a)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o := a.Operation
	if err := domain.ValidateVMOperation(&o); err != nil {
		return nil, err
	}
	for _, old := range r.ops {
		if old.OrgID == o.OrgID && old.IdempotencyKey == o.IdempotencyKey {
			if old.RequestHash != o.RequestHash {
				return nil, repository.ErrConflict
			}
			return &repository.VMOperationAdmissionResult{Operation: vmCopy(old)}, nil
		}
		if old.OrgID == o.OrgID && old.ResourceID == o.ResourceID && !old.Phase.Terminal() {
			return nil, repository.ErrConflict
		}
	}
	if r.locked[o.ResourceID] {
		return nil, repository.ErrConflict
	}
	v, exists := r.deployments[o.ResourceID]
	if a.ExpectedGeneration == 0 {
		if exists || a.Desired == nil {
			return nil, repository.ErrConflict
		}
	} else if !exists || v.Generation != a.ExpectedGeneration {
		return nil, repository.ErrConflict
	}
	if a.Desired != nil {
		v = vmCopy(*a.Desired)
		h := r.hosts[v.HostID]
		i := r.images[v.ImageID]
		if err := domain.ValidateVMDeploymentReferences(&v, &h, &i); err != nil {
			return nil, err
		}
	}
	reservations := vmCopy(r.reservations)
	for _, row := range a.Reservations {
		reservations[row.ID] = row
	}
	for _, h := range r.hosts {
		var total domain.VMCapacity
		for _, row := range reservations {
			if row.OrgID == h.OrgID && row.HostID == h.ID {
				total.VCPU += row.Capacity.VCPU
				total.MemoryBytes += row.Capacity.MemoryBytes
				total.DiskBytes += row.Capacity.DiskBytes
			}
		}
		if !total.Fits(h.Quota) {
			return nil, repository.ErrConflict
		}
	}
	if o.Phase == domain.VMOperationAccepted {
		if err := r.consume(&o); err != nil {
			return nil, err
		}
	}
	if a.Desired != nil {
		if a.ExpectedGeneration == 0 {
			v.CreatedAt = vmTestNow
		}
		r.deployments[v.ID] = v
	}
	r.reservations = reservations
	o.CreatedAt = vmTestNow
	o.UpdatedAt = vmTestNow
	r.ops[o.ID] = vmCopy(o)
	return &repository.VMOperationAdmissionResult{Operation: o, Created: true}, nil
}
func (r *vmMemoryRepo) TransitionOperation(ctx context.Context, t repository.VMOperationTransition) (*domain.VMOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.beforeTransition != nil {
		if err := r.beforeTransition(t); err != nil {
			return nil, err
		}
	}
	o, ok := r.ops[t.OperationID]
	if !ok || o.OrgID != t.OrgID {
		return nil, repository.ErrNotFound
	}
	if o.Phase != t.ExpectedPhase || o.Generation != t.ExpectedRevision {
		return nil, repository.ErrConflict
	}
	if !domain.VMOperationTransitionAllowed(o.Phase, t.Phase) {
		return nil, domain.ErrInvalidValue
	}
	if t.Phase == domain.VMOperationAccepted {
		o.ApprovalID = t.ApprovalID
		if err := r.consume(&o); err != nil {
			return nil, err
		}
	}
	o.Phase = t.Phase
	o.Generation++
	o.Outcome = t.Outcome
	if t.PreparedStorageRefs != nil {
		o.PreparedStorageRefs = t.PreparedStorageRefs
	}
	if o.Phase.Terminal() {
		n := vmTestNow
		o.CompletedAt = &n
	}
	r.ops[o.ID] = vmCopy(o)
	r.transitions = append(r.transitions, o.Phase)
	return &o, ctx.Err()
}
func (r *vmMemoryRepo) WithOperationLock(ctx context.Context, org, id uuid.UUID, fn func(context.Context) error) error {
	r.mu.Lock()
	if r.locked[id] {
		r.mu.Unlock()
		return repository.ErrConflict
	}
	r.locked[id] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.locked, id); r.mu.Unlock() }()
	return fn(ctx)
}
func (r *vmMemoryRepo) RotateObservationSession(ctx context.Context, ref repository.VirtualizationResourceRef, generation int64, expected, next uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.deployments[ref.ID]
	if !ok || v.OrgID != ref.OrgID {
		return repository.ErrNotFound
	}
	old := uuid.Nil
	if v.ObservationCursor != nil {
		old = v.ObservationCursor.SessionID
	}
	if v.Generation != generation || old != expected {
		return repository.ErrConflict
	}
	v.ObservationCursor = &domain.VMObservationCursor{SessionID: next}
	r.deployments[v.ID] = v
	return ctx.Err()
}
func (r *vmMemoryRepo) AcceptVMObservation(ctx context.Context, org, id uuid.UUID, o domain.VMObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.deployments[id]
	if !ok || v.OrgID != org {
		return repository.ErrNotFound
	}
	if err := domain.ValidateVMObservation(&v, &o); err != nil {
		return err
	}
	c := v.ObservationCursor
	if c == nil || c.SessionID != o.SessionID || c.Sequence >= o.Sequence {
		return repository.ErrConflict
	}
	copy := vmCopy(o)
	v.Observation = &copy
	v.ObservationCursor = &domain.VMObservationCursor{SessionID: o.SessionID, Sequence: o.Sequence}
	r.deployments[id] = v
	return ctx.Err()
}

type vmPermissionFake struct {
	deny bool
	seen []domain.Permission
	mu   sync.Mutex
}

func (p *vmPermissionFake) CheckPermission(ctx context.Context, a *auth.Principal, org uuid.UUID, perm domain.Permission) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, perm)
	if p.deny {
		return errors.New("denied")
	}
	return ctx.Err()
}

type vmFakeProvider struct {
	mu                sync.Mutex
	observations      map[uuid.UUID]domain.VMObservation
	executed          []domain.VMProviderOperation
	inspected         []domain.VMResourceIdentity
	execute           func(context.Context, domain.VMProviderOperation) (*domain.VMProviderResult, error)
	inspectError      error
	measurementDigest string
}

func (p *vmFakeProvider) Inspect(ctx context.Context, id domain.VMResourceIdentity) (*domain.VMObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inspected = append(p.inspected, id)
	if p.inspectError != nil {
		return nil, p.inspectError
	}
	if o, ok := p.observations[id.ProviderResourceID]; ok {
		copy := vmCopy(o)
		return &copy, nil
	}
	state := domain.VMRuntimeAbsent
	return &domain.VMObservation{Identity: id, LifecycleClass: domain.VMLifecyclePersistent, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Ownership: domain.VMOwnershipUnknown, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}, ctx.Err()
}
func (p *vmFakeProvider) Inventory(context.Context, domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	return nil, nil
}
func (p *vmFakeProvider) PlanChange(ctx context.Context, q domain.VMChangeRequest) (*domain.VMChangePlan, error) {
	return &domain.VMChangePlan{LifecycleClass: domain.VMLifecyclePersistent, ExpectedGeneration: q.Current.Generation, CurrentConfigDigest: q.Current.ConfigDigest, DesiredConfigDigest: q.Desired.ConfigDigest, RequiredTier: domain.VMApprovalOperator}, ctx.Err()
}
func (p *vmFakeProvider) Execute(ctx context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
	p.mu.Lock()
	p.executed = append(p.executed, q)
	p.mu.Unlock()
	if p.execute != nil {
		return p.execute(ctx, q)
	}
	return p.complete(q), ctx.Err()
}
func (p *vmFakeProvider) complete(q domain.VMProviderOperation) *domain.VMProviderResult {
	v := q.Deployment
	state := domain.VMRuntimeStopped
	switch q.Operation.Kind {
	case domain.VMOperationStart, domain.VMOperationReboot:
		state = domain.VMRuntimeRunning
	case domain.VMOperationDelete:
		if q.Operation.DeleteTarget == domain.VMDeleteDeployment {
			state = domain.VMRuntimeAbsent
		}
	case domain.VMOperationClone:
		v = *q.CloneTarget
	}
	o := vmOwnedObservation(v, q.Image, q.Operation.ID, state)
	p.mu.Lock()
	p.observations[v.Identity.ProviderResourceID] = o
	p.mu.Unlock()
	result := &domain.VMProviderResult{LifecycleClass: domain.VMLifecyclePersistent, OperationID: q.Operation.ID, Confirmed: true, Observation: &o}
	if q.Operation.Kind == domain.VMOperationCheckpoint {
		c := *q.Checkpoint
		c.State = domain.VMArtifactReady
		c.ManifestDigest = vmTestDigest("e")
		c.Components = []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: q.Operation.PreparedStorageRefs[0], Digest: vmTestDigest("f"), SizeBytes: v.Allocation.DiskBytes}}
		result.Checkpoint = &c
	}
	return result
}
func vmOwnedObservation(v domain.PersistentVMDeployment, i domain.VMImage, op uuid.UUID, state domain.VMRuntimeState) domain.VMObservation {
	return domain.VMObservation{Identity: v.Identity, LifecycleClass: domain.VMLifecyclePersistent, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftInSync, GuestHealth: domain.VMGuestHealthy, Ownership: domain.VMOwned, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: v.Identity, AppliedGeneration: v.Generation, OperationID: op, ImageDigest: i.ManifestDigest, ConfigDigest: v.ConfigDigest}, AppliedImageDigest: i.ManifestDigest, AppliedConfigDigest: v.ConfigDigest, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
}
func vmFixture(t *testing.T) (*PersistentVMService, *vmMemoryRepo, *vmFakeProvider, domain.PersistentVMDeployment, *auth.Principal) {
	t.Helper()
	r := vmNewMemory()
	org := uuid.New()
	policy := domain.DesktopVMPilotPolicy()
	h := domain.VirtualizationHost{VirtualizationResourceMeta: vmTestMeta(org), PilotPolicy: &policy, InstallationID: uuid.New(), Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Capacity: policy.AggregateCeiling, Quota: policy.AggregateCeiling, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	i := domain.VMImage{VirtualizationResourceMeta: vmTestMeta(org), ManifestDigest: vmTestDigest("a"), Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Firmware: domain.VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "1", ReleaseRef: uuid.New(), Provenance: domain.VMProvenance{EventID: strings.Repeat("b", 64), Signer: strings.Repeat("c", 64), Verified: true, VerifiedAt: vmTestNow}, AllowedProfiles: []string{"desktop/gnome-dev"}, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: vmTestDigest("a"), SizeBytes: 100 << 30}}}
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: vmTestMeta(org), LifecycleClass: domain.VMLifecyclePersistent, Purpose: domain.VMPurposeDesktop, Provider: h.Provider, HostID: h.ID, ImageID: i.ID, DisplayName: "same display name", Profile: "desktop/gnome-dev", ProfileRevision: 1, DesiredPower: domain.VMDesiredStopped, Allocation: policy.Profiles[0].Minimum, StoragePoolRef: uuid.New(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, Firmware: domain.VMFirmwareBIOS, ConfigDigest: vmTestDigest("a")}
	v.Identity = domain.VMResourceIdentity{InstallationID: h.InstallationID, OrgID: org, HostID: h.ID, DeploymentID: v.ID, Provider: h.Provider, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	require.NoError(t, domain.ValidateVMDeploymentReferences(&v, &h, &i))
	r.hosts[h.ID] = h
	r.images[i.ID] = i
	p := &vmFakeProvider{observations: map[uuid.UUID]domain.VMObservation{}}
	s, err := NewPersistentVMService(PersistentVMServiceConfig{Repository: r, Provider: p, Permissions: &vmPermissionFake{}, Operator: func(context.Context, *auth.Principal) error { return nil }, Now: func() time.Time { return vmTestNow }})
	require.NoError(t, err)
	return s, r, p, v, &auth.Principal{Subject: "requester", PubKey: "requester", Method: auth.MethodSystem}
}
func vmCreate(t *testing.T, s *PersistentVMService, v domain.PersistentVMDeployment, p *auth.Principal) (*domain.PersistentVMDeployment, *domain.VMOperation) {
	t.Helper()
	d, o, e := s.CreateDeployment(context.Background(), p, VMCreateRequest{Deployment: v, IdempotencyKey: v.ID.String(), Reason: "create VM"})
	require.NoError(t, e)
	return d, o
}

func TestPersistentVMAdmissionAuthorizationAndExactReferences(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	_, _, err := s.CreateDeployment(ctx, nil, VMCreateRequest{Deployment: v})
	require.Error(t, err)
	require.Empty(t, r.ops)
	bad := v
	bad.Identity.ProviderResourceID = uuid.Nil
	_, _, err = s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: bad, IdempotencyKey: "bad", Reason: "test"})
	require.Error(t, err)
	bad = v
	bad.Allocation.VCPU--
	_, _, err = s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: bad, IdempotencyKey: "profile", Reason: "test"})
	require.Error(t, err)
	bad = v
	bad.OrgID = uuid.New()
	_, _, err = s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: bad, IdempotencyKey: "tenant", Reason: "test"})
	require.ErrorIs(t, err, repository.ErrNotFound)
	created, op := vmCreate(t, s, v, principal)
	require.Equal(t, v.Identity, created.Identity)
	require.Equal(t, domain.VMOperationAccepted, op.Phase)
	require.Empty(t, p.executed)
	again, replayed, err := s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: v, IdempotencyKey: v.ID.String(), Reason: "create VM"})
	require.NoError(t, err)
	require.Equal(t, created.ID, again.ID)
	require.Equal(t, op.ID, replayed.ID)
	_, _, err = s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: v, IdempotencyKey: v.ID.String(), Reason: "changed content"})
	require.ErrorIs(t, err, repository.ErrConflict)
	for _, id := range p.inspected {
		require.Equal(t, v.Identity, id)
	}
	_, err = s.Get(ctx, principal, uuid.New(), v.ID)
	require.ErrorIs(t, err, repository.ErrNotFound)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = s.CreateDeployment(cancelled, principal, VMCreateRequest{Deployment: v})
	require.ErrorIs(t, err, context.Canceled)
	s.cfg.Operator = func(context.Context, *auth.Principal) error { return errors.New("not operator") }
	_, err = s.List(ctx, principal, v.OrgID, 10, 0)
	require.NoError(t, err, "tier-zero reads require organization read permission, not operator eligibility")
	_, err = s.Start(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "not-operator", Reason: "denied"})
	require.Error(t, err)
	s.cfg.Permissions.(*vmPermissionFake).deny = true
	_, err = s.List(ctx, principal, v.OrgID, 10, 0)
	require.Error(t, err)
}
func TestPersistentVMAdmissionConcurrentQuotaAndDedupe(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			s, r, _, base, p := vmFixture(t)
			start := make(chan struct{})
			results := make(chan error, 3)
			ids := make(chan uuid.UUID, 3)
			var wg sync.WaitGroup
			for n := 0; n < 3; n++ {
				v := base
				if !same {
					v.ID = uuid.New()
					v.Identity.DeploymentID = v.ID
					v.Identity.ProviderResourceID = uuid.New()
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, op, err := s.CreateDeployment(context.Background(), p, VMCreateRequest{Deployment: v, IdempotencyKey: v.ID.String(), Reason: "quota test"})
					if err == nil {
						ids <- op.ID
					}
					results <- err
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			close(ids)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, repository.ErrConflict)
				}
			}
			if same {
				require.Equal(t, 3, success)
				require.Len(t, r.ops, 1)
				var first uuid.UUID
				for id := range ids {
					if first == uuid.Nil {
						first = id
					}
					require.Equal(t, first, id)
				}
			} else {
				require.Equal(t, 2, success)
				require.Len(t, r.ops, 2)
				require.Len(t, r.deployments, 2)
			}
		})
	}
}
func TestPersistentVMConcurrentLifecycleReplayAfterGenerationAdvance(t *testing.T) {
	s, r, _, v, principal := vmFixture(t)
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(context.Background(), v.OrgID, created.ID))
	firstRead, secondRead, releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var count int
	var mu sync.Mutex
	r.beforeDeploymentGet = func() {
		mu.Lock()
		count++
		n := count
		mu.Unlock()
		switch n {
		case 1:
			close(firstRead)
			<-releaseFirst
		case 2:
			close(secondRead)
			<-releaseSecond
		}
	}
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, Kind: domain.VMOperationStart, IdempotencyKey: "concurrent-start", Reason: "start"}
	type result struct {
		op  *domain.VMOperation
		err error
	}
	one, two := make(chan result, 1), make(chan result, 1)
	go func() { op, err := s.RequestOperation(context.Background(), principal, req); one <- result{op, err} }()
	<-firstRead
	go func() { op, err := s.RequestOperation(context.Background(), principal, req); two <- result{op, err} }()
	<-secondRead
	close(releaseFirst)
	a := <-one
	require.NoError(t, a.err)
	close(releaseSecond)
	b := <-two
	require.NoError(t, b.err)
	require.Equal(t, a.op.ID, b.op.ID)
}

func TestPersistentVMMissingAuthorizationDependencies(t *testing.T) {
	s, _, _, _, _ := vmFixture(t)
	cfg := s.cfg
	cfg.Permissions = nil
	_, err := NewPersistentVMService(cfg)
	require.Error(t, err)
	cfg = s.cfg
	cfg.Operator = nil
	_, err = NewPersistentVMService(cfg)
	require.Error(t, err)
}

var _ repository.VirtualizationRepository = (*vmMemoryRepo)(nil)
var _ domain.PersistentVMProvider = (*vmFakeProvider)(nil)

func (p *vmFakeProvider) MeasureAdoption(ctx context.Context, q domain.VMChangeRequest) (*domain.VMAdoptionMeasurement, error) {
	o, err := p.Inspect(ctx, q.Desired.Identity)
	if err != nil {
		return nil, err
	}
	m := &domain.VMAdoptionMeasurement{SchemaVersion: 1, Identity: q.Desired.Identity, Generation: q.Desired.Generation, ImageID: q.Image.ID, ImageDigest: q.Image.ManifestDigest, ConfigDigest: domain.VMAdoptionConfigDigest(q.Desired, o.Diagnostic.EvidenceDigest), ProviderFingerprint: o.Diagnostic.EvidenceDigest, StoragePoolRef: q.Desired.StoragePoolRef}
	digest := p.measurementDigest
	if digest == "" {
		digest = vmTestDigest("a")
	}
	m.Components = []domain.VMAdoptionComponent{{VMComponent: domain.VMComponent{Kind: domain.VMComponentDisk, StorageRef: uuid.NewSHA1(q.Desired.ID, []byte("disk")), Digest: digest, SizeBytes: 4}, StorageKey: vmTestDigest("b"), SourceDigest: vmTestDigest("c")}}
	m.Digest = domain.VMAdoptionDigest(*m)
	return m, nil
}
