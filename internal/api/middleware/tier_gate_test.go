package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type tierGatePolicy struct {
	RequestedMode string
	ActiveTier    int
}

func (p *tierGatePolicy) RouteEnabled(requiredTier int) bool {
	return requiredTier <= p.ActiveTier
}

func TestTierGateAllowsEnabledRoute(t *testing.T) {
	policy := &tierGatePolicy{RequestedMode: "degraded", ActiveTier: 2}
	handler := TierGate(policy, 2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestTierGateReturnsServiceUnavailableForDisabledRoute(t *testing.T) {
	policy := &tierGatePolicy{RequestedMode: "degraded", ActiveTier: 2}
	handler := TierGate(policy, 3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type=%q, want application/json", got)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "route unavailable in current mode" || body["mode"] != "degraded" || body["active_tier"] != float64(2) || body["required_tier"] != float64(3) {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestTierGateNilPolicySkipsGating(t *testing.T) {
	handler := TierGate(nil, 3)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusNoContent)
	}
}
