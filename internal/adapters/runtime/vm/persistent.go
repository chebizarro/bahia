package vm

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PersistentResource is a private, freshly inspected provider definition. Paths
// never cross the domain/public boundary. Fingerprint excludes ownership metadata.
type PersistentResource struct {
	AdoptionFiles      []AdoptionFile
	RevalidateAdoption func(context.Context) error
	AdoptionBarrier    func(context.Context) error
	Adoption           *domain.VMAdoptionMeasurement
	ID                 uuid.UUID
	State              domain.VMRuntimeState
	Marker             *domain.VMOwnershipMarker
	Fingerprint        string
	Components         map[domain.VMComponentKind]string
}

// PersistentSpec reuses the legacy image/instance preparation contract without
// invoking Runtime.Deploy or its replace-on-update semantics.
type PersistentSpec struct {
	Instance   InstanceSpec
	Deployment domain.PersistentVMDeployment
	Marker     domain.VMOwnershipMarker
	Components map[domain.VMComponentKind]string
}

// PersistentDriver is additive: existing Hypervisor implementations are unchanged.
// Every mutator must recheck the supplied resource fingerprint and ownership.
// Transition returns only after an event and authoritative state confirmation.
type PersistentDriver interface {
	InspectPersistent(context.Context, uuid.UUID) (*PersistentResource, error)
	ListPersistent(context.Context) ([]uuid.UUID, error)
	DefinePersistent(context.Context, PersistentSpec, *PersistentResource) error
	AdoptPersistent(context.Context, *PersistentResource, domain.VMOwnershipMarker) error
	TransitionPersistent(context.Context, *PersistentResource, domain.VMOperationKind, bool) error
	CopyPersistentComponent(context.Context, domain.VMComponentKind, string, string) error
}

// PersistentConfig is trusted operator configuration for exactly one host/pool.
// VerifyImage must verify provenance against Host.TrustPolicyRef (not just trust
// the public Verified flag). ResolveRelease selects the registered immutable pin.
// Neither callback may execute guest/operator-supplied host commands.
type PersistentConfig struct {
	Host           domain.VirtualizationHost
	StoragePoolRef uuid.UUID
	StateDir       string
	ResolveRelease func(context.Context, domain.VMImage) (*Release, error)
	VerifyImage    func(context.Context, domain.VirtualizationHost, domain.VMImage) error
}

type PersistentProvider struct {
	cfg     PersistentConfig
	driver  PersistentDriver
	session uuid.UUID
	seq     atomic.Int64
	gate    chan struct{}
}

type persistentRecord struct {
	Adoption      *domain.VMAdoptionMeasurement     `json:"adoption,omitempty"`
	SchemaVersion int                               `json:"schema_version"`
	Marker        domain.VMOwnershipMarker          `json:"marker"`
	Deployment    domain.PersistentVMDeployment     `json:"deployment"`
	Fingerprint   string                            `json:"fingerprint"`
	Components    map[domain.VMComponentKind]string `json:"components"`
}

func NewPersistentProvider(cfg PersistentConfig, driver PersistentDriver) (*PersistentProvider, error) {
	if cfg.Host.OperationLimits == (domain.VMOperationLimits{}) {
		cfg.Host.OperationLimits = domain.DefaultVMOperationLimits()
	}
	if err := domain.ValidateVirtualizationHost(&cfg.Host); err != nil {
		return nil, ProviderError(domain.VMErrorInvalid, err)
	}
	if driver == nil || cfg.StoragePoolRef == uuid.Nil || !filepath.IsAbs(cfg.StateDir) {
		return nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	if cfg.Host.ExecutionLocation != domain.VMExecutionLocal {
		return nil, ProviderError(domain.VMErrorUnsupported, nil)
	}
	return &PersistentProvider{cfg: cfg, driver: driver, session: uuid.New(), gate: make(chan struct{}, 1)}, nil
}

func (r *Runtime) PersistentProvider(cfg PersistentConfig) (*PersistentProvider, error) {
	if (r.cfg.RuntimeType == domain.RuntimeTypeVMQEMU && cfg.Host.Provider != domain.VMProviderLibvirt) || (r.cfg.RuntimeType == domain.RuntimeTypeVMFirecracker && cfg.Host.Provider != domain.VMProviderFirecracker) {
		return nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	driver, ok := r.hv.(PersistentDriver)
	if !ok {
		return nil, ProviderError(domain.VMErrorUnsupported, nil)
	}
	if filepath.Clean(cfg.StateDir) != filepath.Clean(r.cfg.StateDir) {
		return nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	return NewPersistentProvider(cfg, driver)
}

func ProviderError(code domain.VMErrorCode, cause error) error {
	return &domain.VMProviderError{Code: code, Cause: cause, Unconfirmed: code == domain.VMErrorUnconfirmed, Retryable: code == domain.VMErrorUnconfirmed || code == domain.VMErrorUnavailable}
}

// JoinCleanupError retains both causes without exposing cleanup paths through a
// classified provider error or changing its retry/confirmation policy.
func JoinCleanupError(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	joined := errors.Join(primary, cleanup)
	var provider *domain.VMProviderError
	if errors.As(joined, &provider) {
		classified := *provider
		classified.Cause = joined
		return &classified
	}
	return joined
}

func SameIdentity(a, b domain.VMResourceIdentity) bool { return reflect.DeepEqual(a, b) }

// CheckPersistentResource is used at the last driver boundary before mutation.
func CheckPersistentResource(expected, actual *PersistentResource) error {
	if expected == nil || actual == nil || expected.ID != actual.ID || expected.State != actual.State || expected.Fingerprint != actual.Fingerprint || !reflect.DeepEqual(expected.Marker, actual.Marker) || !maps.Equal(expected.Components, actual.Components) {
		return ProviderError(domain.VMErrorConflict, nil)
	}
	return nil
}

// CheckDefinitionBaseline prevents redefining an unowned or substituted resource.
func CheckDefinitionBaseline(current *PersistentResource, marker domain.VMOwnershipMarker) error {
	if current == nil || current.ID != marker.ProviderResourceID {
		return ProviderError(domain.VMErrorConflict, nil)
	}
	if current.State != domain.VMRuntimeAbsent && (current.Marker == nil || domain.ValidateVMOwnershipMarker(*current.Marker) != nil || !SameIdentity(current.Marker.VMResourceIdentity, marker.VMResourceIdentity)) {
		return ProviderError(domain.VMErrorForeign, nil)
	}
	return nil
}

// CheckWritableComponents binds storage to this resource, not merely its pool.
// Exact component paths are also persisted in the core record and compared on
// every operation; restored sets must live under this resource's private root.
func CheckWritableComponents(dir string, components map[domain.VMComponentKind]string) error {
	for kind, path := range components {
		if kind == domain.VMComponentKernel {
			continue
		}
		if filepath.Clean(path) == filepath.Clean(dir) {
			return ProviderError(domain.VMErrorIntegrity, nil)
		}
		if err := CheckContainedPath(dir, path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return ProviderError(domain.VMErrorIntegrity, err)
		}
		if kind == domain.VMComponentSWTPM {
			if !info.IsDir() {
				return ProviderError(domain.VMErrorIntegrity, nil)
			}
		} else if !info.Mode().IsRegular() {
			return ProviderError(domain.VMErrorIntegrity, nil)
		} else if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			return ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	return nil
}

func (p *PersistentProvider) checkIdentity(id domain.VMResourceIdentity) error {
	h := p.cfg.Host
	if domain.ValidateVMResourceIdentity(id) != nil || id.InstallationID != h.InstallationID || id.OrgID != h.OrgID || id.HostID != h.ID || id.Provider != h.Provider {
		return ProviderError(domain.VMErrorForeign, nil)
	}
	return nil
}

func (p *PersistentProvider) bound(ctx context.Context, kind domain.VMOperationKind) (context.Context, context.CancelFunc) {
	l := p.cfg.Host.OperationLimits
	n := l.MutationSeconds
	switch kind {
	case "":
		n = l.InspectSeconds
	case domain.VMOperationGracefulStop:
		n = l.GracefulStopSeconds
	case domain.VMOperationCheckpoint, domain.VMOperationExport, domain.VMOperationClone, domain.VMOperationRestore:
		n = l.TransferSeconds
	}
	return context.WithTimeout(ctx, time.Duration(n)*time.Second)
}

func (p *PersistentProvider) recordPath(id uuid.UUID) string {
	return filepath.Join(InstancesDir(p.cfg.StateDir), id.String(), metadataFileName)
}
func (p *PersistentProvider) readRecord(id uuid.UUID) (*persistentRecord, error) {
	path := p.recordPath(id)
	if err := CheckContainedPath(p.cfg.StateDir, path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r persistentRecord
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.SchemaVersion == 0 || r.SchemaVersion == 1 {
		var legacy InstanceMetadata
		if err = json.Unmarshal(data, &legacy); err != nil {
			return nil, err
		}
		if legacy.OwnershipID != uuid.Nil && legacy.OwnershipID != id {
			return nil, ProviderError(domain.VMErrorForeign, nil)
		}
		return nil, nil
	} // Legacy metadata is never mutation authority.
	if r.SchemaVersion != 2 {
		return nil, ProviderError(domain.VMErrorIntegrity, nil)
	}
	if domain.ValidateVMOwnershipMarker(r.Marker) != nil || r.Marker.ProviderResourceID != id {
		return nil, ProviderError(domain.VMErrorIntegrity, nil)
	}
	return &r, nil
}
func (p *PersistentProvider) writeRecord(ctx context.Context, d domain.PersistentVMDeployment, r *PersistentResource) error {
	if r.Marker == nil {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	path := p.recordPath(r.ID)
	if err := CheckContainedPath(p.cfg.StateDir, path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	d.Observation = nil
	record := persistentRecord{SchemaVersion: 2, Marker: *r.Marker, Deployment: d, Fingerprint: r.Fingerprint, Components: r.Components}
	if previous, err := p.readRecord(r.ID); err != nil {
		return err
	} else if previous != nil {
		record.Adoption = previous.Adoption
	}
	if r.Adoption != nil {
		record.Adoption = r.Adoption
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(ctx, path, ".metadata-*.tmp", data, 0600)
}

func (p *PersistentProvider) observation(id domain.VMResourceIdentity, r *PersistentResource, rec *persistentRecord) *domain.VMObservation {
	now := time.Now().UTC()
	state := r.State
	o := &domain.VMObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: p.session, Sequence: p.seq.Add(1), ObservedAt: now}, Identity: id, LifecycleClass: id.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, RuntimeObservedAt: &now, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestNotConfigured, Ownership: domain.VMForeign}
	// EvidenceDigest is the normalized provider fingerprint used by adoption and
	// destructive approval. No raw XML, command output, or private paths escape.
	o.Diagnostic.EvidenceDigest = r.Fingerprint
	if r.Marker != nil && domain.ValidateVMOwnershipMarker(*r.Marker) == nil && SameIdentity(r.Marker.VMResourceIdentity, id) {
		marker := *r.Marker
		o.Marker = &marker
		o.Ownership = domain.VMOrphan
		o.ObservedGeneration = marker.AppliedGeneration
		o.AppliedImageDigest = marker.ImageDigest
		o.AppliedConfigDigest = marker.ConfigDigest
		if rec != nil && reflect.DeepEqual(rec.Marker, marker) && len(rec.Components) > 0 && maps.Equal(rec.Components, r.Components) {
			o.Ownership = domain.VMOwned
			o.Drift = domain.VMDriftInSync
			if rec.Fingerprint != r.Fingerprint {
				o.Drift = domain.VMDriftDrifted
			}
		}
	}
	return o
}
func (p *PersistentProvider) Inspect(ctx context.Context, id domain.VMResourceIdentity) (*domain.VMObservation, error) {
	if err := p.checkIdentity(id); err != nil {
		return nil, err
	}
	ctx, cancel := p.bound(ctx, "")
	defer cancel()
	r, err := p.driver.InspectPersistent(ctx, id.ProviderResourceID)
	if err != nil {
		return nil, ProviderError(domain.VMErrorUnavailable, err)
	}
	rec, err := p.readRecord(id.ProviderResourceID)
	if err != nil {
		return nil, ProviderError(domain.VMErrorIntegrity, err)
	}
	return p.observation(id, r, rec), nil
}
func (p *PersistentProvider) Inventory(ctx context.Context, h domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	if h.ID != p.cfg.Host.ID || h.InstallationID != p.cfg.Host.InstallationID || h.OrgID != p.cfg.Host.OrgID || h.Provider != p.cfg.Host.Provider {
		return nil, ProviderError(domain.VMErrorForeign, nil)
	}
	ctx, cancel := p.bound(ctx, "")
	defer cancel()
	ids, err := p.driver.ListPersistent(ctx)
	if err != nil {
		return nil, ProviderError(domain.VMErrorUnavailable, err)
	}
	out := make([]domain.VMInventoryEntry, 0, len(ids))
	for _, id := range ids {
		r, err := p.driver.InspectPersistent(ctx, id)
		if err != nil {
			return nil, ProviderError(domain.VMErrorUnavailable, err)
		}
		identity := domain.VMResourceIdentity{ProviderResourceID: id, Provider: h.Provider, HostID: h.ID}
		var rec *persistentRecord
		if r.Marker != nil {
			identity = r.Marker.VMResourceIdentity
			if p.checkIdentity(identity) == nil {
				rec, err = p.readRecord(id)
				if err != nil {
					return nil, err
				}
			}
		}
		o := p.observation(identity, r, rec)
		if p.checkIdentity(identity) != nil {
			o.Ownership = domain.VMForeign
			o.Marker = nil
		}
		out = append(out, domain.VMInventoryEntry{LifecycleClass: identity.LifecycleClass, ProviderResourceID: id, Ownership: o.Ownership, Marker: o.Marker, Observation: *o})
	}
	return out, nil
}

func (p *PersistentProvider) PlanChange(ctx context.Context, q domain.VMChangeRequest) (*domain.VMChangePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, b := q.Current, q.Desired
	if err := p.checkIdentity(a.Identity); err != nil {
		return nil, err
	}
	if a.ID != b.ID || a.OrgID != b.OrgID {
		return nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	plan := &domain.VMChangePlan{LifecycleClass: domain.VMLifecyclePersistent, ExpectedGeneration: a.Generation, CurrentConfigDigest: a.ConfigDigest, DesiredConfigDigest: b.ConfigDigest, RequiredTier: domain.VMApprovalOperator}
	add := func(field string, class domain.VMChangeClass) {
		plan.Changes = append(plan.Changes, domain.VMFieldChange{Field: field, Class: class, ReasonCode: string(class)})
		if class == domain.VMChangeRequiresStopped {
			plan.RequiresStopped = true
		}
		if class == domain.VMChangeRecreateRequired {
			plan.RecreateRequired = true
			plan.RequiredTier = domain.VMApprovalDestructive
		}
	}
	for _, v := range []struct {
		field string
		a, b  any
	}{{"host", a.HostID, b.HostID}, {"provider", a.Provider, b.Provider}, {"image", a.ImageID, b.ImageID}, {"firmware", a.Firmware, b.Firmware}, {"tpm", a.TPM, b.TPM}, {"network", a.Network, b.Network}, {"storage_pool", a.StoragePoolRef, b.StoragePoolRef}, {"identity", a.Identity, b.Identity}, {"bootstrap", a.Bootstrap, b.Bootstrap}} {
		if !reflect.DeepEqual(v.a, v.b) {
			add(v.field, domain.VMChangeRecreateRequired)
		}
	}
	if a.ImageID == b.ImageID && q.Observation.AppliedImageDigest != "" && q.Image.ManifestDigest != "" && q.Observation.AppliedImageDigest != q.Image.ManifestDigest {
		add("image_digest", domain.VMChangeRecreateRequired)
	}
	if a.Allocation.VCPU > b.Allocation.VCPU || a.Allocation.MemoryBytes > b.Allocation.MemoryBytes {
		plan.RequiredTier = domain.VMApprovalDestructive
	}
	if a.Allocation.VCPU != b.Allocation.VCPU {
		add("vcpu", domain.VMChangeRequiresStopped)
	}
	if a.Allocation.MemoryBytes != b.Allocation.MemoryBytes {
		add("memory", domain.VMChangeRequiresStopped)
	}
	if a.Allocation.DiskBytes != b.Allocation.DiskBytes {
		class := domain.VMChangeRequiresStopped
		if b.Allocation.DiskBytes < a.Allocation.DiskBytes {
			class = domain.VMChangeRecreateRequired
		}
		add("disk", class)
	}
	if a.DisplayName != b.DisplayName || !reflect.DeepEqual(a.Labels, b.Labels) {
		add("display_metadata", domain.VMChangeSafeMutable)
	}
	if a.DesiredPower != b.DesiredPower {
		add("power", domain.VMChangeLifecycle)
	}
	if a.Autostart != b.Autostart {
		add("autostart", domain.VMChangeRequiresStopped)
	}
	return plan, nil
}

func (p *PersistentProvider) Execute(ctx context.Context, q domain.VMProviderOperation) (result *domain.VMProviderResult, err error) {
	op, d := q.Operation, q.Deployment
	result = &domain.VMProviderResult{LifecycleClass: domain.VMLifecyclePersistent, OperationID: op.ID, RetainedStorageRefs: append([]uuid.UUID(nil), op.PreparedStorageRefs...)}
	defer func() {
		if err != nil {
			var pe *domain.VMProviderError
			if !errors.As(err, &pe) {
				err = ProviderError(domain.VMErrorUnconfirmed, err)
				errors.As(err, &pe)
			}
			result.Diagnostic.Code = pe.Code
		}
	}()
	if err = p.checkIdentity(d.Identity); err != nil {
		return result, err
	}
	if domain.ValidateVMOperation(&op) != nil || domain.ValidatePersistentVMDeployment(&d) != nil || op.ResourceID != d.ID || op.OrgID != d.OrgID || op.ResourceGeneration != d.Generation || op.Phase != domain.VMOperationExecuting || q.Host.ID != p.cfg.Host.ID || q.Host.OrgID != p.cfg.Host.OrgID || d.StoragePoolRef != p.cfg.StoragePoolRef {
		return result, ProviderError(domain.VMErrorInvalid, nil)
	}
	if op.RequiredTier == domain.VMApprovalDestructive && op.ApprovalID == nil {
		return result, ProviderError(domain.VMErrorApprovalRequired, nil)
	}
	boundKind := op.Kind
	if op.Kind == domain.VMOperationDelete && op.DataDisposition.Effective() == domain.VMDataExport {
		boundKind = domain.VMOperationExport
	}
	ctx, cancel := p.bound(ctx, boundKind)
	defer cancel()
	ctx, deadlineCancel := context.WithDeadline(ctx, op.Deadline)
	defer deadlineCancel()
	if err = ctx.Err(); err != nil {
		return result, ProviderError(domain.VMErrorUnconfirmed, err)
	}
	// C owns the durable exclusive slot; this lock additionally protects local
	// adapters from concurrent operations within this provider instance.
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		return result, ProviderError(domain.VMErrorUnconfirmed, ctx.Err())
	}
	defer func() { <-p.gate }()
	r, err := p.driver.InspectPersistent(ctx, d.Identity.ProviderResourceID)
	if err != nil {
		return result, err
	}
	rec, err := p.readRecord(r.ID)
	if err != nil {
		return result, ProviderError(domain.VMErrorIntegrity, err)
	}
	if rec != nil && !SameIdentity(rec.Marker.VMResourceIdentity, d.Identity) {
		return result, ProviderError(domain.VMErrorForeign, nil)
	}
	absent := r.State == domain.VMRuntimeAbsent
	owned := r.Marker != nil && domain.ValidateVMOwnershipMarker(*r.Marker) == nil && SameIdentity(r.Marker.VMResourceIdentity, d.Identity)
	if !absent && op.Kind != domain.VMOperationAdopt && (!owned || rec == nil || !reflect.DeepEqual(rec.Marker, *r.Marker)) {
		return result, ProviderError(domain.VMErrorForeign, nil)
	}
	if !absent && op.Kind != domain.VMOperationAdopt {
		if err := CheckWritableComponents(filepath.Dir(p.recordPath(r.ID)), r.Components); err != nil {
			return result, err
		}
	}
	if !absent && op.Kind != domain.VMOperationAdopt && (rec == nil || !maps.Equal(rec.Components, r.Components)) {
		return result, ProviderError(domain.VMErrorIntegrity, nil)
	}
	if owned && r.Marker.AppliedGeneration > op.ResourceGeneration {
		return result, ProviderError(domain.VMErrorConflict, nil)
	}
	// ExpectedGeneration is the desired-revision CAS, not the last applied
	// revision: cancelled admissions may legitimately leave the provider behind.
	if owned && r.Marker.AppliedGeneration > op.ExpectedGeneration && (r.Marker.OperationID != op.ID || r.Marker.AppliedGeneration != op.ResourceGeneration) {
		return result, ProviderError(domain.VMErrorConflict, nil)
	}
	if owned && rec != nil && r.Marker.OperationID == op.ID && r.Marker.AppliedGeneration == op.ResourceGeneration {
		confirmed := false
		switch op.Kind {
		case domain.VMOperationDefine:
			confirmed = rec.Fingerprint == r.Fingerprint
		case domain.VMOperationStart, domain.VMOperationReboot:
			confirmed = r.State == domain.VMRuntimeRunning
		case domain.VMOperationGracefulStop:
			confirmed = r.State == domain.VMRuntimeStopped
		}
		if confirmed {
			result.Observation = p.observation(d.Identity, r, rec)
			result.Confirmed = true
			result.RetainedStorageRefs = nil
			return result, nil
		}
	}
	if op.RequiredTier == domain.VMApprovalDestructive && !absent && op.ProviderFingerprint != r.Fingerprint {
		return result, ProviderError(domain.VMErrorConflict, nil)
	}
	marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: d.Identity, AppliedGeneration: d.Generation, OperationID: op.ID, ImageDigest: q.Image.ManifestDigest, ConfigDigest: d.ConfigDigest}
	if owned {
		marker.ImageDigest = r.Marker.ImageDigest
	}
	switch op.Kind {
	case domain.VMOperationDefine:
		if err = p.define(ctx, q, r, rec, marker); err != nil {
			return result, err
		}
	case domain.VMOperationAdopt:
		if absent || r.State != domain.VMRuntimeStopped || !sha256DigestPattern.MatchString(op.ProviderFingerprint) || op.ProviderFingerprint != r.Fingerprint {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		if op.Adoption == nil {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		measurement, guard, measureErr := p.measureAdoption(ctx, d, q.Image, r)
		if guard != nil {
			defer func() { err = JoinCleanupError(err, guard.Close()) }()
		}
		if measureErr != nil {
			return result, measureErr
		}
		if !reflect.DeepEqual(measurement, op.Adoption) || measurement.ConfigDigest != d.ConfigDigest {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		marker.ImageDigest = measurement.ImageDigest
		if r.Marker != nil && reflectMarker(*r.Marker, marker) && rec != nil && reflect.DeepEqual(rec.Adoption, measurement) && rec.Fingerprint == r.Fingerprint && maps.Equal(rec.Components, r.Components) {
			result.Observation = p.observation(d.Identity, r, rec)
			result.Confirmed = true
			result.RetainedStorageRefs = nil
			return result, nil
		}
		if err = guard.Check(ctx); err != nil {
			return result, err
		}
		if err = p.driver.AdoptPersistent(ctx, r, marker); err != nil {
			return result, err
		}

	case domain.VMOperationStart, domain.VMOperationGracefulStop, domain.VMOperationReboot:
		if absent {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		if err = p.driver.TransitionPersistent(ctx, r, op.Kind, false); err != nil {
			return result, err
		}
		// Lifecycle confirmation does not apply an outstanding hardware revision.
		// Retain the actual configuration for the next PlanChange comparison.
		marker.ConfigDigest = r.Marker.ConfigDigest
		applied := rec.Deployment
		applied.Generation = d.Generation
		applied.DesiredPower = d.DesiredPower
		d = applied
	case domain.VMOperationCheckpoint, domain.VMOperationExport, domain.VMOperationClone, domain.VMOperationRestore:
		return p.transfer(ctx, q, r, rec, result)
	case domain.VMOperationDelete:
		if op.DeleteTarget != domain.VMDeleteDeployment {
			return p.deleteArtifact(ctx, q, result)
		}
		return p.deleteDeployment(ctx, q, r, rec, result)
	default:
		return result, ProviderError(domain.VMErrorUnsupported, nil)
	}
	after, err := p.driver.InspectPersistent(ctx, r.ID)
	if err != nil {
		return result, err
	}
	if after.Marker == nil || !SameIdentity(after.Marker.VMResourceIdentity, d.Identity) {
		return result, ProviderError(domain.VMErrorUnconfirmed, nil)
	}
	if op.Kind != domain.VMOperationDefine && op.Kind != domain.VMOperationAdopt {
		if err = p.driver.AdoptPersistent(ctx, after, marker); err != nil {
			return result, err
		}
		after, err = p.driver.InspectPersistent(ctx, r.ID)
		if err != nil {
			return result, err
		}
	}
	if after.Marker == nil || !reflectMarker(*after.Marker, marker) {
		return result, ProviderError(domain.VMErrorUnconfirmed, nil)
	}
	if op.Kind == domain.VMOperationAdopt {
		if after.Fingerprint != r.Fingerprint || !maps.Equal(after.Components, r.Components) {
			return result, ProviderError(domain.VMErrorUnconfirmed, nil)
		}
		after.Adoption = op.Adoption
	}
	if err = p.writeRecord(ctx, d, after); err != nil {
		return result, err
	}
	rec, err = p.readRecord(r.ID)
	if err != nil {
		return result, err
	}
	result.Observation = p.observation(d.Identity, after, rec)
	result.Confirmed = true
	result.RetainedStorageRefs = nil
	return result, nil
}

func (p *PersistentProvider) define(ctx context.Context, q domain.VMProviderOperation, r *PersistentResource, rec *persistentRecord, marker domain.VMOwnershipMarker) error {
	d := q.Deployment
	// Current guest protocol has health/metrics but no bootstrap opcode. Refuse
	// bindings before definition rather than put secrets into XML/argv/files.
	if len(d.Bootstrap) > 0 || q.Bootstrap != nil {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	if len(d.Network.PassthroughDeviceRefs) > 0 || (d.Provider == domain.VMProviderFirecracker && (d.Network.Mode != domain.VMNetworkIsolated || d.Network.NetworkRef != nil)) {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	if domain.ValidateVMDeploymentReferences(&d, &p.cfg.Host, &q.Image) != nil {
		return ProviderError(domain.VMErrorInvalid, nil)
	}
	if r.State != domain.VMRuntimeAbsent {
		if rec == nil {
			return ProviderError(domain.VMErrorForeign, nil)
		}
		if r.Marker.ImageDigest != q.Image.ManifestDigest {
			return ProviderError(domain.VMErrorConflict, nil)
		}
		plan, err := p.PlanChange(ctx, domain.VMChangeRequest{Current: rec.Deployment, Desired: d})
		if err != nil {
			return err
		}
		if plan.RecreateRequired {
			return ProviderError(domain.VMErrorConflict, nil)
		}
		if q.Operation.RequiredTier < plan.RequiredTier || (plan.RequiredTier == domain.VMApprovalDestructive && q.Operation.ApprovalID == nil) {
			return ProviderError(domain.VMErrorApprovalRequired, nil)
		}
		if !plan.RequiresStopped {
			return p.driver.AdoptPersistent(ctx, r, marker)
		}
		if r.State != domain.VMRuntimeStopped {
			return ProviderError(domain.VMErrorConflict, nil)
		}
	}
	if p.cfg.VerifyImage == nil || p.cfg.ResolveRelease == nil {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	if err := p.cfg.VerifyImage(ctx, p.cfg.Host, q.Image); err != nil {
		return ProviderError(domain.VMErrorIntegrity, err)
	}
	release, err := p.cfg.ResolveRelease(ctx, q.Image)
	if err != nil {
		return ProviderError(domain.VMErrorIntegrity, err)
	}
	if release == nil || release.ManifestDigest != q.Image.ManifestDigest || release.Manifest.Format != string(q.Image.Format) {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	if d.Allocation.MemoryBytes%(1<<20) != 0 {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	dir := filepath.Dir(p.recordPath(r.ID))
	if err := CheckContainedPath(p.cfg.StateDir, dir); err != nil {
		return err
	}
	if r.State == domain.VMRuntimeAbsent {
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			return ProviderError(domain.VMErrorConflict, err)
		}
	}
	if err := writeJSON(ctx, filepath.Join(dir, "operation-"+q.Operation.ID.String()+".json"), struct {
		Marker    domain.VMOwnershipMarker
		Operation domain.VMOperation
	}{marker, q.Operation}); err != nil {
		return err
	}
	spec := PersistentSpec{Deployment: d, Marker: marker, Components: r.Components, Instance: InstanceSpec{Name: r.ID.String(), InstanceDir: dir, Image: release.ImageSpec(), VCPUs: int(d.Allocation.VCPU), MemoryMB: int(d.Allocation.MemoryBytes >> 20)}}
	if err := p.driver.DefinePersistent(ctx, spec, r); err != nil {
		return err
	}
	verified, err := p.cfg.ResolveRelease(ctx, q.Image)
	if err != nil || verified == nil || verified.ManifestDigest != release.ManifestDigest || verified.Dir != release.Dir {
		return ProviderError(domain.VMErrorUnconfirmed, err)
	}
	return nil
}

var _ domain.PersistentVMProvider = (*PersistentProvider)(nil)
