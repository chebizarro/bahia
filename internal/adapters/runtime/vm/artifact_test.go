package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseImageRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	repo, parsed, err := ParseImageRef("vm/base-noble@" + digest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "vm/base-noble" || parsed != digest {
		t.Fatalf("unexpected parse result: %q %q", repo, parsed)
	}
}

func TestParseImageRefRejectsTagOnly(t *testing.T) {
	_, _, err := ParseImageRef("vm/base-noble:v3")
	if err == nil || !strings.Contains(err.Error(), "tag-only") {
		t.Fatalf("expected tag-only rejection, got %v", err)
	}
}

func TestParseImageRefRejectsMissingDigest(t *testing.T) {
	_, _, err := ParseImageRef("vm/base-noble")
	if err == nil || !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("expected missing-digest rejection, got %v", err)
	}
}

func TestParseImageRefRejectsMalformedDigest(t *testing.T) {
	for _, ref := range []string{
		"vm/base@sha256:short",
		"vm/base@md5:" + strings.Repeat("ab", 32),
		"vm/base@" + strings.Repeat("ab", 32),
	} {
		if _, _, err := ParseImageRef(ref); err == nil {
			t.Errorf("expected malformed digest rejection for %q", ref)
		}
	}
}

func TestParseImageRefRejectsTraversalRepo(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, repo := range []string{"../escape", "a/../../b", "/abs/path", "a//b", "."} {
		if _, _, err := ParseImageRef(repo + "@" + digest); err == nil {
			t.Errorf("expected repo rejection for %q", repo)
		}
	}
}

// writeRelease builds <root>/<repo>/<releaseID>/ with a disk, optional UEFI
// vars, and a manifest (declaring a pre-service-mode v1 agent), plus a
// "current" symlink, returning the manifest digest.
func writeRelease(t *testing.T, root, repo, releaseID, format string, uefi bool) string {
	t.Helper()
	return writeReleaseAgent(t, root, repo, releaseID, format, uefi, 1)
}

// writeReleaseAgent is writeRelease with an explicit agent_protocol_version.
func writeReleaseAgent(t *testing.T, root, repo, releaseID, format string, uefi bool, agentVersion int) string {
	t.Helper()
	releaseDir := filepath.Join(root, filepath.FromSlash(repo), releaseID)
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	if format == FormatFirecrackerRootFS {
		kernel := []byte("fake-kernel-" + releaseID)
		if err := os.WriteFile(filepath.Join(releaseDir, "kernel"), kernel, 0o644); err != nil {
			t.Fatal(err)
		}
		kernelSum := sha256.Sum256(kernel)
		hashes["kernel"] = hex.EncodeToString(kernelSum[:])
		rootfs := []byte("fake-rootfs-ext4-" + releaseID)
		if err := os.WriteFile(filepath.Join(releaseDir, "rootfs.ext4"), rootfs, 0o644); err != nil {
			t.Fatal(err)
		}
		rootfsSum := sha256.Sum256(rootfs)
		hashes["rootfs"] = hex.EncodeToString(rootfsSum[:])
	} else {
		disk := []byte("fake-qcow2-disk-content-" + releaseID)
		if err := os.WriteFile(filepath.Join(releaseDir, "disk.qcow2"), disk, 0o644); err != nil {
			t.Fatal(err)
		}
		diskSum := sha256.Sum256(disk)
		hashes["disk"] = hex.EncodeToString(diskSum[:])
	}
	manifest := map[string]any{
		"image_id":               releaseID,
		"arch":                   "x86_64",
		"format":                 format,
		"agent_protocol_version": agentVersion,
		"sha256":                 hashes,
	}
	if uefi {
		vars := []byte("fake-uefi-vars-" + releaseID)
		if err := os.WriteFile(filepath.Join(releaseDir, "uefi-vars.fd"), vars, 0o644); err != nil {
			t.Fatal(err)
		}
		varsSum := sha256.Sum256(vars)
		manifest["sha256"].(map[string]string)["uefi_vars"] = hex.EncodeToString(varsSum[:])
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, filepath.FromSlash(repo), "current")
	_ = os.Remove(current)
	if err := os.Symlink(releaseID, current); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestResolveReleaseHappyPath(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)

	release, err := ResolveRelease(root, "vm/base", digest, FormatQCOW2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release.ManifestDigest != digest {
		t.Errorf("manifest digest mismatch: %s", release.ManifestDigest)
	}
	if release.Manifest.ImageID != "rel-001" {
		t.Errorf("unexpected image id: %s", release.Manifest.ImageID)
	}
	if filepath.Base(release.Dir) != "rel-001" {
		t.Errorf("expected release dir rel-001, got %s", release.Dir)
	}
	if release.DiskPath != filepath.Join(release.Dir, "disk.qcow2") {
		t.Errorf("unexpected disk path: %s", release.DiskPath)
	}
	if release.UEFIVarsPath != "" {
		t.Errorf("expected no uefi vars, got %s", release.UEFIVarsPath)
	}
}

func TestResolveReleaseUEFI(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/win", "rel-win", FormatQCOW2, true)

	release, err := ResolveRelease(root, "vm/win", digest, FormatQCOW2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release.UEFIVarsPath != filepath.Join(release.Dir, "uefi-vars.fd") {
		t.Errorf("unexpected uefi vars path: %s", release.UEFIVarsPath)
	}
}

func TestResolveReleaseFirecracker(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/micro", "rel-fc", FormatFirecrackerRootFS, false)

	release, err := ResolveRelease(root, "vm/micro", digest, FormatFirecrackerRootFS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release.KernelPath != filepath.Join(release.Dir, "kernel") {
		t.Errorf("unexpected kernel path: %s", release.KernelPath)
	}
	if release.RootFSPath != filepath.Join(release.Dir, "rootfs.ext4") {
		t.Errorf("unexpected rootfs path: %s", release.RootFSPath)
	}
	if release.DiskPath != "" || release.UEFIVarsPath != "" {
		t.Errorf("qcow2 fields should be empty for firecracker releases: %+v", release)
	}
	spec := release.ImageSpec()
	if spec.KernelPath != release.KernelPath || spec.RootFSPath != release.RootFSPath {
		t.Errorf("ImageSpec did not carry firecracker paths: %+v", spec)
	}
}

func TestResolveReleaseFirecrackerMissingHashes(t *testing.T) {
	root := t.TempDir()
	releaseDir := filepath.Join(root, "vm", "micro", "rel-fc")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{
		"image_id": "rel-fc",
		"format":   FormatFirecrackerRootFS,
		"sha256":   map[string]string{},
	})
	if err := os.WriteFile(filepath.Join(releaseDir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("rel-fc", filepath.Join(root, "vm", "micro", "current")); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	_, err := ResolveRelease(root, "vm/micro", digest, FormatFirecrackerRootFS)
	if err == nil || !strings.Contains(err.Error(), "sha256.kernel") {
		t.Fatalf("expected missing kernel hash error, got %v", err)
	}
}

func TestResolveReleaseFirecrackerCorruptRootfs(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/micro", "rel-fc", FormatFirecrackerRootFS, false)
	rootfsPath := filepath.Join(root, "vm", "micro", "rel-fc", "rootfs.ext4")
	if err := os.WriteFile(rootfsPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveRelease(root, "vm/micro", digest, FormatFirecrackerRootFS)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected rootfs hash mismatch, got %v", err)
	}
}

func TestResolveReleaseDigestMismatch(t *testing.T) {
	root := t.TempDir()
	writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)

	wrong := "sha256:" + strings.Repeat("00", 32)
	_, err := ResolveRelease(root, "vm/base", wrong, FormatQCOW2)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch error, got %v", err)
	}
}

func TestResolveReleaseFormatMismatch(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)

	_, err := ResolveRelease(root, "vm/base", digest, FormatFirecrackerRootFS)
	if err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("expected format mismatch error, got %v", err)
	}
}

func TestResolveReleaseMissingChannel(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("ab", 32)
	_, err := ResolveRelease(root, "vm/absent", digest, FormatQCOW2)
	if err == nil || !strings.Contains(err.Error(), "no current release") {
		t.Fatalf("expected missing-channel error, got %v", err)
	}
}

func TestResolveReleaseCorruptDisk(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)
	// Corrupt the disk after the manifest was hashed.
	diskPath := filepath.Join(root, "vm", "base", "rel-001", "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveRelease(root, "vm/base", digest, FormatQCOW2)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected disk hash mismatch, got %v", err)
	}
}

func TestResolveReleaseCurrentAsPlainDirectory(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)
	// Replace the symlink with a real directory containing the release.
	channel := filepath.Join(root, "vm", "base")
	if err := os.Remove(filepath.Join(channel, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(channel, "rel-001"), filepath.Join(channel, "current")); err != nil {
		t.Fatal(err)
	}
	release, err := ResolveRelease(root, "vm/base", digest, FormatQCOW2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(release.Dir) != "current" {
		t.Errorf("unexpected release dir: %s", release.Dir)
	}
}

func TestResolveReleaseRejectsChainedSymlink(t *testing.T) {
	root := t.TempDir()
	digest := writeRelease(t, root, "vm/base", "rel-001", FormatQCOW2, false)
	channel := filepath.Join(root, "vm", "base")
	// current -> hop -> rel-001 (two hops).
	if err := os.Symlink("rel-001", filepath.Join(channel, "hop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(channel, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hop", filepath.Join(channel, "current")); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveRelease(root, "vm/base", digest, FormatQCOW2)
	if err == nil || !strings.Contains(err.Error(), "at most once") {
		t.Fatalf("expected chained symlink rejection, got %v", err)
	}
}

func TestResolveReleaseMissingFormat(t *testing.T) {
	root := t.TempDir()
	releaseDir := filepath.Join(root, "vm", "base", "rel-001")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"image_id": "rel-001", "sha256": map[string]string{}})
	if err := os.WriteFile(filepath.Join(releaseDir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("rel-001", filepath.Join(root, "vm", "base", "current")); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	_, err := ResolveRelease(root, "vm/base", digest, FormatQCOW2)
	if err == nil || !strings.Contains(err.Error(), "missing the format field") {
		t.Fatalf("expected missing-format error, got %v", err)
	}
}
