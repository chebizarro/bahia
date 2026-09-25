package packagebackend

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/redact"
)

var ErrArtifactNotFound = errors.New("artifact not found")

var ErrChecksumUnavailable = errors.New("backend checksum observation requires a supported configured API version")

const maxPageRequests = 1000

type Requester struct {
	baseURL    string
	client     *http.Client
	auth       AuthConfig
	secrets    map[string]string
	redactions []string
}

func NewRequester(baseURL string, client *http.Client, auth AuthConfig, secrets map[string]string, sensitive ...string) *Requester {
	// Never mutate a caller's client or let Go's redirect policy forward Basic
	// credentials to a subdomain, another port, or a plaintext endpoint.
	isolated := *client
	isolated.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	isolated.Jar = nil
	values := append([]string{auth.Username, auth.Password, auth.BearerToken}, sensitive...)
	if auth.Username != "" || auth.Password != "" {
		values = append(values, auth.Username+":"+auth.Password)
	}
	secretCopy := make(map[string]string, len(secrets))
	for key, value := range secrets {
		secretCopy[key] = value
		values = append(values, value)
	}
	return &Requester{baseURL: baseURL, client: &isolated, auth: auth, secrets: secretCopy, redactions: values}
}

// ScrubError is the diagnostic boundary used by the HTTP adapters. Original
// error objects are not retained: formatting or unwrapping cannot reveal them.
func (r *Requester) ScrubError(err *error, safe ...error) {
	for _, sentinel := range safe {
		if errors.Is(*err, sentinel) {
			*err = sentinel
			return
		}
	}
	if errors.Is(*err, ErrArtifactNotFound) {
		*err = ErrArtifactNotFound
		return
	}
	if errors.Is(*err, ErrChecksumUnavailable) {
		*err = ErrChecksumUnavailable
		return
	}
	*err = redact.Error(*err, r.redactions...)
}

// CheckPublic refuses secret-bearing URLs or fields before they can be sent or
// projected. Do not "sanitize" a credential-bearing URL into a different URL.
func (r *Requester) CheckPublic(values ...string) error {
	for _, value := range values {
		if redact.Text(value, r.redactions...) != value {
			return errors.New("backend public data contains secret material")
		}
	}
	return nil
}

func (r *Requester) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (response *http.Response, err error) {
	defer r.ScrubError(&err)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if err := r.CheckPublic(r.baseURL + path); err != nil {
		return nil, err
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
func (r *Requester) GetPages(ctx context.Context, firstPath, action string, decode func(io.Reader) (string, error)) (err error) {
	defer r.ScrubError(&err)
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
		first, _ := url.Parse(firstPath)
		pageURL, _ := url.Parse(path)
		if pageURL.Path != first.Path {
			return fmt.Errorf("%s: next page changed API path", action)
		}
		for key, values := range first.Query() {
			if strings.Join(pageURL.Query()[key], "\x00") != strings.Join(values, "\x00") {
				return fmt.Errorf("%s: next page changed query scope", action)
			}
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
	// Error bodies may echo credentials, signed URLs, or TLS material. Status is
	// sufficient diagnostic evidence; untrusted body text is never a diagnostic.
	return fmt.Errorf("%s failed: status=%d", action, resp.StatusCode)
}

func ValidSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// DecodeJSON requires one complete JSON document, not a successful prefix of a
// malformed response. Checksum callers also validate required schema fields.
func DecodeJSON(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid backend JSON response")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("invalid backend JSON response: trailing data")
	}
	return nil
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
