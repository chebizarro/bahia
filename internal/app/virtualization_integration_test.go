//go:build integration

package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/signing"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

type integrationVMProvider struct {
	mu          sync.Mutex
	observation *domain.VMObservation
	effects     int
	crash       bool
	notify      func()
	watched     chan struct{}
}

func (p *integrationVMProvider) Inspect(_ context.Context, id domain.VMResourceIdentity) (*domain.VMObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observation != nil {
		copy := *p.observation
		return &copy, nil
	}
	state := domain.VMRuntimeAbsent
	return &domain.VMObservation{Identity: id, LifecycleClass: id.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Ownership: domain.VMOwnershipUnknown, Diagnostic: domain.VMDiagnostic{EvidenceDigest: "sha256:" + strings.Repeat("a", 64)}}, nil
}
func (p *integrationVMProvider) Inventory(context.Context, domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	return []domain.VMInventoryEntry{}, nil
}
func (p *integrationVMProvider) PlanChange(_ context.Context, q domain.VMChangeRequest) (*domain.VMChangePlan, error) {
	return &domain.VMChangePlan{LifecycleClass: q.Current.LifecycleClass, ExpectedGeneration: q.Current.Generation, CurrentConfigDigest: q.Current.ConfigDigest, DesiredConfigDigest: q.Desired.ConfigDigest, RequiredTier: domain.VMApprovalOperator}, nil
}
func (p *integrationVMProvider) Execute(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.effects++
	state := domain.VMRuntimeStopped
	if q.Operation.Kind == domain.VMOperationReboot || q.Operation.Kind == domain.VMOperationStart {
		state = domain.VMRuntimeRunning
	}
	p.observation = &domain.VMObservation{Identity: q.Deployment.Identity, LifecycleClass: domain.VMLifecyclePersistent, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftInSync, GuestHealth: domain.VMGuestHealthy, Ownership: domain.VMOwned, AppliedImageDigest: q.Image.ManifestDigest, AppliedConfigDigest: q.Deployment.ConfigDigest, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: q.Deployment.Identity, AppliedGeneration: q.Deployment.Generation, OperationID: q.Operation.ID, ImageDigest: q.Image.ManifestDigest, ConfigDigest: q.Deployment.ConfigDigest}, Diagnostic: domain.VMDiagnostic{EvidenceDigest: "sha256:" + strings.Repeat("a", 64)}}
	if p.crash {
		return nil, context.DeadlineExceeded
	}
	return &domain.VMProviderResult{OperationID: q.Operation.ID, LifecycleClass: domain.VMLifecyclePersistent, Confirmed: true}, nil
}
func (p *integrationVMProvider) WatchPersistentVM(ctx context.Context, _ domain.VMResourceIdentity, notify func()) error {
	p.mu.Lock()
	p.notify = notify
	p.mu.Unlock()
	select {
	case p.watched <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

type integrationPlane struct {
	mu             sync.Mutex
	state          domain.ExecutionPlaneObservation
	desired        domain.ExecutionPlaneDesired
	observations   chan domain.ExecutionPlaneObservation
	applied        chan struct{}
	probeAttempts  int
	failFirstProbe bool
}

func (p *integrationPlane) Discover(context.Context, domain.ExecutionPlaneEndpoint) (domain.ExecutionPlaneSupport, error) {
	return domain.ExecutionPlaneSupport{ProtocolVersion: 1, Tools: []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool}, LifecycleClasses: p.desired.LifecycleClasses}, nil
}
func (p *integrationPlane) Inspect(context.Context, domain.ExecutionPlaneEndpoint, uuid.UUID) (*domain.ExecutionPlaneObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := p.state
	return &copy, nil
}
func (p *integrationPlane) Apply(_ context.Context, _ domain.ExecutionPlaneEndpoint, q domain.ExecutionPlaneApplyRequest) (*domain.ExecutionPlaneAcknowledgment, error) {
	p.mu.Lock()
	p.state.PackageDigest, p.state.ConfigRevision, p.state.ImagePins = q.Desired.Package.Digest, q.Desired.Configuration.Revision, q.Desired.ImagePins
	p.state.ReservedCapacity, p.state.Concurrency, p.state.State = q.Desired.ReservedCapacity, q.Desired.Concurrency, q.Desired.State
	copy := p.state
	p.mu.Unlock()
	p.observations <- copy
	p.applied <- struct{}{}
	return &domain.ExecutionPlaneAcknowledgment{OperationID: q.OperationID, Accepted: true}, nil
}
func (p *integrationPlane) Probe(_ context.Context, _ domain.ExecutionPlaneEndpoint, q domain.ExecutionPlaneProbeRequest) (*domain.ExecutionPlaneProbeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probeAttempts++
	if p.failFirstProbe && p.probeAttempts == 1 {
		return nil, context.DeadlineExceeded
	}
	p.state.VMObservationStamp = domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: q.Generation, SessionID: q.SessionID, Sequence: q.Sequence, ObservedAt: time.Now().UTC()}
	evidence := &domain.ExecutionPlaneProbeEvidence{VMObservationStamp: p.state.VMObservationStamp, PlaneID: q.PlaneID, Author: p.state.Author, LifecycleClasses: q.LifecycleClasses, Successful: true, PackageDigest: p.state.PackageDigest, ConfigRevision: p.state.ConfigRevision, ImagePins: p.state.ImagePins, Capabilities: p.desired.ExpectedCapabilities}
	p.state.Probe = evidence
	p.observations <- p.state
	return evidence, nil
}
func (p *integrationPlane) Observe(ctx context.Context, _ domain.ExecutionPlaneEndpoint, _ uuid.UUID, observer domain.ExecutionPlaneObserver) error {
	if err := observer.OnEOSE(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case o := <-p.observations:
			if err := observer.OnObservation(ctx, o); err != nil {
				return err
			}
		}
	}
}

type integrationVMWorkers struct {
	repository.WorkerRepository
	key string
}

func (w integrationVMWorkers) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return &domain.Worker{PubKey: w.key, Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, MaxConcurrentJobs: 2}, nil
}

func TestVirtualizationPostgresIntentRecoveryAndPlane(t *testing.T) {
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
	plane := domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: meta(), HostID: h.ID, WorkerPubKey: key.Hex(), ManagementAuthor: key.Hex(), ManagementEndpointRef: uuid.New(), Desired: domain.ExecutionPlaneDesired{LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}, Package: domain.ExecutionPlanePackagePin{Digest: digest, Version: "1", Provenance: provenance}, Configuration: domain.ExecutionPlaneConfiguration{Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}}, ImagePins: []domain.ExecutionPlaneImagePin{{LifecycleClass: domain.VMLifecycleLoomQEMU, ImageID: image.ID, ManifestDigest: digest}}, ReservedCapacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 4 << 30, DiskBytes: 10 << 30}, Concurrency: 2, ExpectedCapabilities: []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "1"}}, State: domain.ExecutionPlaneEnabled, ProbePolicy: domain.ExecutionPlaneProbePolicy{IntervalSeconds: 60, FreshnessSeconds: 120}}}
	plane.Desired.Configuration.Revision, err = service.ExecutionPlaneConfigRevision(plane.Desired.Configuration)
	require.NoError(t, err)
	boundary := &integrationPlane{failFirstProbe: true, desired: plane.Desired, observations: make(chan domain.ExecutionPlaneObservation, 8), applied: make(chan struct{}, 8), state: domain.ExecutionPlaneObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: uuid.New(), Sequence: 1, ObservedAt: time.Now().UTC()}, PlaneID: plane.ID, HostID: h.ID, Author: key.Hex(), LifecycleClasses: plane.Desired.LifecycleClasses, Availability: domain.VMObservationAvailable, Drift: domain.VMDriftDrifted, PackageDigest: "sha256:" + strings.Repeat("b", 64), ConfigRevision: plane.Desired.Configuration.Revision, ImagePins: plane.Desired.ImagePins, ReservedCapacity: plane.Desired.ReservedCapacity, Concurrency: 2, State: domain.ExecutionPlaneEnabled}}
	provider := &integrationVMProvider{watched: make(chan struct{}, 8)}
	rbac := auth.NewRBAC(vmAppMembers{org})
	policy := &virtualizationPolicy{config: config.VirtualizationConfig{OperatorPubkeys: []string{key.Hex(), approverKey.Hex()}, Hosts: []config.VirtualizationHostPolicy{{OrgID: org, HostID: h.ID, TrustPolicyRef: h.TrustPolicyRef, TrustedSigners: []string{key.Hex()}}}}, rbac: rbac, events: store}
	metrics := telemetry.Setup(telemetry.Config{}, zap.NewNop())
	t.Cleanup(func() { require.NoError(t, metrics.Shutdown(context.Background())) })
	start := func() (*Virtualization, func()) {
		bus := events.NewInProcessPublisher(zap.NewNop())
		v, err := NewVirtualization(VirtualizationDependencies{Repository: repo, RBAC: rbac, Bus: bus, Store: store, Publisher: pub, Organizations: vmAppOrganizations{org}, CanonicalAuthor: key.Hex(), Services: &VirtualizationServices{Provider: provider, Host: &h, VMConfig: service.PersistentVMServiceConfig{Permissions: rbac, Operator: policy.operator}, PlaneClient: boundary, PlanePolicy: policy, Endpoints: []domain.ExecutionPlaneEndpoint{{HostID: h.ID, EndpointRef: plane.ManagementEndpointRef, Author: key.Hex()}}, Workers: integrationVMWorkers{key: key.Hex()}, Logger: zaptest.NewLogger(t)}})
		require.NoError(t, err)
		life, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- v.Run(life) }()
		select {
		case <-v.runtime.started:
		case err := <-done:
			t.Fatalf("startup: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var once sync.Once
		closeRun := func() { once.Do(func() { stop(); require.NoError(t, <-done) }) }
		t.Cleanup(closeRun)
		return v, closeRun
	}
	v, stop := start()
	intent := func(v *Virtualization, method string, m controlplane.VirtualizationMutation) controlplane.VirtualizationAcknowledgment {
		payload, err := json.Marshal(m)
		require.NoError(t, err)
		out, err := v.Handlers.Handle(ctx, method, controlplane.ContextVMRequest{Event: &nostr.Event{PubKey: key}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload}})
		require.NoError(t, err)
		return out.(controlplane.VirtualizationAcknowledgment)
	}
	// Waiting is driven by durable projection checkpoints, never sleeps or timers.
	await := func(check func() bool) {
		for {
			if check() {
				return
			}
			select {
			case <-store.checkpoint:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	t.Log("define")
	ack := intent(v, "persistent-vm/create", controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, VM: &vm, IdempotencyKey: "create", Reason: "integration"})
	await(func() bool {
		op, err := repo.GetOperation(ctx, org, ack.OperationID)
		return err == nil && op.Phase == domain.VMOperationSucceeded
	})
	require.Equal(t, ack.OperationID, intent(v, "persistent-vm/create", controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, VM: &vm, IdempotencyKey: "create", Reason: "integration"}).OperationID)
	select {
	case <-provider.watched:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	provider.mu.Lock()
	provider.observation.GuestHealth = domain.VMGuestUnhealthy
	notify := provider.notify
	provider.mu.Unlock()
	notify()
	await(func() bool {
		vm, err := repo.Deployments().Get(ctx, org, vm.ID)
		return err == nil && vm.Observation != nil && vm.Observation.GuestHealth == domain.VMGuestUnhealthy
	})
	intent(v, "execution-plane/create", controlplane.VirtualizationMutation{OrgID: org, ID: plane.ID, Plane: &plane, IdempotencyKey: "plane", Reason: "integration"})
	select {
	case <-boundary.applied:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Log("plane capability")
	await(func() bool {
		caps, err := v.VerifiedPlanes().VerifiedCapabilities(ctx, key.Hex(), time.Now())
		return err == nil && len(caps) == 1
	})
	boundary.mu.Lock()
	require.GreaterOrEqual(t, boundary.probeAttempts, 2, "transient probe timeout must reconnect without another desired write")
	boundary.mu.Unlock()
	require.NoError(t, v.Metrics.Collect(ctx))
	output := httptest.NewRecorder()
	metrics.MetricsHandler()(output, httptest.NewRequest("GET", "/metrics", nil))
	require.Contains(t, output.Body.String(), "bahia_virtualization_operations_total{")
	require.Contains(t, output.Body.String(), "bahia_virtualization_plane_probes_total{")
	require.Contains(t, output.Body.String(), "bahia_virtualization_desired_observed{")
	require.NoError(t, v.Projector.Recover(ctx, org))
	projected, err := store.ListByKind(ctx, kinds.CASControlState, 100)
	require.NoError(t, err)
	data, _ := json.Marshal(projected)
	require.Contains(t, string(data), vm.ID.String())
	require.Contains(t, string(data), plane.ID.String())
	// Public approval for an exact desired revision, without admitting it first.
	desired, err := repo.Deployments().Get(ctx, org, vm.ID)
	require.NoError(t, err)
	desired.Generation++
	desired.Observation, desired.ObservationCursor = nil, nil
	network := uuid.New()
	desired.Network = domain.VMNetwork{Mode: domain.VMNetworkBridged, NetworkRef: &network}
	proposal := controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, ExpectedGeneration: 1, Operation: domain.VMOperationDefine, VM: desired, IdempotencyKey: "network-change", Reason: "bridge desktop", Requester: key.Hex(), ApprovalReason: "reviewed bridge"}
	payload, err := json.Marshal(proposal)
	require.NoError(t, err)
	_, err = v.Handlers.Handle(ctx, "vm-operation/approve-plan", controlplane.ContextVMRequest{Event: &nostr.Event{PubKey: key}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload}})
	require.Error(t, err, "self approval is forbidden")
	approved, err := v.Handlers.Handle(ctx, "vm-operation/approve-plan", controlplane.ContextVMRequest{Event: &nostr.Event{PubKey: approverKey}, RPC: controlplane.ContextVMJSONRPCRequest{Params: payload}})
	require.NoError(t, err)
	approvalAck := approved.(controlplane.VirtualizationAcknowledgment)
	require.Equal(t, "approved", approvalAck.Status)
	require.NotNil(t, approvalAck.ApprovalID)
	require.Empty(t, approvalAck.OperationDTag, "approval must not invent an operation")
	stored, err := repo.Deployments().Get(ctx, org, vm.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), stored.Generation)
	proposal.ApprovalID = approvalAck.ApprovalID
	proposal.Requester, proposal.ApprovalReason = "", ""
	updated := intent(v, "persistent-vm/update", proposal)
	await(func() bool {
		op, err := repo.GetOperation(ctx, org, updated.OperationID)
		return err == nil && op.Phase == domain.VMOperationSucceeded
	})
	approval, err := repo.GetApproval(ctx, org, *approvalAck.ApprovalID)
	require.NoError(t, err)
	require.NotNil(t, approval.ConsumedAt)
	provider.mu.Lock()
	provider.crash = true
	provider.mu.Unlock()
	interrupted := intent(v, "persistent-vm/operate", controlplane.VirtualizationMutation{OrgID: org, ID: vm.ID, ExpectedGeneration: 2, Operation: domain.VMOperationStart, IdempotencyKey: "interrupted-start", Reason: "restart"})
	t.Log("unconfirmed interrupted")
	await(func() bool {
		op, err := repo.GetOperation(ctx, org, interrupted.OperationID)
		return err == nil && op.Phase == domain.VMOperationUnconfirmed
	})
	stop()
	provider.mu.Lock()
	effects := provider.effects
	provider.crash = false
	provider.mu.Unlock()
	restarted, stopAgain := start()
	defer stopAgain()
	op, err := repo.GetOperation(ctx, org, interrupted.OperationID)
	require.NoError(t, err)
	require.Equal(t, domain.VMOperationSucceeded, op.Phase)
	stopAgain()
	require.NoError(t, restarted.runtime.worker.Recover(ctx, org))
	provider.mu.Lock()
	require.Equal(t, effects, provider.effects)
	provider.mu.Unlock()
	ops, err := repo.ListOperations(ctx, org, vm.ID, 100, 0)
	require.NoError(t, err)
	require.Len(t, ops, 3)
}
