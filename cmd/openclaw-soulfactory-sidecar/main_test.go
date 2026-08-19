package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type staticReadiness struct {
	state soulfactory.OpenClawSidecarReadiness
}

func (s staticReadiness) Readiness() soulfactory.OpenClawSidecarReadiness { return s.state }

func TestLoadPrivateKeyUsesEnvironmentOrFile(t *testing.T) {
	if got, err := loadPrivateKey("", "  env-secret  "); err != nil || got != "env-secret" {
		t.Fatalf("environment key = %q, err = %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if got, err := loadPrivateKey(path, ""); err != nil || got != "file-secret" {
		t.Fatalf("file key = %q, err = %v", got, err)
	}
}

func TestHealthServerReadinessRequiresCapabilityAndEOSE(t *testing.T) {
	notReady := newHealthServer("", staticReadiness{state: soulfactory.OpenClawSidecarReadiness{CapabilityPublished: true}})
	recorder := httptest.NewRecorder()
	notReady.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var state soulfactory.OpenClawSidecarReadiness
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if state.Ready || !state.CapabilityPublished || state.SubscriptionEOSE {
		t.Fatalf("unexpected readiness: %+v", state)
	}

	ready := newHealthServer("", staticReadiness{state: soulfactory.OpenClawSidecarReadiness{Ready: true, CapabilityPublished: true, SubscriptionEOSE: true}})
	recorder = httptest.NewRecorder()
	ready.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestLoadPrivateKeyRejectsAmbiguousSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := loadPrivateKey(path, "env-secret"); err == nil {
		t.Fatal("expected ambiguous private-key source error")
	}
}
