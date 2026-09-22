package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestVMOperationWorkerConfirmedLifecycleAndDurableIntent(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, created, err := s.CreateDeployment(ctx, principal, VMCreateRequest{Deployment: v, IdempotencyKey: "create", Reason: "create VM"})
	require.NoError(t, err)
	cancel()
	worker, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.NoError(t, worker.Process(context.Background(), v.OrgID, created.ID))
	done, err := r.GetOperation(context.Background(), v.OrgID, created.ID)
	require.NoError(t, err)
	require.Equal(t, domain.VMOperationSucceeded, done.Phase)
	require.Equal(t, []domain.VMOperationPhase{domain.VMOperationExecuting, domain.VMOperationVerifying, domain.VMOperationSucceeded}, r.transitions)
	require.NotEmpty(t, p.executed[0].Operation.PreparedStorageRefs)
	for _, kind := range []domain.VMOperationKind{domain.VMOperationStart, domain.VMOperationReboot, domain.VMOperationGracefulStop} {
		current, e := r.Deployments().Get(context.Background(), v.OrgID, v.ID)
		require.NoError(t, e)
		op, e := s.RequestOperation(context.Background(), principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: current.Generation, Kind: kind, IdempotencyKey: string(kind), Reason: "requested lifecycle"})
		require.NoError(t, e)
		require.NoError(t, worker.Process(context.Background(), v.OrgID, op.ID))
		require.NoError(t, worker.Process(context.Background(), v.OrgID, op.ID))
		current, e = r.Deployments().Get(context.Background(), v.OrgID, v.ID)
		require.NoError(t, e)
		if kind == domain.VMOperationGracefulStop {
			require.Equal(t, domain.VMDesiredStopped, current.DesiredPower)
			require.Equal(t, domain.VMRuntimeStopped, *current.Observation.RuntimeState)
		} else {
			require.Equal(t, domain.VMDesiredRunning, current.DesiredPower)
			require.Equal(t, domain.VMRuntimeRunning, *current.Observation.RuntimeState)
		}
	}
	require.Len(t, p.executed, 4)
	for _, call := range p.executed {
		require.Equal(t, v.Identity, call.Deployment.Identity)
		require.False(t, call.Operation.AllowForceStop)
	}
	require.Len(t, r.reservations, 1, "stopped VM keeps its reservation")
}
func TestVMOperationWorkerUnconfirmedInspectBeforeReplay(t *testing.T) {
	for _, sideEffect := range []bool{false, true} {
		t.Run(map[bool]string{false: "before effect", true: "after effect"}[sideEffect], func(t *testing.T) {
			s, r, p, v, principal := vmFixture(t)
			_, op := vmCreate(t, s, v, principal)
			worker, err := NewVMOperationWorker(s)
			require.NoError(t, err)
			p.execute = func(ctx context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
				if sideEffect {
					p.complete(q)
				}
				return nil, errors.New("sensitive provider text must not escape")
			}
			err = worker.Process(context.Background(), v.OrgID, op.ID)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive")
			uncertain, _ := r.GetOperation(context.Background(), v.OrgID, op.ID)
			require.Equal(t, domain.VMOperationUnconfirmed, uncertain.Phase)
			_, err = s.Start(context.Background(), principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "overlap", Reason: "overlap"})
			require.Error(t, err)
			restarted, err := NewVMOperationWorker(s)
			require.NoError(t, err)
			err = restarted.Process(context.Background(), v.OrgID, op.ID)
			if sideEffect {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Len(t, p.executed, 1, "recovery inspects and never replays uncertain effects")
			recovered, _ := r.GetOperation(context.Background(), v.OrgID, op.ID)
			if sideEffect {
				require.Equal(t, domain.VMOperationSucceeded, recovered.Phase)
			} else {
				require.Equal(t, domain.VMOperationUnconfirmed, recovered.Phase)
			}
		})
	}
}
func TestVMOperationWorkerCrashAfterExecutingTransition(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	_, op := vmCreate(t, s, v, principal)
	_, err := r.TransitionOperation(context.Background(), repository.VMOperationTransition{OrgID: v.OrgID, OperationID: op.ID, ExpectedPhase: op.Phase, ExpectedRevision: op.Generation, Phase: domain.VMOperationExecuting, PreparedStorageRefs: []uuid.UUID{uuid.New()}})
	require.NoError(t, err)
	worker, _ := NewVMOperationWorker(s)
	require.Error(t, worker.Recover(context.Background(), v.OrgID))
	require.Empty(t, p.executed)
	stored, _ := r.GetOperation(context.Background(), v.OrgID, op.ID)
	require.Equal(t, domain.VMOperationUnconfirmed, stored.Phase)
}
func TestVMOperationWorkerAckIsNotSuccessAndCompletionPersists(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	_, op := vmCreate(t, s, v, principal)
	p.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		return &domain.VMProviderResult{LifecycleClass: domain.VMLifecyclePersistent, OperationID: q.Operation.ID, Confirmed: true}, nil
	}
	w, _ := NewVMOperationWorker(s)
	require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
	stored, _ := r.GetOperation(context.Background(), v.OrgID, op.ID)
	require.Equal(t, domain.VMOperationUnconfirmed, stored.Phase)
	s, r, p, v, principal = vmFixture(t)
	_, op = vmCreate(t, s, v, principal)
	w, _ = NewVMOperationWorker(s)
	failOnce := true
	r.beforeTransition = func(change repository.VMOperationTransition) error {
		if change.Phase == domain.VMOperationSucceeded && failOnce {
			failOnce = false
			return errors.New("database unavailable")
		}
		return nil
	}
	require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
	stored, _ = r.GetOperation(context.Background(), v.OrgID, op.ID)
	require.Equal(t, domain.VMOperationUnconfirmed, stored.Phase)
	require.NoError(t, w.Process(context.Background(), v.OrgID, op.ID))
	require.Len(t, p.executed, 1)
}
func TestVMOperationWorkerGracefulStopNeverForceStops(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	_, create := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(context.Background(), v.OrgID, create.ID))
	p.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		require.False(t, q.Operation.AllowForceStop)
		require.Equal(t, domain.VMOperationGracefulStop, q.Operation.Kind)
		return nil, &domain.VMProviderError{Code: domain.VMErrorUnconfirmed, Unconfirmed: true}
	}
	op, err := s.GracefulStop(context.Background(), principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "stop", Reason: "stop"})
	require.NoError(t, err)
	require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
	require.Len(t, p.executed, 2)
	stored, _ := r.GetOperation(context.Background(), v.OrgID, op.ID)
	require.Equal(t, domain.VMOperationUnconfirmed, stored.Phase)
}
func TestVMOperationWorkerCheckpointAndDelete(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	_, create := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(context.Background(), v.OrgID, create.ID))
	image := r.images[v.ImageID]
	c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactCreating, Firmware: v.Firmware, RetainUntil: vmTestNow.AddDate(0, 1, 0)}
	require.NoError(t, s.RegisterCheckpoint(context.Background(), principal, c))
	op, err := s.Checkpoint(context.Background(), principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "checkpoint", Reason: "cold backup", CheckpointID: &c.ID})
	require.NoError(t, err)
	require.NoError(t, w.Process(context.Background(), v.OrgID, op.ID))
	ready, err := r.Checkpoints().Get(context.Background(), v.OrgID, c.ID)
	require.NoError(t, err)
	require.NoError(t, domain.ValidateVMCheckpoint(ready))
	require.Equal(t, domain.VMArtifactReady, ready.State)
	deletion, err := s.Delete(context.Background(), principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "delete", Reason: "decommission", DeleteTarget: domain.VMDeleteDeployment})
	require.NoError(t, err)
	require.Equal(t, domain.VMOperationAwaitingApproval, deletion.Phase)
	approver := *principal
	approver.PubKey = "approver"
	approver.Subject = "approver"
	_, err = s.ApproveOperation(context.Background(), &approver, v.OrgID, deletion.ID, "approved decommission")
	require.NoError(t, err)
	require.NoError(t, w.Process(context.Background(), v.OrgID, deletion.ID))
	require.Len(t, r.reservations, 1, "checkpoint reservation retained")
	for _, res := range r.reservations {
		require.Equal(t, c.ID, res.ResourceID)
	}
	require.Equal(t, domain.VMOperationDelete, p.executed[len(p.executed)-1].Operation.Kind)
}
func TestVMOperationWorkerCloneRestoreAndExactArtifactReferences(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	h := r.hosts[v.HostID]
	h.PilotPolicy = nil
	h.Capacity.DiskBytes = 1 << 40
	h.Quota.DiskBytes = 1 << 40
	r.hosts[h.ID] = h
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
	image := r.images[v.ImageID]
	c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactCreating, Firmware: v.Firmware, RetainUntil: vmTestNow.AddDate(0, 1, 0)}
	require.NoError(t, s.RegisterCheckpoint(ctx, principal, c))
	checkpoint, err := s.Checkpoint(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "checkpoint", Reason: "cold backup", CheckpointID: &c.ID})
	require.NoError(t, err)
	require.NoError(t, w.Process(ctx, v.OrgID, checkpoint.ID))
	target := v
	target.ID = uuid.New()
	target.Identity.DeploymentID = target.ID
	target.Identity.ProviderResourceID = uuid.New()
	require.NoError(t, s.RegisterCloneTarget(ctx, principal, target))
	cloneRequest := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "clone", Reason: "clone cold checkpoint", CheckpointID: &c.ID, CloneTargetID: &target.ID}
	cloned, err := s.Clone(ctx, principal, cloneRequest)
	require.NoError(t, err)
	require.NoError(t, w.Process(ctx, v.OrgID, cloned.ID))
	got, err := r.Deployments().Get(ctx, v.OrgID, target.ID)
	require.NoError(t, err)
	require.Equal(t, target.Identity, got.Observation.Identity)
	require.Equal(t, cloned.ID, got.Observation.Marker.OperationID)
	restore, err := s.Restore(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "restore", Reason: "restore cold checkpoint", CheckpointID: &c.ID})
	require.NoError(t, err)
	approver := *principal
	approver.PubKey = "approver"
	approver.Subject = "approver"
	_, err = s.ApproveOperation(ctx, &approver, v.OrgID, restore.ID, "approved restore")
	require.NoError(t, err)
	require.NoError(t, w.Process(ctx, v.OrgID, restore.ID))
	require.Equal(t, domain.VMOperationRestore, p.executed[len(p.executed)-1].Operation.Kind)
	wrong := c
	wrong.ID = uuid.New()
	wrong.Identity.DeploymentID = uuid.New()
	r.checkpoints[wrong.ID] = wrong
	_, err = s.Restore(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "wrong checkpoint", Reason: "must reject", CheckpointID: &wrong.ID})
	require.Error(t, err)
}

func TestVMOperationWorkerSerializesConcurrentWorkers(t *testing.T) {
	s, _, p, v, principal := vmFixture(t)
	_, op := vmCreate(t, s, v, principal)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	p.execute = func(ctx context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		close(entered)
		<-release
		return p.complete(q), nil
	}
	worker, _ := NewVMOperationWorker(s)
	go func() { done <- worker.Process(context.Background(), v.OrgID, op.ID) }()
	<-entered
	require.ErrorIs(t, worker.Process(context.Background(), v.OrgID, op.ID), repository.ErrConflict)
	close(release)
	require.NoError(t, <-done)
	require.Len(t, p.executed, 1)
}

func TestVMOperationWorkerOlderAppliedGenerationRemainsManageable(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancelled", false: "definite failure"}[cancelled], func(t *testing.T) {
			s, r, p, v, principal := vmFixture(t)
			ctx := context.Background()
			_, created := vmCreate(t, s, v, principal)
			w, _ := NewVMOperationWorker(s)
			require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
			first, err := s.Start(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "first", Reason: "start"})
			require.NoError(t, err)
			if cancelled {
				_, err = s.CancelOperation(ctx, principal, v.OrgID, first.ID)
				require.NoError(t, err)
			} else {
				p.execute = func(context.Context, domain.VMProviderOperation) (*domain.VMProviderResult, error) {
					return nil, &domain.VMProviderError{Code: domain.VMErrorUnsupported}
				}
				require.Error(t, w.Process(ctx, v.OrgID, first.ID))
				p.execute = nil
			}
			current, err := r.Deployments().Get(ctx, v.OrgID, v.ID)
			require.NoError(t, err)
			require.EqualValues(t, 2, current.Generation)
			require.EqualValues(t, 1, p.observations[v.Identity.ProviderResourceID].Marker.AppliedGeneration)
			retry, err := s.Start(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 2, IdempotencyKey: "retry", Reason: "retry admitted revision"})
			require.NoError(t, err)
			require.NoError(t, w.Process(ctx, v.OrgID, retry.ID))
			require.EqualValues(t, 2, p.observations[v.Identity.ProviderResourceID].Marker.AppliedGeneration)
		})
	}
}

func TestVMOperationWorkerUnconfirmedCloneFencesTarget(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
	image := r.images[v.ImageID]
	c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactReady, Firmware: v.Firmware, RetainUntil: vmTestNow.AddDate(0, 1, 0), ManifestDigest: vmTestDigest("e"), Components: image.Components}
	r.checkpoints[c.ID] = c
	target := v
	target.ID = uuid.New()
	target.Identity.DeploymentID = target.ID
	target.Identity.ProviderResourceID = uuid.New()
	require.NoError(t, s.RegisterCloneTarget(ctx, principal, target))
	clone, err := s.Clone(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "clone", Reason: "clone", CheckpointID: &c.ID, CloneTargetID: &target.ID})
	require.NoError(t, err)
	p.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		p.complete(q)
		return nil, context.DeadlineExceeded
	}
	require.Error(t, w.Process(ctx, v.OrgID, clone.ID))
	before := len(p.executed)
	_, err = s.Start(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: target.ID, ExpectedGeneration: 1, IdempotencyKey: "start-target", Reason: "must remain fenced"})
	require.ErrorIs(t, err, repository.ErrConflict)
	require.Len(t, p.executed, before)
	p.execute = nil
	require.NoError(t, w.Process(ctx, v.OrgID, clone.ID))
	require.Len(t, p.executed, before)
	start, err := s.Start(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: target.ID, ExpectedGeneration: 1, IdempotencyKey: "start-target", Reason: "must remain fenced"})
	require.NoError(t, err)
	require.NoError(t, w.Process(ctx, v.OrgID, start.ID))
}

func TestVMCloneAdmissionSerializesTargetRevision(t *testing.T) {
	s, r, _, v, principal := vmFixture(t)
	ctx := context.Background()
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
	image := r.images[v.ImageID]
	c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactReady, Firmware: v.Firmware, RetainUntil: vmTestNow.AddDate(0, 1, 0), ManifestDigest: vmTestDigest("e"), Components: image.Components}
	r.checkpoints[c.ID] = c
	target := v
	target.ID = uuid.New()
	target.Identity.DeploymentID = target.ID
	target.Identity.ProviderResourceID = uuid.New()
	require.NoError(t, s.RegisterCloneTarget(ctx, principal, target))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	r.beforeAdmission = func(input repository.VMOperationAdmission) {
		if input.Operation.Kind == domain.VMOperationClone {
			close(entered)
			<-release
		}
	}
	go func() {
		_, err := s.Clone(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "clone", Reason: "clone", CheckpointID: &c.ID, CloneTargetID: &target.ID})
		done <- err
	}()
	<-entered
	desired := target
	desired.Generation++
	desired.DisplayName = "changed while clone admission is in flight"
	_, err := s.UpdateDeployment(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: target.ID, ExpectedGeneration: 1, IdempotencyKey: "target-update", Reason: "target revision", Desired: &desired})
	close(release)
	require.ErrorIs(t, err, repository.ErrConflict)
	require.NoError(t, <-done)
	current, err := r.Deployments().Get(ctx, v.OrgID, target.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, current.Generation)
}

func TestVMOperationWorkerArtifactDeletionRequiresExactEvidence(t *testing.T) {
	for _, proof := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack only", true: "verified deletion"}[proof], func(t *testing.T) {
			s, r, p, v, principal := vmFixture(t)
			ctx := context.Background()
			_, created := vmCreate(t, s, v, principal)
			w, _ := NewVMOperationWorker(s)
			require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
			image := r.images[v.ImageID]
			c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactReady, Firmware: v.Firmware, RetainUntil: vmTestNow.Add(-time.Hour), ManifestDigest: vmTestDigest("e"), Components: image.Components}
			r.checkpoints[c.ID] = c
			reserved := vmReservation(v, domain.VMCheckpointResource, c.ID, domain.VMCapacity{DiskBytes: v.Allocation.DiskBytes})
			r.reservations[reserved.ID] = reserved
			deletion, err := s.Delete(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "delete-checkpoint", Reason: "expired checkpoint", DeleteTarget: domain.VMDeleteCheckpoint, CheckpointID: &c.ID})
			require.NoError(t, err)
			approver := *principal
			approver.PubKey = "approver"
			approver.Subject = "approver"
			_, err = s.ApproveOperation(ctx, &approver, v.OrgID, deletion.ID, "approved exact artifact")
			require.NoError(t, err)
			p.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
				result := &domain.VMProviderResult{LifecycleClass: domain.VMLifecyclePersistent, OperationID: q.Operation.ID, Confirmed: true}
				if proof {
					deleted := *q.Checkpoint
					deleted.State = domain.VMArtifactDeleted
					result.Checkpoint = &deleted
				}
				return result, nil
			}
			err = w.Process(ctx, v.OrgID, deletion.ID)
			if proof {
				require.NoError(t, err)
				require.Equal(t, domain.VMArtifactDeleted, r.checkpoints[c.ID].State)
				require.Len(t, r.reservations, 1)
			} else {
				require.Error(t, err)
				require.Equal(t, domain.VMArtifactReady, r.checkpoints[c.ID].State)
				require.Len(t, r.reservations, 2)
				require.Equal(t, domain.VMOperationUnconfirmed, r.ops[deletion.ID].Phase)
			}
		})
	}
}

func TestVMOperationWorkerForeignEvidenceAndExpiry(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	_, op := vmCreate(t, s, v, principal)
	other := v
	other.Identity.ProviderResourceID = uuid.New()
	p.observations[v.Identity.ProviderResourceID] = vmOwnedObservation(other, r.images[v.ImageID], uuid.New(), domain.VMRuntimeStopped)
	w, _ := NewVMOperationWorker(s)
	require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
	require.Empty(t, p.executed)
	delete(p.observations, v.Identity.ProviderResourceID)
	s.cfg.Now = func() time.Time { return op.Deadline.Add(time.Second) }
	require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
	require.Empty(t, p.executed)
}
