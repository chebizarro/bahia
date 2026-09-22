package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func testMeta(id, org uuid.UUID) domain.VirtualizationResourceMeta {
	return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: 1, CreatedBy: "test", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}
func persistentRequest(t *testing.T) (PersistentConfig, domain.VMProviderOperation) {
	t.Helper()
	org, host, install, dep, image, pool := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	h := domain.VirtualizationHost{VirtualizationResourceMeta: testMeta(host, org), InstallationID: install, Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 60}
	id := domain.VMResourceIdentity{InstallationID: install, OrgID: org, HostID: host, DeploymentID: dep, Provider: domain.VMProviderLibvirt, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	digest := "sha256:" + strings.Repeat("a", 64)
	d := domain.PersistentVMDeployment{VirtualizationResourceMeta: testMeta(dep, org), LifecycleClass: domain.VMLifecyclePersistent, Purpose: domain.VMPurposeService, Provider: domain.VMProviderLibvirt, HostID: host, ImageID: image, DisplayName: "service", DesiredPower: domain.VMDesiredStopped, Allocation: domain.VMCapacity{VCPU: 2, MemoryBytes: 2 << 30, DiskBytes: 1 << 30}, StoragePoolRef: pool, Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, Firmware: domain.VMFirmwareBIOS, Identity: id, ConfigDigest: digest}
	i := domain.VMImage{VirtualizationResourceMeta: testMeta(image, org), ManifestDigest: digest, Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, Firmware: domain.VMFirmwareBIOS, DriverContract: "libvirt-v1", AgentProtocolVersion: "2", ReleaseRef: uuid.New(), Provenance: domain.VMProvenance{EventID: strings.Repeat("a", 64), Signer: strings.Repeat("b", 64), Verified: true, VerifiedAt: time.Now().UTC()}, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: digest, SizeBytes: 4}}}
	op := domain.VMOperation{VirtualizationResourceMeta: testMeta(uuid.New(), org), LifecycleClass: domain.VMLifecyclePersistent, ResourceID: dep, ResourceGeneration: 1, ExpectedGeneration: 0, IdempotencyKey: "define", RequestHash: digest, Actor: "operator", Reason: "test", Kind: domain.VMOperationDefine, Phase: domain.VMOperationExecuting, RequiredTier: domain.VMApprovalOperator, ProviderCorrelationID: uuid.New(), Deadline: time.Now().Add(time.Hour)}
	cfg := PersistentConfig{Host: h, StoragePoolRef: pool, StateDir: t.TempDir(), VerifyImage: func(context.Context, domain.VirtualizationHost, domain.VMImage) error { return nil }, ResolveRelease: func(context.Context, domain.VMImage) (*Release, error) {
		return &Release{ManifestDigest: digest, Manifest: Manifest{Format: FormatQCOW2, Arch: "amd64"}}, nil
	}}
	q := domain.VMProviderOperation{Host: h, Deployment: d, Image: i, Operation: op}
	if err := domain.ValidateVMDeploymentReferences(&d, &h, &i); err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateVMOperation(&op); err != nil {
		t.Fatal(err)
	}
	return cfg, q
}

type memoryPersistentDriver struct {
	resource       *PersistentResource
	mutations      int
	failTransition bool
	failCopy       domain.VMComponentKind
}

func (f *memoryPersistentDriver) InspectPersistent(_ context.Context, id uuid.UUID) (*PersistentResource, error) {
	if f.resource == nil || f.resource.ID != id {
		return &PersistentResource{ID: id, State: domain.VMRuntimeAbsent}, nil
	}
	r := *f.resource
	return &r, nil
}
func (f *memoryPersistentDriver) ListPersistent(context.Context) ([]uuid.UUID, error) {
	if f.resource == nil {
		return nil, nil
	}
	return []uuid.UUID{f.resource.ID}, nil
}
func (f *memoryPersistentDriver) DefinePersistent(_ context.Context, s PersistentSpec, _ *PersistentResource) error {
	f.mutations++
	f.resource = &PersistentResource{ID: s.Marker.ProviderResourceID, Marker: &s.Marker, State: domain.VMRuntimeStopped, Fingerprint: DigestBytes([]byte("definition")), Components: s.Components}
	if len(s.Components) == 0 {
		path := filepath.Join(s.Instance.InstanceDir, "disk")
		if err := os.WriteFile(path, []byte("disk bytes"), 0600); err != nil {
			return err
		}
		f.resource.Components = map[domain.VMComponentKind]string{domain.VMComponentDisk: path}
	}
	return nil
}
func (f *memoryPersistentDriver) AdoptPersistent(_ context.Context, r *PersistentResource, m domain.VMOwnershipMarker) error {
	f.mutations++
	f.resource.Marker = &m
	return nil
}
func (f *memoryPersistentDriver) TransitionPersistent(_ context.Context, r *PersistentResource, k domain.VMOperationKind, _ bool) error {
	f.mutations++
	if f.failTransition {
		return ProviderError(domain.VMErrorUnconfirmed, errors.New("lost event"))
	}
	switch k {
	case domain.VMOperationStart:
		f.resource.State = domain.VMRuntimeRunning
	case domain.VMOperationGracefulStop:
		f.resource.State = domain.VMRuntimeStopped
	case domain.VMOperationDelete:
		f.resource.State = domain.VMRuntimeAbsent
	}
	return nil
}
func (f *memoryPersistentDriver) CopyPersistentComponent(ctx context.Context, k domain.VMComponentKind, src, dst string) error {
	if f.failCopy == k {
		return errors.New("missing component")
	}
	if k == domain.VMComponentSWTPM {
		return PackTPM(ctx, src, dst)
	}
	return CopyRegularFile(ctx, src, dst)
}

func TestLegacyLookupRejectsAmbiguityAndMismatchedDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := WriteInstanceMetadata(dir, &InstanceMetadata{Name: name, ServiceName: "service", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FindInstancesByService(root, "service"); err == nil {
		t.Fatal("ambiguous legacy lookup selected a newest instance")
	}
	if err := WriteInstanceMetadata(filepath.Join(root, "second"), &InstanceMetadata{Name: "different-directory", ServiceName: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := FindInstancesByService(root, "other"); err == nil {
		t.Fatal("legacy metadata redirected resource identity")
	}
}

func TestPersistentExactOwnershipAndLegacyObservationOnly(t *testing.T) {
	cfg, q := persistentRequest(t)
	f := &memoryPersistentDriver{resource: &PersistentResource{ID: q.Deployment.Identity.ProviderResourceID, State: domain.VMRuntimeStopped, Fingerprint: DigestBytes([]byte("foreign"))}}
	p, err := NewPersistentProvider(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	q.Operation.Kind = domain.VMOperationStart
	q.Operation.ExpectedGeneration = 1
	o, err := p.Inspect(context.Background(), q.Deployment.Identity)
	if err != nil || o.Ownership != domain.VMForeign {
		t.Fatalf("foreign observation: %+v %v", o, err)
	}
	if _, err = p.Execute(context.Background(), q); err == nil || f.mutations != 0 {
		t.Fatal("foreign resource mutated")
	}
	marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: q.Deployment.Identity, AppliedGeneration: 1, OperationID: uuid.New(), ImageDigest: q.Image.ManifestDigest, ConfigDigest: q.Deployment.ConfigDigest}
	f.resource.Marker = &marker
	o, err = p.Inspect(context.Background(), q.Deployment.Identity)
	if err != nil || o.Ownership != domain.VMOrphan {
		t.Fatalf("orphan observation: %+v %v", o, err)
	}
	// A v1 name-scanned metadata record cannot turn an orphan into owned.
	dir := filepath.Dir(p.recordPath(f.resource.ID))
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = WriteInstanceMetadata(dir, &InstanceMetadata{Name: "bahia-prefix", ServiceName: "service"}); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Execute(context.Background(), q); err == nil || f.mutations != 0 {
		t.Fatal("v1 metadata authorized a mutation")
	}
	other := q.Deployment.Identity
	other.DeploymentID = uuid.New()
	if _, err = p.Inspect(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	marker.OrgID = uuid.New()
	f.resource.Marker = &marker
	o, err = p.Inspect(context.Background(), q.Deployment.Identity)
	if err != nil || o.Ownership != domain.VMForeign {
		t.Fatal("cross-org ownership inferred")
	}
}

func TestPersistentDefineUpdateNeverReplaces(t *testing.T) {
	cfg, q := persistentRequest(t)
	f := &memoryPersistentDriver{}
	p, _ := NewPersistentProvider(cfg, f)
	result, err := p.Execute(context.Background(), q)
	if err != nil || !result.Confirmed {
		t.Fatalf("define: %+v %v", result, err)
	}
	q.Deployment.Generation = 2
	q.Operation.ID = uuid.New()
	q.Operation.ResourceGeneration = 2
	q.Operation.ExpectedGeneration = 1
	q.Deployment.ImageID = uuid.New()
	if _, err = p.Execute(context.Background(), q); err == nil || f.mutations != 1 {
		t.Fatalf("recreate change mutated existing resource: %d %v", f.mutations, err)
	}
	q.Deployment.ImageID = q.Image.ID
	q.Deployment.DisplayName = "renamed"
	if _, err = p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if f.mutations != 2 || f.resource.State != domain.VMRuntimeStopped {
		t.Fatal("metadata update replaced guest")
	}
}
func TestCancelledDesiredRevisionDoesNotStrandOwnedVM(t *testing.T) {
	for _, kind := range []domain.VMOperationKind{domain.VMOperationStart, domain.VMOperationDelete} {
		t.Run(string(kind), func(t *testing.T) {
			cfg, q := persistentRequest(t)
			f := &memoryPersistentDriver{}
			p, _ := NewPersistentProvider(cfg, f)
			if _, err := p.Execute(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			actualMemory, actualDigest := q.Deployment.Allocation.MemoryBytes, q.Deployment.ConfigDigest
			// Revision 2 was admitted but cancelled before applying its hardware.
			q.Deployment.Generation = 2
			q.Deployment.Allocation.MemoryBytes *= 2
			q.Deployment.ConfigDigest = DigestBytes([]byte("cancelled hardware"))
			q.Operation.ID = uuid.New()
			q.Operation.ExpectedGeneration = 2
			q.Operation.ResourceGeneration = 2
			q.Operation.Kind = kind
			if kind == domain.VMOperationDelete {
				q.Operation.DeleteTarget = domain.VMDeleteDeployment
				q.Operation.RequiredTier = domain.VMApprovalDestructive
				approval := uuid.New()
				q.Operation.ApprovalID = &approval
				q.Operation.ProviderFingerprint = f.resource.Fingerprint
			}
			result, err := p.Execute(context.Background(), q)
			if err != nil || !result.Confirmed {
				t.Fatalf("older applied revision stranded resource: %+v %v", result, err)
			}
			if kind == domain.VMOperationStart {
				record, err := p.readRecord(f.resource.ID)
				if err != nil || record.Marker.AppliedGeneration != 2 || record.Marker.ConfigDigest != actualDigest || record.Deployment.Allocation.MemoryBytes != actualMemory {
					t.Fatalf("lifecycle falsely applied cancelled hardware: %+v %v", record, err)
				}
				q.Operation.ID = uuid.New()
				q.Operation.ExpectedGeneration = 1
				before := f.mutations
				if _, err = p.Execute(context.Background(), q); err == nil || f.mutations != before {
					t.Fatal("future applied revision accepted")
				}
			}
		})
	}
}

func TestPersistentTimeoutAndFailedDeleteRetainStorage(t *testing.T) {
	cfg, q := persistentRequest(t)
	f := &memoryPersistentDriver{}
	p, _ := NewPersistentProvider(cfg, f)
	if _, err := p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Operation.Kind = domain.VMOperationDelete
	q.Operation.DeleteTarget = domain.VMDeleteDeployment
	q.Operation.ExpectedGeneration = 1
	q.Operation.RequiredTier = domain.VMApprovalDestructive
	approval := uuid.New()
	q.Operation.ApprovalID = &approval
	q.Operation.ProviderFingerprint = f.resource.Fingerprint
	f.failTransition = true
	result, err := p.Execute(context.Background(), q)
	var pe *domain.VMProviderError
	if !errors.As(err, &pe) || !pe.Unconfirmed || result.Confirmed {
		t.Fatalf("delete outcome: %+v %v", result, err)
	}
	if _, err = os.Stat(p.recordPath(f.resource.ID)); err != nil {
		t.Fatal("metadata removed on unconfirmed deletion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := f.mutations
	if _, err = p.Execute(ctx, q); err == nil || f.mutations != before {
		t.Fatal("canceled operation mutated")
	}
}
func TestPersistentMutabilityMatrix(t *testing.T) {
	cfg, q := persistentRequest(t)
	p, _ := NewPersistentProvider(cfg, &memoryPersistentDriver{})
	for _, tt := range []struct {
		name   string
		change func(*domain.PersistentVMDeployment)
		class  domain.VMChangeClass
	}{{"display", func(d *domain.PersistentVMDeployment) { d.DisplayName = "new" }, domain.VMChangeSafeMutable}, {"power", func(d *domain.PersistentVMDeployment) { d.DesiredPower = domain.VMDesiredRunning }, domain.VMChangeLifecycle}, {"cpu", func(d *domain.PersistentVMDeployment) { d.Allocation.VCPU++ }, domain.VMChangeRequiresStopped}, {"growth", func(d *domain.PersistentVMDeployment) { d.Allocation.DiskBytes++ }, domain.VMChangeRequiresStopped}, {"shrink", func(d *domain.PersistentVMDeployment) { d.Allocation.DiskBytes-- }, domain.VMChangeRecreateRequired}, {"image", func(d *domain.PersistentVMDeployment) { d.ImageID = uuid.New() }, domain.VMChangeRecreateRequired}, {"network", func(d *domain.PersistentVMDeployment) { d.Network.Mode = domain.VMNetworkNAT }, domain.VMChangeRecreateRequired}} {
		t.Run(tt.name, func(t *testing.T) {
			desired := q.Deployment
			tt.change(&desired)
			plan, err := p.PlanChange(context.Background(), domain.VMChangeRequest{Current: q.Deployment, Desired: desired})
			if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Class != tt.class {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
		})
	}
}
