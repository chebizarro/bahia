package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/httpclient"
)

const (
	defaultPowerDNSServerID = "localhost"
	maxPowerDNSResponseBody = 4 << 20
)

// PowerDNSConfig contains PowerDNS HTTP API connection settings.
type PowerDNSConfig struct {
	APIURL            string
	APIKey            string
	ServerID          string
	AllowInsecureHTTP bool
}

// PowerDNSBackend reconciles DNS zone snapshots through the PowerDNS HTTP API.
type PowerDNSBackend struct {
	apiURL   string
	apiKey   string
	serverID string
	http     powerDNSHTTP
}

type powerDNSHTTP interface {
	Do(req *http.Request) (*http.Response, error)
}

type powerDNSRRset struct {
	Name       string               `json:"name"`
	Type       string               `json:"type"`
	TTL        int                  `json:"ttl,omitempty"`
	ChangeType string               `json:"changetype,omitempty"`
	Records    []powerDNSRRsetEntry `json:"records,omitempty"`
}

type powerDNSRRsetEntry struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type powerDNSZoneResponse struct {
	RRsets []powerDNSRRset `json:"rrsets"`
}

type powerDNSPatchRequest struct {
	RRsets []powerDNSRRset `json:"rrsets"`
}

// NewPowerDNSBackend creates a PowerDNS HTTP API backend.
func NewPowerDNSBackend(cfg PowerDNSConfig) (*PowerDNSBackend, error) {
	return newPowerDNSBackend(cfg, httpclient.New(httpclient.DefaultTimeout))
}

func newPowerDNSBackend(cfg PowerDNSConfig, client powerDNSHTTP) (*PowerDNSBackend, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	if apiURL == "" {
		return nil, fmt.Errorf("PowerDNS API URL is required")
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("PowerDNS API URL must be a valid URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !cfg.AllowInsecureHTTP {
			return nil, fmt.Errorf("PowerDNS API URL must use HTTPS unless insecure HTTP is explicitly enabled")
		}
	default:
		return nil, fmt.Errorf("PowerDNS API URL must use HTTP or HTTPS")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("PowerDNS API key is required")
	}
	serverID := strings.TrimSpace(cfg.ServerID)
	if serverID == "" {
		serverID = defaultPowerDNSServerID
	}
	if client == nil {
		return nil, fmt.Errorf("PowerDNS HTTP client is required")
	}
	return &PowerDNSBackend{apiURL: apiURL, apiKey: apiKey, serverID: serverID, http: client}, nil
}

func (b *PowerDNSBackend) BackendType() domain.DNSBackendType {
	return domain.DNSBackendTypePowerDNS
}

func (b *PowerDNSBackend) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := b.do(ctx, http.MethodGet, b.serverPath(), nil)
	if err != nil {
		return fmt.Errorf("PowerDNS backend health check failed: %w", err)
	}
	defer resp.Body.Close()
	if err := expectPowerDNSSuccess(resp); err != nil {
		return fmt.Errorf("PowerDNS backend health check failed: %w", err)
	}
	return nil
}

func (b *PowerDNSBackend) ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	resp, err := b.do(ctx, http.MethodGet, b.zonePath(zone.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("list PowerDNS zone %q records: %w", zone.Name, err)
	}
	defer resp.Body.Close()
	if err := expectPowerDNSSuccess(resp); err != nil {
		return nil, fmt.Errorf("list PowerDNS zone %q records: %w", zone.Name, err)
	}
	body, err := readBoundedPowerDNSBody(resp.Body, maxPowerDNSResponseBody)
	if err != nil {
		return nil, fmt.Errorf("read PowerDNS zone %q response: %w", zone.Name, err)
	}
	var payload powerDNSZoneResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode PowerDNS zone %q response: %w", zone.Name, err)
	}
	return powerDNSRecordsFromRRsets(zone, payload.RRsets)
}

func (b *PowerDNSBackend) SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return err
	}
	actual, err := b.ListRecords(ctx, zone)
	if err != nil {
		return err
	}
	patch, err := buildPowerDNSPatch(zone, actual, records)
	if err != nil {
		return err
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode PowerDNS zone %q patch: %w", zone.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := b.do(ctx, http.MethodPatch, b.zonePath(zone.Name), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sync PowerDNS zone %q: %w", zone.Name, err)
	}
	defer resp.Body.Close()
	if err := expectPowerDNSSuccess(resp); err != nil {
		return fmt.Errorf("sync PowerDNS zone %q: %w", zone.Name, err)
	}
	return nil
}

func (b *PowerDNSBackend) do(ctx context.Context, method string, path string, body io.Reader) (*http.Response, error) {
	if b == nil || b.http == nil {
		return nil, fmt.Errorf("PowerDNS HTTP client is required")
	}
	request, err := http.NewRequestWithContext(ctx, method, b.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-API-Key", b.apiKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	return b.http.Do(request)
}

func (b *PowerDNSBackend) serverPath() string {
	return "/api/v1/servers/" + url.PathEscape(b.serverID)
}

func (b *PowerDNSBackend) zonePath(zoneName string) string {
	return b.serverPath() + "/zones/" + url.PathEscape(powerDNSZoneName(zoneName))
}

func buildPowerDNSPatch(zone domain.DNSZone, actual []domain.DNSRecord, desired []domain.DNSRecord) (powerDNSPatchRequest, error) {
	desiredRRsets, desiredKeys, err := powerDNSReplaceRRsets(zone, desired)
	if err != nil {
		return powerDNSPatchRequest{}, err
	}
	actualRRsets, _, err := powerDNSReplaceRRsets(zone, actual)
	if err != nil {
		return powerDNSPatchRequest{}, err
	}
	deleteRRsets := make([]powerDNSRRset, 0)
	for _, rrset := range actualRRsets {
		key := powerDNSRRsetKey(rrset.Name, rrset.Type)
		if _, ok := desiredKeys[key]; ok {
			continue
		}
		deleteRRsets = append(deleteRRsets, powerDNSRRset{Name: rrset.Name, Type: rrset.Type, ChangeType: "DELETE"})
	}
	patchRRsets := append([]powerDNSRRset{}, desiredRRsets...)
	patchRRsets = append(patchRRsets, deleteRRsets...)
	return powerDNSPatchRequest{RRsets: patchRRsets}, nil
}

func powerDNSReplaceRRsets(zone domain.DNSZone, records []domain.DNSRecord) ([]powerDNSRRset, map[string]struct{}, error) {
	grouped := make(map[string]*powerDNSRRset)
	keys := make([]string, 0)
	for _, record := range sortedRecords(records) {
		rrset, err := powerDNSRRsetFromRecord(zone, record)
		if err != nil {
			return nil, nil, err
		}
		key := powerDNSRRsetKey(rrset.Name, rrset.Type)
		existing, ok := grouped[key]
		if !ok {
			copy := rrset
			copy.Records = nil
			grouped[key] = &copy
			keys = append(keys, key)
			existing = &copy
		}
		existing.Records = append(existing.Records, rrset.Records...)
		grouped[key] = existing
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(keys))
	rrsets := make([]powerDNSRRset, 0, len(keys))
	for _, key := range keys {
		rrset := grouped[key]
		sort.Slice(rrset.Records, func(i, j int) bool { return rrset.Records[i].Content < rrset.Records[j].Content })
		rrsets = append(rrsets, *rrset)
		seen[key] = struct{}{}
	}
	return rrsets, seen, nil
}

func powerDNSRRsetFromRecord(zone domain.DNSZone, record domain.DNSRecord) (powerDNSRRset, error) {
	record.Zone = strings.TrimSpace(record.Zone)
	if record.Zone == "" {
		record.Zone = zone.Name
	}
	if record.Zone != zone.Name {
		return powerDNSRRset{}, fmt.Errorf("DNS record %q zone %q does not match sync zone %q", record.FQDN, record.Zone, zone.Name)
	}
	fqdn := strings.TrimSpace(record.FQDN)
	if fqdn == "" {
		fqdn = fqdnFromRecordName(record.Name, zone.Name)
	}
	fqdn = strings.Trim(strings.ToLower(fqdn), ".")
	if fqdn == "" {
		return powerDNSRRset{}, fmt.Errorf("DNS record FQDN is required")
	}
	if !strings.HasSuffix(fqdn, zone.Name) || (fqdn != zone.Name && !strings.HasSuffix(fqdn, "."+zone.Name)) {
		return powerDNSRRset{}, fmt.Errorf("DNS record FQDN %q is outside zone %q", fqdn, zone.Name)
	}
	if !record.Type.IsValid() {
		return powerDNSRRset{}, fmt.Errorf("DNS record %q type %q is not valid", fqdn, record.Type)
	}
	value := strings.TrimSpace(record.Value)
	if value == "" {
		return powerDNSRRset{}, fmt.Errorf("DNS record %q value is required", fqdn)
	}
	ttl := record.TTL
	if ttl <= 0 {
		ttl = zone.TTL
	}
	return powerDNSRRset{
		Name:       powerDNSFQDN(fqdn),
		Type:       string(record.Type),
		TTL:        ttl,
		ChangeType: "REPLACE",
		Records:    []powerDNSRRsetEntry{{Content: value, Disabled: false}},
	}, nil
}

func powerDNSRecordsFromRRsets(zone domain.DNSZone, rrsets []powerDNSRRset) ([]domain.DNSRecord, error) {
	records := make([]domain.DNSRecord, 0)
	for _, rrset := range rrsets {
		recordType := domain.DNSRecordType(strings.ToUpper(strings.TrimSpace(rrset.Type)))
		if !recordType.IsValid() {
			continue
		}
		fqdn := strings.Trim(strings.ToLower(strings.TrimSpace(rrset.Name)), ".")
		if fqdn == "" {
			return nil, fmt.Errorf("PowerDNS RRset name is required")
		}
		if !strings.HasSuffix(fqdn, zone.Name) || (fqdn != zone.Name && !strings.HasSuffix(fqdn, "."+zone.Name)) {
			continue
		}
		ttl := rrset.TTL
		if ttl <= 0 {
			ttl = zone.TTL
		}
		for _, item := range rrset.Records {
			if item.Disabled {
				continue
			}
			content := strings.TrimSpace(item.Content)
			if content == "" {
				return nil, fmt.Errorf("PowerDNS RRset %q %s contains an empty record content", rrset.Name, rrset.Type)
			}
			records = append(records, domain.DNSRecord{
				Zone:  zone.Name,
				Name:  relativeDNSName(fqdn, zone.Name),
				FQDN:  fqdn,
				Type:  recordType,
				Value: strings.Trim(content, "."),
				TTL:   ttl,
			})
		}
	}
	return sortedRecords(records), nil
}

func readBoundedPowerDNSBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return data, nil
}

func expectPowerDNSSuccess(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("PowerDNS response is required")
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("PowerDNS API returned HTTP %d: %s", resp.StatusCode, message)
}

func powerDNSZoneName(zoneName string) string {
	return powerDNSFQDN(zoneName)
}

func powerDNSFQDN(name string) string {
	name = strings.Trim(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" {
		return "."
	}
	return name + "."
}

func powerDNSRRsetKey(name string, recordType string) string {
	return powerDNSFQDN(name) + "\x00" + strings.ToUpper(strings.TrimSpace(recordType))
}

var _ Backend = (*PowerDNSBackend)(nil)
