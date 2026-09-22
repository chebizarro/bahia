//go:build integration

package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/signing"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func (p *integrationVMProvider) MeasureAdoption(ctx context.Context, q domain.VMChangeRequest) (*domain.VMAdoptionMeasurement, error) {
	o, err := p.Inspect(ctx, q.Desired.Identity)
	if err != nil {
		return nil, err
	}
	m := &domain.VMAdoptionMeasurement{SchemaVersion: 1, Identity: q.Desired.Identity, Generation: q.Desired.Generation, ImageID: q.Image.ID, ImageDigest: q.Image.ManifestDigest, ProviderFingerprint: o.Diagnostic.EvidenceDigest, ConfigDigest: domain.VMAdoptionConfigDigest(q.Desired, o.Diagnostic.EvidenceDigest), StoragePoolRef: q.Desired.StoragePoolRef, Components: []domain.VMAdoptionComponent{{VMComponent: domain.VMComponent{Kind: domain.VMComponentDisk, StorageRef: uuid.NewSHA1(q.Desired.ID, []byte("disk")), Digest: q.Image.Components[0].Digest, SizeBytes: q.Image.Components[0].SizeBytes}, StorageKey: q.Image.Components[0].Digest, SourceDigest: q.Image.Components[0].Digest}}}
	m.Digest = domain.VMAdoptionDigest(*m)
	return m, nil
}

func TestVirtualizationPostgresMeasuredAdoptionIntent(t *testing.T) {
	dsn := os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires a disposable PostgreSQL database")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "vm_app_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	pgcfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	pgcfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pgcfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, pgcfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
		admin.Close()
	})
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	repo := repository.NewPgVirtualizationRepository(pool)
	signer := keyer.NewPlainKeySigner([32]byte{8})
	key, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	approverKey, err := keyer.NewPlainKeySigner([32]byte{9}).GetPublicKey(ctx)
	require.NoError(t, err)
	org := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'integration',$3)`, org, org.String(), key.Hex())
	require.NoError(t, err)
	meta := func() domain.VirtualizationResourceMeta {
		return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: org, Generation: 1, CreatedBy: key.Hex()}
	}
	h := domain.VirtualizationHost{VirtualizationResourceMeta: meta(), InstallationID: uuid.New(), Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent, domain.VMLifecycleLoomQEMU}, Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	require.NoError(t, repo.Hosts().Create(ctx, &h))
	session := uuid.New()
	require.NoError(t, repo.RotateObservationSession(ctx, repository.VirtualizationResourceRef{OrgID: org, Kind: domain.VirtualizationHostResource, ID: h.ID}, 1, uuid.Nil, session))
	require.NoError(t, repo.AcceptHostObservation(ctx, org, h.ID, domain.VirtualizationHostObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: session, Sequence: 1, ObservedAt: time.Now().UTC()}, LifecycleClasses: h.LifecycleClasses, Availability: domain.VMObservationAvailable, Free: h.Capacity}))
	store := &vmAppStore{InMemoryNostrEventRepository: repository.NewInMemoryNostrEventRepository(), checkpoint: make(chan struct{}, 1)}
	pub := vmAppPublisher{store, signer}
	digest := "sha256:" + strings.Repeat("a", 64)
	attestation, err := json.Marshal(signing.NostrArtifactAttestation{ImageDigest: digest, Approved: true})
	require.NoError(t, err)
	evidence := &nostr.Event{Kind: signing.NostrSignatureKind, CreatedAt: nostr.Now(), Content: string(attestation)}
	require.NoError(t, pub.PublishSignedEvent(ctx, evidence))
	provenance := domain.VMProvenance{EventID: evidence.ID.Hex(), Signer: key.Hex(), Verified: true, VerifiedAt: time.Now().UTC()}
	image := domain.VMImage{VirtualizationResourceMeta: meta(), ManifestDigest: digest, Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: h.LifecycleClasses, Firmware: domain.VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "1", ReleaseRef: uuid.New(), Provenance: provenance, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: digest, SizeBytes: 100 << 30}}}
	require.NoError(t, repo.Images().Create(ctx, &image))
	vm := domain.PersistentVMDeployment{VirtualizationResourceMeta: meta(), LifecycleClass: domain.VMLifecyclePersistent, Purpose: domain.VMPurposeDesktop, Provider: h.Provider, HostID: h.ID, ImageID: image.ID, DisplayName: "loom-job-name-is-not-identity", DesiredPower: domain.VMDesiredStopped, Allocation: domain.VMCapacity{VCPU: 8, MemoryBytes: 24 << 30, DiskBytes: 100 << 30}, StoragePoolRef: uuid.New(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, Firmware: domain.VMFirmwareBIOS, ConfigDigest: digest}
	vm.Identity = domain.VMResourceIdentity{InstallationID: h.InstallationID, OrgID: org, HostID: h.ID, DeploymentID: vm.ID, Provider: h.Provider, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}

	state := domain.VMRuntimeStopped
	provider := &integrationVMProvider{observation: &domain.VMObservation{Identity: vm.Identity, LifecycleClass: vm.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Diagnostic: domain.VMDiagnostic{EvidenceDigest: digest}}}
	rbac := auth.NewRBAC(vmAppMembers{org})
	svc, err := service.NewPersistentVMService(service.PersistentVMServiceConfig{Repository: repo, Provider: provider, Permissions: rbac, Operator: func(context.Context, *auth.Principal) error { return nil }})
	require.NoError(t, err)
	runtime := &virtualizationRuntime{repo: repo, vmService: svc}
	runtime.ready.Store(true)
	handlers := controlplane.VirtualizationHandlers{RBAC: rbac, Persistent: vmAdmission{runtime}, CanonicalAuthor: key.Hex(), ProjectionReady: true}
	invoke := func(actor nostr.PubKey, method string, input controlplane.VirtualizationMutation) (controlplane.VirtualizationAcknowledgment, error) {
		data, err := json.Marshal(input)
		require.NoError(t, err)
		out, err := handlers.Handle(ctx, method, controlplane.ContextVMRequest{Event: &nostr.Event{PubKey: actor}, RPC: controlplane.ContextVMJSONRPCRequest{Params: data}})
		if err != nil {
			return controlplane.VirtualizationAcknowledgment{}, err
		}
		return out.(controlplane.VirtualizationAcknowledgment), nil
	}
	vm.ConfigDigest = "" // Registration derives the pin; no caller placeholder is required.
	registered, err := invoke(key, "persistent-vm/register-adoption", controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, VM: &vm})
	require.NoError(t, err)
	require.Equal(t, "registered", registered.Status)
	require.Equal(t, uuid.Nil, registered.OperationID)
	require.Zero(t, provider.effects)
	stored, err := repo.Deployments().Get(ctx, org, vm.ID)
	require.NoError(t, err)
	require.Equal(t, domain.VMAdoptionConfigDigest(vm, digest), stored.ConfigDigest)
	req := controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, ExpectedGeneration: 1, IdempotencyKey: "adoption", Reason: "explicit measured enrollment", Operation: domain.VMOperationAdopt, Requester: key.Hex(), ApprovalReason: "approve exact snapshot"}
	_, err = invoke(key, "vm-operation/approve-plan", req)
	require.Error(t, err)
	approved, err := invoke(approverKey, "vm-operation/approve-plan", req)
	require.NoError(t, err)
	require.Equal(t, "approved", approved.Status)
	require.NotNil(t, approved.ApprovalID)
	req.ApprovalID = approved.ApprovalID
	req.Requester = ""
	req.ApprovalReason = ""
	admitted, err := invoke(key, "persistent-vm/operate", req)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, admitted.OperationID)
	worker, err := service.NewVMOperationWorker(svc)
	require.NoError(t, err)
	require.NoError(t, worker.Process(ctx, org, admitted.OperationID))
	op, err := repo.GetOperation(ctx, org, admitted.OperationID)
	require.NoError(t, err)
	require.Equal(t, domain.VMOperationSucceeded, op.Phase)
	require.NotNil(t, op.Adoption)
	inventory, err := repo.ListAdoptionStorage(ctx, org, vm.ID)
	require.NoError(t, err)
	require.Len(t, inventory, 1)
	require.True(t, inventory[0].Registered)
	require.Equal(t, op.Adoption.Digest, inventory[0].MeasurementDigest)
	require.Equal(t, 1, provider.effects)
}
