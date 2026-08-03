package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func cloudflareTestPlan() *domain.DesiredPublicRoutePlan {
	serviceID, environmentID, unitID := uuid.New(), uuid.New(), uuid.New()
	hostname := "arcana.example.com"
	return &domain.DesiredPublicRoutePlan{
		SchemaVersion: domain.PublicRouteSchemaVersion,
		ServiceID:     serviceID, EnvironmentID: environmentID, DeploymentUnitID: unitID,
		Hostname: hostname, Zone: "example.com", BackendRef: "edge",
		Provider: "cloudflare_tunnel", ProviderConfigHash: "sha256:config",
		DNS: domain.DesiredPublicRouteDNS{
			Type: "CNAME", Name: hostname, Value: "tunnel.example.net", TTL: 1,
			Proxied: true, SourceCoordinate: domain.PublicRouteCoordinate(serviceID, environmentID, unitID),
		},
		Tunnel: domain.DesiredPublicRouteTunnel{TunnelRef: "tunnel-1", Hostname: hostname, OriginURL: "http://edge-01.internal:8080"},
		Proxy: domain.DesiredPublicRouteProxy{
			HostMatch: hostname, UpstreamScheme: "http", UpstreamHost: "edge-01.internal",
			UpstreamPort: 8080, HealthPath: "/healthz",
		},
		TLS:        domain.DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"},
		Operations: []domain.DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply"}, {Order: 2, Resource: "edge", Action: "upsert"}},
		Rollback:   []domain.DesiredPublicRouteChange{{Order: 1, Resource: "edge", Action: "restore"}},
	}
}

func writeCFResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestUpsertIngressKeepsCatchAllLast(t *testing.T) {
	got := upsertIngress([]map[string]any{
		{"hostname": "one.example.com", "service": "http://one:80"},
		{"service": "http_status:503"},
	}, "arcana.example.com", "http://edge:8080")
	if len(got) != 3 || got[1]["hostname"] != "arcana.example.com" || got[2]["service"] != "http_status:503" {
		t.Fatalf("unexpected ingress ordering: %#v", got)
	}

	got = upsertIngress([]map[string]any{{"hostname": "arcana.example.com", "service": "http://old:8080"}}, "arcana.example.com", "http://edge:8080")
	if len(got) != 2 || got[0]["service"] != "http://edge:8080" || got[1]["service"] != "http_status:404" {
		t.Fatalf("missing generated catch-all: %#v", got)
	}
}

func TestCloudflareCheckRejectsUnmanagedDNSCollision(t *testing.T) {
	plan := cloudflareTestPlan()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/dns_records"):
			writeCFResult(t, w, []cfDNSRecord{{ID: "foreign", Type: "CNAME", Name: plan.Hostname, Content: plan.DNS.Value, Proxied: true}})
		default:
			t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIBaseURL: server.URL, APIToken: "secret-token", AccountID: "account",
		TunnelID: "tunnel-1", ZoneIDs: map[string]string{"example.com": "zone"},
	}, server.Client())
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	err = backend.Check(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "unmanaged DNS") {
		t.Fatalf("Check error = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("provider token leaked in error")
	}
}

func TestCloudflareCheckAllowsOwnedRouteOriginUpdate(t *testing.T) {
	plan := cloudflareTestPlan()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/dns_records"):
			writeCFResult(t, w, []cfDNSRecord{{ID: "owned", Comment: plan.DNS.SourceCoordinate}})
		case strings.Contains(r.URL.Path, "/configurations"):
			result := cfTunnelResult{Config: map[string]any{"ingress": []map[string]any{{"hostname": plan.Hostname, "service": "http://old-origin:8080"}, {"service": "http_status:404"}}}}
			writeCFResult(t, w, result)
		default:
			t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIBaseURL: server.URL, APIToken: "token", AccountID: "account",
		TunnelID: "tunnel-1", ZoneIDs: map[string]string{"example.com": "zone"},
	}, server.Client())
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	if err := backend.Check(context.Background(), plan); err != nil {
		t.Fatalf("owned route update rejected: %v", err)
	}
}

func TestCloudflareApplyCompensatesFailedHTTPSVerification(t *testing.T) {
	plan := cloudflareTestPlan()
	initialIngress := []map[string]any{{"hostname": "existing.example.com", "service": "http://existing:80"}, {"service": "http_status:404"}}
	currentIngress := initialIngress
	var currentDNS []cfDNSRecord
	var ingressWrites, dnsCreates, dnsDeletes int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/configurations") && r.Method == http.MethodGet:
			result := cfTunnelResult{Config: map[string]any{"ingress": currentIngress, "warp-routing": map[string]any{"enabled": true}}}
			writeCFResult(t, w, result)
		case strings.Contains(r.URL.Path, "/configurations") && r.Method == http.MethodPut:
			var body struct {
				Config struct {
					Ingress     []map[string]any `json:"ingress"`
					WarpRouting map[string]any   `json:"warp-routing"`
				} `json:"config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode ingress: %v", err)
			}
			if body.Config.WarpRouting == nil {
				t.Fatal("unrelated remote Tunnel configuration was not preserved")
			}
			currentIngress = body.Config.Ingress
			ingressWrites++
			writeCFResult(t, w, map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			writeCFResult(t, w, currentDNS)
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodPost:
			var record cfDNSRecord
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Fatalf("decode DNS: %v", err)
			}
			record.ID = "created"
			currentDNS = []cfDNSRecord{record}
			dnsCreates++
			writeCFResult(t, w, record)
		case strings.Contains(r.URL.Path, "/dns_records/") && r.Method == http.MethodDelete:
			currentDNS = nil
			dnsDeletes++
			writeCFResult(t, w, map[string]any{})
		default:
			t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIBaseURL: server.URL, APIToken: "token", AccountID: "account",
		TunnelID: "tunnel-1", ZoneIDs: map[string]string{"example.com": "zone"},
	}, server.Client())
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	backend.verifyClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: make(http.Header)}, nil
	})}

	err = backend.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "previous public route restored") {
		t.Fatalf("Apply error = %v", err)
	}
	if ingressWrites != 2 || dnsCreates != 1 || dnsDeletes != 1 || len(currentDNS) != 0 {
		t.Fatalf("compensation counts/state = ingress:%d create:%d delete:%d DNS:%#v", ingressWrites, dnsCreates, dnsDeletes, currentDNS)
	}
	if len(currentIngress) != len(initialIngress) || currentIngress[0]["hostname"] != initialIngress[0]["hostname"] {
		t.Fatalf("tunnel ingress was not restored: %#v", currentIngress)
	}
}
