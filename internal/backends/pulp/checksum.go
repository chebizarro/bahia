package pulp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ObserveArtifact reads file-content SHA-256 from one immutable repository
// version. It never filters by the expected digest: that would hide a mismatch.
func (b *Backend) ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
	if !b.checksumAPI {
		return result, packagebackend.ErrChecksumUnavailable
	}
	if !packagebackend.ValidSHA256(artifact.SHA256) {
		return result, fmt.Errorf("artifact has no valid expected SHA-256 for comparison")
	}
	path := strings.TrimSpace(artifact.BackendPath)
	if path == "" {
		path, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return result, err
		}
	}
	name := packagebackend.BackendRepoName(repo)
	version, err := b.checksumRepositoryVersion(ctx, name)
	if err != nil {
		return result, err
	}
	result = packagebackend.ArtifactObservation{BackendPath: path, DownloadURL: b.artifactURL(name, path)}
	if version == "" {
		return result, nil // A valid empty repository lookup proves absence.
	}
	query := url.Values{"repository_version": {version}, "relative_path": {path}}
	err = b.GetPages(ctx, "/pulp/api/v3/content/file/files/?"+query.Encode(), "observe pulp checksum", func(body io.Reader) (string, error) {
		var page struct {
			Count   *int            `json:"count"`
			Next    json.RawMessage `json:"next"`
			Results []struct {
				RelativePath string `json:"relative_path"`
				SHA256       string `json:"sha256"`
			} `json:"results"`
		}
		if err := packagebackend.DecodeJSON(body, &page); err != nil {
			return "", err
		}
		// A file repository version has at most one unit at a relative path.
		if page.Count == nil || page.Results == nil || *page.Count != len(page.Results) || *page.Count > 1 || string(page.Next) != "null" {
			return "", fmt.Errorf("pulp checksum lookup is incomplete or ambiguous")
		}
		for _, item := range page.Results {
			if item.RelativePath != path || !packagebackend.ValidSHA256(item.SHA256) {
				return "", fmt.Errorf("pulp content did not provide an exact path and valid backend SHA-256")
			}
			if err := b.CheckPublic(item.RelativePath, item.SHA256); err != nil {
				return "", err
			}
			result.Exists = true
			result.SHA256 = strings.ToLower(item.SHA256)
			result.Metadata = map[string]string{"repository_version": version}
		}
		return "", nil
	})
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return result, nil
}

func (b *Backend) checksumRepositoryVersion(ctx context.Context, name string) (string, error) {
	var version string
	err := b.GetPages(ctx, "/pulp/api/v3/repositories/file/file/?name="+url.QueryEscape(name), "find pulp checksum repository", func(body io.Reader) (string, error) {
		var page struct {
			Count   *int            `json:"count"`
			Next    json.RawMessage `json:"next"`
			Results []struct {
				Name          string `json:"name"`
				Href          string `json:"pulp_href"`
				LatestVersion string `json:"latest_version_href"`
			} `json:"results"`
		}
		if err := packagebackend.DecodeJSON(body, &page); err != nil {
			return "", err
		}
		if page.Count == nil || page.Results == nil || *page.Count != len(page.Results) || *page.Count > 1 || string(page.Next) != "null" {
			return "", fmt.Errorf("pulp repository lookup is incomplete or ambiguous")
		}
		for _, item := range page.Results {
			if item.Name != name {
				return "", fmt.Errorf("pulp repository lookup returned a different repository")
			}
			href, err := validPulpPath(item.Href, "/pulp/api/v3/repositories/file/file/", "repository href")
			if err != nil {
				return "", err
			}
			prefix := href + "versions/"
			number := strings.TrimSuffix(strings.TrimPrefix(item.LatestVersion, prefix), "/")
			n, err := strconv.ParseUint(number, 10, 64)
			if err != nil || item.LatestVersion != prefix+strconv.FormatUint(n, 10)+"/" {
				return "", fmt.Errorf("pulp repository has no valid immutable latest version href")
			}
			if err := b.CheckPublic(item.LatestVersion); err != nil {
				return "", err
			}
			version = item.LatestVersion
		}
		return "", nil
	})
	return version, err
}
