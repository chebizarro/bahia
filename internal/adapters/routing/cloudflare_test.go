package routing

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func startTestDNSResponder(t *testing.T, response func(qtype uint16, aAttempt int) (rcode byte, answer bool)) (string, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen UDP DNS: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var aQueries atomic.Int32
	ready := make(chan struct{})
	go func() {
		close(ready)
		buf := make([]byte, 512)
		for {
			n, client, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			questionEnd := 12
			for questionEnd < len(query) && query[questionEnd] != 0 {
				questionEnd += int(query[questionEnd]) + 1
			}
			questionEnd += 5 // root label plus QTYPE and QCLASS
			if len(query) < 12 || questionEnd > len(query) {
				continue
			}
			qtype := binary.BigEndian.Uint16(query[questionEnd-4 : questionEnd-2])
			aAttempt := int(aQueries.Load())
			if qtype == 1 {
				aAttempt = int(aQueries.Add(1))
			}
			rcode, answer := response(qtype, aAttempt)
			flags := uint16(0x8180) | uint16(rcode&0x0f)
			result := make([]byte, 12, 64)
			copy(result[0:2], query[0:2])
			binary.BigEndian.PutUint16(result[2:4], flags)
			binary.BigEndian.PutUint16(result[4:6], 1)
			if answer && qtype == 1 {
				binary.BigEndian.PutUint16(result[6:8], 1)
			}
			result = append(result, query[12:questionEnd]...)
			if answer && qtype == 1 {
				result = append(result, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01)
				result = append(result, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 127, 0, 0, 1)
			}
			_, _ = conn.WriteToUDP(result, client)
		}
	}()
	<-ready
	return conn.LocalAddr().String(), &aQueries
}

func TestNewVerifyTransportSystemUsesSystemResolver(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen TCP: %v", err)
	}
	defer listener.Close()

	transport, resolver := newVerifyTransport("system")
	defer transport.CloseIdleConnections()
	if resolver != nil {
		t.Fatal("system resolver mode created a custom resolver")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	conn, err := transport.DialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("system-resolved dial: %v", err)
	}
	_ = conn.Close()
}

func TestNewVerifyTransportUsesCustomResolver(t *testing.T) {
	resolverAddr, queries := startTestDNSResponder(t, func(qtype uint16, _ int) (byte, bool) {
		return 0, qtype == 1
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen TCP: %v", err)
	}
	defer listener.Close()

	transport, resolver := newVerifyTransport(resolverAddr)
	defer transport.CloseIdleConnections()
	if resolver == nil {
		t.Fatal("custom resolver mode did not create a resolver")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	conn, err := transport.DialContext(context.Background(), "tcp", net.JoinHostPort("verify-target.test", port))
	if err != nil {
		t.Fatalf("custom-resolved dial: %v", err)
	}
	_ = conn.Close()
	if queries.Load() == 0 {
		t.Fatal("custom DNS responder received no A query")
	}
}

func TestCloudflareVerifyHTTPSReportsNoPublicDNSRecordBeforeHTTP(t *testing.T) {
	resolverAddr, queries := startTestDNSResponder(t, func(uint16, int) (byte, bool) { return 3, false })
	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIToken: "token", AccountID: "account", TunnelID: "tunnel-1",
		// The in-process responder returns NXDOMAIN immediately; the loop remains
		// bounded while preserving that actionable result at the final deadline.
		ZoneIDs: map[string]string{"example.com": "zone"}, VerifyTimeout: 500 * time.Millisecond,
		VerifyResolverAddr: resolverAddr,
	}, nil)
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	backend.verifyBackoff = 5 * time.Millisecond
	var httpAttempts atomic.Int32
	backend.verifyClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpAttempts.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	plan := cloudflareTestPlan()
	plan.Hostname = "verify-target.test"

	err = backend.verifyHTTPS(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "public DNS has no record for verify-target.test (resolver "+resolverAddr+")") {
		t.Fatalf("verifyHTTPS error = %v (A queries=%d)", err, queries.Load())
	}
	if !strings.Contains(err.Error(), "last HTTP failure: not attempted") {
		t.Fatalf("verifyHTTPS error lacks HTTP detail: %v", err)
	}
	if httpAttempts.Load() != 0 {
		t.Fatalf("HTTP attempts = %d, want 0", httpAttempts.Load())
	}
}

func TestCloudflareVerifyHTTPSRetriesDNSUntilRecordAppears(t *testing.T) {
	resolverAddr, queries := startTestDNSResponder(t, func(qtype uint16, aAttempt int) (byte, bool) {
		return 0, qtype == 1 && aAttempt >= 2
	})
	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIToken: "token", AccountID: "account", TunnelID: "tunnel-1",
		ZoneIDs: map[string]string{"example.com": "zone"}, VerifyTimeout: time.Second,
		VerifyResolverAddr: resolverAddr,
	}, nil)
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	backend.verifyBackoff = time.Millisecond
	var httpAttempts atomic.Int32
	backend.verifyClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpAttempts.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	plan := cloudflareTestPlan()
	plan.Hostname = "verify-target.test"

	if err := backend.verifyHTTPS(context.Background(), plan); err != nil {
		t.Fatalf("verifyHTTPS: %v", err)
	}
	if queries.Load() < 2 {
		t.Fatalf("A queries = %d, want at least 2", queries.Load())
	}
	if httpAttempts.Load() != 1 {
		t.Fatalf("HTTP attempts = %d, want 1 after DNS appeared", httpAttempts.Load())
	}
}

func TestCloudflareOwnershipMarker(t *testing.T) {
	coordinate := "public-route:00000000-0000-0000-0000-000000000000:11111111-1111-1111-1111-111111111111:22222222-2222-2222-2222-222222222222"
	const want = "bahia:0450fab315a18e05bc63cdc13b68e009fde56407db0c9ebd8ea82700bae21917"

	got := cloudflareOwnershipMarker(coordinate)
	if got != want {
		t.Fatalf("cloudflareOwnershipMarker() = %q, want %q", got, want)
	}
	if len(got) > 100 {
		t.Fatalf("marker length = %d, exceeds Cloudflare limit", len(got))
	}
	if repeated := cloudflareOwnershipMarker(coordinate); repeated != got {
		t.Fatalf("marker is not deterministic: %q != %q", repeated, got)
	}
	if different := cloudflareOwnershipMarker(coordinate + "-different"); different == got {
		t.Fatalf("distinct coordinates produced the same marker %q", got)
	}
}

func TestCloudflareUpsertDNSUsesOwnershipMarker(t *testing.T) {
	plan := cloudflareTestPlan()
	marker := cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)

	for _, tc := range []struct {
		name       string
		current    []cfDNSRecord
		wantMethod string
		wantPath   string
	}{
		{name: "create", wantMethod: http.MethodPost, wantPath: "/zones/zone/dns_records"},
		{name: "update", current: []cfDNSRecord{{ID: "owned", Comment: marker}}, wantMethod: http.MethodPut, wantPath: "/zones/zone/dns_records/owned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.wantMethod || r.URL.Path != tc.wantPath {
					t.Fatalf("request = %s %s, want %s %s", r.Method, r.URL.Path, tc.wantMethod, tc.wantPath)
				}
				var record cfDNSRecord
				if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
					t.Fatalf("decode DNS record: %v", err)
				}
				if record.Comment != marker {
					t.Fatalf("payload comment = %q, want %q", record.Comment, marker)
				}
				writeCFResult(t, w, record)
			}))
			defer server.Close()

			backend, err := NewCloudflareBackend(CloudflareConfig{
				APIBaseURL: server.URL, APIToken: "token", AccountID: "account",
				TunnelID: "tunnel-1", ZoneIDs: map[string]string{"example.com": "zone"},
			}, server.Client())
			if err != nil {
				t.Fatalf("NewCloudflareBackend: %v", err)
			}
			if err := backend.upsertDNS(context.Background(), plan, tc.current); err != nil {
				t.Fatalf("upsertDNS: %v", err)
			}
		})
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

func TestCloudflareCheckTreatsRawCoordinateAsUnmanagedCollision(t *testing.T) {
	plan := cloudflareTestPlan()
	if len(plan.DNS.SourceCoordinate) != 123 {
		t.Fatalf("test coordinate length = %d, want 123", len(plan.DNS.SourceCoordinate))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/dns_records") {
			t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
		}
		writeCFResult(t, w, []cfDNSRecord{{ID: "raw-coordinate", Comment: plan.DNS.SourceCoordinate}})
	}))
	defer server.Close()

	backend, err := NewCloudflareBackend(CloudflareConfig{
		APIBaseURL: server.URL, APIToken: "token", AccountID: "account",
		TunnelID: "tunnel-1", ZoneIDs: map[string]string{"example.com": "zone"},
	}, server.Client())
	if err != nil {
		t.Fatalf("NewCloudflareBackend: %v", err)
	}
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "unmanaged DNS") {
		t.Fatalf("Check error = %v, want unmanaged DNS collision", err)
	}
}

func TestCloudflareCheckAllowsOwnedRouteOriginUpdate(t *testing.T) {
	plan := cloudflareTestPlan()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/dns_records"):
			writeCFResult(t, w, []cfDNSRecord{{ID: "owned", Comment: cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)}})
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

func TestCloudflareRestoreDNSSelectsOwnershipMarker(t *testing.T) {
	plan := cloudflareTestPlan()
	marker := cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)
	deletes, restores := 0, 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
			writeCFResult(t, w, []cfDNSRecord{
				{ID: "foreign", Comment: plan.DNS.SourceCoordinate},
				{ID: "current-owned", Comment: marker},
			})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/dns_records/current-owned"):
			deletes++
			writeCFResult(t, w, map[string]any{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var record cfDNSRecord
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Fatalf("decode restored DNS record: %v", err)
			}
			if record.ID != "" || record.Content != "previous.example.net" || record.Comment != marker {
				t.Fatalf("restored record = %#v", record)
			}
			restores++
			writeCFResult(t, w, record)
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
	previous := []cfDNSRecord{
		{ID: "previous-owned", Type: "CNAME", Name: plan.Hostname, Content: "previous.example.net", TTL: 1, Proxied: true, Comment: marker},
		{ID: "previous-foreign", Comment: plan.DNS.SourceCoordinate},
	}
	if err := backend.restoreDNS(context.Background(), plan, previous); err != nil {
		t.Fatalf("restoreDNS: %v", err)
	}
	if deletes != 1 || restores != 1 {
		t.Fatalf("restore operations = deletes:%d restores:%d, want 1 each", deletes, restores)
	}
}

func TestCloudflareApplyRetainsTunnelWhenDNSCompensationFails(t *testing.T) {
	plan := cloudflareTestPlan()
	published := false
	tunnelWrites := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/configurations") && r.Method == http.MethodGet:
			writeCFResult(t, w, cfTunnelResult{Config: map[string]any{"ingress": []map[string]any{{"service": "http_status:404"}}}})
		case strings.Contains(r.URL.Path, "/configurations") && r.Method == http.MethodPut:
			tunnelWrites++
			writeCFResult(t, w, map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			if published {
				writeCFResult(t, w, []cfDNSRecord{{ID: "created", Name: plan.Hostname, Comment: cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)}})
			} else {
				writeCFResult(t, w, []cfDNSRecord{})
			}
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodPost:
			published = true
			writeCFResult(t, w, cfDNSRecord{ID: "created"})
		case strings.Contains(r.URL.Path, "/dns_records/") && r.Method == http.MethodDelete:
			http.Error(w, "temporary provider failure", http.StatusServiceUnavailable)
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
	backend.cfg.VerifyTimeout = 10 * time.Millisecond
	backend.verifyBackoff = time.Millisecond

	err = backend.Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "DNS compensation failed and tunnel ingress was retained for retry") {
		t.Fatalf("Apply error = %v", err)
	}
	if tunnelWrites != 1 {
		t.Fatalf("tunnel writes = %d, want only the initial apply while DNS compensation remains retryable", tunnelWrites)
	}
	if !published {
		t.Fatal("test setup did not retain the published DNS record")
	}
}

func TestCloudflareApplyCompensatesFailedHTTPSVerification(t *testing.T) {
	plan := cloudflareTestPlan()
	initialIngress := []map[string]any{{"hostname": "existing.example.com", "service": "http://existing:80"}, {"service": "http_status:404"}}
	currentIngress := initialIngress
	var currentDNS []cfDNSRecord
	var ingressWrites, dnsCreates, dnsDeletes int
	var operations []string

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
			operations = append(operations, "tunnel")
			writeCFResult(t, w, map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodGet:
			writeCFResult(t, w, currentDNS)
		case strings.HasSuffix(r.URL.Path, "/dns_records") && r.Method == http.MethodPost:
			var record cfDNSRecord
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Fatalf("decode DNS: %v", err)
			}
			if record.Comment != cloudflareOwnershipMarker(plan.DNS.SourceCoordinate) {
				t.Fatalf("published comment = %q", record.Comment)
			}
			record.ID = "created"
			currentDNS = []cfDNSRecord{record}
			dnsCreates++
			operations = append(operations, "dns_publish")
			writeCFResult(t, w, record)
		case strings.Contains(r.URL.Path, "/dns_records/") && r.Method == http.MethodDelete:
			currentDNS = nil
			dnsDeletes++
			operations = append(operations, "dns_restore")
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
	backend.cfg.VerifyTimeout = 10 * time.Millisecond
	backend.verifyBackoff = time.Millisecond

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
	wantOperations := []string{"tunnel", "dns_publish", "dns_restore", "tunnel"}
	if strings.Join(operations, ",") != strings.Join(wantOperations, ",") {
		t.Fatalf("operation order = %v, want %v", operations, wantOperations)
	}
}
