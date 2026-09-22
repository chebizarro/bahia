//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestVMControlPlaneMeasuredAdoptionInventory(t *testing.T) {
	pool, r := vmPostgres(t)
	h, i, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	makeOperation := func(v *domain.PersistentVMDeployment) domain.VMOperation {
		op := vmPGOperation(v, domain.VMOperationAdopt)
		m := &domain.VMAdoptionMeasurement{SchemaVersion: 1, Identity: v.Identity, Generation: v.Generation, ImageID: i.ID, ImageDigest: i.ManifestDigest, ConfigDigest: v.ConfigDigest, ProviderFingerprint: op.ProviderFingerprint, StoragePoolRef: v.StoragePoolRef, Components: []domain.VMAdoptionComponent{{VMComponent: domain.VMComponent{Kind: domain.VMComponentDisk, StorageRef: uuid.NewSHA1(v.ID, []byte("disk")), Digest: vmPGDigest(), SizeBytes: 100}, StorageKey: vmPGDigest(), SourceDigest: vmPGDigest()}}}
		m.Digest = domain.VMAdoptionDigest(*m)
		op.Adoption = m
		now := time.Now().UTC()
		a := &domain.VMApproval{SchemaVersion: 1, ID: uuid.New(), OrgID: v.OrgID, ResourceID: v.ID, LifecycleClass: v.LifecycleClass, Generation: v.Generation, RequestHash: op.RequestHash, ProviderFingerprint: op.ProviderFingerprint, AdoptionDigest: m.Digest, Tier: domain.VMApprovalDestructive, Requester: op.Actor, Approver: "second-operator", Reason: "approve exact measurement", CreatedAt: now, ExpiresAt: now.Add(domain.VMApprovalMaxAge)}
		require.NoError(t, r.CreateApproval(ctx, a))
		op.ApprovalID = &a.ID
		return op
	}
	op := makeOperation(v)
	// Admission checks the separately bound measurement, even with an unchanged
	// request hash and provider definition fingerprint.
	wrong := op
	copy := *op.Adoption
	copy.Components = append([]domain.VMAdoptionComponent(nil), copy.Components...)
	copy.Components[0].SizeBytes++
	copy.Digest = domain.VMAdoptionDigest(copy)
	wrong.Adoption = &copy
	_, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: wrong, ExpectedGeneration: 1, Reservations: []domain.VMCapacityReservation{vmPGReservation(v)}})
	require.ErrorIs(t, err, ErrConflict)
	a, err := r.GetApproval(ctx, v.OrgID, *op.ApprovalID)
	require.NoError(t, err)
	require.Nil(t, a.ConsumedAt)
	admitted, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: op, ExpectedGeneration: 1, Reservations: []domain.VMCapacityReservation{vmPGReservation(v)}})
	require.NoError(t, err)
	executing := vmPGTransition(t, r, &admitted.Operation, domain.VMOperationExecuting)
	rows, err := r.ListAdoptionStorage(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].Registered)
	require.Equal(t, op.Adoption.Components[0], rows[0].Component)
	foreign, err := r.ListAdoptionStorage(ctx, uuid.New(), v.ID)
	require.NoError(t, err)
	require.Empty(t, foreign)
	// Another resource on the same host cannot claim the same writable file.
	peer := vmPGDeployment(h, i)
	require.NoError(t, r.Deployments().Create(ctx, peer))
	peerOp := makeOperation(peer)
	peerAdmitted, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: peerOp, ExpectedGeneration: 1, Reservations: []domain.VMCapacityReservation{vmPGReservation(peer)}})
	require.NoError(t, err)
	_, err = r.TransitionOperation(ctx, VMOperationTransition{OrgID: peer.OrgID, OperationID: peerOp.ID, ExpectedPhase: peerAdmitted.Operation.Phase, ExpectedRevision: peerAdmitted.Operation.Generation, Phase: domain.VMOperationExecuting})
	require.ErrorIs(t, err, ErrConflict)
	verifying := vmPGTransition(t, r, executing, domain.VMOperationVerifying)
	observation := vmPGObserve(t, r, v, op.ID, domain.VMRuntimeStopped)
	_, err = r.TransitionOperation(ctx, VMOperationTransition{OrgID: v.OrgID, OperationID: op.ID, ExpectedPhase: verifying.Phase, ExpectedRevision: verifying.Generation, Phase: domain.VMOperationSucceeded})
	require.ErrorIs(t, err, ErrConflict, "ownership marker without applied pins cannot register inventory")
	observation.Sequence++
	observation.AppliedConfigDigest, observation.AppliedImageDigest = v.ConfigDigest, i.ManifestDigest
	require.NoError(t, r.AcceptVMObservation(ctx, v.OrgID, v.ID, observation))
	vmPGTransition(t, r, verifying, domain.VMOperationSucceeded)
	rows, err = r.ListAdoptionStorage(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.True(t, rows[0].Registered)
	require.Equal(t, op.Adoption.Digest, rows[0].MeasurementDigest)
	down, err := os.ReadFile("../db/migrations/000067_vm_measured_adoption.down.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(down))
	require.ErrorContains(t, err, "rollback refused")
}

func TestVMControlPlaneMeasuredAdoptionMigrationRoundTrip(t *testing.T) {
	pool, _ := vmPostgres(t)
	ctx := context.Background()
	for _, direction := range []string{"down", "up"} {
		sql, err := os.ReadFile("../db/migrations/000067_vm_measured_adoption." + direction + ".sql")
		require.NoError(t, err)
		_, err = pool.Exec(ctx, string(sql))
		require.NoError(t, err)
	}
}
