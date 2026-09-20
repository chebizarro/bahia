package packagebackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

var ErrArtifactNotFound = errors.New("artifact not found")

const maxPageRequests = 1000

type Requester struct {
	baseURL string
	client  *http.Client
	auth    AuthConfig
	secrets map[string]string
}

func NewRequester(baseURL string, client *http.Client, auth AuthConfig, secrets map[string]string) *Requester {
	return &Requester{baseURL: baseURL, client: client, auth: auth, secrets: secrets}
}

func (r *Requester) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r.applyAuth(req)
	return r.client.Do(req)
}

func (r *Requester) applyAuth(req *http.Request) {
	if token := strings.TrimSpace(r.auth.BearerToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	if r.auth.Username != "" || r.auth.Password != "" {
		req.SetBasicAuth(r.auth.Username, r.auth.Password)
	}
}

func (r *Requester) Secret(name string) (string, bool) {
	value, ok := r.secrets[name]
	return value, ok
}

// GetPages follows same-origin page URLs until decode returns an empty next URL.
func (r *Requester) GetPages(ctx context.Context, firstPath, action string, decode func(io.Reader) (string, error)) error {
	path := firstPath
	seen := map[string]struct{}{}
	for page := 0; page < maxPageRequests; page++ {
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("%s: repeated page cursor %q", action, path)
		}
		seen[path] = struct{}{}
		resp, err := r.Do(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := ResponseError(resp, action)
			_ = resp.Body.Close()
			return err
		}
		next, err := decode(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if strings.TrimSpace(next) == "" {
			return nil
		}
		path, err = r.sameOriginPath(next)
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	return fmt.Errorf("%s: exceeded %d page requests", action, maxPageRequests)
}

func (r *Requester) sameOriginPath(raw string) (string, error) {
	base, _ := url.Parse(r.baseURL)
	next, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid next page URL: %w", err)
	}
	if next.User != nil || next.Fragment != "" {
		return "", fmt.Errorf("invalid next page URL %q", raw)
	}
	if next.IsAbs() || next.Host != "" {
		if !strings.EqualFold(next.Scheme, base.Scheme) || !strings.EqualFold(next.Host, base.Host) {
			return "", fmt.Errorf("next page URL %q is not same-origin", raw)
		}
	} else if !strings.HasPrefix(next.Path, "/") {
		return "", fmt.Errorf("next page URL %q is not server-relative", raw)
	}
	path := next.EscapedPath()
	basePath := strings.TrimSuffix(base.EscapedPath(), "/")
	if basePath != "" {
		if path != basePath && !strings.HasPrefix(path, basePath+"/") {
			return "", fmt.Errorf("next page URL %q is outside the backend base path", raw)
		}
		path = strings.TrimPrefix(path, basePath)
	}
	if path == "" {
		path = "/"
	}
	if next.RawQuery != "" {
		path += "?" + next.RawQuery
	}
	return path, nil
}

func ResponseError(resp *http.Response, action string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s failed: status=%d body=%s", action, resp.StatusCode, strings.TrimSpace(string(body)))
}

func BackendRepoName(repo domain.PackageRepository) string {
	if name := strings.TrimSpace(repo.ExternalRepositoryName); name != "" {
		return name
	}
	return strings.TrimSpace(repo.Name)
}

func EscapePath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
