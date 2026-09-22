package vm

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/domain"
)

// CheckContainedPath rejects symlink traversal, including existing ancestors of
// a not-yet-created target. The trusted root itself may use an operator-selected
// mount alias (e.g. macOS /var); untrusted descendants may not use symlinks.
func CheckContainedPath(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsAbs(root) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ProviderError(domain.VMErrorIntegrity, err)
	}
	for current := path; current != root; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return ProviderError(domain.VMErrorIntegrity, nil)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func CopyRegularFile(ctx context.Context, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ProviderError(domain.VMErrorIntegrity, err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, contextReader{ctx, in})
	syncErr := out.Sync()
	closeErr := out.Close()
	final, statErr := in.Stat()
	if statErr != nil || final.Size() != info.Size() || !final.ModTime().Equal(info.ModTime()) {
		return ProviderError(domain.VMErrorIntegrity, statErr)
	}
	return errors.Join(copyErr, syncErr, closeErr)
}

func HashComponent(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, ProviderError(domain.VMErrorIntegrity, err)
	}
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// PackTPM records regular files only. Sockets, symlinks and device nodes indicate
// an unsafe/incomplete cold state and fail the entire coordinated checkpoint.
func PackTPM(ctx context.Context, src, dst string) error {
	if err := CheckContainedPath(src, src); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	count := 0
	walkErr := filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if e.IsDir() || e.Name() == ".lock" {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ProviderError(domain.VMErrorIntegrity, nil)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0600, Size: info.Size()}); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, contextReader{ctx, in})
		closeErr := in.Close()
		count++
		return errors.Join(err, closeErr)
	})
	err = errors.Join(walkErr, tw.Close(), out.Sync(), out.Close())
	if err == nil && count == 0 {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	return err
}
func UnpackTPM(ctx context.Context, src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = os.Mkdir(dst, 0700); err != nil {
		return err
	}
	tr := tar.NewReader(contextReader{ctx, f})
	count := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !filepath.IsLocal(h.Name) || h.Size < 0 || filepath.Base(h.Name) == ".lock" {
			return ProviderError(domain.VMErrorIntegrity, nil)
		}
		path := filepath.Join(dst, h.Name)
		if err = CheckContainedPath(dst, path); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		err = errors.Join(err, out.Sync(), out.Close())
		if err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	return nil
}

func RequiredComponents(provider domain.VMProvider, firmware domain.VMFirmware, tpm bool) []domain.VMComponentKind {
	kinds := []domain.VMComponentKind{domain.VMComponentDisk}
	if provider == domain.VMProviderFirecracker {
		return []domain.VMComponentKind{domain.VMComponentKernel, domain.VMComponentRootFS}
	}
	if firmware == domain.VMFirmwareUEFI {
		kinds = append(kinds, domain.VMComponentNVRAM)
	}
	if tpm {
		kinds = append(kinds, domain.VMComponentSWTPM)
	}
	return kinds
}
func (p *PersistentProvider) artifactDir(group string, id uuid.UUID) string {
	return filepath.Join(p.cfg.StateDir, group, id.String())
}
func checkpointDigest(c domain.VMCheckpoint) string {
	c.ManifestDigest = ""
	b, _ := json.Marshal(c)
	return DigestBytes(b)
}
func writeJSON(ctx context.Context, path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(ctx, path, ".write-*.tmp", data, 0600)
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (p *PersistentProvider) loadCheckpoint(ctx context.Context, q domain.VMProviderOperation) (*domain.VMCheckpoint, map[domain.VMComponentKind]string, error) {
	if q.Checkpoint == nil || q.Operation.CheckpointID == nil || q.Checkpoint.ID != *q.Operation.CheckpointID {
		return nil, nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	dir := p.artifactDir("checkpoints", q.Checkpoint.ID)
	if err := CheckContainedPath(p.cfg.StateDir, filepath.Join(dir, "manifest.json")); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	var c domain.VMCheckpoint
	if err = json.Unmarshal(data, &c); err != nil {
		return nil, nil, err
	}
	if c.ID != q.Checkpoint.ID || c.OrgID != q.Deployment.OrgID || !SameIdentity(c.Identity, q.Deployment.Identity) || c.State != domain.VMArtifactReady || c.ManifestDigest != checkpointDigest(c) || (q.Checkpoint.ManifestDigest != "" && c.ManifestDigest != q.Checkpoint.ManifestDigest) {
		return nil, nil, ProviderError(domain.VMErrorIntegrity, nil)
	}
	paths := map[domain.VMComponentKind]string{}
	for _, component := range c.Components {
		if component.StorageRef == uuid.Nil || paths[component.Kind] != "" {
			return nil, nil, ProviderError(domain.VMErrorIntegrity, nil)
		}
		path := filepath.Join(dir, component.StorageRef.String())
		if err = CheckContainedPath(dir, path); err != nil {
			return nil, nil, err
		}
		digest, size, err := HashComponent(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		if digest != component.Digest || size != component.SizeBytes {
			return nil, nil, ProviderError(domain.VMErrorIntegrity, nil)
		}
		paths[component.Kind] = path
	}
	required := RequiredComponents(c.Identity.Provider, c.Firmware, c.TPMEnabled)
	if len(paths) != len(required) {
		return nil, nil, ProviderError(domain.VMErrorIntegrity, nil)
	}
	for _, kind := range required {
		if paths[kind] == "" {
			return nil, nil, ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	return &c, paths, nil
}

func (p *PersistentProvider) transfer(ctx context.Context, q domain.VMProviderOperation, r *PersistentResource, rec *persistentRecord, result *domain.VMProviderResult) (*domain.VMProviderResult, error) {
	if r.State != domain.VMRuntimeStopped || rec == nil {
		return result, ProviderError(domain.VMErrorConflict, nil)
	}
	if q.Operation.Kind == domain.VMOperationCheckpoint {
		return p.checkpoint(ctx, q, r, result)
	}
	cp, paths, err := p.loadCheckpoint(ctx, q)
	if err != nil {
		return result, err
	}
	switch q.Operation.Kind {
	case domain.VMOperationExport:
		if q.Export == nil || q.Operation.ExportID == nil || q.Export.ID != *q.Operation.ExportID || q.Export.OrgID != q.Deployment.OrgID || q.Export.CheckpointID != cp.ID || q.Export.StorageRef == uuid.Nil {
			return result, ProviderError(domain.VMErrorInvalid, nil)
		}
		dir := p.artifactDir("exports", q.Export.ID)
		stage, err := p.stage(dir)
		if err != nil {
			return result, err
		}
		defer os.RemoveAll(stage)
		for _, c := range cp.Components {
			if err = CopyRegularFile(ctx, paths[c.Kind], filepath.Join(stage, c.StorageRef.String())); err != nil {
				return result, err
			}
		}
		e := *q.Export
		e.Components = append([]domain.VMComponent(nil), cp.Components...)
		e.State = domain.VMArtifactReady
		e.ManifestDigest = ""
		manifestBytes, err := json.Marshal(e)
		if err != nil {
			return result, err
		}
		e.ManifestDigest = DigestBytes(manifestBytes)
		if err = writeJSON(ctx, filepath.Join(stage, "checkpoint.json"), cp); err != nil {
			return result, err
		}
		if err = writeJSON(ctx, filepath.Join(stage, "manifest.json"), e); err != nil {
			return result, err
		}
		for _, c := range e.Components {
			digest, size, err := HashComponent(ctx, filepath.Join(stage, c.StorageRef.String()))
			if err != nil {
				return result, err
			}
			if digest != c.Digest || size != c.SizeBytes {
				return result, ProviderError(domain.VMErrorIntegrity, nil)
			}
		}
		if err = commitDirectory(stage, dir); err != nil {
			return result, err
		}
		result.Export = &e
	case domain.VMOperationClone, domain.VMOperationRestore:
		target := q.Deployment
		if q.Operation.Kind == domain.VMOperationClone {
			if cp.TPMEnabled {
				return result, ProviderError(domain.VMErrorUnsupported, nil)
			}
			if q.CloneTarget == nil || q.Operation.CloneTargetID == nil || q.CloneTarget.ID != *q.Operation.CloneTargetID {
				return result, ProviderError(domain.VMErrorInvalid, nil)
			}
			target = *q.CloneTarget
			if domain.ValidatePersistentVMDeployment(&target) != nil {
				return result, ProviderError(domain.VMErrorInvalid, nil)
			}
			if p.checkIdentity(target.Identity) != nil || target.ID == q.Deployment.ID || target.Identity.ProviderResourceID == r.ID || target.DesiredPower != domain.VMDesiredStopped || target.TPM.Enabled || target.StoragePoolRef != p.cfg.StoragePoolRef || len(target.Bootstrap) > 0 {
				return result, ProviderError(domain.VMErrorInvalid, nil)
			}
		}
		if target.ImageID != cp.ImageID || target.Firmware != cp.Firmware || target.TPM.Enabled != cp.TPMEnabled || (q.Operation.Kind == domain.VMOperationRestore && target.ConfigDigest != cp.ConfigDigest) {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		dest, err := p.driver.InspectPersistent(ctx, target.Identity.ProviderResourceID)
		if err != nil {
			return result, err
		}
		if q.Operation.Kind == domain.VMOperationClone && dest.State != domain.VMRuntimeAbsent {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		if q.Operation.Kind == domain.VMOperationRestore {
			if err = CheckPersistentResource(r, dest); err != nil {
				return result, err
			}
			// Preserve admission's ownership/config baseline through the driver
			// recheck; a fresh foreign/stopped resource is not a new authority.
			dest = r
		}
		dir := filepath.Dir(p.recordPath(dest.ID))
		if err = CheckContainedPath(p.cfg.StateDir, dir); err != nil {
			return result, err
		}
		if dest.State == domain.VMRuntimeAbsent {
			if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
				return result, err
			}
			if err = os.Mkdir(dir, 0700); err != nil {
				return result, err
			}
		}
		setDir := filepath.Join(dir, "set-"+q.Operation.ID.String())
		if err = os.Mkdir(setDir, 0700); err != nil {
			return result, ProviderError(domain.VMErrorConflict, err)
		}
		restored := map[domain.VMComponentKind]string{}
		for _, c := range cp.Components {
			restored[c.Kind] = filepath.Join(setDir, string(c.Kind))
		}
		// The journal and old component set remain until verified recovery/cleanup.
		if err = writeJSON(ctx, filepath.Join(dir, "operation-"+q.Operation.ID.String()+".json"), struct {
			Operation      uuid.UUID
			Previous, Next map[domain.VMComponentKind]string
		}{q.Operation.ID, dest.Components, restored}); err != nil {
			return result, err
		}
		for _, c := range cp.Components {
			out := filepath.Join(setDir, string(c.Kind))
			blob := out + ".staged"
			if err = CopyRegularFile(ctx, paths[c.Kind], blob); err != nil {
				return result, err
			}
			digest, size, err := HashComponent(ctx, blob)
			if err != nil {
				return result, err
			}
			if digest != c.Digest || size != c.SizeBytes {
				return result, ProviderError(domain.VMErrorIntegrity, nil)
			}
			if c.Kind == domain.VMComponentSWTPM {
				if err = UnpackTPM(ctx, blob, out); err == nil {
					err = os.Remove(blob)
				}
			} else {
				err = os.Rename(blob, out)
			}
			if err != nil {
				return result, err
			}
			restored[c.Kind] = out
		}
		marker := domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: target.Identity, AppliedGeneration: target.Generation, OperationID: q.Operation.ID, ImageDigest: cp.ImageDigest, ConfigDigest: target.ConfigDigest}
		spec := PersistentSpec{Deployment: target, Marker: marker, Components: restored, Instance: InstanceSpec{Name: dest.ID.String(), InstanceDir: dir, VCPUs: int(target.Allocation.VCPU), MemoryMB: int(target.Allocation.MemoryBytes >> 20), Image: ImageSpec{Arch: q.Image.Architecture}}}
		if err = p.driver.DefinePersistent(ctx, spec, dest); err != nil {
			return result, err
		}
		after, err := p.driver.InspectPersistent(ctx, dest.ID)
		if err != nil {
			return result, err
		}
		if after.State != domain.VMRuntimeStopped || after.Marker == nil || !reflectMarker(*after.Marker, marker) {
			return result, ProviderError(domain.VMErrorUnconfirmed, nil)
		}
		if err = p.writeRecord(ctx, target, after); err != nil {
			return result, err
		}
		stored, err := p.readRecord(dest.ID)
		if err != nil {
			return result, err
		}
		result.Observation = p.observation(target.Identity, after, stored)
	}
	result.Confirmed = true
	// Restore deliberately retains the original coordinated set for recovery.
	if q.Operation.Kind != domain.VMOperationRestore {
		result.RetainedStorageRefs = nil
	}
	return result, nil
}
func reflectMarker(a, b domain.VMOwnershipMarker) bool {
	return SameIdentity(a.VMResourceIdentity, b.VMResourceIdentity) && a.SchemaVersion == b.SchemaVersion && a.AppliedGeneration == b.AppliedGeneration && a.OperationID == b.OperationID && a.ImageDigest == b.ImageDigest && a.ConfigDigest == b.ConfigDigest
}

func (p *PersistentProvider) stage(dest string) (string, error) {
	if err := CheckContainedPath(p.cfg.StateDir, dest); err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return "", ProviderError(domain.VMErrorConflict, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(filepath.Dir(dest), ".stage-*")
}
func commitDirectory(stage, dest string) error {
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return ProviderError(domain.VMErrorConflict, err)
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dest))
}

func (p *PersistentProvider) checkpoint(ctx context.Context, q domain.VMProviderOperation, r *PersistentResource, result *domain.VMProviderResult) (*domain.VMProviderResult, error) {
	if q.Checkpoint == nil || q.Operation.CheckpointID == nil || q.Checkpoint.ID != *q.Operation.CheckpointID || q.Checkpoint.OrgID != q.Deployment.OrgID || !SameIdentity(q.Checkpoint.Identity, q.Deployment.Identity) {
		return result, ProviderError(domain.VMErrorInvalid, nil)
	}
	kinds := RequiredComponents(q.Deployment.Provider, q.Deployment.Firmware, q.Deployment.TPM.Enabled)
	if len(r.Components) != len(kinds) || len(q.Operation.PreparedStorageRefs) != len(kinds) {
		return result, ProviderError(domain.VMErrorIntegrity, nil)
	}
	dir := p.artifactDir("checkpoints", q.Checkpoint.ID)
	if existing, _, err := p.loadCheckpoint(ctx, q); err == nil {
		result.Checkpoint = existing
		result.Confirmed = true
		result.RetainedStorageRefs = nil
		return result, nil
	}
	fenced, ok := p.driver.(ColdCopyDriver)
	if !ok {
		return result, ProviderError(domain.VMErrorUnsupported, nil)
	}
	guard, err := fenced.BeginColdCopy(ctx, r)
	if err != nil {
		return result, err
	}
	defer guard.Close()
	ctx = guard.Context()
	if err = guard.Check(ctx); err != nil {
		return result, err
	}
	stage, err := p.stage(dir)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	c := *q.Checkpoint
	c.Components = nil
	c.State = domain.VMArtifactCreating
	c.Consistency = domain.VMCheckpointCold
	c.DeploymentID = q.Deployment.ID
	c.DeploymentGeneration = q.Deployment.Generation
	c.ImageID = q.Deployment.ImageID
	c.ImageDigest = r.Marker.ImageDigest
	c.ConfigDigest = r.Marker.ConfigDigest
	c.Firmware = q.Deployment.Firmware
	c.TPMEnabled = q.Deployment.TPM.Enabled
	seen := map[uuid.UUID]bool{}
	for i, kind := range kinds {
		ref := q.Operation.PreparedStorageRefs[i]
		if ref == uuid.Nil || seen[ref] || r.Components[kind] == "" {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		seen[ref] = true
		out := filepath.Join(stage, ref.String())
		if err = p.driver.CopyPersistentComponent(ctx, kind, r.Components[kind], out); err != nil {
			return result, err
		}
		digest, size, err := HashComponent(ctx, out)
		if err != nil {
			return result, err
		}
		c.Components = append(c.Components, domain.VMComponent{Kind: kind, StorageRef: ref, Digest: digest, SizeBytes: size})
	}
	if err = guard.Check(ctx); err != nil {
		return result, err
	}
	// Recheck stopped identity/configuration after all copies before publication.
	after, err := p.driver.InspectPersistent(ctx, r.ID)
	if err != nil {
		return result, err
	}
	if err = CheckPersistentResource(r, after); err != nil {
		return result, err
	}
	c.State = domain.VMArtifactReady
	c.ManifestDigest = checkpointDigest(c)
	if err = domain.ValidateVMCheckpoint(&c); err != nil {
		return result, ProviderError(domain.VMErrorInvalid, err)
	}
	if err = writeJSON(ctx, filepath.Join(stage, "manifest.json"), c); err != nil {
		return result, err
	}
	if err = guard.Check(ctx); err != nil {
		return result, err
	}
	if err = commitDirectory(stage, dir); err != nil {
		return result, err
	}
	result.Checkpoint = &c
	result.Confirmed = true
	result.RetainedStorageRefs = nil
	return result, nil
}

type artifactDeletion struct {
	OperationID uuid.UUID                 `json:"operation_id"`
	RequestHash string                    `json:"request_hash"`
	Identity    domain.VMResourceIdentity `json:"identity"`
	Checkpoint  *domain.VMCheckpoint      `json:"checkpoint,omitempty"`
	Export      *domain.VMExport          `json:"export,omitempty"`
}

func (p *PersistentProvider) deleteArtifact(ctx context.Context, q domain.VMProviderOperation, result *domain.VMProviderResult) (*domain.VMProviderResult, error) {
	journal := filepath.Join(p.cfg.StateDir, "deletions", q.Operation.ID.String()+".json")
	if err := CheckContainedPath(p.cfg.StateDir, journal); err != nil {
		return result, err
	}
	if data, err := os.ReadFile(journal); err == nil {
		var deletion artifactDeletion
		if err = json.Unmarshal(data, &deletion); err != nil {
			return result, ProviderError(domain.VMErrorIntegrity, err)
		}
		return p.finishArtifactDeletion(ctx, q, deletion, result)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	var dir string
	var checkpoint *domain.VMCheckpoint
	var export *domain.VMExport
	switch q.Operation.DeleteTarget {
	case domain.VMDeleteCheckpoint:
		c, _, err := p.loadCheckpoint(ctx, q)
		if err != nil {
			return result, err
		}
		if c.RetainUntil.After(time.Now()) {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		dir = p.artifactDir("checkpoints", c.ID)
		checkpoint = c
	case domain.VMDeleteExport:
		if q.Export == nil || q.Operation.ExportID == nil || q.Export.ID != *q.Operation.ExportID || q.Export.OrgID != q.Deployment.OrgID {
			return result, ProviderError(domain.VMErrorInvalid, nil)
		}
		dir = p.artifactDir("exports", q.Export.ID)
		for _, file := range []string{"manifest.json", "checkpoint.json"} {
			if err := CheckContainedPath(p.cfg.StateDir, filepath.Join(dir, file)); err != nil {
				return result, err
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return result, err
		}
		var e domain.VMExport
		if err = json.Unmarshal(data, &e); err != nil {
			return result, err
		}
		checkpointData, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
		if err != nil {
			return result, err
		}
		var source domain.VMCheckpoint
		if err = json.Unmarshal(checkpointData, &source); err != nil {
			return result, err
		}
		if !SameIdentity(source.Identity, q.Deployment.Identity) || source.ID != e.CheckpointID || source.ManifestDigest != checkpointDigest(source) {
			return result, ProviderError(domain.VMErrorForeign, nil)
		}
		if e.ID != q.Export.ID || e.OrgID != q.Deployment.OrgID || e.CheckpointID != q.Export.CheckpointID || e.StorageRef != q.Export.StorageRef || e.RetainUntil.After(time.Now()) {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		digest := e.ManifestDigest
		e.ManifestDigest = ""
		unsigned, err := json.Marshal(e)
		e.ManifestDigest = digest
		if err != nil || digest != DigestBytes(unsigned) || digest != q.Export.ManifestDigest || !reflect.DeepEqual(e.Components, q.Export.Components) {
			return result, ProviderError(domain.VMErrorIntegrity, err)
		}
		export = &e
	default:
		return result, ProviderError(domain.VMErrorInvalid, nil)
	}
	deletion := artifactDeletion{OperationID: q.Operation.ID, RequestHash: q.Operation.RequestHash, Identity: q.Deployment.Identity, Checkpoint: checkpoint, Export: export}
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		return result, err
	}
	if err := writeJSON(ctx, journal, deletion); err != nil {
		return result, err
	}
	if err := syncDirectory(filepath.Dir(journal)); err != nil {
		return result, err
	}
	return p.finishArtifactDeletion(ctx, q, deletion, result)
}

// Keep the independently verified pre-delete manifest outside the removed set.
// After a crash, retry confirms exact-path absence before releasing capacity.
func (p *PersistentProvider) finishArtifactDeletion(ctx context.Context, q domain.VMProviderOperation, deletion artifactDeletion, result *domain.VMProviderResult) (*domain.VMProviderResult, error) {
	if deletion.OperationID != q.Operation.ID || deletion.RequestHash != q.Operation.RequestHash || !SameIdentity(deletion.Identity, q.Deployment.Identity) {
		return result, ProviderError(domain.VMErrorIntegrity, nil)
	}
	checkpoint, export := deletion.Checkpoint, deletion.Export
	var dir string
	switch q.Operation.DeleteTarget {
	case domain.VMDeleteCheckpoint:
		if checkpoint == nil || export != nil || q.Checkpoint == nil || q.Operation.CheckpointID == nil || *q.Operation.CheckpointID != checkpoint.ID || checkpoint.ID != q.Checkpoint.ID || checkpoint.OrgID != q.Deployment.OrgID || !SameIdentity(checkpoint.Identity, q.Deployment.Identity) || checkpoint.ManifestDigest != q.Checkpoint.ManifestDigest || !reflect.DeepEqual(checkpoint.Components, q.Checkpoint.Components) || checkpoint.ManifestDigest != checkpointDigest(*checkpoint) || checkpoint.RetainUntil.After(time.Now()) {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		dir = p.artifactDir("checkpoints", checkpoint.ID)
	case domain.VMDeleteExport:
		if export == nil || checkpoint != nil || q.Export == nil || q.Operation.ExportID == nil || *q.Operation.ExportID != export.ID || export.ID != q.Export.ID || export.OrgID != q.Deployment.OrgID || export.CheckpointID != q.Export.CheckpointID || export.StorageRef != q.Export.StorageRef || export.ManifestDigest != q.Export.ManifestDigest || !reflect.DeepEqual(export.Components, q.Export.Components) || export.RetainUntil.After(time.Now()) {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		dir = p.artifactDir("exports", export.ID)
	default:
		return result, ProviderError(domain.VMErrorInvalid, nil)
	}
	if err := CheckContainedPath(p.cfg.StateDir, dir); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return result, err
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return result, ProviderError(domain.VMErrorUnconfirmed, err)
	}
	if err := syncDirectory(filepath.Dir(dir)); err != nil {
		return result, err
	}
	// Return the independently loaded artifact, preserving its original digest
	// and component inventory as evidence for C's capacity-release verification.
	if checkpoint != nil {
		checkpoint.State = domain.VMArtifactDeleted
		result.Checkpoint = checkpoint
	}
	if export != nil {
		export.State = domain.VMArtifactDeleted
		result.Export = export
	}
	result.Confirmed = true
	result.RetainedStorageRefs = nil
	return result, nil
}
