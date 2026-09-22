package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AdoptionDriver verifies provider semantics and returns the immutable source
// paths of writable images. A filename, legacy record, or marker is not proof.
type AdoptionDriver interface {
	MeasurePersistent(context.Context, *PersistentResource, domain.PersistentVMDeployment, *Release) (*AdoptionProof, error)
}

type AdoptionProof struct {
	Sources map[domain.VMComponentKind]string
	Files   []AdoptionFile
}

type AdoptionFile struct {
	Path   string
	Key    string
	Digest string
	Size   int64
}

// MeasureAdoption never installs a marker or writes an applied baseline.
func (p *PersistentProvider) MeasureAdoption(ctx context.Context, q domain.VMChangeRequest) (measurement *domain.VMAdoptionMeasurement, err error) {
	defer func() {
		if err != nil {
			var pe *domain.VMProviderError
			if !errors.As(err, &pe) {
				err = ProviderError(domain.VMErrorIntegrity, err)
			}
		}
	}()
	ctx, cancel := p.bound(ctx, "")
	defer cancel()
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.gate }()
	r, err := p.driver.InspectPersistent(ctx, q.Desired.Identity.ProviderResourceID)
	if err != nil {
		return nil, err
	}
	m, guard, err := p.measureAdoption(ctx, q.Desired, q.Image, r)
	if guard != nil {
		defer func() { err = JoinCleanupError(err, guard.Close()) }()
	}
	return m, err
}

func (p *PersistentProvider) measureAdoption(ctx context.Context, d domain.PersistentVMDeployment, image domain.VMImage, r *PersistentResource) (*domain.VMAdoptionMeasurement, ColdCopyGuard, error) {
	if err := p.checkIdentity(d.Identity); err != nil {
		return nil, nil, err
	}
	if r == nil || r.ID != d.Identity.ProviderResourceID || r.State != domain.VMRuntimeStopped {
		return nil, nil, ProviderError(domain.VMErrorConflict, nil)
	}
	if record, err := p.readRecord(r.ID); err != nil {
		return nil, nil, err
	} else if record != nil && !SameIdentity(record.Marker.VMResourceIdentity, d.Identity) {
		return nil, nil, ProviderError(domain.VMErrorForeign, nil)
	}
	clean := d
	clean.Observation, clean.ObservationCursor = nil, nil
	clean.ConfigDigest = domain.VMAdoptionConfigDigest(d, r.Fingerprint)
	if domain.ValidateVMDeploymentReferences(&clean, &p.cfg.Host, &image) != nil || d.StoragePoolRef != p.cfg.StoragePoolRef || len(d.Bootstrap) != 0 || d.BootstrapApplied {
		return nil, nil, ProviderError(domain.VMErrorInvalid, nil)
	}
	// Explicitly foreign ownership is never adoptable, even with approval.
	if r.Marker != nil && (domain.ValidateVMOwnershipMarker(*r.Marker) != nil || !SameIdentity(r.Marker.VMResourceIdentity, d.Identity)) {
		return nil, nil, ProviderError(domain.VMErrorForeign, nil)
	}
	if err := CheckWritableComponents(filepath.Dir(p.recordPath(r.ID)), r.Components); err != nil {
		return nil, nil, err
	}
	driver, ok := p.driver.(AdoptionDriver)
	cold, canWatch := p.driver.(ColdCopyDriver)
	if !ok || !canWatch || p.cfg.VerifyImage == nil || p.cfg.ResolveRelease == nil {
		return nil, nil, ProviderError(domain.VMErrorUnsupported, nil)
	}
	guard, err := cold.BeginColdCopy(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	ctx = guard.Context()
	if err = p.cfg.VerifyImage(ctx, p.cfg.Host, image); err != nil {
		return nil, guard, ProviderError(domain.VMErrorIntegrity, err)
	}
	release, err := p.cfg.ResolveRelease(ctx, image)
	if err != nil || release == nil || release.ManifestDigest != image.ManifestDigest || release.Manifest.Format != string(image.Format) {
		return nil, guard, ProviderError(domain.VMErrorIntegrity, err)
	}
	r.AdoptionFiles = nil
	proof, err := driver.MeasurePersistent(ctx, r, d, release)
	if err != nil {
		return nil, guard, err
	}
	kinds := RequiredComponents(d.Provider, d.Firmware, d.TPM.Enabled)
	if len(kinds) != len(r.Components) || proof == nil || len(proof.Sources) != len(kinds) {
		return nil, guard, ProviderError(domain.VMErrorIntegrity, nil)
	}
	m := &domain.VMAdoptionMeasurement{SchemaVersion: 1, Identity: d.Identity, Generation: d.Generation, ImageID: image.ID, ImageDigest: image.ManifestDigest, ConfigDigest: domain.VMAdoptionConfigDigest(d, r.Fingerprint), ProviderFingerprint: r.Fingerprint, StoragePoolRef: d.StoragePoolRef}
	for _, file := range proof.Files {
		data, err := json.Marshal(file)
		if err != nil {
			return nil, guard, err
		}
		m.ProviderEvidence = append(m.ProviderEvidence, DigestBytes(data))
		r.AdoptionFiles = append(r.AdoptionFiles, file)
	}
	for _, kind := range kinds {
		file, err := MeasureAdoptionFile(ctx, r.Components[kind])
		if err != nil {
			return nil, guard, err
		}
		source, err := MeasureAdoptionFile(ctx, proof.Sources[kind])
		if err != nil {
			return nil, guard, err
		}
		r.AdoptionFiles = append(r.AdoptionFiles, file, source)
		m.Components = append(m.Components, domain.VMAdoptionComponent{VMComponent: domain.VMComponent{Kind: kind, StorageRef: uuid.NewSHA1(d.Identity.ProviderResourceID, []byte("adoption:"+string(kind))), Digest: file.Digest, SizeBytes: file.Size}, StorageKey: file.Key, SourceDigest: source.Digest})
	}
	r.AdoptionBarrier = guard.Check
	r.RevalidateAdoption = func(ctx context.Context) error {
		if err := p.cfg.VerifyImage(ctx, p.cfg.Host, image); err != nil {
			return ProviderError(domain.VMErrorIntegrity, err)
		}
		fresh, err := driver.MeasurePersistent(ctx, r, d, release)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(fresh, proof) {
			return ProviderError(domain.VMErrorConflict, nil)
		}
		return nil
	}
	m.Digest = domain.VMAdoptionDigest(*m)
	if err = domain.ValidateVMAdoptionMeasurement(m); err != nil {
		return nil, guard, err
	}
	if err = CheckAdoptionFiles(ctx, r); err != nil {
		return nil, guard, err
	}
	if err = guard.Check(ctx); err != nil {
		return nil, guard, err
	}
	return m, guard, nil
}

// MeasureAdoptionFile hashes regular files or a sorted, non-symlink TPM tree.
// Device/inode identity prevents a same-path replacement from inheriting a claim.
func MeasureAdoptionFile(ctx context.Context, path string) (file AdoptionFile, retErr error) {
	result := AdoptionFile{Path: path}
	if !filepath.IsAbs(path) || CheckContainedPath(filepath.Dir(path), path) != nil {
		return result, ProviderError(domain.VMErrorIntegrity, nil)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return result, ProviderError(domain.VMErrorIntegrity, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (!info.IsDir() && (!info.Mode().IsRegular() || stat.Nlink != 1)) {
		return result, ProviderError(domain.VMErrorIntegrity, nil)
	}
	b, _ := json.Marshal([]uint64{uint64(stat.Dev), stat.Ino})
	result.Key = DigestBytes(b)
	if info.Mode().IsRegular() {
		f, openErr := os.Open(path)
		if openErr != nil {
			return result, ProviderError(domain.VMErrorIntegrity, openErr)
		}
		defer func() { retErr = JoinCleanupError(retErr, f.Close()) }()
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			return result, ProviderError(domain.VMErrorConflict, statErr)
		}
		h := sha256.New()
		result.Size, err = io.Copy(h, contextReader{ctx: ctx, r: f})
		result.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
		final, statErr := f.Stat()
		if statErr != nil || final.Size() != opened.Size() || !final.ModTime().Equal(opened.ModTime()) {
			return result, ProviderError(domain.VMErrorConflict, statErr)
		}
	} else {
		type entry struct {
			Name, Digest, Key string
			Size              int64
		}
		var entries []entry
		err = filepath.WalkDir(path, func(name string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			if name == filepath.Join(path, ".lock") {
				return nil
			}
			f, err := MeasureAdoptionFile(ctx, name)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(path, name)
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel, f.Digest, f.Key, f.Size})
			result.Size += f.Size
			return nil
		})
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		b, _ := json.Marshal(entries)
		result.Digest = DigestBytes(b)
	}
	if err != nil {
		return result, ProviderError(domain.VMErrorIntegrity, err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return result, ProviderError(domain.VMErrorConflict, err)
	}
	return result, nil
}

// CheckAdoptionMarker also protects direct driver calls. Existing owned marker
// revisions are used by lifecycle operations; acquiring ownership needs proof.
func CheckAdoptionMarker(r *PersistentResource, marker domain.VMOwnershipMarker) error {
	if r == nil || domain.ValidateVMOwnershipMarker(marker) != nil || marker.ProviderResourceID != r.ID {
		return ProviderError(domain.VMErrorInvalid, nil)
	}
	if r.Marker != nil {
		if domain.ValidateVMOwnershipMarker(*r.Marker) != nil || !SameIdentity(r.Marker.VMResourceIdentity, marker.VMResourceIdentity) {
			return ProviderError(domain.VMErrorForeign, nil)
		}
	} else if len(r.AdoptionFiles) == 0 || r.RevalidateAdoption == nil || r.AdoptionBarrier == nil {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	return nil
}

func CheckAdoptionFiles(ctx context.Context, r *PersistentResource) error {
	if r.RevalidateAdoption != nil {
		if err := r.RevalidateAdoption(ctx); err != nil {
			return err
		}
	}
	for _, expected := range r.AdoptionFiles {
		actual, err := MeasureAdoptionFile(ctx, expected.Path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, expected) {
			return ProviderError(domain.VMErrorConflict, nil)
		}
	}
	if r.AdoptionBarrier != nil {
		return r.AdoptionBarrier(ctx)
	}
	return nil
}
