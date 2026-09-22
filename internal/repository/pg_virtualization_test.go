//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// An explicit integration gate fails rather than silently skipping database proof.
// Every test owns a fresh schema; never point this at a production database.
func vmPostgres(t *testing.T) (*pgxpool.Pool, *PgVirtualizationRepository) {
	t.Helper()
	dsn := os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires BAHIA_VM_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "vm_item_a_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
		admin.Close()
	})
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	return pool, NewPgVirtualizationRepository(pool)
}
func vmPGDigest() string { return "sha256:" + strings.Repeat("a", 64) }
func vmPGMeta(org uuid.UUID) domain.VirtualizationResourceMeta {
	return domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: org, Generation: 1, CreatedBy: "operator"}
}
func vmPGFixtures(t *testing.T, pool *pgxpool.Pool, r *PgVirtualizationRepository) (*domain.VirtualizationHost, *domain.VMImage, *domain.PersistentVMDeployment) {
	t.Helper()
	ctx := context.Background()
	org := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'VM test','operator')`, org, org.String())
	require.NoError(t, err)
	h := &domain.VirtualizationHost{VirtualizationResourceMeta: vmPGMeta(org), InstallationID: uuid.New(), Provider: domain.VMProviderLibvirt, ExecutionLocation: domain.VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent, domain.VMLifecycleLoomQEMU}, Capacity: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, Quota: domain.VMCapacity{VCPU: 16, MemoryBytes: 64 << 30, DiskBytes: 220 << 30}, OperationLimits: domain.DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	require.NoError(t, r.Hosts().Create(ctx, h))
	session := uuid.New()
	require.NoError(t, r.RotateObservationSession(ctx, VirtualizationResourceRef{org, domain.VirtualizationHostResource, h.ID}, 1, uuid.Nil, session))
	obs := domain.VirtualizationHostObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: session, Sequence: 1, ObservedAt: time.Now().UTC()}, LifecycleClasses: h.LifecycleClasses, Availability: domain.VMObservationAvailable, Free: h.Capacity}
	require.NoError(t, r.AcceptHostObservation(ctx, org, h.ID, obs))
	i := &domain.VMImage{VirtualizationResourceMeta: vmPGMeta(org), ManifestDigest: vmPGDigest(), Format: domain.VMImageQCOW2, Architecture: "amd64", OS: domain.VMOSLinux, LifecycleClasses: h.LifecycleClasses, Firmware: domain.VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "1", ReleaseRef: uuid.New(), Provenance: domain.VMProvenance{EventID: strings.Repeat("b", 64), Signer: strings.Repeat("c", 64), Verified: true, VerifiedAt: time.Now().UTC()}, Components: []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: vmPGDigest(), SizeBytes: 100 << 30}}}
	require.NoError(t, r.Images().Create(ctx, i))
	v := vmPGDeployment(h, i)
	require.NoError(t, r.Deployments().Create(ctx, v))
	return h, i, v
}
func vmPGDeployment(h *domain.VirtualizationHost, i *domain.VMImage) *domain.PersistentVMDeployment {
	v := &domain.PersistentVMDeployment{VirtualizationResourceMeta: vmPGMeta(h.OrgID), LifecycleClass: domain.VMLifecyclePersistent, Purpose: domain.VMPurposeDesktop, Provider: h.Provider, HostID: h.ID, ImageID: i.ID, DisplayName: "desktop", DesiredPower: domain.VMDesiredStopped, Allocation: domain.VMCapacity{VCPU: 8, MemoryBytes: 24 << 30, DiskBytes: 100 << 30}, StoragePoolRef: uuid.New(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}, Firmware: domain.VMFirmwareBIOS, ConfigDigest: vmPGDigest()}
	v.Identity = domain.VMResourceIdentity{InstallationID: h.InstallationID, OrgID: h.OrgID, HostID: h.ID, DeploymentID: v.ID, Provider: h.Provider, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	return v
}
func vmPGReservation(v *domain.PersistentVMDeployment) domain.VMCapacityReservation {
	return domain.VMCapacityReservation{ID: uuid.New(), OrgID: v.OrgID, HostID: v.HostID, ResourceID: v.ID, ResourceKind: domain.PersistentVMResource, LifecycleClass: v.LifecycleClass, Capacity: v.Allocation}
}
func vmPGObserve(t *testing.T, r *PgVirtualizationRepository, v *domain.PersistentVMDeployment, operation uuid.UUID, state domain.VMRuntimeState) domain.VMObservation {
	t.Helper()
	ctx := context.Background()
	session := uuid.New()
	require.NoError(t, r.RotateObservationSession(ctx, VirtualizationResourceRef{v.OrgID, domain.PersistentVMResource, v.ID}, v.Generation, uuid.Nil, session))
	now := time.Now().UTC()
	o := domain.VMObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: v.Generation, SessionID: session, Sequence: 1, ObservedAt: now}, Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, RuntimeObservedAt: &now, Drift: domain.VMDriftInSync, GuestHealth: domain.VMGuestHealthy, Ownership: domain.VMOwned, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: v.Identity, AppliedGeneration: v.Generation, OperationID: operation, ImageDigest: vmPGDigest(), ConfigDigest: v.ConfigDigest}}
	require.NoError(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, o))
	return o
}
func TestVMControlPlanePostgresMigrationUpDown(t *testing.T) {
	pool, _ := vmPostgres(t)
	ctx := context.Background()
	up, err := os.ReadFile("../db/migrations/000066_vm_control_plane.up.sql")
	require.NoError(t, err)
	down, err := os.ReadFile("../db/migrations/000066_vm_control_plane.down.sql")
	require.NoError(t, err)
	org, env, unit := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO organizations(id,name,display_name,owner_pubkey) VALUES($1,$2,'legacy','operator')`, org, org.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,org_id,name) VALUES($1,$2,'legacy-environment')`, env, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO deployment_units(id,environment_id,unit_key,runtime_type,reconcile_mode,ownership_mode) VALUES($1,$2,'default','docker','observe_only','bahia_managed')`, unit, env)
	require.NoError(t, err)
	var before string
	require.NoError(t, pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM deployment_units d WHERE id=$1`, unit).Scan(&before))
	_, err = pool.Exec(ctx, string(down))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	var after string
	require.NoError(t, pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM deployment_units d WHERE id=$1`, unit).Scan(&after))
	require.Equal(t, before, after)
	r := NewPgVirtualizationRepository(pool)
	vmPGFixtures(t, pool, r)
	_, err = pool.Exec(ctx, string(down))
	require.ErrorContains(t, err, "rollback refused")
}
func TestVMControlPlanePostgresResourcesCASAndJournal(t *testing.T) {
	pool, r := vmPostgres(t)
	h, i, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	got, err := r.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Bootstrap)
	require.NotNil(t, got.Labels)
	require.Equal(t, v.Identity, got.Identity)
	_, err = r.Deployments().Get(ctx, uuid.New(), v.ID)
	require.ErrorIs(t, err, ErrNotFound)
	list, err := r.Deployments().List(ctx, v.OrgID, 10, 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
	before, err := r.ListChanges(ctx, v.OrgID, 0, 100)
	require.NoError(t, err)
	stale := *v
	v.Generation++
	v.DisplayName = "renamed"
	require.NoError(t, r.Deployments().Update(ctx, v, 1))
	stale.Generation++
	stale.DisplayName = "stale"
	require.ErrorIs(t, r.Deployments().Update(ctx, &stale, 1), ErrConflict)
	changes, err := r.ListChanges(ctx, v.OrgID, before[len(before)-1].Sequence, 100)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "updated", changes[0].ChangeType)
	i.Generation++
	i.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
	require.ErrorIs(t, r.Images().Update(ctx, i, 1), ErrConflict)
	h.Generation++
	h.Provider = domain.VMProviderFirecracker
	require.ErrorIs(t, r.Hosts().Update(ctx, h, 1), ErrConflict)
	// Direct SQL cannot desynchronize typed identity from the JSON envelope or relax enums.
	_, err = pool.Exec(ctx, `UPDATE persistent_vm_deployments SET document=jsonb_set(document,'{lifecycle_class}','"unknown"') WHERE id=$1`, v.ID)
	require.Error(t, err)
	_, err = pool.Exec(ctx, `UPDATE persistent_vm_deployments SET document=jsonb_set(document,'{schema_version}','2') WHERE id=$1`, v.ID)
	require.Error(t, err)
}
func TestVMControlPlanePostgresQuotaRaceAndStoppedRetention(t *testing.T) {
	pool, r := vmPostgres(t)
	h, i, a := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	b := vmPGDeployment(h, i)
	c := vmPGDeployment(h, i)
	require.NoError(t, r.Deployments().Create(ctx, b))
	require.NoError(t, r.Deployments().Create(ctx, c))
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, v := range []*domain.PersistentVMDeployment{a, b, c} {
		wg.Add(1)
		go func(v *domain.PersistentVMDeployment) {
			defer wg.Done()
			<-start
			results <- r.ReserveCapacity(ctx, vmPGReservation(v))
		}(v)
	}
	close(start)
	wg.Wait()
	close(results)
	passed := 0
	for err := range results {
		if err == nil {
			passed++
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
	require.Equal(t, 2, passed)
	rs, err := r.ListReservations(ctx, h.OrgID, h.ID)
	require.NoError(t, err)
	require.Len(t, rs, 2)
	require.NoError(t, r.ReserveCapacity(ctx, rs[0]), "reservation replay")
	v, err := r.Deployments().Get(ctx, h.OrgID, rs[0].ResourceID)
	require.NoError(t, err)
	o := vmPGObserve(t, r, v, uuid.New(), domain.VMRuntimeStopped)
	require.ErrorIs(t, r.ReleaseCapacity(ctx, h.OrgID, rs[0].ID), ErrConflict, "stopped keeps capacity")
	absent := domain.VMRuntimeAbsent
	o.RuntimeState = &absent
	o.Sequence++
	o.ObservedAt = time.Now().UTC()
	o.RuntimeObservedAt = &o.ObservedAt
	require.NoError(t, r.AcceptVMObservation(ctx, h.OrgID, v.ID, o))
	require.NoError(t, r.ReleaseCapacity(ctx, h.OrgID, rs[0].ID))
}
func TestVMControlPlanePostgresArtifactsAndPlaneProbes(t *testing.T) {
	pool, r := vmPostgres(t)
	h, i, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	c := &domain.VMCheckpoint{VirtualizationResourceMeta: vmPGMeta(v.OrgID), LifecycleClass: v.LifecycleClass, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: i.ID, ImageDigest: i.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactCreating, Firmware: v.Firmware}
	require.NoError(t, r.Checkpoints().Create(ctx, c))
	c.Generation++
	c.State = domain.VMArtifactReady
	c.Components = append([]domain.VMComponent{}, i.Components...)
	c.ManifestDigest = vmPGDigest()
	c.RetainUntil = time.Now().UTC().Add(time.Hour)
	require.NoError(t, r.Checkpoints().Update(ctx, c, 1))
	frozen := *c
	frozen.Generation++
	frozen.Components = append([]domain.VMComponent{}, c.Components...)
	frozen.Components[0].Digest = "sha256:" + strings.Repeat("e", 64)
	require.ErrorIs(t, r.Checkpoints().Update(ctx, &frozen, 2), ErrConflict)
	e := &domain.VMExport{VirtualizationResourceMeta: vmPGMeta(v.OrgID), LifecycleClass: v.LifecycleClass, CheckpointID: c.ID, State: domain.VMArtifactCreating, StorageRef: uuid.New(), AccessPolicyRef: uuid.New()}
	require.NoError(t, r.Exports().Create(ctx, e))
	e.Generation++
	e.State = domain.VMArtifactReady
	e.ManifestDigest = vmPGDigest()
	e.Components = append([]domain.VMComponent{}, c.Components...)
	e.Components[0].StorageRef = uuid.New()
	e.Provenance = i.Provenance
	e.RetainUntil = c.RetainUntil
	require.NoError(t, r.Exports().Update(ctx, e, 1))
	stored, err := r.Exports().Get(ctx, e.OrgID, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.ManifestDigest, stored.ManifestDigest)
	require.Equal(t, e.Components, stored.Components)
	p := &domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: vmPGMeta(v.OrgID), HostID: h.ID, WorkerPubKey: strings.Repeat("d", 64), ManagementEndpointRef: uuid.New(), ManagementAuthor: strings.Repeat("e", 64), Desired: domain.ExecutionPlaneDesired{LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}, Package: domain.ExecutionPlanePackagePin{Digest: vmPGDigest(), Version: "1", Provenance: i.Provenance}, Configuration: domain.ExecutionPlaneConfiguration{Revision: vmPGDigest(), Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}}, ImagePins: []domain.ExecutionPlaneImagePin{{LifecycleClass: domain.VMLifecycleLoomQEMU, ImageID: i.ID, ManifestDigest: i.ManifestDigest}}, ReservedCapacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 4 << 30, DiskBytes: 10 << 30}, Concurrency: 2, ExpectedCapabilities: []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "1"}}, State: domain.ExecutionPlaneEnabled, ProbePolicy: domain.DefaultExecutionPlaneProbePolicy()}}
	require.NoError(t, r.ExecutionPlanes().Create(ctx, p))
	session := uuid.New()
	ref := VirtualizationResourceRef{p.OrgID, domain.ExecutionPlaneResource, p.ID}
	require.NoError(t, r.RotateObservationSession(ctx, ref, 1, uuid.Nil, session))
	stamp := domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: session, Sequence: 1, ObservedAt: time.Now().UTC()}
	probe := domain.ExecutionPlaneProbeEvidence{VMObservationStamp: stamp, PlaneID: p.ID, Author: p.ManagementAuthor, LifecycleClasses: p.Desired.LifecycleClasses, Successful: true, PackageDigest: p.Desired.Package.Digest, ConfigRevision: p.Desired.Configuration.Revision, ImagePins: p.Desired.ImagePins, Capabilities: p.Desired.ExpectedCapabilities}
	o := domain.ExecutionPlaneObservation{VMObservationStamp: stamp, PlaneID: p.ID, HostID: p.HostID, Author: p.ManagementAuthor, LifecycleClasses: p.Desired.LifecycleClasses, Availability: domain.VMObservationAvailable, Drift: domain.VMDriftInSync, PackageDigest: probe.PackageDigest, ConfigRevision: probe.ConfigRevision, ImagePins: probe.ImagePins, ReservedCapacity: p.Desired.ReservedCapacity, Concurrency: p.Desired.Concurrency, State: domain.ExecutionPlaneEnabled, Probe: &probe}
	require.NoError(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o))
	plane, err := r.ExecutionPlanes().Get(ctx, p.OrgID, p.ID)
	require.NoError(t, err)
	require.Len(t, domain.EffectiveExecutionPlaneCapabilities(plane, session, time.Now().UTC()).Capabilities, 1)
	failed := probe
	failed.Sequence = 2
	failed.Successful = false
	failed.Capabilities = nil
	failed.ObservedAt = time.Now().UTC()
	o.Sequence = 2
	o.ObservedAt = failed.ObservedAt
	o.Probe = &failed
	require.NoError(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o))
	o.Sequence = 3
	o.ObservedAt = time.Now().UTC()
	o.Probe = nil
	require.NoError(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o))
	plane, err = r.ExecutionPlanes().Get(ctx, p.OrgID, p.ID)
	require.NoError(t, err)
	require.Empty(t, domain.EffectiveExecutionPlaneCapabilities(plane, session, time.Now().UTC()).Capabilities)
	o.Sequence = 4
	o.ObservedAt = time.Now().UTC()
	o.Probe = &probe
	require.ErrorIs(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o), ErrConflict, "old success cannot replace failed probe")
	probe.Sequence = 2
	probe.ObservedAt = failed.ObservedAt
	require.ErrorIs(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o), ErrConflict, "same probe sequence cannot rewrite outcome")
	probe.Sequence = 4
	probe.ObservedAt = o.ObservedAt
	probe.Capabilities = []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSWindows, Architecture: "amd64", AgentProtocolVersion: "1"}}
	require.Error(t, r.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o))
}

func TestVMControlPlanePostgresObservationFencing(t *testing.T) {
	pool, r := vmPostgres(t)
	_, _, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	o := vmPGObserve(t, r, v, uuid.New(), domain.VMRuntimeRunning)
	require.ErrorIs(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, o), ErrConflict, "duplicate sequence")
	stale := o
	stale.Sequence++
	stale.SessionID = uuid.New()
	require.ErrorIs(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, stale), ErrConflict)
	stale = o
	stale.Sequence++
	stale.ObservedGeneration++
	require.Error(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, stale))
	unavailable := o
	unavailable.Sequence++
	unavailable.ObservedAt = time.Now().UTC()
	unavailable.Availability = domain.VMObservationUnavailable
	unavailable.Drift = domain.VMDriftUnknown
	fabricated := domain.VMRuntimeAbsent
	unavailable.RuntimeState = &fabricated
	unavailable.RuntimeObservedAt = &unavailable.ObservedAt
	require.NoError(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, unavailable))
	got, err := r.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.Equal(t, *o.RuntimeState, *got.Observation.RuntimeState)
	require.True(t, o.RuntimeObservedAt.Equal(*got.Observation.RuntimeObservedAt))
	next := uuid.New()
	ref := VirtualizationResourceRef{v.OrgID, domain.PersistentVMResource, v.ID}
	require.NoError(t, r.RotateObservationSession(ctx, ref, 1, o.SessionID, next))
	o.Sequence = 100
	require.ErrorIs(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, o), ErrConflict)
	require.ErrorIs(t, r.RotateObservationSession(ctx, ref, 1, o.SessionID, uuid.New()), ErrConflict)
}
