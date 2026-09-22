package vm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Keep deletion authority outside the resource root so an interrupted cleanup
// can be retried without converting definition absence into storage authority.
type deploymentDeletion struct {
	RootDevice   uint64                   `json:"root_device"`
	RootInode    uint64                   `json:"root_inode"`
	Completed    bool                     `json:"completed"`
	OperationID  uuid.UUID                `json:"operation_id"`
	RequestHash  string                   `json:"request_hash"`
	Disposition  domain.VMDataDisposition `json:"disposition"`
	Record       persistentRecord         `json:"record"`
	ExportDigest string                   `json:"export_digest,omitempty"`
}

type retainedExport struct {
	RequestHash   string                        `json:"request_hash"`
	Deployment    domain.PersistentVMDeployment `json:"deployment"`
	SchemaVersion int                           `json:"schema_version"`
	OperationID   uuid.UUID                     `json:"operation_id"`
	Identity      domain.VMResourceIdentity     `json:"identity"`
	Marker        domain.VMOwnershipMarker      `json:"marker"`
	Components    []domain.VMComponent          `json:"components"`
}

func (p *PersistentProvider) deleteDeployment(ctx context.Context, q domain.VMProviderOperation, r *PersistentResource, rec *persistentRecord, result *domain.VMProviderResult) (*domain.VMProviderResult, error) {
	disposition := q.Operation.DataDisposition.Effective()
	if !disposition.Valid() {
		return result, ProviderError(domain.VMErrorInvalid, nil)
	}
	if disposition == domain.VMDataDelete && (q.Operation.RequiredTier != domain.VMApprovalDestructive || q.Operation.ApprovalID == nil) {
		return result, ProviderError(domain.VMErrorApprovalRequired, nil)
	}
	dir := filepath.Dir(p.recordPath(r.ID))
	if err := CheckContainedPath(p.cfg.StateDir, dir); err != nil {
		return result, err
	}
	journal := filepath.Join(p.cfg.StateDir, "deletions", q.Operation.ID.String()+"-deployment.json")
	if err := CheckContainedPath(p.cfg.StateDir, journal); err != nil {
		return result, err
	}
	var deletion deploymentDeletion
	if data, err := os.ReadFile(journal); err == nil {
		if json.Unmarshal(data, &deletion) != nil || deletion.OperationID != q.Operation.ID || deletion.RequestHash != q.Operation.RequestHash || deletion.Disposition != disposition || !SameIdentity(deletion.Record.Marker.VMResourceIdentity, q.Deployment.Identity) || domain.ValidateVMOwnershipMarker(deletion.Record.Marker) != nil {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		if rec != nil && !reflect.DeepEqual(*rec, deletion.Record) {
			return result, ProviderError(domain.VMErrorConflict, nil)
		}
		if rec == nil && r.State != domain.VMRuntimeAbsent {
			return result, ProviderError(domain.VMErrorForeign, nil)
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if rec == nil {
			if r.State != domain.VMRuntimeAbsent {
				return result, ProviderError(domain.VMErrorForeign, nil)
			}
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				return result, ProviderError(domain.VMErrorForeign, err)
			}
			// There is no resource and no private data to retain, export, or erase.
			result.Observation = p.observation(q.Deployment.Identity, r, nil)
			result.Confirmed = true
			result.RetainedStorageRefs = nil
			return result, nil
		}
		if !SameIdentity(rec.Marker.VMResourceIdentity, q.Deployment.Identity) {
			return result, ProviderError(domain.VMErrorForeign, nil)
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return result, ProviderError(domain.VMErrorIntegrity, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return result, ProviderError(domain.VMErrorUnsupported, nil)
		}
		deletion = deploymentDeletion{RootDevice: uint64(stat.Dev), RootInode: stat.Ino, OperationID: q.Operation.ID, RequestHash: q.Operation.RequestHash, Disposition: disposition, Record: *rec}
		if disposition == domain.VMDataExport {
			if r.State != domain.VMRuntimeStopped {
				return result, ProviderError(domain.VMErrorConflict, nil)
			}
			digest, err := p.exportForDeletion(ctx, q, r, rec)
			if err != nil {
				return result, err
			}
			deletion.ExportDigest = digest
		}
		if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
			return result, err
		}
		if err := writeJSON(ctx, journal, deletion); err != nil {
			return result, err
		}
		if err := syncDirectory(filepath.Dir(journal)); err != nil {
			return result, err
		}
	}
	if info, err := os.Lstat(dir); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || uint64(stat.Dev) != deletion.RootDevice || stat.Ino != deletion.RootInode || (deletion.Completed && disposition == domain.VMDataDelete) {
			return result, ProviderError(domain.VMErrorForeign, nil)
		}
	} else if !errors.Is(err, os.ErrNotExist) || disposition != domain.VMDataDelete {
		return result, ProviderError(domain.VMErrorIntegrity, err)
	}
	for kind, path := range deletion.Record.Components {
		if kind == domain.VMComponentKernel {
			continue
		}
		if filepath.Clean(path) == dir {
			return result, ProviderError(domain.VMErrorIntegrity, nil)
		}
		if err := CheckContainedPath(dir, path); err != nil {
			return result, err
		}
	}
	if disposition == domain.VMDataExport {
		if _, err := p.verifyDeletionExport(ctx, q, deletion.ExportDigest); err != nil {
			return result, err
		}
	}
	if r.State != domain.VMRuntimeAbsent {
		if err := p.driver.TransitionPersistent(ctx, r, domain.VMOperationDelete, q.Operation.AllowForceStop); err != nil {
			return result, err
		}
	}
	after, err := p.driver.InspectPersistent(ctx, r.ID)
	if err != nil || after.State != domain.VMRuntimeAbsent {
		return result, ProviderError(domain.VMErrorUnconfirmed, err)
	}
	result.RetainedStorageRefs = []uuid.UUID{r.ID}
	switch disposition {
	case domain.VMDataDelete:
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
		result.RetainedStorageRefs = nil
	case domain.VMDataExport:
		result.RetainedStorageRefs = append(result.RetainedStorageRefs, q.Operation.ID)
		result.Diagnostic.EvidenceDigest = deletion.ExportDigest
	}
	deletion.Completed = true
	if err := writeJSON(ctx, journal, deletion); err != nil {
		return result, err
	}
	result.Observation = p.observation(q.Deployment.Identity, after, nil)
	result.Confirmed = true
	return result, nil
}

// A deletion export is a private retained recovery set, not a public VMExport
// with invented access-policy credentials. The immutable manifest and opaque
// retained references survive undefine; original disks/restore sets remain too.
func (p *PersistentProvider) exportForDeletion(ctx context.Context, q domain.VMProviderOperation, r *PersistentResource, rec *persistentRecord) (digest string, retErr error) {
	dir := p.artifactDir("retained-exports", q.Operation.ID)
	if _, err := os.Lstat(dir); err == nil {
		return p.verifyDeletionExport(ctx, q, "")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	driver, ok := p.driver.(ColdCopyDriver)
	if !ok {
		return "", ProviderError(domain.VMErrorUnsupported, nil)
	}
	guard, err := driver.BeginColdCopy(ctx, r)
	if err != nil {
		return "", err
	}
	defer func() { retErr = JoinCleanupError(retErr, guard.Close()) }()
	ctx = guard.Context()
	if err = guard.Check(ctx); err != nil {
		return "", err
	}
	stage, err := p.stage(dir)
	if err != nil {
		return "", err
	}
	defer func() { retErr = JoinCleanupError(retErr, os.RemoveAll(stage)) }()
	manifest := retainedExport{RequestHash: q.Operation.RequestHash, Deployment: rec.Deployment, SchemaVersion: 1, OperationID: q.Operation.ID, Identity: q.Deployment.Identity, Marker: *r.Marker}
	kinds := RequiredComponents(manifest.Deployment.Provider, manifest.Deployment.Firmware, manifest.Deployment.TPM.Enabled)
	if len(kinds) != len(r.Components) {
		return "", ProviderError(domain.VMErrorIntegrity, nil)
	}
	for _, kind := range kinds {
		if r.Components[kind] == "" {
			return "", ProviderError(domain.VMErrorIntegrity, nil)
		}
		ref := uuid.NewSHA1(q.Operation.ID, []byte(kind))
		path := filepath.Join(stage, ref.String())
		if err = p.driver.CopyPersistentComponent(ctx, kind, r.Components[kind], path); err != nil {
			return "", err
		}
		digest, size, err := HashComponent(ctx, path)
		if err != nil {
			return "", err
		}
		manifest.Components = append(manifest.Components, domain.VMComponent{Kind: kind, StorageRef: ref, Digest: digest, SizeBytes: size})
	}
	if err = guard.Check(ctx); err != nil {
		return "", err
	}
	if err = writeJSON(ctx, filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return "", err
	}
	if err = guard.Check(ctx); err != nil {
		return "", err
	}
	if err = commitDirectory(stage, dir); err != nil {
		return "", err
	}
	return p.verifyDeletionExport(ctx, q, "")
}
func (p *PersistentProvider) verifyDeletionExport(ctx context.Context, q domain.VMProviderOperation, expected string) (string, error) {
	dir := p.artifactDir("retained-exports", q.Operation.ID)
	path := filepath.Join(dir, "manifest.json")
	if err := CheckContainedPath(p.cfg.StateDir, path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := DigestBytes(data)
	var manifest retainedExport
	if json.Unmarshal(data, &manifest) != nil || manifest.SchemaVersion != 1 || manifest.OperationID != q.Operation.ID || manifest.RequestHash != q.Operation.RequestHash || !SameIdentity(manifest.Identity, q.Deployment.Identity) || !SameIdentity(manifest.Marker.VMResourceIdentity, manifest.Identity) || domain.ValidateVMOwnershipMarker(manifest.Marker) != nil || (expected != "" && digest != expected) {
		return "", ProviderError(domain.VMErrorIntegrity, nil)
	}
	if domain.ValidatePersistentVMDeployment(&manifest.Deployment) != nil || !SameIdentity(manifest.Deployment.Identity, manifest.Identity) || manifest.Deployment.ConfigDigest != manifest.Marker.ConfigDigest {
		return "", ProviderError(domain.VMErrorIntegrity, nil)
	}
	kinds := RequiredComponents(manifest.Deployment.Provider, manifest.Deployment.Firmware, manifest.Deployment.TPM.Enabled)
	if len(manifest.Components) != len(kinds) {
		return "", ProviderError(domain.VMErrorIntegrity, nil)
	}
	for i, c := range manifest.Components {
		if c.Kind != kinds[i] || c.StorageRef != uuid.NewSHA1(q.Operation.ID, []byte(c.Kind)) {
			return "", ProviderError(domain.VMErrorIntegrity, nil)
		}
		path := filepath.Join(dir, c.StorageRef.String())
		if err = CheckContainedPath(dir, path); err != nil {
			return "", err
		}
		actual, size, err := HashComponent(ctx, path)
		if err != nil || actual != c.Digest || size != c.SizeBytes {
			return "", ProviderError(domain.VMErrorIntegrity, err)
		}
	}
	return digest, nil
}
