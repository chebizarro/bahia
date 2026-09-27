package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type staticReadiness struct {
	state soulfactory.OpenClawSidecarReadiness
}

func (s staticReadiness) Readiness() soulfactory.OpenClawSidecarReadiness { return s.state }

type healthLifecycleRunner struct {
	address string
	state   soulfactory.OpenClawSidecarReadiness
}

func (runner healthLifecycleRunner) Readiness() soulfactory.OpenClawSidecarReadiness {
	return runner.state
}

func (runner healthLifecycleRunner) Run(ctx context.Context) error {
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + runner.address + "/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("health server never became reachable")
}

func TestRunSidecarWithHealthKeepsServerAliveDuringRun(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	runner := healthLifecycleRunner{
		address: address,
		state: soulfactory.OpenClawSidecarReadiness{
			Ready: true, CapabilityPublished: true, SubscriptionEOSE: true,
		},
	}
	if err := runSidecarWithHealth(t.Context(), address, runner); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrivateKeyUsesFile(t *testing.T) {
	t.Setenv("OPENCLAW_SOULFACTORY_PRIVATE_KEY", "")
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if got, err := config.LoadPrivateKey(path, "OPENCLAW_SOULFACTORY_PRIVATE_KEY"); err != nil || got != "file-secret" {
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

func TestLoadPrivateKeyRejectsLegacyEnvironmentSource(t *testing.T) {
	t.Setenv("OPENCLAW_SOULFACTORY_PRIVATE_KEY", "legacy-secret")
	path := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := config.LoadPrivateKey(path, "OPENCLAW_SOULFACTORY_PRIVATE_KEY"); err == nil {
		t.Fatal("expected deprecated private-key environment source error")
	}
}
