package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Release manifest formats supported by the VM runtimes.
const (
	// FormatQCOW2 is a qcow2 disk release for libvirt/QEMU domains
	// (disk.qcow2 + sha256.disk, optional uefi-vars.fd + sha256.uefi_vars).
	FormatQCOW2 = "qcow2"
	// FormatFirecrackerRootFS is a Firecracker kernel+rootfs release.
	FormatFirecrackerRootFS = "firecracker-rootfs"
)

const (
	manifestFileName = "manifest.json"
	currentLinkName  = "current"
	diskFileName     = "disk.qcow2"
	uefiVarsFileName = "uefi-vars.fd"
)

// Manifest is the VM image release manifest (cascadia-go packaging format).
// The artifact digest bahia stores is the sha256 of the canonical
// manifest.json bytes.
type Manifest struct {
	ImageID              string            `json:"image_id"`
	Arch                 string            `json:"arch,omitempty"`
	Format               string            `json:"format"`
	AgentProtocolVersion int               `json:"agent_protocol_version,omitempty"`
	SHA256               map[string]string `json:"sha256"`
}

// Release is a resolved, digest-verified image release.
type Release struct {
	// Dir is the resolved release directory (after following the
	// channel's "current" symlink at most once).
	Dir string
	// Manifest is the decoded release manifest.
	Manifest Manifest
	// ManifestDigest is "sha256:<hex>" over the manifest.json bytes.
	ManifestDigest string
	// DiskPath is the verified base disk (qcow2 releases only).
	DiskPath string
	// UEFIVarsPath is the verified UEFI vars template (optional, qcow2
	// releases only).
	UEFIVarsPath string
}

// ImageSpec converts the release into the driver-facing image description.
func (r *Release) ImageSpec() ImageSpec {
	return ImageSpec{
		Format:         r.Manifest.Format,
		Arch:           r.Manifest.Arch,
		ReleaseDir:     r.Dir,
		DiskPath:       r.DiskPath,
		UEFIVarsPath:   r.UEFIVarsPath,
		ImageID:        r.Manifest.ImageID,
		ManifestDigest: r.ManifestDigest,
	}
}

var sha256DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ParseImageRef splits a VM artifact reference of the form
// "<repo>@sha256:<hex>" into its repo path and digest. Tag-only references
// and malformed refs are rejected with explicit errors — VM deploys require
// digest pinning, and the repo must be a clean relative path under the
// configured image_root.
func ParseImageRef(image string) (repo, digest string, err error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", "", fmt.Errorf("vm image reference is empty")
	}
	at := strings.LastIndex(image, "@")
	if at < 0 {
		if strings.Contains(image, ":") {
			return "", "", fmt.Errorf("vm image reference %q is tag-only: VM runtimes require a digest-pinned reference (<repo>@sha256:<hex>)", image)
		}
		return "", "", fmt.Errorf("vm image reference %q has no digest: VM runtimes require a digest-pinned reference (<repo>@sha256:<hex>)", image)
	}
	repo = strings.TrimSpace(image[:at])
	digest = strings.ToLower(strings.TrimSpace(image[at+1:]))
	if !sha256DigestPattern.MatchString(digest) {
		return "", "", fmt.Errorf("vm image reference %q has malformed digest %q (want sha256:<64 hex>)", image, digest)
	}
	if err := validateRepoPath(repo); err != nil {
		return "", "", fmt.Errorf("vm image reference %q: %w", image, err)
	}
	return repo, digest, nil
}

func validateRepoPath(repo string) error {
	if repo == "" {
		return fmt.Errorf("repo path is empty")
	}
	slashed := filepath.ToSlash(repo)
	if strings.HasPrefix(slashed, "/") {
		return fmt.Errorf("repo path %q must be relative to image_root", repo)
	}
	clean := filepath.Clean(filepath.FromSlash(slashed))
	if clean != filepath.FromSlash(slashed) {
		return fmt.Errorf("repo path %q is not a clean path", repo)
	}
	for _, part := range strings.Split(slashed, "/") {
		if part == ".." || part == "." || part == "" {
			return fmt.Errorf("repo path %q must not contain relative traversal elements", repo)
		}
	}
	return nil
}

// ResolveRelease resolves <imageRoot>/<repo>/current (following the symlink
// at most once), recomputes the canonical manifest.json hash, and verifies
// it against the expected digest. wantFormat pins the manifest format the
// calling runtime type supports; mismatches fail explicitly before any
// hypervisor call.
func ResolveRelease(imageRoot, repo, digest, wantFormat string) (*Release, error) {
	if strings.TrimSpace(imageRoot) == "" {
		return nil, fmt.Errorf("vm image_root is not configured")
	}
	if err := validateRepoPath(repo); err != nil {
		return nil, err
	}
	channelDir := filepath.Join(imageRoot, filepath.FromSlash(repo))
	releaseDir, err := resolveCurrent(channelDir)
	if err != nil {
		return nil, fmt.Errorf("resolving release for repo %q: %w", repo, err)
	}

	manifestPath := filepath.Join(releaseDir, manifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading release manifest for repo %q: %w", repo, err)
	}
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != digest {
		return nil, fmt.Errorf("release manifest digest mismatch for repo %q: current release is %s but the artifact pins %s (the channel's current release does not match the deployed artifact)", repo, actual, digest)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decoding release manifest for repo %q: %w", repo, err)
	}
	if strings.TrimSpace(manifest.ImageID) == "" {
		return nil, fmt.Errorf("release manifest for repo %q is missing image_id", repo)
	}
	switch manifest.Format {
	case FormatQCOW2, FormatFirecrackerRootFS:
	case "":
		return nil, fmt.Errorf("release manifest for repo %q is missing the format field (want %q or %q)", repo, FormatQCOW2, FormatFirecrackerRootFS)
	default:
		return nil, fmt.Errorf("release manifest for repo %q has unsupported format %q (want %q or %q)", repo, manifest.Format, FormatQCOW2, FormatFirecrackerRootFS)
	}
	if manifest.Format != wantFormat {
		return nil, fmt.Errorf("release manifest for repo %q has format %q but this runtime requires %q", repo, manifest.Format, wantFormat)
	}

	release := &Release{Dir: releaseDir, Manifest: manifest, ManifestDigest: actual}
	switch manifest.Format {
	case FormatQCOW2:
		diskDigest := strings.TrimSpace(manifest.SHA256["disk"])
		if diskDigest == "" {
			return nil, fmt.Errorf("qcow2 release manifest for repo %q is missing sha256.disk", repo)
		}
		release.DiskPath = filepath.Join(releaseDir, diskFileName)
		if err := verifyFileSHA256(release.DiskPath, diskDigest); err != nil {
			return nil, fmt.Errorf("verifying qcow2 base disk for repo %q: %w", repo, err)
		}
		if varsDigest := strings.TrimSpace(manifest.SHA256["uefi_vars"]); varsDigest != "" {
			release.UEFIVarsPath = filepath.Join(releaseDir, uefiVarsFileName)
			if err := verifyFileSHA256(release.UEFIVarsPath, varsDigest); err != nil {
				return nil, fmt.Errorf("verifying UEFI vars template for repo %q: %w", repo, err)
			}
		}
	case FormatFirecrackerRootFS:
		// Firecracker file resolution (kernel + rootfs entries) lands with
		// the Firecracker driver; the manifest digest and format are
		// verified above, which is all the shared core needs.
	}
	return release, nil
}

// resolveCurrent resolves the channel's "current" entry: a symlink is
// followed exactly once and must land on a real directory; a plain
// directory is used as-is.
func resolveCurrent(channelDir string) (string, error) {
	currentPath := filepath.Join(channelDir, currentLinkName)
	info, err := os.Lstat(currentPath)
	if err != nil {
		return "", fmt.Errorf("release channel has no current release: %w", err)
	}
	releaseDir := currentPath
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(currentPath)
		if err != nil {
			return "", fmt.Errorf("reading current release symlink: %w", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(channelDir, target)
		}
		releaseDir = filepath.Clean(target)
		targetInfo, err := os.Lstat(releaseDir)
		if err != nil {
			return "", fmt.Errorf("resolving current release target: %w", err)
		}
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("current release target %q is itself a symlink (symlinks are followed at most once)", releaseDir)
		}
		info = targetInfo
	}
	if !info.IsDir() {
		return "", fmt.Errorf("current release %q is not a directory", releaseDir)
	}
	return releaseDir, nil
}

func verifyFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return fmt.Errorf("sha256 mismatch for %s: manifest declares %s but file hashes to %s", path, expected, actual)
	}
	return nil
}
