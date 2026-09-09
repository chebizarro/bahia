package gitea

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var buildDependencyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// BuildDependencySpec is one fleet-authorized repository to pin for a build.
type BuildDependencySpec struct {
	Name     string
	CloneURL string
}

// PinnedBuildDependency is safe to project into a public kind-5100 dep tag.
type PinnedBuildDependency struct {
	Name      string
	CloneURL  string
	CommitSHA string
}

type buildDependencyRepositoryClient interface {
	GetRepo(ctx context.Context, owner, name string) (*RepoInfo, error)
	ResolveRef(ctx context.Context, owner, name, ref string) (string, error)
}

// BuildDependencyResolver pins authorized dependency repositories to their
// current fleet-Gitea default-branch heads. Resolution is all-or-nothing.
type BuildDependencyResolver struct {
	client buildDependencyRepositoryClient
	origin string
}

func NewBuildDependencyResolver(client buildDependencyRepositoryClient, baseURL string) (*BuildDependencyResolver, error) {
	if client == nil {
		return nil, fmt.Errorf("build dependency resolver requires a Gitea client")
	}
	origin, err := dependencyGiteaOrigin(baseURL)
	if err != nil {
		return nil, fmt.Errorf("build dependency resolver requires a credential-free https Gitea base URL")
	}
	return &BuildDependencyResolver{client: client, origin: origin}, nil
}

// Resolve validates and pins every configured dependency. It never returns a
// partial set, so callers cannot publish a job with only some authorized inputs.
func (r *BuildDependencyResolver) Resolve(ctx context.Context, specs []BuildDependencySpec) ([]PinnedBuildDependency, error) {
	pins := make([]PinnedBuildDependency, 0, len(specs))
	seenNames := make(map[string]struct{}, len(specs))
	for index, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if !buildDependencyNamePattern.MatchString(name) {
			return nil, fmt.Errorf("build dependency %d has an invalid name", index)
		}
		if _, duplicate := seenNames[name]; duplicate {
			return nil, fmt.Errorf("build dependency %d duplicates an authorized name", index)
		}
		seenNames[name] = struct{}{}

		cloneURL := strings.TrimSpace(spec.CloneURL)
		owner, repoName, err := parseDependencyCloneURL(cloneURL, r.origin)
		if err != nil {
			return nil, fmt.Errorf("build dependency %d has an invalid clone URL", index)
		}
		repo, err := r.client.GetRepo(ctx, owner, repoName)
		if err != nil {
			return nil, fmt.Errorf("resolve build dependency %q repository metadata: %w", name, err)
		}
		if repo == nil {
			return nil, fmt.Errorf("resolve build dependency %q repository metadata: repository not found", name)
		}
		defaultBranch := strings.TrimSpace(repo.DefaultBranch)
		if defaultBranch == "" {
			return nil, fmt.Errorf("resolve build dependency %q: repository has no default branch", name)
		}
		sha, err := r.client.ResolveRef(ctx, owner, repoName, defaultBranch)
		if err != nil {
			return nil, fmt.Errorf("resolve build dependency %q default branch: %w", name, err)
		}
		sha = strings.ToLower(strings.TrimSpace(sha))
		if !isFullCommitSHA(sha) {
			return nil, fmt.Errorf("resolve build dependency %q: Gitea returned a non-immutable commit", name)
		}
		pins = append(pins, PinnedBuildDependency{Name: name, CloneURL: cloneURL, CommitSHA: sha})
	}
	return pins, nil
}

func dependencyGiteaOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("invalid URL")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func parseDependencyCloneURL(raw, allowedOrigin string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid URL")
	}
	if strings.ContainsAny(raw, "\r\n\t ") || strings.Contains(parsed.EscapedPath(), "%") {
		return "", "", fmt.Errorf("invalid URL")
	}
	if strings.ToLower(parsed.Scheme+"://"+parsed.Host) != allowedOrigin {
		return "", "", fmt.Errorf("invalid URL")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", "", fmt.Errorf("invalid URL")
	}
	return parts[0], parts[1], nil
}
