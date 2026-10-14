package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func caddyTestPlan() *domain.DesiredPublicRoutePlan {
	plan := cloudflareTestPlan()
	plan.Provider = "caddy"
	plan.DNS = domain.DesiredPublicRouteDNS{}
	plan.Tunnel = domain.DesiredPublicRouteTunnel{}
	plan.Caddy = &domain.DesiredPublicRouteCaddy{
		ServerID: "public", RouteID: domain.PublicRouteCaddyID(plan.ServiceID, plan.EnvironmentID, plan.DeploymentUnitID),
	}
	plan.TLS.Provider = "caddy"
	return plan
}

type fakeCaddy struct {
	mu                      sync.Mutex
	routes, policies        []json.RawMessage
	httpVersion, tlsVersion int
	disableTLS              bool
	malformedHTTP           bool
	failPolicyAfter         bool
	failRouteAfter          bool
	mutations               []string
	restoreRouteBody        []byte
	restorePolicyBody       []byte
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/config/apps/http":
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("\"http-%d\"", f.httpVersion))
		if f.malformedHTTP {
			_, _ = w.Write([]byte("{"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": map[string]any{"public": map[string]any{
			"listen": []string{":443"}, "automatic_https": map[string]bool{"disable_certificates": f.disableTLS},
			"routes": f.routes,
		}}})
		return
	case "/config/apps/tls":
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("\"tls-%d\"", f.tlsVersion))
		_ = json.NewEncoder(w).Encode(map[string]any{"automation": map[string]any{"policies": f.policies}})
		return
	}
	isPolicy := strings.HasSuffix(r.URL.Path, ":tls") || strings.Contains(r.URL.Path, "/policies/")
	var items *[]json.RawMessage
	version := &f.httpVersion
	if isPolicy {
		items, version = &f.policies, &f.tlsVersion
	} else {
		items = &f.routes
	}
	if r.Header.Get("If-Match") != fmt.Sprintf("\"%s-%d\"", map[bool]string{true: "tls", false: "http"}[isPolicy], *version) {
		http.Error(w, "precondition", http.StatusPreconditionFailed)
		return
	}
	var body json.RawMessage
	if r.Method != http.MethodDelete {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
	}
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/0"):
		*items = append([]json.RawMessage{body}, *items...)
	case strings.HasPrefix(r.URL.Path, "/id/"):
		target := strings.TrimPrefix(r.URL.Path, "/id/")
		found := -1
		for i, item := range *items {
			id, _ := caddyObjectID(item)
			if id == target {
				found = i
				break
			}
		}
		if found == -1 {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			(*items)[found] = body
			if !isPolicy {
				f.restoreRouteBody = append([]byte(nil), body...)
			} else {
				f.restorePolicyBody = append([]byte(nil), body...)
			}
		case http.MethodDelete:
			*items = append((*items)[:found], (*items)[found+1:]...)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
	default:
		http.Error(w, "path", http.StatusNotFound)
		return
	}
	*version++
	f.mutations = append(f.mutations, r.Method+" "+r.URL.Path)
	if isPolicy && f.failPolicyAfter {
		f.failPolicyAfter = false
		http.Error(w, "opaque provider failure", http.StatusServiceUnavailable)
		return
	}
	if !isPolicy && f.failRouteAfter {
		f.failRouteAfter = false
		http.Error(w, "opaque provider failure", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func caddyFixture(t *testing.T) (*CaddyBackend, *domain.DesiredPublicRoutePlan, *fakeCaddy) {
	t.Helper()
	fake := &fakeCaddy{routes: []json.RawMessage{}, policies: []json.RawMessage{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	backend, err := NewCaddyBackend(CaddyConfig{AdminURL: server.URL, ServerID: "public"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return backend, caddyTestPlan(), fake
}

func TestCaddyCheckCollisionAndTLSMismatch(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	fake.routes = []json.RawMessage{json.RawMessage(`{"@id":"foreign","match":[{"host":["arcana.example.com"]}],"handle":[]}`)}
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
	fake.routes = []json.RawMessage{json.RawMessage(`{"@id":"foreign","match":[{"path":["/*"]}],"handle":[]}`)}
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("path-only collision error = %v", err)
	}
	fake.routes = []json.RawMessage{}
	fake.disableTLS = true
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "managed HTTPS") {
		t.Fatalf("TLS mismatch error = %v", err)
	}
	if len(fake.mutations) != 0 {
		t.Fatalf("Check mutated provider: %v", fake.mutations)
	}
}

func TestCaddyApplyIdempotentByPlanHash(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	count := len(fake.mutations)
	if count != 2 {
		t.Fatalf("mutations = %v", fake.mutations)
	}
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(fake.mutations) != count {
		t.Fatalf("idempotent re-apply mutated provider: %v", fake.mutations)
	}
	plan.ProviderConfigHash = "sha256:new-config"
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(fake.mutations) != count+1 {
		t.Fatalf("new hash did not update route: %v", fake.mutations)
	}
}

func TestCaddyCompensationRestoresExactSnapshot(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	oldRoute := json.RawMessage(fmt.Sprintf(`{"@id":%q,"match":[{"host":[%q]}],"handle":[{"handler":"static_response","body":"old"}],"terminal":true}`, plan.Caddy.RouteID, plan.Hostname))
	oldPolicy := json.RawMessage(fmt.Sprintf(`{"@id":%q,"subjects":[%q],"issuers":[{"module":"acme"}],"key_type":"p256"}`, caddyPolicyID(plan), plan.Hostname))
	fake.routes, fake.policies = []json.RawMessage{oldRoute}, []json.RawMessage{oldPolicy}
	snapshot, err := backend.read(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	inverse, err := backend.ApplyWithCompensation(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := inverse(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(fake.routes[0], oldRoute) || !sameJSON(fake.policies[0], oldPolicy) {
		t.Fatalf("prior subtrees not restored: route=%s policy=%s", fake.routes[0], fake.policies[0])
	}
	if !bytes.Equal(fake.restoreRouteBody, snapshot.route) || !bytes.Equal(fake.restorePolicyBody, snapshot.policy) {
		t.Fatalf("compensation did not restore exact prior bytes: route=%s policy=%s", fake.restoreRouteBody, fake.restorePolicyBody)
	}
}

func TestCaddyWithdrawStopsAtFailedPhaseAndResumes(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	stop := fmt.Errorf("receipt failed")
	err := backend.Withdraw(context.Background(), plan, func(_ context.Context, phase string) error {
		if phase != "route_withdrawn" {
			t.Fatalf("first phase = %q", phase)
		}
		return stop
	})
	if err != stop {
		t.Fatalf("withdraw = %v", err)
	}
	if len(fake.routes) != 0 || len(fake.policies) != 1 {
		t.Fatalf("effects crossed failed phase: routes=%d policies=%d", len(fake.routes), len(fake.policies))
	}
	var phases []string
	if err := backend.Withdraw(context.Background(), plan, func(_ context.Context, phase string) error { phases = append(phases, phase); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(fake.policies) != 0 || strings.Join(phases, ",") != "route_withdrawn,tls_withdrawn" {
		t.Fatalf("withdraw did not resume: phases=%v policies=%d", phases, len(fake.policies))
	}
}

func TestCaddyCompensatesAmbiguousPartialPolicyFailure(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	fake.failPolicyAfter = true
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err == nil {
		t.Fatal("ambiguous policy failure was accepted")
	}
	if len(fake.routes) != 0 || len(fake.policies) != 0 {
		t.Fatalf("partial apply was not compensated: routes=%d policies=%d", len(fake.routes), len(fake.policies))
	}
}

func TestCaddyCompensatesRouteFailureAfterTLSPolicy(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	fake.failRouteAfter = true
	if _, err := backend.ApplyWithCompensation(context.Background(), plan); err == nil {
		t.Fatal("ambiguous route failure was accepted")
	}
	if len(fake.routes) != 0 || len(fake.policies) != 0 {
		t.Fatalf("partial apply was not compensated: routes=%d policies=%d", len(fake.routes), len(fake.policies))
	}
}

func TestCaddyFailsClosedOnUnreachableOrMalformedAdmin(t *testing.T) {
	backend, plan, fake := caddyFixture(t)
	fake.malformedHTTP = true
	if err := backend.Check(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "invalid Caddy HTTP") {
		t.Fatalf("malformed provider configuration accepted: %v", err)
	}
	fake.malformedHTTP = false
	backend.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("secret-url") })}
	if err := backend.Check(context.Background(), plan); err == nil || strings.Contains(err.Error(), "secret-url") {
		t.Fatalf("unsafe error = %v", err)
	}
}
