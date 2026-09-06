package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/security"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/strutil"
	"go.uber.org/zap"
)

var DefaultTrustedSources = map[string][]string{
	"apt":   {"debian", "ubuntu"},
	"pip":   {"pypi"},
	"npm":   {"npmjs"},
	"cargo": {"crates.io"},
	"bun":   {"bun.sh"},
}

var PopularPackages = map[string][]string{
	"npm": {"lodash", "express", "react", "axios", "moment", "webpack"},
	"pip": {"requests", "numpy", "pandas", "flask", "django", "pytest"},
}

type ToolSecurityService struct {
	denylistRepo repository.ToolProvisioningRepository
	osvClient    *security.OSVClient
	logger       *zap.Logger
	config       ToolSecurityConfig
	httpClient   *http.Client
}

type ToolSecurityConfig struct {
	TrustedSources        map[string][]string // manager -> trusted sources
	MaxCriticalVulns      int
	MaxHighVulns          int
	MinPackageAgeDays     int
	MinDownloads          int
	TyposquatCheckEnabled bool
}

func NewToolSecurityService(repo repository.ToolProvisioningRepository, osvClient *security.OSVClient, logger *zap.Logger, cfg ToolSecurityConfig) *ToolSecurityService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if osvClient == nil {
		osvClient = security.NewOSVClient()
	}
	if len(cfg.TrustedSources) == 0 {
		cfg.TrustedSources = DefaultTrustedSources
	}
	if cfg.MaxCriticalVulns < 0 {
		cfg.MaxCriticalVulns = 0
	}
	if cfg.MaxHighVulns < 0 {
		cfg.MaxHighVulns = 0
	}

	return &ToolSecurityService{
		denylistRepo: repo,
		osvClient:    osvClient,
		logger:       logger,
		config:       cfg,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// ValidateTools validates a list of tool requests.
// Returns: resolved tools, security scan results, approval flags, error
func (s *ToolSecurityService) ValidateTools(ctx context.Context, tools []domain.ToolRequest) ([]domain.ResolvedTool, *domain.SecurityScanResult, []string, error) {
	entries, err := s.CheckDenylist(ctx, tools)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(entries) > 0 {
		blocked := make([]string, 0, len(entries))
		for _, e := range entries {
			blocked = append(blocked, fmt.Sprintf("%s:%s (%s)", e.Manager, e.PackageName, e.Reason))
		}
		return nil, nil, nil, fmt.Errorf("tool request blocked by denylist: %s", strings.Join(blocked, ", "))
	}

	trusted, untrusted := s.ValidateTrust(tools)
	resolved := make([]domain.ResolvedTool, 0, len(tools))
	for _, t := range trusted {
		resolved = append(resolved, resolveTool(t, true))
	}
	for _, t := range untrusted {
		resolved = append(resolved, resolveTool(t, false))
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].Manager == resolved[j].Manager {
			return resolved[i].Name < resolved[j].Name
		}
		return resolved[i].Manager < resolved[j].Manager
	})

	scan, err := s.ScanForVulnerabilities(ctx, resolved)
	if err != nil {
		return nil, nil, nil, err
	}

	flags, err := s.CheckHeuristics(ctx, resolved)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(untrusted) > 0 {
		for _, tool := range untrusted {
			flags = append(flags, fmt.Sprintf("untrusted source for %s:%s", strings.ToLower(tool.Manager), strings.ToLower(tool.Name)))
		}
	}
	if scan.CriticalCount > s.config.MaxCriticalVulns {
		flags = append(flags, fmt.Sprintf("critical vulnerabilities (%d) exceed allowed threshold (%d)", scan.CriticalCount, s.config.MaxCriticalVulns))
	}
	if scan.HighCount > s.config.MaxHighVulns {
		flags = append(flags, fmt.Sprintf("high vulnerabilities (%d) exceed allowed threshold (%d)", scan.HighCount, s.config.MaxHighVulns))
	}

	return resolved, scan, dedupeStrings(flags), nil
}

// CheckDenylist checks if any tools are on the denylist.
func (s *ToolSecurityService) CheckDenylist(ctx context.Context, tools []domain.ToolRequest) ([]domain.ToolDenylistEntry, error) {
	if s.denylistRepo == nil {
		return nil, nil
	}
	found := make([]domain.ToolDenylistEntry, 0)
	for _, tool := range tools {
		isBlocked, err := s.denylistRepo.IsDenylisted(ctx, strings.TrimSpace(tool.Name), strings.ToLower(strings.TrimSpace(tool.Manager)))
		if err != nil {
			return nil, fmt.Errorf("checking denylist for %s:%s: %w", tool.Manager, tool.Name, err)
		}
		if isBlocked {
			entries, err := s.denylistRepo.ListDenylist(ctx)
			if err != nil {
				return nil, fmt.Errorf("listing denylist entries: %w", err)
			}
			for _, entry := range entries {
				if strings.EqualFold(entry.PackageName, tool.Name) && strings.EqualFold(entry.Manager, tool.Manager) {
					found = append(found, entry)
					break
				}
			}
		}
	}
	return found, nil
}

// ValidateTrust checks if tools come from trusted sources.
func (s *ToolSecurityService) ValidateTrust(tools []domain.ToolRequest) (trusted []domain.ToolRequest, untrusted []domain.ToolRequest) {
	for _, tool := range tools {
		manager := strings.ToLower(strings.TrimSpace(tool.Manager))
		source := inferSourceForManager(manager)
		allowed := s.config.TrustedSources[manager]
		if containsFold(allowed, source) {
			trusted = append(trusted, tool)
		} else {
			untrusted = append(untrusted, tool)
		}
	}
	return trusted, untrusted
}

// ScanForVulnerabilities checks CVE databases.
func (s *ToolSecurityService) ScanForVulnerabilities(ctx context.Context, tools []domain.ResolvedTool) (*domain.SecurityScanResult, error) {
	result := &domain.SecurityScanResult{}
	for _, tool := range tools {
		ecosystem := osvEcosystem(tool.Manager)
		if ecosystem == "" {
			continue
		}
		vulns, err := s.osvClient.QueryPackage(ctx, ecosystem, tool.Name, tool.Version)
		if err != nil {
			return nil, fmt.Errorf("vulnerability scan failed for %s:%s: %w", tool.Manager, tool.Name, err)
		}
		for _, v := range vulns {
			sev := strings.ToUpper(strings.TrimSpace(v.Severity))
			switch sev {
			case "CRITICAL":
				result.CriticalCount++
			case "HIGH":
				result.HighCount++
			case "MEDIUM":
				result.MediumCount++
			default:
				result.LowCount++
			}
			result.Findings = append(result.Findings, domain.SecurityFinding{
				PackageName: tool.Name,
				CVE:         v.CVE,
				Severity:    sev,
				Description: v.Summary,
			})
		}
	}
	return result, nil
}

// CheckHeuristics applies heuristic rules (age, downloads, typosquat).
func (s *ToolSecurityService) CheckHeuristics(ctx context.Context, tools []domain.ResolvedTool) (flags []string, err error) {
	for _, tool := range tools {
		ageDays, ageErr := s.lookupPackageAgeDays(ctx, tool)
		if ageErr != nil {
			s.logger.Warn("package age lookup failed", zap.String("manager", tool.Manager), zap.String("name", tool.Name), zap.Error(ageErr))
			flags = append(flags, fmt.Sprintf("could not verify package age for %s:%s", tool.Manager, tool.Name))
		} else if s.config.MinPackageAgeDays > 0 && ageDays >= 0 && ageDays < s.config.MinPackageAgeDays {
			flags = append(flags, fmt.Sprintf("package %s:%s is %d days old (< %d)", tool.Manager, tool.Name, ageDays, s.config.MinPackageAgeDays))
		}

		downloads, dErr := s.lookupPackageDownloads(ctx, tool)
		if dErr != nil {
			s.logger.Warn("package download lookup failed", zap.String("manager", tool.Manager), zap.String("name", tool.Name), zap.Error(dErr))
			flags = append(flags, fmt.Sprintf("could not verify downloads for %s:%s", tool.Manager, tool.Name))
		} else if s.config.MinDownloads > 0 && downloads >= 0 && downloads < s.config.MinDownloads {
			flags = append(flags, fmt.Sprintf("package %s:%s has %d downloads (< %d)", tool.Manager, tool.Name, downloads, s.config.MinDownloads))
		}

		if s.config.TyposquatCheckEnabled {
			if candidate := typosquatCandidate(tool); candidate != "" {
				flags = append(flags, fmt.Sprintf("package %s:%s may typosquat %s", tool.Manager, tool.Name, candidate))
			}
		}
	}
	return dedupeStrings(flags), nil
}

func resolveTool(req domain.ToolRequest, trusted bool) domain.ResolvedTool {
	manager := strings.ToLower(strings.TrimSpace(req.Manager))
	source := inferSourceForManager(manager)
	if !trusted {
		source = "unknown"
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = "latest"
	}
	return domain.ResolvedTool{
		Name:    strings.TrimSpace(req.Name),
		Version: version,
		Manager: manager,
		Source:  source,
	}
}

func inferSourceForManager(manager string) string {
	switch strings.ToLower(manager) {
	case "apt":
		return "debian"
	case "pip":
		return "pypi"
	case "npm":
		return "npmjs"
	case "cargo":
		return "crates.io"
	case "bun":
		return "bun.sh"
	default:
		return "unknown"
	}
}

func osvEcosystem(manager string) string {
	switch strings.ToLower(strings.TrimSpace(manager)) {
	case "pip":
		return "PyPI"
	case "npm":
		return "npm"
	case "cargo":
		return "crates.io"
	case "go":
		return "Go"
	default:
		return ""
	}
}

func (s *ToolSecurityService) lookupPackageAgeDays(ctx context.Context, tool domain.ResolvedTool) (int, error) {
	pubDate, err := s.lookupFirstPublishDate(ctx, tool)
	if err != nil {
		return -1, err
	}
	if pubDate.IsZero() {
		return -1, fmt.Errorf("publish date unavailable")
	}
	return int(time.Since(pubDate).Hours() / 24), nil
}

func (s *ToolSecurityService) lookupPackageDownloads(ctx context.Context, tool domain.ResolvedTool) (int, error) {
	switch strings.ToLower(tool.Manager) {
	case "npm":
		u := "https://api.npmjs.org/downloads/point/last-month/" + url.PathEscape(tool.Name)
		var payload struct {
			Downloads int `json:"downloads"`
		}
		if err := s.getJSON(ctx, u, &payload); err != nil {
			return -1, err
		}
		return payload.Downloads, nil
	case "pip":
		u := "https://pypistats.org/api/packages/" + url.PathEscape(tool.Name) + "/recent"
		var payload struct {
			Data struct {
				LastMonth int `json:"last_month"`
			} `json:"data"`
		}
		if err := s.getJSON(ctx, u, &payload); err != nil {
			return -1, err
		}
		return payload.Data.LastMonth, nil
	default:
		return -1, fmt.Errorf("downloads lookup unsupported for manager %s", tool.Manager)
	}
}

func (s *ToolSecurityService) lookupFirstPublishDate(ctx context.Context, tool domain.ResolvedTool) (time.Time, error) {
	switch strings.ToLower(tool.Manager) {
	case "npm":
		u := "https://registry.npmjs.org/" + url.PathEscape(tool.Name)
		var payload struct {
			Time map[string]string `json:"time"`
		}
		if err := s.getJSON(ctx, u, &payload); err != nil {
			return time.Time{}, err
		}
		if created := strings.TrimSpace(payload.Time["created"]); created != "" {
			parsed, err := time.Parse(time.RFC3339, created)
			if err == nil {
				return parsed, nil
			}
		}
		return time.Time{}, fmt.Errorf("npm publish date missing for %s", tool.Name)
	case "pip":
		u := "https://pypi.org/pypi/" + url.PathEscape(tool.Name) + "/json"
		var payload struct {
			Releases map[string][]struct {
				UploadTimeISO8601 string `json:"upload_time_iso_8601"`
			} `json:"releases"`
		}
		if err := s.getJSON(ctx, u, &payload); err != nil {
			return time.Time{}, err
		}
		var earliest time.Time
		for _, entries := range payload.Releases {
			for _, e := range entries {
				t := strings.TrimSpace(e.UploadTimeISO8601)
				if t == "" {
					continue
				}
				parsed, parseErr := time.Parse(time.RFC3339, t)
				if parseErr != nil {
					continue
				}
				if earliest.IsZero() || parsed.Before(earliest) {
					earliest = parsed
				}
			}
		}
		if earliest.IsZero() {
			return time.Time{}, fmt.Errorf("pypi publish date missing for %s", tool.Name)
		}
		return earliest, nil
	default:
		return time.Time{}, fmt.Errorf("age lookup unsupported for manager %s", tool.Manager)
	}
}

func typosquatCandidate(tool domain.ResolvedTool) string {
	manager := strings.ToLower(strings.TrimSpace(tool.Manager))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	for _, popular := range PopularPackages[manager] {
		p := strings.ToLower(popular)
		if name == p {
			continue
		}
		d := strutil.LevenshteinDistance(name, p)
		if d == 1 {
			return popular
		}
	}
	return ""
}

func (s *ToolSecurityService) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create request %s: %w", rawURL, err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("query %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retryAfter == "" {
			retryAfter = "unknown"
		}
		return fmt.Errorf("rate limited by %s (retry-after: %s)", rawURL, retryAfter)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("query %s returned %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", rawURL, err)
	}
	return nil
}

func containsFold(values []string, wanted string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		n := strings.TrimSpace(item)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}
