package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestPrivilegedRoutesDisabledByDefault(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults()})

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "adoption scan", path: "/api/v1/adoption/scan", body: `{}`},
		{name: "adoption import", path: "/api/v1/adoption/import", body: `{}`},
		{name: "runtime deploy", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/deploy", body: `{}`},
		{name: "runtime restart", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", body: `{}`},
		{name: "runtime stop", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/stop", body: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("disabled route %s status=%d, want 404, body=%s", tt.path, w.Code, w.Body.String())
			}
		})
	}
}

func TestPrivilegedRoutesRequireOperatorAccess(t *testing.T) {
	operatorPubkey, err := nostr.GetPublicKey(routerNIP98Key)
	if err != nil {
		t.Fatal(err)
	}
	const userKey = "0000000000000000000000000000000000000000000000000000000000000003"
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Adoption.Enabled = true
	cfg.Adoption.AllowedPubkeys = []string{operatorPubkey}
	cfg.DirectRuntime.Enabled = true
	cfg.DirectRuntime.AllowedPubkeys = []string{operatorPubkey}

	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config: cfg,
		AuthMiddleware: auth.MiddlewareConfig{
			Enabled:        true,
			NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config()),
		},
	})

	tests := []struct {
		name       string
		path       string
		key        string
		wantStatus int
	}{
		{name: "adoption unauthorized", path: "/api/v1/adoption/scan", wantStatus: http.StatusUnauthorized},
		{name: "adoption forbidden", path: "/api/v1/adoption/scan", key: userKey, wantStatus: http.StatusForbidden},
		{name: "adoption operator reaches handler", path: "/api/v1/adoption/scan", key: routerNIP98Key, wantStatus: http.StatusServiceUnavailable},
		{name: "runtime unauthorized", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", wantStatus: http.StatusUnauthorized},
		{name: "runtime forbidden", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", key: userKey, wantStatus: http.StatusForbidden},
		{name: "runtime operator reaches handler", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", key: routerNIP98Key, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if tt.key != "" {
				req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, tt.key, http.MethodPost, "http://example.com"+tt.path))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d, body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestAdoptionRoutesScanManagedEndpointsWithOperatorAuth(t *testing.T) {
	operatorPubkey, err := nostr.GetPublicKey(routerNIP98Key)
	if err != nil {
		t.Fatal(err)
	}
	hostA := newManagedEndpointDockerServer(t, "container-a", "legacy-api-a", "registry.example/api-a", "sha256:repoa")
	defer hostA.Close()
	hostB := newManagedEndpointDockerServer(t, "container-b", "legacy-api-b", "registry.example/api-b", "sha256:repob")
	defer hostB.Close()

	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artifactRepo := newMockArtifactRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()
	registrySvc := service.NewRegistryService(
		svcRepo, envRepo, buildRepo, artifactRepo,
		newMockIntentRepo(), newMockRunRepo(), obsRepo, stateRepo,
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)

	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Adoption.Enabled = true
	cfg.Adoption.AllowedPubkeys = []string{operatorPubkey}
	cfg.Adoption.AllowRawDockerHosts = false
	cfg.Runtime.Endpoints = map[string]config.RuntimeEndpointConfig{
		"host-a": {DockerHost: hostA.URL},
		"host-b": {DockerHost: hostB.URL},
	}
	adoptionSvc := service.NewAdoptionService(
		registrySvc, svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, &events.NoopPublisher{}, zap.NewNop(),
		service.WithAdoptionRuntimeConfig(cfg.Runtime, cfg.Adoption.AllowRawDockerHosts),
	)

	h := router.NewWithDeps(registrySvc, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:   cfg,
		Adoption: adoptionSvc,
		AuthMiddleware: auth.MiddlewareConfig{
			Enabled:        true,
			NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config()),
		},
	})

	reqPath := "/api/v1/adoption/scan"
	req := httptest.NewRequest(http.MethodPost, reqPath, strings.NewReader(`{"targets":[{"name":"host-a","endpoint_ref":"host-a"},{"name":"host-b","endpoint_ref":"host-b"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, routerNIP98Key, http.MethodPost, "http://example.com"+reqPath))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("scan status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			Target struct {
				Name        string `json:"name"`
				EndpointRef string `json:"endpoint_ref"`
				DockerHost  string `json:"docker_host"`
			} `json:"target"`
			Containers []struct {
				Discovered struct {
					Environment             map[string]string `json:"environment"`
					RedactedEnvironmentKeys []string          `json:"redacted_environment_keys"`
				} `json:"discovered"`
			} `json:"containers"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode scan response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected two endpoint scan results, got %#v", resp.Data)
	}
	seen := map[string]bool{}
	for _, preview := range resp.Data {
		seen[preview.Target.Name] = true
		if preview.Target.EndpointRef != preview.Target.Name {
			t.Fatalf("target endpoint_ref = %q, want %q", preview.Target.EndpointRef, preview.Target.Name)
		}
		if preview.Target.DockerHost != "" {
			t.Fatalf("managed endpoint response leaked docker_host: %#v", preview.Target)
		}
		if len(preview.Containers) != 1 {
			t.Fatalf("expected one container for %s, got %#v", preview.Target.Name, preview.Containers)
		}
		discovered := preview.Containers[0].Discovered
		if discovered.Environment["APP_ENV"] != "prod" {
			t.Fatalf("safe env missing for %s: %#v", preview.Target.Name, discovered.Environment)
		}
		if _, ok := discovered.Environment["SECRET_TOKEN"]; ok {
			t.Fatalf("sensitive env leaked for %s: %#v", preview.Target.Name, discovered.Environment)
		}
		if !containsString(discovered.RedactedEnvironmentKeys, "SECRET_TOKEN") {
			t.Fatalf("redacted keys missing SECRET_TOKEN for %s: %#v", preview.Target.Name, discovered.RedactedEnvironmentKeys)
		}
	}
	if !seen["host-a"] || !seen["host-b"] {
		t.Fatalf("missing host scan result(s): %#v", seen)
	}

	rawReqPath := "/api/v1/adoption/scan"
	rawReq := httptest.NewRequest(http.MethodPost, rawReqPath, strings.NewReader(`{"targets":[{"name":"raw","docker_host":"`+hostA.URL+`"}]}`))
	rawReq.Header.Set("Content-Type", "application/json")
	rawReq.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, routerNIP98Key, http.MethodPost, "http://example.com"+rawReqPath))
	rawW := httptest.NewRecorder()
	h.ServeHTTP(rawW, rawReq)
	if rawW.Code != http.StatusBadRequest || !strings.Contains(rawW.Body.String(), "raw docker_host targets are disabled") {
		t.Fatalf("raw-host scan status=%d body=%s, want policy rejection", rawW.Code, rawW.Body.String())
	}
}

func newManagedEndpointDockerServer(t *testing.T, containerID, containerName, imageRepo, imageDigest string) *httptest.Server {
	t.Helper()
	imageID := "sha256:" + containerID
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"Id":%q,"Names":[%q],"Image":%q,"ImageID":%q,"State":"running"}]`, containerID, "/"+containerName, imageRepo+":1.0.0", imageID)
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/"+containerID+"/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"Id":%q,
				"Name":%q,
				"Image":%q,
				"Config":{"Image":%q,"Env":["APP_ENV=prod","SECRET_TOKEN=top-secret"],"Labels":{"com.example.owner":"platform"}},
				"State":{"Status":"running","Health":{"Status":"healthy"}},
				"HostConfig":{"NetworkMode":"bridge","RestartPolicy":{"Name":"unless-stopped"}},
				"NetworkSettings":{"Ports":{},"Networks":{"bridge":{"Aliases":[%q]}}}
			}`, containerID, "/"+containerName, imageID, imageRepo+":1.0.0", containerName)
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/"+imageID+"/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"Id":%q,"RepoDigests":[%q]}`, imageID, imageRepo+"@"+imageDigest)
		default:
			http.Error(w, fmt.Sprintf("unexpected docker request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
