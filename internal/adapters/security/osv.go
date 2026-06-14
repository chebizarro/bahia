package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	packageurl "github.com/anchore/packageurl-go"
)

const defaultOSVBaseURL = "https://api.osv.dev/v1"

const (
	defaultOSVCacheTTL     = 5 * time.Minute
	defaultOSVMaxBatchSize = 1000
	defaultOSVMaxRetries   = 2
)

type cacheEntry struct {
	vulns     []Vulnerability
	expiresAt time.Time
}

type detailCacheEntry struct {
	detail    *OSVVulnerabilityDetail
	expiresAt time.Time
}

// OSVClient queries the OSV (Open Source Vulnerabilities) database.
type OSVClient struct {
	httpClient *http.Client
	baseURL    string

	mu           sync.RWMutex
	cacheTTL     time.Duration
	cache        map[string]cacheEntry
	detailCache  map[string]detailCacheEntry
	maxBatchSize int
	maxRetries   int
	backoff      func(context.Context, int, *OSVError) error
}

// OSVOption configures OSVClient while preserving the zero-argument constructor.
type OSVOption func(*OSVClient)

func WithOSVHTTPClient(client *http.Client) OSVOption {
	return func(c *OSVClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithOSVBaseURL(baseURL string) OSVOption {
	return func(c *OSVClient) {
		if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
			c.baseURL = trimmed
		}
	}
}

func WithOSVCacheTTL(ttl time.Duration) OSVOption {
	return func(c *OSVClient) {
		if ttl > 0 {
			c.cacheTTL = ttl
		}
	}
}

func WithOSVMaxBatchSize(size int) OSVOption {
	return func(c *OSVClient) {
		if size > 0 {
			c.maxBatchSize = size
		}
	}
}

func WithOSVMaxRetries(retries int) OSVOption {
	return func(c *OSVClient) {
		if retries >= 0 {
			c.maxRetries = retries
		}
	}
}

func WithOSVBackoff(backoff func(context.Context, int, *OSVError) error) OSVOption {
	return func(c *OSVClient) {
		if backoff != nil {
			c.backoff = backoff
		}
	}
}

func NewOSVClient(opts ...OSVOption) *OSVClient {
	client := &OSVClient{
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		baseURL:      defaultOSVBaseURL,
		cacheTTL:     defaultOSVCacheTTL,
		cache:        make(map[string]cacheEntry),
		detailCache:  make(map[string]detailCacheEntry),
		maxBatchSize: defaultOSVMaxBatchSize,
		maxRetries:   defaultOSVMaxRetries,
	}
	client.backoff = defaultOSVBackoff
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// OSVQuery is one normalized OSV query request. PURL is preferred over package coordinates.
type OSVQuery struct {
	Ecosystem string
	Name      string
	Version   string
	PURL      string
	Commit    string
}

// OSVQueryResult preserves the original query/result ordering returned by QueryBatch.
type OSVQueryResult struct {
	Query           OSVQuery
	Vulnerabilities []Vulnerability
}

// OSVVulnerabilityDetail contains hydrated OSV vulnerability data.
type OSVVulnerabilityDetail struct {
	ID         string
	Summary    string
	Details    string
	Aliases    []string
	Severity   string
	CVE        string
	Modified   string
	Published  string
	Withdrawn  string
	References []string
	Raw        map[string]any
}

// OSVError classifies provider and transport failures for retry decisions.
type OSVError struct {
	Operation  string
	StatusCode int
	Retryable  bool
	RetryAfter *time.Duration
	Reason     string
	Err        error
}

func (e *OSVError) Error() string {
	parts := []string{"osv", e.Operation}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status %d", e.StatusCode))
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *OSVError) Unwrap() error { return e.Err }

// QueryPackage checks a package for known vulnerabilities and hydrates returned IDs.
func (c *OSVClient) QueryPackage(ctx context.Context, ecosystem string, name string, version string) ([]Vulnerability, error) {
	results, err := c.QueryBatch(ctx, []OSVQuery{{Ecosystem: ecosystem, Name: name, Version: version}})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return c.hydrateVulnerabilities(ctx, results[0].Vulnerabilities)
}

// QueryPURL checks a package-url target and hydrates returned IDs.
func (c *OSVClient) QueryPURL(ctx context.Context, rawPURL string) ([]Vulnerability, error) {
	results, err := c.QueryBatch(ctx, []OSVQuery{{PURL: rawPURL}})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return c.hydrateVulnerabilities(ctx, results[0].Vulnerabilities)
}

// QueryCommit checks a git commit hash and hydrates returned IDs. repoURL is accepted for caller symmetry; OSV commit queries use the commit hash field.
func (c *OSVClient) QueryCommit(ctx context.Context, repoURL, commitHash string) ([]Vulnerability, error) {
	_ = strings.TrimSpace(repoURL)
	results, err := c.QueryBatch(ctx, []OSVQuery{{Commit: commitHash}})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return c.hydrateVulnerabilities(ctx, results[0].Vulnerabilities)
}

// QueryBatch checks multiple package/PURL/commit coordinates, preserving input order and deduplicating identical requests.
func (c *OSVClient) QueryBatch(ctx context.Context, queries []OSVQuery) ([]OSVQueryResult, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	normalized := make([]OSVQuery, len(queries))
	keys := make([]string, len(queries))
	resultsByKey := make(map[string][]Vulnerability)
	unique := make([]OSVQuery, 0, len(queries))
	seen := make(map[string]struct{})
	for i, query := range queries {
		norm, key, err := normalizeOSVQuery(query)
		if err != nil {
			return nil, err
		}
		normalized[i] = norm
		keys[i] = key
		if vulns, ok := c.getCached(key); ok {
			resultsByKey[key] = vulns
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, norm)
	}

	for start := 0; start < len(unique); start += c.maxBatchSize {
		end := start + c.maxBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		chunkResults, err := c.postQueryBatch(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for i, vulns := range chunkResults {
			_, key, err := normalizeOSVQuery(chunk[i])
			if err != nil {
				return nil, err
			}
			resultsByKey[key] = vulns
			c.setCached(key, vulns)
		}
	}

	out := make([]OSVQueryResult, len(queries))
	for i := range queries {
		vulns := resultsByKey[keys[i]]
		copyVulns := make([]Vulnerability, len(vulns))
		copy(copyVulns, vulns)
		out[i] = OSVQueryResult{Query: normalized[i], Vulnerabilities: copyVulns}
	}
	return out, nil
}

// HydrateVulnerability fetches full details for one OSV vulnerability ID.
func (c *OSVClient) HydrateVulnerability(ctx context.Context, id string) (*OSVVulnerabilityDetail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, &OSVError{Operation: "hydrate", Retryable: false, Reason: "vulnerability id is required"}
	}
	if detail, ok := c.getDetailCached(id); ok {
		return detail, nil
	}
	var parsed osvVulnerabilityPayload
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/vulns/"+url.PathEscape(id), nil, &parsed, "hydrate"); err != nil {
		return nil, err
	}
	detail := detailFromPayload(parsed)
	c.setDetailCached(id, detail)
	return detail, nil
}

func (c *OSVClient) hydrateVulnerabilities(ctx context.Context, vulns []Vulnerability) ([]Vulnerability, error) {
	out := make([]Vulnerability, 0, len(vulns))
	for _, vuln := range vulns {
		detail, err := c.HydrateVulnerability(ctx, vuln.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, vulnerabilityFromDetail(detail, vuln.Modified))
	}
	return out, nil
}

func (c *OSVClient) postQueryBatch(ctx context.Context, queries []OSVQuery) ([][]Vulnerability, error) {
	payloadQueries := make([]osvQueryPayload, len(queries))
	for i, query := range queries {
		payloadQueries[i] = queryPayload(query)
	}
	payload, err := json.Marshal(map[string]any{"queries": payloadQueries})
	if err != nil {
		return nil, fmt.Errorf("marshal osv querybatch request: %w", err)
	}
	var parsed struct {
		Results []struct {
			Vulns []struct {
				ID       string `json:"id"`
				Modified string `json:"modified"`
			} `json:"vulns"`
			NextPageToken string `json:"next_page_token"`
		} `json:"results"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/querybatch", payload, &parsed, "querybatch"); err != nil {
		return nil, err
	}
	if len(parsed.Results) != len(queries) {
		return nil, &OSVError{Operation: "querybatch", Retryable: false, Reason: fmt.Sprintf("response result count %d does not match query count %d", len(parsed.Results), len(queries))}
	}
	out := make([][]Vulnerability, len(parsed.Results))
	for i, result := range parsed.Results {
		for _, item := range result.Vulns {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			out[i] = append(out[i], Vulnerability{ID: id, CVE: firstCVE([]string{id}), Severity: "UNKNOWN", Modified: strings.TrimSpace(item.Modified)})
		}
	}
	return out, nil
}

func (c *OSVClient) doJSON(ctx context.Context, method, endpoint string, payload []byte, dest any, operation string) error {
	attempts := c.maxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			var osvErr *OSVError
			_ = errors.As(lastErr, &osvErr)
			if err := c.backoff(ctx, attempt, osvErr); err != nil {
				return err
			}
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
		if err != nil {
			return fmt.Errorf("create osv %s request: %w", operation, err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return err
			}
			osvErr := classifyTransportError(operation, err)
			lastErr = osvErr
			if !osvErr.Retryable || attempt == attempts-1 {
				return osvErr
			}
			continue
		}
		err = decodeOSVResponse(resp, dest, operation)
		if err == nil {
			return nil
		}
		lastErr = err
		var osvErr *OSVError
		if errors.As(err, &osvErr) && osvErr.Retryable && attempt < attempts-1 {
			continue
		}
		return err
	}
	return lastErr
}

func decodeOSVResponse(resp *http.Response, dest any, operation string) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return classifyHTTPError(operation, resp.StatusCode, resp.Header.Get("Retry-After"), strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return &OSVError{Operation: operation, Retryable: false, Reason: "decode response", Err: err}
	}
	return nil
}

func classifyHTTPError(operation string, status int, retryAfterHeader string, body string) *OSVError {
	err := &OSVError{Operation: operation, StatusCode: status, Reason: body}
	switch {
	case status == http.StatusBadRequest:
		err.Retryable = false
		if err.Reason == "" {
			err.Reason = "invalid request"
		}
	case status == http.StatusNotFound:
		err.Retryable = false
		if err.Reason == "" {
			err.Reason = "not found"
		}
	case status == http.StatusTooManyRequests:
		err.Retryable = true
		err.RetryAfter = parseRetryAfter(retryAfterHeader)
		if err.Reason == "" {
			err.Reason = "rate limited"
		}
	case status >= 500:
		err.Retryable = true
		if err.Reason == "" {
			err.Reason = "server error"
		}
	default:
		err.Retryable = false
		if err.Reason == "" {
			err.Reason = "unexpected status"
		}
	}
	return err
}

func classifyTransportError(operation string, err error) *OSVError {
	retryable := true
	var netErr net.Error
	if errors.As(err, &netErr) && !netErr.Temporary() && !netErr.Timeout() {
		retryable = false
	}
	return &OSVError{Operation: operation, Retryable: retryable, Reason: "transport error", Err: err}
}

func parseRetryAfter(value string) *time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		d := time.Duration(seconds) * time.Second
		return &d
	}
	if when, err := http.ParseTime(value); err == nil {
		d := time.Until(when)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}

func defaultOSVBackoff(ctx context.Context, attempt int, osvErr *OSVError) error {
	if osvErr != nil && osvErr.RetryAfter != nil {
		timer := time.NewTimer(*osvErr.RetryAfter)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	delay := time.Duration(attempt) * 250 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeOSVQuery(query OSVQuery) (OSVQuery, string, error) {
	normalized := OSVQuery{
		Ecosystem: strings.TrimSpace(query.Ecosystem),
		Name:      strings.TrimSpace(query.Name),
		Version:   strings.TrimSpace(query.Version),
		PURL:      strings.TrimSpace(query.PURL),
		Commit:    strings.ToLower(strings.TrimSpace(query.Commit)),
	}
	if normalized.Commit != "" {
		if normalized.Version != "" {
			return OSVQuery{}, "", &OSVError{Operation: "validate", Retryable: false, Reason: "commit query cannot include version"}
		}
		return normalized, "commit:" + normalized.Commit, nil
	}
	if normalized.PURL != "" {
		parsed, err := packageurl.FromString(normalized.PURL)
		if err != nil {
			return OSVQuery{}, "", &OSVError{Operation: "validate", Retryable: false, Reason: "invalid purl", Err: err}
		}
		normalized.PURL = parsed.ToString()
		if parsed.Version != "" && normalized.Version != "" {
			return OSVQuery{}, "", &OSVError{Operation: "validate", Retryable: false, Reason: "versioned purl cannot include top-level version"}
		}
		return normalized, "purl:" + normalized.PURL + ":" + normalized.Version, nil
	}
	normalized.Ecosystem = strings.TrimSpace(normalized.Ecosystem)
	normalized.Name = strings.TrimSpace(normalized.Name)
	if normalized.Ecosystem == "" || normalized.Name == "" {
		return OSVQuery{}, "", &OSVError{Operation: "validate", Retryable: false, Reason: "package ecosystem and name are required"}
	}
	return normalized, "package:" + strings.ToLower(normalized.Ecosystem) + ":" + strings.ToLower(normalized.Name) + ":" + normalized.Version, nil
}

type osvQueryPayload struct {
	Commit    string             `json:"commit,omitempty"`
	Version   string             `json:"version,omitempty"`
	Package   *osvPackagePayload `json:"package,omitempty"`
	PageToken string             `json:"page_token,omitempty"`
}

type osvPackagePayload struct {
	Name      string `json:"name,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
	PURL      string `json:"purl,omitempty"`
}

func queryPayload(query OSVQuery) osvQueryPayload {
	if query.Commit != "" {
		return osvQueryPayload{Commit: query.Commit}
	}
	if query.PURL != "" {
		return osvQueryPayload{Version: query.Version, Package: &osvPackagePayload{PURL: query.PURL}}
	}
	return osvQueryPayload{Version: query.Version, Package: &osvPackagePayload{Name: query.Name, Ecosystem: query.Ecosystem}}
}

func (c *OSVClient) getCached(key string) ([]Vulnerability, bool) {
	c.mu.RLock()
	entry, ok := c.cache[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	copyVulns := make([]Vulnerability, len(entry.vulns))
	copy(copyVulns, entry.vulns)
	return copyVulns, true
}

func (c *OSVClient) setCached(key string, vulns []Vulnerability) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copyVulns := make([]Vulnerability, len(vulns))
	copy(copyVulns, vulns)
	c.cache[key] = cacheEntry{vulns: copyVulns, expiresAt: time.Now().Add(c.cacheTTL)}
}

func (c *OSVClient) getDetailCached(id string) (*OSVVulnerabilityDetail, bool) {
	c.mu.RLock()
	entry, ok := c.detailCache[id]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	copyDetail := *entry.detail
	copyDetail.Aliases = append([]string(nil), entry.detail.Aliases...)
	copyDetail.References = append([]string(nil), entry.detail.References...)
	copyDetail.Raw = copyMap(entry.detail.Raw)
	return &copyDetail, true
}

func (c *OSVClient) setDetailCached(id string, detail *OSVVulnerabilityDetail) {
	if detail == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copyDetail := *detail
	copyDetail.Aliases = append([]string(nil), detail.Aliases...)
	copyDetail.References = append([]string(nil), detail.References...)
	copyDetail.Raw = copyMap(detail.Raw)
	c.detailCache[id] = detailCacheEntry{detail: &copyDetail, expiresAt: time.Now().Add(c.cacheTTL)}
}

func copyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type osvSeverity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvVulnerabilityPayload struct {
	ID               string   `json:"id"`
	Summary          string   `json:"summary"`
	Details          string   `json:"details"`
	Aliases          []string `json:"aliases"`
	Modified         string   `json:"modified"`
	Published        string   `json:"published"`
	Withdrawn        string   `json:"withdrawn"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Severity   []osvSeverity `json:"severity"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
}

func detailFromPayload(parsed osvVulnerabilityPayload) *OSVVulnerabilityDetail {
	raw := map[string]any{
		"id":        strings.TrimSpace(parsed.ID),
		"modified":  strings.TrimSpace(parsed.Modified),
		"published": strings.TrimSpace(parsed.Published),
	}
	if parsed.Withdrawn != "" {
		raw["withdrawn"] = strings.TrimSpace(parsed.Withdrawn)
	}
	detail := &OSVVulnerabilityDetail{
		ID:        strings.TrimSpace(parsed.ID),
		Summary:   strings.TrimSpace(parsed.Summary),
		Details:   strings.TrimSpace(parsed.Details),
		Aliases:   append([]string(nil), parsed.Aliases...),
		Severity:  normalizeSeverity(parsed.DatabaseSpecific.Severity, parsed.Severity),
		CVE:       firstCVE(append(parsed.Aliases, parsed.ID)),
		Modified:  strings.TrimSpace(parsed.Modified),
		Published: strings.TrimSpace(parsed.Published),
		Withdrawn: strings.TrimSpace(parsed.Withdrawn),
		Raw:       raw,
	}
	for _, ref := range parsed.References {
		if u := strings.TrimSpace(ref.URL); u != "" {
			detail.References = append(detail.References, u)
		}
	}
	return detail
}

func vulnerabilityFromDetail(detail *OSVVulnerabilityDetail, fallbackModified string) Vulnerability {
	modified := detail.Modified
	if modified == "" {
		modified = fallbackModified
	}
	return Vulnerability{
		ID:         detail.ID,
		Summary:    detail.Summary,
		Details:    detail.Details,
		Severity:   detail.Severity,
		CVE:        detail.CVE,
		Aliases:    append([]string(nil), detail.Aliases...),
		Modified:   modified,
		Withdrawn:  detail.Withdrawn,
		References: append([]string(nil), detail.References...),
	}
}

func firstCVE(values []string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(strings.ToUpper(trimmed), "CVE-") {
			return trimmed
		}
	}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeSeverity(databaseSeverity string, severities []osvSeverity) string {
	if s := strings.ToUpper(strings.TrimSpace(databaseSeverity)); s != "" {
		s = strings.ReplaceAll(s, "_", "")
		s = strings.ReplaceAll(s, "-", "")
		switch s {
		case "CRITICAL", "HIGH", "MEDIUM", "MODERATE", "LOW":
			if s == "MODERATE" {
				return "MEDIUM"
			}
			return s
		}
	}
	for _, sev := range severities {
		score := strings.TrimSpace(sev.Score)
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sev.Type)), "CVSS") && score != "" {
			parts := strings.Split(score, "/")
			if len(parts) >= 2 {
				if n, err := strconv.ParseFloat(parts[1], 64); err == nil {
					switch {
					case n >= 9.0:
						return "CRITICAL"
					case n >= 7.0:
						return "HIGH"
					case n >= 4.0:
						return "MEDIUM"
					default:
						return "LOW"
					}
				}
			}
		}
	}
	return "UNKNOWN"
}

type Vulnerability struct {
	ID         string
	Summary    string
	Details    string
	Severity   string // CRITICAL, HIGH, MEDIUM, LOW, UNKNOWN
	CVE        string
	Aliases    []string
	Modified   string
	Withdrawn  string
	References []string
}
