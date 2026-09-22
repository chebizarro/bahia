package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestLegacyUnmarkedAndUnconfirmedCleanupPreservesData(t *testing.T) {
	for _, operation := range []string{"replace", "undeploy", "stop", "restart"} {
		t.Run(operation, func(t *testing.T) {
			fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
			ctx := context.Background()
			if err := fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{}); err != nil {
				t.Fatal(err)
			}
			name := InstanceName(uuid.Nil, "api")
			dir := filepath.Join(fx.rt.instancesDir(), name)
			md, _ := ReadInstanceMetadata(dir)
			md.OwnershipID = uuid.Nil
			if err := WriteInstanceMetadata(dir, md); err != nil {
				t.Fatal(err)
			}
			before := len(fx.hv.calls)
			var err error
			switch operation {
			case "replace":
				err = fx.rt.Deploy(ctx, "api", "vm/base@"+fx.digest, DeployOptions{})
			case "undeploy":
				err = fx.rt.Undeploy(ctx, "api")
			case "stop":
				err = fx.rt.Stop(ctx, "api")
			case "restart":
				err = fx.rt.Restart(ctx, "api")
			}
			if err == nil || len(fx.hv.calls) != before {
				t.Fatal("unmarked metadata authorized mutation")
			}
			if _, err = os.Stat(dir); err != nil {
				t.Fatal("unmarked directory erased")
			}
		})
	}
	t.Run("metadata-free-leftover", func(t *testing.T) {
		fx := newCoreFixture(t, domain.RuntimeTypeVMQEMU)
		dir := filepath.Join(fx.rt.instancesDir(), InstanceName(uuid.Nil, "api"))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "foreign-disk")
		if err := os.WriteFile(path, []byte("guest data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := fx.rt.Deploy(context.Background(), "api", "vm/base@"+fx.digest, DeployOptions{}); err == nil {
			t.Fatal("unmarked leftover reclaimed")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "guest data" {
			t.Fatal("leftover data destroyed")
		}
	})
}

func TestRestoreRejectsOwnershipReplacementBetweenInspections(t *testing.T) {
	for _, change := range []string{"foreign", "absent", "fingerprint", "generation", "running"} {
		t.Run(change, func(t *testing.T) {
			p, f, q := checkpointRequest(t)
			created, err := p.Execute(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			q.Checkpoint = created.Checkpoint
			q.Operation.Kind = domain.VMOperationRestore
			q.Operation.ID = uuid.New()
			q.Operation.RequiredTier = domain.VMApprovalDestructive
			approval := uuid.New()
			q.Operation.ApprovalID = &approval
			q.Operation.ProviderFingerprint = f.resource.Fingerprint
			calls := 0
			f.inspectHook = func() {
				calls++
				if calls != 2 {
					return
				}
				marker := *f.resource.Marker
				switch change {
				case "foreign":
					marker.DeploymentID = uuid.New()
					f.resource.Marker = &marker
				case "absent":
					f.resource.State = domain.VMRuntimeAbsent
				case "fingerprint":
					f.resource.Fingerprint = DigestBytes([]byte("replaced"))
				case "generation":
					marker.AppliedGeneration++
					f.resource.Marker = &marker
				case "running":
					f.resource.State = domain.VMRuntimeRunning
				}
			}
			before := f.mutations
			result, err := p.Execute(context.Background(), q)
			if err == nil || result.Confirmed || f.mutations != before {
				t.Fatal("restore discarded admitted ownership baseline")
			}
		})
	}
}

func TestStorageInventoryRejectsSamePoolSubstitution(t *testing.T) {
	p, f, q := checkpointRequest(t)
	other := filepath.Join(p.cfg.StateDir, "instances", uuid.NewString(), "disk")
	if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("foreign data"), 0600); err != nil {
		t.Fatal(err)
	}
	f.resource.Components = map[domain.VMComponentKind]string{domain.VMComponentDisk: other}
	q.Operation.Kind = domain.VMOperationDefine
	q.Operation.ID = uuid.New()
	q.Deployment.Allocation.DiskBytes *= 2
	before := f.mutations
	if _, err := p.Execute(context.Background(), q); err == nil || f.mutations != before {
		t.Fatal("pool containment accepted another resource's storage")
	}
	data, err := os.ReadFile(other)
	if err != nil || string(data) != "foreign data" {
		t.Fatal("foreign disk changed")
	}
}

func TestWritableComponentInventoryRejectsHardlinkedPoolPeer(t *testing.T) {
	root := t.TempDir()
	own, other := filepath.Join(root, "own"), filepath.Join(root, "other")
	for _, dir := range []string{own, other} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(other, "disk")
	if err := os.WriteFile(foreign, []byte("foreign data"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(own, "disk")
	if err := os.Link(foreign, alias); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritableComponents(own, map[domain.VMComponentKind]string{domain.VMComponentDisk: alias}); err == nil {
		t.Fatal("hardlinked peer disk accepted as private storage")
	}
}

func TestColdCheckpointRejectsRoundTripTransitionAndLostWatch(t *testing.T) {
	for _, reason := range []string{"start-stop", "watch-lost"} {
		t.Run(reason, func(t *testing.T) {
			p, f, q := checkpointRequest(t)
			f.copyHook = func() { f.coldInvalid = true; f.resource.State = domain.VMRuntimeStopped }
			result, err := p.Execute(context.Background(), q)
			if err == nil || result.Confirmed || result.Checkpoint != nil {
				t.Fatal("cold checkpoint published across invalidated interval")
			}
			if _, err = os.Stat(p.artifactDir("checkpoints", q.Checkpoint.ID)); !os.IsNotExist(err) {
				t.Fatal("invalid cold set published")
			}
		})
	}
}

func TestAdoptionCannotClaimUnverifiedDesiredPins(t *testing.T) {
	cfg, q := persistentRequest(t)
	f := &memoryPersistentDriver{resource: &PersistentResource{ID: q.Deployment.Identity.ProviderResourceID, State: domain.VMRuntimeStopped, Fingerprint: DigestBytes([]byte("unmeasured foreign definition"))}}
	p, _ := NewPersistentProvider(cfg, f)
	q.Operation.Kind = domain.VMOperationAdopt
	q.Operation.RequiredTier = domain.VMApprovalDestructive
	approval := uuid.New()
	q.Operation.ApprovalID = &approval
	q.Operation.ProviderFingerprint = f.resource.Fingerprint
	result, err := p.Execute(context.Background(), q)
	if err == nil || result.Confirmed || f.mutations != 0 {
		t.Fatal("adoption asserted desired config/image without measurement")
	}
	if rec, err := p.readRecord(f.resource.ID); err != nil || rec != nil {
		t.Fatal("unverified applied record written")
	}
}
