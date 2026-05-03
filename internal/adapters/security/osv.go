package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultOSVBaseURL = "https://api.osv.dev/v1"

type cacheEntry struct {
	vulns     []Vulnerability
	expiresAt time.Time
}

// OSVClient queries the OSV (Open Source Vulnerabilities) database.
type OSVClient struct {
	httpClient *http.Client
	baseURL    string

	mu       sync.RWMutex
	cacheTTL time.Duration
	cache    map[string]cacheEntry
}

func NewOSVClient() *OSVClient {
	return &OSVClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    defaultOSVBaseURL,
		cacheTTL:   5 * time.Minute,
		cache:      make(map[string]cacheEntry),
	}
}

// QueryPackage checks a package for known vulnerabilities.
func (c *OSVClient) QueryPackage(ctx context.Context, ecosystem string, name string, version string) ([]Vulnerability, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(ecosystem)) + ":" + strings.ToLower(strings.TrimSpace(name)) + ":" + strings.TrimSpace(version)
	if vulns, ok := c.getCached(cacheKey); ok {
		return vulns, nil
	}

	body := map[string]any{
		"package": map[string]string{
			"name":      name,
			"ecosystem": ecosystem,
		},
	}
	if strings.TrimSpace(version) != "" {
		body["version"] = strings.TrimSpace(version)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal osv request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/query", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create osv request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query osv: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retryAfter == "" {
			return nil, fmt.Errorf("osv rate limit exceeded (429)")
		}
		if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
			return nil, fmt.Errorf("osv rate limit exceeded (429), retry after %ds", seconds)
		}
		return nil, fmt.Errorf("osv rate limit exceeded (429), retry after %s", retryAfter)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("osv returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Vulns []struct {
			ID               string `json:"id"`
			Summary          string `json:"summary"`
			Aliases          []string `json:"aliases"`
			DatabaseSpecific struct {
				Severity string `json:"severity"`
			} `json:"database_specific"`
			Severity []struct {
				Type  string `json:"type"`
				Score string `json:"score"`
			} `json:"severity"`
		} `json:"vulns"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode osv response: %w", err)
	}

	vulns := make([]Vulnerability, 0, len(parsed.Vulns))
	for _, item := range parsed.Vulns {
		v := Vulnerability{
			ID:       strings.TrimSpace(item.ID),
			Summary:  strings.TrimSpace(item.Summary),
			Severity: normalizeSeverity(item.DatabaseSpecific.Severity, item.Severity),
		}
		for _, alias := range item.Aliases {
			if strings.HasPrefix(strings.ToUpper(alias), "CVE-") {
				v.CVE = alias
				break
			}
		}
		if v.CVE == "" {
			v.CVE = v.ID
		}
		vulns = append(vulns, v)
	}

	c.setCached(cacheKey, vulns)
	return vulns, nil
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

func normalizeSeverity(databaseSeverity string, severities []struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}) string {
	if s := strings.ToUpper(strings.TrimSpace(databaseSeverity)); s != "" {
		s = strings.ReplaceAll(s, "_", "")
		s = strings.ReplaceAll(s, "-", "")
		switch s {
		case "CRITICAL", "HIGH", "MEDIUM", "LOW":
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
	ID       string
	Summary  string
	Severity string // CRITICAL, HIGH, MEDIUM, LOW
	CVE      string
}
