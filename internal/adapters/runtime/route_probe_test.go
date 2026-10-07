package runtime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func mustPort(t *testing.T, rawURL string) int {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("port from %q: %v", rawURL, err)
	}
	return port
}

func httpTarget(t *testing.T, server *httptest.Server) domain.RouteCanaryTarget {
	t.Helper()
	return domain.RouteCanaryTarget{
		Perspective:       domain.RouteCanaryPerspectivePublicEdge,
		Scheme:            "http",
		Hostname:          "127.0.0.1",
		Port:              mustPort(t, server.URL),
		Path:              "/healthz",
		Method:            "GET",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           5 * time.Second,
	}
}

func TestProbeRouteHealthyRouteIsRouteOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("service ok"))
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.ExpectedBodyContains = "service ok"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe returned an error for a reachable route: %v", err)
	}
	if !observation.Resolved || !observation.Connected {
		t.Fatalf("expected resolved+connected, got %+v", observation)
	}
	if !observation.BodyMatched {
		t.Fatalf("expected body match, got body %q", observation.Body)
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}

// TestProbeRouteStaleUpstreamIsUpstreamError is the git.sharegap.net repro at
// the adapter layer: the route answers, but the origin behind it returns 502.
func TestProbeRouteStaleUpstreamIsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><head><title>502 Bad Gateway</title></head></html>"))
	}))
	defer server.Close()

	observation, err := RouteProber{}.ProbeRoute(context.Background(), httpTarget(t, server))
	if err != nil {
		t.Fatalf("probe returned an error for a 502 route: %v", err)
	}
	if !observation.Connected || observation.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected a connected 502 observation, got %+v", observation)
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationUpstreamError {
		t.Fatalf("got %q, want upstream_error", got)
	}
}

func TestProbeRouteBodyMismatchIsDetected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("maintenance placeholder"))
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.ExpectedBodyContains = "service ok"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationBodyMismatch {
		t.Fatalf("got %q, want body_mismatch", got)
	}
}

// TestProbeRouteRejectsUntrustedTLS proves certificate verification is never
// disabled. httptest's TLS server presents a self-signed certificate that the
// system trust store does not accept, so the probe must classify tls_invalid
// rather than reporting a healthy route.
func TestProbeRouteRejectsUntrustedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	target := domain.RouteCanaryTarget{
		Perspective:       domain.RouteCanaryPerspectivePublicEdge,
		Scheme:            "https",
		Hostname:          "127.0.0.1",
		Port:              mustPort(t, server.URL),
		Path:              "/healthz",
		Method:            "GET",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           5 * time.Second,
	}

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.Connected {
		t.Fatal("probe accepted an untrusted certificate; TLS verification is disabled")
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationTLSInvalid {
		t.Fatalf("got %q, want tls_invalid (observation %+v)", got, observation)
	}
}

// TestProbeRouteResolveToPinsTheDialAddress proves the internal_lan perspective
// reaches a host that DNS would never resolve, which is how a split-DNS LAN
// check works without depending on the prober's own DNS view.
func TestProbeRouteResolveToPinsTheDialAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var seenHost string
	server := &httptest.Server{
		Listener: listener,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenHost = r.Host
			w.WriteHeader(http.StatusOK)
		})},
	}
	server.Start()
	defer server.Close()

	port := mustPort(t, server.URL)
	target := domain.RouteCanaryTarget{
		Perspective: domain.RouteCanaryPerspectiveInternalLAN,
		Scheme:      "http",
		// A hostname in the reserved .invalid TLD, which must never resolve.
		Hostname:          "git.canary.invalid",
		Port:              port,
		Path:              "/healthz",
		Method:            "GET",
		ResolveTo:         "127.0.0.1",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           5 * time.Second,
	}

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !observation.Connected {
		t.Fatalf("pinned dial did not connect: %+v", observation)
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
	// The canonical hostname must survive as the Host header even though the
	// dial address was pinned, otherwise a name-based vhost would not match.
	if !strings.HasPrefix(seenHost, "git.canary.invalid") {
		t.Fatalf("Host header was not the canonical hostname: %q", seenHost)
	}
}

// TestProbeRouteUnresolvableHostIsDNSUnresolved uses a resolver pointed at a
// closed port so the failure is deterministic and does not depend on the
// machine's DNS or on any network egress.
func TestProbeRouteUnresolvableHostIsDNSUnresolved(t *testing.T) {
	target := domain.RouteCanaryTarget{
		Perspective:       domain.RouteCanaryPerspectivePublicEdge,
		Scheme:            "https",
		Hostname:          "git.canary.invalid",
		Port:              443,
		Path:              "/healthz",
		Method:            "GET",
		ResolverAddr:      "127.0.0.1:1",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           2 * time.Second,
	}

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.Resolved {
		t.Fatalf("expected resolution failure, got %+v", observation)
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationDNSUnresolved {
		t.Fatalf("got %q, want dns_unresolved", got)
	}
}

func TestProbeRouteConnectionRefusedIsConnectFailed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Close immediately so the port is almost certainly refusing connections.
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	target := domain.RouteCanaryTarget{
		Perspective:       domain.RouteCanaryPerspectiveInternalLAN,
		Scheme:            "http",
		Hostname:          "git.canary.invalid",
		Port:              port,
		Path:              "/healthz",
		Method:            "GET",
		ResolveTo:         "127.0.0.1",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           2 * time.Second,
	}

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.Connected {
		t.Fatal("expected no connection to a closed port")
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationConnectFailed {
		t.Fatalf("got %q, want connect_failed (observation %+v)", got, observation)
	}
}

func TestProbeRouteRejectsInvalidTarget(t *testing.T) {
	if _, err := (RouteProber{}).ProbeRoute(context.Background(), domain.RouteCanaryTarget{}); err == nil {
		t.Fatal("expected an error for an invalid target")
	}
}

// TestProbeRouteRejectsConflictingResolutionOverrides proves the two resolution
// modes cannot be combined into an ambiguous probe.
func TestProbeRouteRejectsConflictingResolutionOverrides(t *testing.T) {
	target := domain.RouteCanaryTarget{
		Perspective:       domain.RouteCanaryPerspectivePublicEdge,
		Scheme:            "https",
		Hostname:          "git.canary.invalid",
		Port:              443,
		Path:              "/",
		Method:            "GET",
		ResolverAddr:      "127.0.0.1:53",
		ResolveTo:         "127.0.0.1",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 299,
		Timeout:           time.Second,
	}
	if _, err := (RouteProber{}).ProbeRoute(context.Background(), target); err == nil {
		t.Fatal("expected an error when both resolver_addr and resolve_to are set")
	}
}

// TestProbeRouteSanitizesResponseBody proves credentials echoed by a broken
// upstream never reach stored evidence, while assertion matching still runs
// against the raw body.
func TestProbeRouteSanitizesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("echo Authorization: Bearer supersecrettokenvalue end"))
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.ExpectedBodyContains = "supersecrettokenvalue"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !observation.BodyMatched {
		t.Fatal("matching must run against the raw body so redaction cannot change semantics")
	}
	if strings.Contains(observation.Body, "supersecrettokenvalue") {
		t.Fatalf("stored body evidence was not sanitized: %q", observation.Body)
	}
}

// TestProbeRouteDetectsCatchAllServer reproduces the live Astillero condition at
// the adapter layer: a single-page-application server that answers every path
// with an identical 200 shell. The probe must observe that the control path is
// indistinguishable from the health path.
func TestProbeRouteDetectsCatchAllServer(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><body>app shell</body></html>"))
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.ControlPath = "/.bahia-route-canary-control/deadbeef"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if len(requested) != 2 {
		t.Fatalf("expected health and control requests, got %v", requested)
	}
	if observation.Control == nil || !observation.Control.Performed || !observation.Control.Connected {
		t.Fatalf("control probe was not performed: %+v", observation.Control)
	}
	if !observation.Indistinguishable() {
		t.Fatal("identical catch-all responses were not detected as indistinguishable")
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationHealthPathNotDiscriminating {
		t.Fatalf("got %q, want health_path_not_discriminating", got)
	}
}

// A server with a real health endpoint must not be flagged.
func TestProbeRouteDiscriminatingServerIsRouteOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.Path = "/healthz"
	target.ControlPath = "/.bahia-route-canary-control/deadbeef"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.Indistinguishable() {
		t.Fatal("a real health endpoint was wrongly flagged as a catch-all")
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}

// A control probe that cannot be completed must never turn into a route
// failure; the health verdict stands on its own.
func TestProbeRouteControlFailureDoesNotFailTheRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		// Hijack the connection so the control request fails at transport level.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()

	target := httpTarget(t, server)
	target.Path = "/healthz"
	target.ControlPath = "/.bahia-route-canary-control/deadbeef"

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.Indistinguishable() {
		t.Fatal("an unusable control must not be treated as indistinguishable")
	}
	if got := domain.ClassifyRouteObservation(observation, time.Now()); got != domain.RouteCanaryClassificationRouteOK {
		t.Fatalf("got %q, want route_ok", got)
	}
}
