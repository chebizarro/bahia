package dns

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type mockPowerDNSHTTP struct {
	requests  []*http.Request
	bodies    []string
	responses []*http.Response
	err       error
}

func (m *mockPowerDNSHTTP) Do(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		m.bodies = append(m.bodies, string(body))
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.responses) == 0 {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func powerDNSTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
}

func powerDNSTestZone() domain.DNSZone {
	return domain.DNSZone{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityInternal, BackendRef: "powerdns-main", TTL: 300}
}

func TestNewPowerDNSBackendUsesBoundedTLSSafeClient(t *testing.T) {
	backend, err := NewPowerDNSBackend(PowerDNSConfig{APIURL: "https://powerdns.example", APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewPowerDNSBackend returned error: %v", err)
	}
	client, ok := backend.http.(*http.Client)
	if !ok {
		t.Fatalf("HTTP client type = %T, want *http.Client", backend.http)
	}
	if client.Timeout <= 0 {
		t.Fatalf("HTTP client timeout = %s, want positive timeout", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatalf("HTTP transport = %#v, want TLS-configured *http.Transport", client.Transport)
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS minimum = %d, want TLS 1.2 or newer", transport.TLSClientConfig.MinVersion)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS certificate verification must not be disabled")
	}
}

func TestNewPowerDNSBackendRejectsCleartextByDefault(t *testing.T) {
	_, err := NewPowerDNSBackend(PowerDNSConfig{APIURL: "http://powerdns.example", APIKey: "secret"})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("error = %v, want HTTPS requirement", err)
	}
}

func TestPowerDNSRRsetSerializationAndDeserialization(t *testing.T) {
	zone := powerDNSTestZone()
	rrset, err := powerDNSRRsetFromRecord(zone, domain.DNSRecord{Zone: zone.Name, Name: "drydock-review", Type: domain.DNSRecordTypeA, Value: "10.0.1.44", TTL: 120})
	if err != nil {
		t.Fatalf("powerDNSRRsetFromRecord returned error: %v", err)
	}
	data, err := json.Marshal(rrset)
	if err != nil {
		t.Fatalf("marshal rrset: %v", err)
	}
	var decoded powerDNSRRset
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal rrset: %v", err)
	}
	if decoded.Name != "drydock-review.prod.cascadia." || decoded.Type != "A" || decoded.TTL != 120 || decoded.ChangeType != "REPLACE" {
		t.Fatalf("unexpected rrset metadata: %+v", decoded)
	}
	if len(decoded.Records) != 1 || decoded.Records[0].Content != "10.0.1.44" || decoded.Records[0].Disabled {
		t.Fatalf("unexpected rrset records: %+v", decoded.Records)
	}
	records, err := powerDNSRecordsFromRRsets(zone, []powerDNSRRset{decoded})
	if err != nil {
		t.Fatalf("powerDNSRecordsFromRRsets returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].FQDN != "drydock-review.prod.cascadia" || records[0].Name != "drydock-review" || records[0].Value != "10.0.1.44" || records[0].TTL != 120 {
		t.Fatalf("unexpected DNS record: %+v", records[0])
	}
}

func TestPowerDNSSyncZoneBuildsReplaceAndDeleteRRsets(t *testing.T) {
	zone := powerDNSTestZone()
	client := &mockPowerDNSHTTP{responses: []*http.Response{
		powerDNSTestResponse(http.StatusOK, `{"rrsets":[{"name":"old.prod.cascadia.","type":"A","ttl":300,"records":[{"content":"10.0.1.10","disabled":false}]},{"name":"keep.prod.cascadia.","type":"A","ttl":300,"records":[{"content":"10.0.1.11","disabled":false}]}]}`),
		powerDNSTestResponse(http.StatusNoContent, ""),
	}}
	backend, err := newPowerDNSBackend(PowerDNSConfig{APIURL: "http://powerdns.local:8081", APIKey: "secret", AllowInsecureHTTP: true}, client)
	if err != nil {
		t.Fatalf("newPowerDNSBackend returned error: %v", err)
	}
	desired := []domain.DNSRecord{
		{Zone: zone.Name, Name: "keep", Type: domain.DNSRecordTypeA, Value: "10.0.1.11", TTL: 300},
		{Zone: zone.Name, Name: "new", Type: domain.DNSRecordTypeA, Value: "10.0.1.12", TTL: 300},
	}
	if err := backend.SyncZone(context.Background(), zone, desired); err != nil {
		t.Fatalf("SyncZone returned error: %v", err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("expected GET and PATCH requests, got %d", len(client.requests))
	}
	if client.requests[0].Method != http.MethodGet || client.requests[1].Method != http.MethodPatch {
		t.Fatalf("unexpected methods: %s %s", client.requests[0].Method, client.requests[1].Method)
	}
	if got := client.requests[1].Header.Get("X-API-Key"); got != "secret" {
		t.Fatalf("expected API key header, got %q", got)
	}
	var patch powerDNSPatchRequest
	if err := json.Unmarshal([]byte(client.bodies[0]), &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	byKey := map[string]powerDNSRRset{}
	for _, rrset := range patch.RRsets {
		byKey[rrset.Name+"|"+rrset.Type] = rrset
	}
	if rrset := byKey["keep.prod.cascadia.|A"]; rrset.ChangeType != "REPLACE" || len(rrset.Records) != 1 || rrset.Records[0].Content != "10.0.1.11" {
		t.Fatalf("unexpected keep rrset: %+v", rrset)
	}
	if rrset := byKey["new.prod.cascadia.|A"]; rrset.ChangeType != "REPLACE" || len(rrset.Records) != 1 || rrset.Records[0].Content != "10.0.1.12" {
		t.Fatalf("unexpected new rrset: %+v", rrset)
	}
	if rrset := byKey["old.prod.cascadia.|A"]; rrset.ChangeType != "DELETE" || len(rrset.Records) != 0 {
		t.Fatalf("unexpected delete rrset: %+v", rrset)
	}
}

func TestPowerDNSListRecordsParsesResponseFormat(t *testing.T) {
	zone := powerDNSTestZone()
	client := &mockPowerDNSHTTP{responses: []*http.Response{
		powerDNSTestResponse(http.StatusOK, `{"rrsets":[{"name":"drydock-review.prod.cascadia.","type":"A","ttl":300,"records":[{"content":"10.0.1.44","disabled":false},{"content":"10.0.1.45","disabled":true}]},{"name":"api.prod.cascadia.","type":"CNAME","ttl":60,"records":[{"content":"drydock-review.prod.cascadia.","disabled":false}]},{"name":"ignored.prod.cascadia.","type":"MX","ttl":300,"records":[{"content":"mail.prod.cascadia.","disabled":false}]}]}`),
	}}
	backend, err := newPowerDNSBackend(PowerDNSConfig{APIURL: "http://powerdns.local:8081", APIKey: "secret", AllowInsecureHTTP: true, ServerID: "primary"}, client)
	if err != nil {
		t.Fatalf("newPowerDNSBackend returned error: %v", err)
	}
	records, err := backend.ListRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 parsed records, got %d: %+v", len(records), records)
	}
	if records[0].FQDN != "api.prod.cascadia" || records[0].Type != domain.DNSRecordTypeCNAME || records[0].Value != "drydock-review.prod.cascadia" || records[0].TTL != 60 {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if records[1].FQDN != "drydock-review.prod.cascadia" || records[1].Type != domain.DNSRecordTypeA || records[1].Value != "10.0.1.44" || records[1].TTL != 300 {
		t.Fatalf("unexpected second record: %+v", records[1])
	}
	if got := client.requests[0].URL.Path; got != "/api/v1/servers/primary/zones/prod.cascadia." {
		t.Fatalf("unexpected request path: %s", got)
	}
}

func TestPowerDNSHealthSuccessAndFailure(t *testing.T) {
	successClient := &mockPowerDNSHTTP{responses: []*http.Response{powerDNSTestResponse(http.StatusOK, `{"id":"localhost"}`)}}
	backend, err := newPowerDNSBackend(PowerDNSConfig{APIURL: "http://powerdns.local:8081", APIKey: "secret", AllowInsecureHTTP: true}, successClient)
	if err != nil {
		t.Fatalf("newPowerDNSBackend returned error: %v", err)
	}
	if err := backend.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error on success: %v", err)
	}
	if got := successClient.requests[0].URL.Path; got != "/api/v1/servers/localhost" {
		t.Fatalf("unexpected health path: %s", got)
	}

	failureClient := &mockPowerDNSHTTP{responses: []*http.Response{powerDNSTestResponse(http.StatusUnauthorized, `{"error":"unauthorized"}`)}}
	backend, err = newPowerDNSBackend(PowerDNSConfig{APIURL: "http://powerdns.local:8081", APIKey: "secret", AllowInsecureHTTP: true}, failureClient)
	if err != nil {
		t.Fatalf("newPowerDNSBackend returned error: %v", err)
	}
	if err := backend.Health(context.Background()); err == nil {
		t.Fatalf("expected Health error on non-2xx response")
	}
}
