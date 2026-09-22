package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func checkpointRequest(t *testing.T) (*PersistentProvider, *memoryPersistentDriver, domain.VMProviderOperation) {
	t.Helper()
	cfg, q := persistentRequest(t)
	f := &memoryPersistentDriver{}
	p, _ := NewPersistentProvider(cfg, f)
	if _, err := p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Operation.ID = uuid.New()
	q.Operation.Kind = domain.VMOperationCheckpoint
	q.Operation.ExpectedGeneration = 1
	id := uuid.New()
	q.Operation.CheckpointID = &id
	q.Operation.PreparedStorageRefs = []uuid.UUID{uuid.New()}
	q.Checkpoint = &domain.VMCheckpoint{VirtualizationResourceMeta: testMeta(id, q.Deployment.OrgID), LifecycleClass: domain.VMLifecyclePersistent, Identity: q.Deployment.Identity, State: domain.VMArtifactCreating, RetainUntil: time.Now().Add(time.Hour)}
	return p, f, q
}
func TestCoordinatedCheckpointManifestAndTamperRejection(t *testing.T) {
	p, f, q := checkpointRequest(t)
	q.Deployment.Firmware = domain.VMFirmwareUEFI
	q.Deployment.TPM.Enabled = true
	tid := uuid.New()
	q.Deployment.TPM.IdentityID = &tid
	dir := filepath.Dir(p.recordPath(f.resource.ID))
	nvram := filepath.Join(dir, "nvram")
	if err := os.WriteFile(nvram, []byte("UEFI variables"), 0600); err != nil {
		t.Fatal(err)
	}
	tpm := filepath.Join(dir, "swtpm")
	if err := os.Mkdir(tpm, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tpm, "tpm2-00.permall"), []byte("TPM identity"), 0600); err != nil {
		t.Fatal(err)
	}
	f.resource.Components[domain.VMComponentNVRAM] = nvram
	f.resource.Components[domain.VMComponentSWTPM] = tpm
	q.Operation.PreparedStorageRefs = append(q.Operation.PreparedStorageRefs, uuid.New(), uuid.New())
	if err := p.writeRecord(context.Background(), q.Deployment, f.resource); err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(context.Background(), q)
	if err != nil || !result.Confirmed {
		t.Fatalf("checkpoint: %+v %v", result, err)
	}
	c := result.Checkpoint
	data, err := os.ReadFile(filepath.Join(p.artifactDir("checkpoints", c.ID), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved domain.VMCheckpoint
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	savedJSON, _ := json.Marshal(saved)
	resultJSON, _ := json.Marshal(c)
	if string(savedJSON) != string(resultJSON) {
		t.Fatal("public manifest differs from committed manifest")
	}
	projection := struct {
		Consistency domain.VMCheckpointConsistency `json:"consistency"`
		State       domain.VMArtifactState         `json:"state"`
		Firmware    domain.VMFirmware              `json:"firmware"`
		TPM         bool                           `json:"tpm"`
		Kinds       []domain.VMComponentKind       `json:"kinds"`
	}{c.Consistency, c.State, c.Firmware, c.TPMEnabled, nil}
	for _, component := range c.Components {
		projection.Kinds = append(projection.Kinds, component.Kind)
		digest, size, err := HashComponent(context.Background(), filepath.Join(p.artifactDir("checkpoints", c.ID), component.StorageRef.String()))
		if err != nil || digest != component.Digest || size != component.SizeBytes {
			t.Fatal("component hash/size not backed by copied bytes")
		}
	}
	fixture, err := os.ReadFile("testdata/conformance/coordinated-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err = json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	actualBytes, _ := json.Marshal(projection)
	var actual any
	_ = json.Unmarshal(actualBytes, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("manifest transform %s", actualBytes)
	}
	q.Checkpoint = c
	if _, _, err = p.loadCheckpoint(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.artifactDir("checkpoints", c.ID), c.Components[0].StorageRef.String()), []byte("tamper"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.loadCheckpoint(context.Background(), q); err == nil {
		t.Fatal("tampered checkpoint accepted")
	}
}
func TestCheckpointMissingComponentNeverPublishesReady(t *testing.T) {
	p, f, q := checkpointRequest(t)
	q.Deployment.Firmware = domain.VMFirmwareUEFI
	q.Operation.PreparedStorageRefs = append(q.Operation.PreparedStorageRefs, uuid.New())
	f.resource.Components[domain.VMComponentNVRAM] = "/missing/nvram"
	f.failCopy = domain.VMComponentNVRAM
	result, err := p.Execute(context.Background(), q)
	if err == nil || result.Confirmed {
		t.Fatal("incomplete coordinated set reported ready")
	}
	if _, err = os.Stat(p.artifactDir("checkpoints", q.Checkpoint.ID)); !os.IsNotExist(err) {
		t.Fatal("failed checkpoint published")
	}
	f.resource.State = domain.VMRuntimePaused
	if _, err = p.Execute(context.Background(), q); err == nil {
		t.Fatal("paused treated as cold")
	}
}
func TestRestoreStagesNewSetAndRetainsOriginal(t *testing.T) {
	p, f, q := checkpointRequest(t)
	original := f.resource.Components[domain.VMComponentDisk]
	result, err := p.Execute(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.Checkpoint = result.Checkpoint
	if err = os.WriteFile(original, []byte("new writes"), 0600); err != nil {
		t.Fatal(err)
	}
	q.Operation.Kind = domain.VMOperationRestore
	q.Operation.ID = uuid.New()
	q.Operation.RequiredTier = domain.VMApprovalDestructive
	approval := uuid.New()
	q.Operation.ApprovalID = &approval
	q.Operation.ProviderFingerprint = f.resource.Fingerprint
	result, err = p.Execute(context.Background(), q)
	if err != nil || !result.Confirmed {
		t.Fatalf("restore: %+v %v", result, err)
	}
	old, err := os.ReadFile(original)
	if err != nil || string(old) != "new writes" {
		t.Fatal("original coordinated set overwritten")
	}
	restored, err := os.ReadFile(f.resource.Components[domain.VMComponentDisk])
	if err != nil || string(restored) != "disk bytes" {
		t.Fatal("restored set not independently staged")
	}
	if f.resource.Components[domain.VMComponentDisk] == original {
		t.Fatal("in-place restore")
	}
}
func TestNonTPMCloneUsesNewIdentityAndIndependentStoppedStorage(t *testing.T) {
	p, f, q := checkpointRequest(t)
	ctx := context.Background()
	sourceID := q.Deployment.Identity.ProviderResourceID
	sourceDisk := f.resource.Components[domain.VMComponentDisk]
	created, err := p.Execute(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	q.Checkpoint = created.Checkpoint
	q.Operation.Kind = domain.VMOperationClone
	q.Operation.ID = uuid.New()
	target := q.Deployment
	target.ID = uuid.New()
	target.Identity.DeploymentID = target.ID
	target.Identity.ProviderResourceID = uuid.New()
	q.CloneTarget = &target
	q.Operation.CloneTargetID = &target.ID
	result, err := p.Execute(ctx, q)
	if err != nil || !result.Confirmed || result.Observation == nil || !SameIdentity(result.Observation.Identity, target.Identity) || *result.Observation.RuntimeState != domain.VMRuntimeStopped {
		t.Fatalf("clone not independently stopped/identified: %+v %v", result, err)
	}
	if f.resource.ID == sourceID || f.resource.Components[domain.VMComponentDisk] == sourceDisk {
		t.Fatal("clone reused source identity/storage")
	}
	for _, path := range []string{sourceDisk, f.resource.Components[domain.VMComponentDisk]} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "disk bytes" {
			t.Fatalf("clone/source content changed: %q %v", data, err)
		}
	}
	if source, err := p.readRecord(sourceID); err != nil || source == nil {
		t.Fatal("source metadata removed")
	}
}

func TestTPMCloneRejectedBeforeTargetCreation(t *testing.T) {
	p, f, q := checkpointRequest(t)
	q.Deployment.Firmware = domain.VMFirmwareUEFI
	q.Deployment.TPM.Enabled = true
	tid := uuid.New()
	q.Deployment.TPM.IdentityID = &tid
	// The required complete source is tested above; a TPM clone must fail even
	// before any missing component could be treated as an empty TPM identity.
	q.Operation.Kind = domain.VMOperationClone
	target := q.Deployment
	target.ID = uuid.New()
	target.Identity.DeploymentID = target.ID
	target.Identity.ProviderResourceID = uuid.New()
	q.CloneTarget = &target
	q.Operation.CloneTargetID = &target.ID
	before := f.mutations
	if _, err := p.Execute(context.Background(), q); err == nil || before != f.mutations {
		t.Fatal("TPM clone mutated a target")
	}
}
func TestArtifactDeletionReturnsVerifiedManifestAndRecoversAfterRestart(t *testing.T) {
	for _, target := range []domain.VMDeleteTarget{domain.VMDeleteCheckpoint, domain.VMDeleteExport} {
		t.Run(string(target), func(t *testing.T) {
			ctx := context.Background()
			p, f, q := checkpointRequest(t)
			q.Checkpoint.RetainUntil = time.Now().Add(-time.Hour)
			created, err := p.Execute(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			q.Checkpoint = created.Checkpoint
			q.Operation.RequiredTier = domain.VMApprovalDestructive
			approval := uuid.New()
			q.Operation.ApprovalID = &approval
			q.Operation.ProviderFingerprint = f.resource.Fingerprint
			dir := p.artifactDir("checkpoints", q.Checkpoint.ID)
			if target == domain.VMDeleteExport {
				id := uuid.New()
				q.Operation.Kind = domain.VMOperationExport
				q.Operation.ID = uuid.New()
				q.Operation.ExportID = &id
				q.Export = &domain.VMExport{VirtualizationResourceMeta: testMeta(id, q.Deployment.OrgID), LifecycleClass: domain.VMLifecyclePersistent, CheckpointID: q.Checkpoint.ID, State: domain.VMArtifactCreating, StorageRef: uuid.New(), AccessPolicyRef: uuid.New(), Provenance: q.Image.Provenance, RetainUntil: time.Now().Add(-time.Hour)}
				created, err = p.Execute(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				q.Export = created.Export
				dir = p.artifactDir("exports", q.Export.ID)
			}
			q.Operation.ID = uuid.New()
			q.Operation.Kind = domain.VMOperationDelete
			q.Operation.DeleteTarget = target
			for attempt := 0; attempt < 2; attempt++ {
				result, err := p.Execute(ctx, q)
				if err != nil || !result.Confirmed || len(result.RetainedStorageRefs) != 0 {
					t.Fatalf("delete attempt %d: %+v %v", attempt, result, err)
				}
				if _, err = os.Lstat(dir); !os.IsNotExist(err) {
					t.Fatal("artifact absence not verified")
				}
				if target == domain.VMDeleteCheckpoint {
					proof := result.Checkpoint
					if proof == nil || domain.ValidateVMCheckpoint(proof) != nil || proof.State != domain.VMArtifactDeleted || proof.ID != q.Checkpoint.ID || !SameIdentity(proof.Identity, q.Checkpoint.Identity) || proof.ManifestDigest != q.Checkpoint.ManifestDigest || !reflect.DeepEqual(proof.Components, q.Checkpoint.Components) {
						t.Fatalf("checkpoint deletion evidence missing: %+v", proof)
					}
				} else {
					proof := result.Export
					if proof == nil || domain.ValidateVMExport(proof) != nil || proof.State != domain.VMArtifactDeleted || proof.ID != q.Export.ID || proof.CheckpointID != q.Export.CheckpointID || proof.StorageRef != q.Export.StorageRef || proof.ManifestDigest != q.Export.ManifestDigest || !reflect.DeepEqual(proof.Components, q.Export.Components) {
						t.Fatalf("export deletion evidence missing: %+v", proof)
					}
				}
				// Recreate the provider: retry must rely on durable evidence, not RAM.
				p, err = NewPersistentProvider(p.cfg, f)
				if err != nil {
					t.Fatal(err)
				}
			}
			q.Operation.RequestHash = DigestBytes([]byte("another admission"))
			if result, err := p.Execute(ctx, q); err == nil || result.Confirmed {
				t.Fatal("deletion journal accepted a different admission")
			}
		})
	}
}

func TestContainedStorageRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := CheckContainedPath(root, filepath.Join(root, "escape", "new")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
