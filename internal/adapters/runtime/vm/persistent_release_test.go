package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestImmutableReleaseIgnoresCurrentAndRejectsComponentEscape(t *testing.T) {
	_, q := persistentRequest(t)
	root := t.TempDir()
	dir := filepath.Join(root, q.Image.ReleaseRef.String())
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	disk := []byte("registered pinned bytes")
	diskDigest := DigestBytes(disk)
	manifest, _ := json.Marshal(Manifest{ImageID: "pinned", Arch: "amd64", Format: FormatQCOW2, SHA256: map[string]string{"disk": strings.TrimPrefix(diskDigest, "sha256:")}})
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, diskFileName), disk, 0600); err != nil {
		t.Fatal(err)
	}
	q.Image.ManifestDigest = DigestBytes(manifest)
	q.Image.Components = []domain.VMComponent{{Kind: domain.VMComponentDisk, StorageRef: uuid.New(), Digest: diskDigest, SizeBytes: int64(len(disk))}}
	other := filepath.Join(root, "different-release")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	r, err := ResolvePinnedRelease(context.Background(), root, q.Image)
	if err != nil || r.Dir != dir {
		t.Fatalf("immutable pin incorrectly follows current: %+v %v", r, err)
	}
	if err = os.Remove(filepath.Join(dir, diskFileName)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "disk")
	if err = os.WriteFile(outside, disk, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, diskFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err = ResolvePinnedRelease(context.Background(), root, q.Image); err == nil {
		t.Fatal("component symlink escape accepted even with matching hash")
	}
}
func TestBootstrapRefusedBeforeDefinition(t *testing.T) {
	cfg, q := persistentRequest(t)
	q.Deployment.Bootstrap = []domain.VMBootstrapBinding{{TargetKey: "password", Ref: domain.SecretRef{ID: uuid.New()}}}
	driver := &memoryPersistentDriver{}
	p, _ := NewPersistentProvider(cfg, driver)
	if _, err := p.Execute(context.Background(), q); err == nil || driver.mutations != 0 {
		t.Fatal("unsupported bootstrap reached hypervisor definition")
	}
}
func TestApprovedAdoptionAndRebootRetryAreIdempotent(t *testing.T) {
	cfg, q := persistentRequest(t)
	driver := &memoryPersistentDriver{}
	p, _ := NewPersistentProvider(cfg, driver)
	if _, err := p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Operation.ID = uuid.New()
	q.Operation.ExpectedGeneration = 1
	q.Operation.Kind = domain.VMOperationAdopt
	q.Operation.RequiredTier = domain.VMApprovalDestructive
	approval := uuid.New()
	q.Operation.ApprovalID = &approval
	q.Operation.ProviderFingerprint = driver.resource.Fingerprint
	result, err := p.Execute(context.Background(), q)
	if err != nil || !result.Confirmed || result.Observation.Ownership != domain.VMOwned {
		t.Fatalf("verified adoption: %+v %v", result, err)
	}
	before := driver.mutations
	if _, err = p.Execute(context.Background(), q); err != nil || driver.mutations != before {
		t.Fatalf("adoption retry mutated: %v", err)
	}
	driver.resource.State = domain.VMRuntimeRunning
	q.Operation.Kind = domain.VMOperationReboot
	q.Operation.ID = uuid.New()
	q.Operation.ExpectedGeneration = 1
	if _, err = p.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	// A durable operation ID already recorded as applied must not issue a second
	// reboot when Execute is retried after acknowledgment delivery was lost.
	before = driver.mutations
	if _, err = p.Execute(context.Background(), q); err != nil || driver.mutations != before {
		t.Fatalf("confirmed retry repeated reboot: %v", err)
	}
}
