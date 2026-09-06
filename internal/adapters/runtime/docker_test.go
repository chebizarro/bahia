package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestNewDockerObserverWithEndpointNormalizesTLSHost(t *testing.T) {
	observer, err := NewDockerObserverWithEndpoint(config.RuntimeEndpointConfig{
		Ref:                "prod-docker",
		DockerHost:         "tcp://docker.example:2376",
		InsecureSkipVerify: true,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewDockerObserverWithEndpoint() error = %v", err)
	}
	if observer.host != "https://docker.example:2376" {
		t.Fatalf("observer.host = %q, want https://docker.example:2376", observer.host)
	}
	if observer.observedHost != "prod-docker" {
		t.Fatalf("observer.observedHost = %q, want alias", observer.observedHost)
	}
}

func TestDockerDeployAddsPortBindingsAndVolumes(t *testing.T) {
	t.Parallel()

	createBodies := make(chan map[string]any, 1)
	handlerErrors := newDockerHandlerErrors()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/create":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				handlerErrors.add(fmt.Sprintf("decode create body: %v", err))
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			createBodies <- body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"container-123"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/container-123/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	err := observer.Deploy(context.Background(), "api", "registry.example/api:latest", DeployOptions{
		Ports:   []string{"8080:80", "5353:53/udp", "192.168.40.104:8443:443", " 127.0.0.1:9000:9001 "},
		Volumes: []string{"/var/lib/api:/data:ro", " ", "/tmp/api-cache:/cache"},
	})
	if err != nil {
		t.Fatalf("Deploy returned error: %v; handler errors: %v", err, handlerErrors.all())
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}

	var createBody map[string]any
	select {
	case createBody = <-createBodies:
	default:
		t.Fatal("expected create request body to be captured")
	}

	exposedPorts, ok := createBody["ExposedPorts"].(map[string]any)
	if !ok {
		t.Fatalf("expected ExposedPorts object, got %#v", createBody["ExposedPorts"])
	}
	for _, port := range []string{"80/tcp", "53/udp", "443/tcp"} {
		if _, ok := exposedPorts[port]; !ok {
			t.Fatalf("expected ExposedPorts[%q] to be present in %#v", port, exposedPorts)
		}
	}

	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected HostConfig object, got %#v", createBody["HostConfig"])
	}
	portBindings, ok := hostConfig["PortBindings"].(map[string]any)
	if !ok {
		t.Fatalf("expected HostConfig.PortBindings object, got %#v", hostConfig["PortBindings"])
	}
	assertHostPort(t, portBindings, "80/tcp", "", "8080")
	assertHostPort(t, portBindings, "53/udp", "", "5353")
	assertHostPort(t, portBindings, "443/tcp", "192.168.40.104", "8443")
	assertHostPort(t, portBindings, "9001/tcp", "127.0.0.1", "9000")

	binds, ok := hostConfig["Binds"].([]any)
	if !ok {
		t.Fatalf("expected HostConfig.Binds array, got %#v", hostConfig["Binds"])
	}
	if len(binds) != 2 || binds[0] != "/var/lib/api:/data:ro" || binds[1] != "/tmp/api-cache:/cache" {
		t.Fatalf("unexpected binds: %#v", binds)
	}
}

func TestDockerDeployPullAlwaysSendsRegistryAuthForMatchingHost(t *testing.T) {
	t.Parallel()

	var authHeader string
	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/images/create":
			authHeader = r.Header.Get("X-Registry-Auth")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/create":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"container-123"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/container-123/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		registryAuth: &RegistryAuthConfig{
			Server:   "https://harbor.sharegap.net",
			Username: "robot$cascadia+edge01-runtime",
			Password: "secret-token",
		},
		logger: zap.NewNop(),
	}

	err := observer.Deploy(context.Background(), "ddgs", "harbor.sharegap.net/cascadia/ddgs:pilot-v1", DeployOptions{PullAlways: true})
	if err != nil {
		t.Fatalf("Deploy returned error: %v; handler errors: %v", err, handlerErrors.all())
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}
	if authHeader == "" {
		t.Fatal("expected X-Registry-Auth header on image pull")
	}
	decoded, err := base64.StdEncoding.DecodeString(authHeader)
	if err != nil {
		t.Fatalf("decode auth header: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal auth header payload: %v", err)
	}
	if payload["username"] != "robot$cascadia+edge01-runtime" || payload["password"] != "secret-token" || payload["serveraddress"] != "harbor.sharegap.net" {
		t.Fatalf("unexpected auth payload: %#v", payload)
	}
}

func TestDockerObserveUsesAllContainersNameFallbackAndRepoDigest(t *testing.T) {
	t.Parallel()

	var sawLabelAll, sawFallbackAll atomic.Bool
	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json" && strings.Contains(r.URL.RawQuery, "filters="):
			if r.URL.Query().Get("all") != "1" {
				handlerErrors.add("label query did not include all=1")
			}
			sawLabelAll.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			if r.URL.Query().Get("all") != "1" {
				handlerErrors.add("fallback query did not include all=1")
			}
			sawFallbackAll.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/adopted-api"],"Image":"registry.example/api:latest","ImageID":"sha256:localdigest","State":"exited"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:localdigest/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:localdigest","RepoDigests":["registry.example/api@sha256:repodigest"]}`))
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	obs, err := observer.Observe(context.Background(), uuid.New(), uuid.New(), "adopted-api")
	if err != nil {
		t.Fatalf("Observe returned error: %v; handler errors: %v", err, handlerErrors.all())
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}
	if !sawLabelAll.Load() || !sawFallbackAll.Load() {
		t.Fatalf("expected label and fallback all-container queries, saw label=%v fallback=%v", sawLabelAll.Load(), sawFallbackAll.Load())
	}
	if obs.HealthStatus != domain.HealthStatusStopped {
		t.Fatalf("expected stopped health, got %s", obs.HealthStatus)
	}
	if obs.ObservedImageDigest != "sha256:repodigest" {
		t.Fatalf("expected repo digest, got %q", obs.ObservedImageDigest)
	}
	if obs.ObservedImageRepo != "registry.example/api" {
		t.Fatalf("expected repo from repo digest, got %q", obs.ObservedImageRepo)
	}
	if obs.NormalizedHash != "" {
		t.Fatalf("expected empty normalized hash without desired label, got %q", obs.NormalizedHash)
	}
}

func TestDockerObservePrefersRunningBahiaManagedContainerOverLegacyServiceLabel(t *testing.T) {
	t.Parallel()

	serviceID := uuid.New()
	envID := uuid.New()
	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			filters := r.URL.Query().Get("filters")
			if !strings.Contains(filters, "bahia.service_id="+serviceID.String()) || !strings.Contains(filters, "bahia.environment_id="+envID.String()) {
				handlerErrors.add(fmt.Sprintf("unexpected container filters: %s", filters))
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"Id":"rollback-container","Names":["/bahia-env-harbormaster-watch-rollback"],"Image":"registry.example/harbormaster-watch:old","ImageID":"sha256:rollbackdigest","State":"exited","Labels":{"bahia.service":"harbormaster-watch"}},
				{"Id":"current-container","Names":["/bahia-env-harbormaster-watch"],"Image":"registry.example/harbormaster-watch:current","ImageID":"sha256:currentdigest","State":"running","Labels":{"bahia.managed":"true","bahia.desired_hash":"sha256:desired-state"}}
			]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:currentdigest/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:currentdigest","RepoDigests":["registry.example/harbormaster-watch@sha256:currentrepo"]}`))
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	obs, err := observer.Observe(context.Background(), serviceID, envID, "harbormaster-watch")
	if err != nil {
		t.Fatalf("Observe returned error: %v; handler errors: %v", err, handlerErrors.all())
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}
	if obs.ObservedContainerID != "current-container" {
		t.Fatalf("expected running current container, got %q", obs.ObservedContainerID)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		t.Fatalf("expected healthy running container, got %s", obs.HealthStatus)
	}
	if obs.ObservedImageDigest != "sha256:currentrepo" {
		t.Fatalf("expected current repo digest, got %q", obs.ObservedImageDigest)
	}
	if obs.NormalizedHash != "sha256:desired-state" {
		t.Fatalf("expected normalized hash from desired label, got %q", obs.NormalizedHash)
	}
}

func TestDockerObservePrefersConfiguredRepoDigestWhenImageIDHasMultipleRepos(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json" && strings.Contains(r.URL.RawQuery, "filters="):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/api"],"Image":"registry.example/api:latest","ImageID":"sha256:sharedimage","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:sharedimage/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:sharedimage","RepoDigests":["registry.example/old-api@sha256:oldrepo","registry.example/api@sha256:wantedrepo"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	obs, err := observer.Observe(context.Background(), uuid.New(), uuid.New(), "api")
	if err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	if obs.ObservedImageRepo != "registry.example/api" || obs.ObservedImageDigest != "sha256:wantedrepo" {
		t.Fatalf("expected preferred repo digest, got repo=%q digest=%q", obs.ObservedImageRepo, obs.ObservedImageDigest)
	}
}

func TestDockerDeployMapsAdoptedRuntimeOptions(t *testing.T) {
	t.Parallel()

	createBodies := make(chan map[string]any, 1)
	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/create":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				handlerErrors.add(fmt.Sprintf("decode create body: %v", err))
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			createBodies <- body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"container-123"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/container-123/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	err := observer.Deploy(context.Background(), "adopted-api", "registry.example/api:latest", DeployOptions{
		Command:     []string{"serve", "--port", "80"},
		Entrypoint:  []string{"/entrypoint.sh"},
		WorkingDir:  "/srv/app",
		NetworkMode: "host",
	})
	if err != nil {
		t.Fatalf("Deploy returned error: %v; handler errors: %v", err, handlerErrors.all())
	}

	var createBody map[string]any
	select {
	case createBody = <-createBodies:
	default:
		t.Fatal("expected create request body to be captured")
	}
	assertStringSlice(t, createBody["Cmd"], []string{"serve", "--port", "80"})
	assertStringSlice(t, createBody["Entrypoint"], []string{"/entrypoint.sh"})
	if createBody["WorkingDir"] != "/srv/app" {
		t.Fatalf("expected WorkingDir, got %#v", createBody["WorkingDir"])
	}
	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected HostConfig object, got %#v", createBody["HostConfig"])
	}
	if hostConfig["NetworkMode"] != "host" {
		t.Fatalf("expected NetworkMode host, got %#v", hostConfig["NetworkMode"])
	}
}

func TestDockerRestartAndStopUseTargetNameFallback(t *testing.T) {
	t.Parallel()

	var restarted, stopped atomic.Bool
	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json" && strings.Contains(r.URL.RawQuery, "filters="):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/adopted-api"],"Image":"api","ImageID":"sha256:1","State":"running"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/container-123/restart":
			restarted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/container-123/stop":
			stopped.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	if err := observer.Restart(context.Background(), "adopted-api"); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if err := observer.Stop(context.Background(), "adopted-api"); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}
	if !restarted.Load() || !stopped.Load() {
		t.Fatalf("expected restart and stop calls, got restart=%v stop=%v", restarted.Load(), stopped.Load())
	}
}

func TestDockerDeployInvalidPortDoesNotTouchDocker(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}

	invalidPorts := []string{
		"8080",
		"abc:80",
		"8080:70000",
		"8080:80/HTTP",
		"8080:80/",
	}
	for _, invalidPort := range invalidPorts {
		err := observer.Deploy(context.Background(), "api", "registry.example/api:latest", DeployOptions{
			Ports: []string{invalidPort},
		})
		if err == nil {
			t.Fatalf("expected invalid port mapping error for %q", invalidPort)
		}
		if !strings.Contains(err.Error(), "invalid port mapping") {
			t.Fatalf("expected invalid port mapping error for %q, got %v", invalidPort, err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected no Docker API requests, got %d", got)
	}
}

func TestDockerDiscoveryNormalizesContainerInspectData(t *testing.T) {
	t.Parallel()

	handlerErrors := newDockerHandlerErrors()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			if r.URL.Query().Get("all") != "1" {
				handlerErrors.add("discovery query did not include all=1")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/web"],"Image":"registry.example/web:1.2.3","ImageID":"sha256:image123","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-123",
				"Name":"/demo-web-1",
				"Image":"sha256:image123",
				"Config":{
					"Image":"registry.example/web:1.2.3",
					"Env":["APP_ENV=prod","EMPTY="],
					"Labels":{
						"com.docker.compose.project":"demo",
						"com.docker.compose.service":"web",
						"com.docker.compose.project.working_dir":"/srv/demo",
						"com.docker.compose.project.config_files":"compose.yml,compose.prod.yml"
					},
					"Cmd":["serve"],
					"Entrypoint":["/entrypoint.sh"],
					"WorkingDir":"/app"
				},
				"State":{"Status":"running","Health":{"Status":"healthy"}},
				"HostConfig":{"Binds":["/host/data:/data:ro"],"NetworkMode":"demo_default","RestartPolicy":{"Name":"unless-stopped"}},
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostPort":"8080"}],"53/udp":[{"HostPort":"5353"}]},"Networks":{"demo_default":{"Aliases":["web","demo-web-1"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:image123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:image123","RepoDigests":["registry.example/web@sha256:repo123"]}`))
		default:
			handlerErrors.add(fmt.Sprintf("unexpected Docker API request: %s %s", r.Method, r.URL.String()))
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	discovery := NewDockerDiscovery(server.URL, zap.NewNop())
	containers, err := discovery.Discover(context.Background(), DockerDiscoveryTarget{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if errors := handlerErrors.all(); len(errors) > 0 {
		t.Fatalf("handler errors: %v", errors)
	}
	if len(containers) != 1 {
		t.Fatalf("expected one container, got %d", len(containers))
	}
	got := containers[0]
	if got.TargetName != "demo-web-1" || got.EnvironmentName != "prod" || got.SourceRuntime != "compose" {
		t.Fatalf("unexpected identity/runtime fields: %#v", got)
	}
	if got.ImageRepo != "registry.example/web" || got.ImageTag != "1.2.3" || got.ImageDigest != "sha256:repo123" {
		t.Fatalf("unexpected image fields: repo=%q tag=%q digest=%q", got.ImageRepo, got.ImageTag, got.ImageDigest)
	}
	if got.HealthStatus != domain.HealthStatusHealthy || !got.Adoptable || len(got.Warnings) != 0 {
		t.Fatalf("unexpected health/adoptable fields: health=%s adoptable=%v warnings=%v", got.HealthStatus, got.Adoptable, got.Warnings)
	}
	if got.Environment["APP_ENV"] != "prod" || got.Environment["EMPTY"] != "" {
		t.Fatalf("unexpected environment: %#v", got.Environment)
	}
	if strings.Join(got.Ports, ",") != "5353:53/udp,8080:80" {
		t.Fatalf("unexpected ports: %#v", got.Ports)
	}
	if len(got.Volumes) != 1 || got.Volumes[0] != "/host/data:/data:ro" {
		t.Fatalf("unexpected volumes: %#v", got.Volumes)
	}
	if got.Restart != "unless-stopped" || got.WorkingDir != "/app" || got.NetworkMode != "demo_default" {
		t.Fatalf("unexpected runtime config fields: %#v", got)
	}
	if got.Compose == nil || got.Compose.ProjectName != "demo" || got.Compose.ServiceName != "web" || len(got.Compose.ConfigFiles) != 2 {
		t.Fatalf("unexpected compose metadata: %#v", got.Compose)
	}
}

func TestDockerDiscoveryWarnsUnsupportedRuntimeShape(t *testing.T) {
	t.Parallel()

	got := normalizeDiscoveredContainer(DockerDiscoveryTarget{EnvironmentName: "prod"}, &dockerContainerInspect{
		ID:    "container-unsafe",
		Name:  "/unsafe",
		Image: "sha256:unsafe",
		Config: dockerContainerConfig{
			Image:  "registry.example/unsafe:latest",
			Labels: map[string]string{},
		},
		HostConfig: dockerContainerHostConfig{NetworkMode: "custom"},
		Mounts:     []dockerContainerMount{{Type: "tmpfs", Destination: "/cache"}},
		NetworkSettings: dockerContainerNetworkConfig{
			Ports: map[string][]dockerPortPublish{
				"80/tcp": {{HostIP: "127.0.0.1", HostPort: "8080"}, {HostIP: "::", HostPort: "8080"}},
			},
			Networks: map[string]dockerContainerAttachment{
				"custom": {Aliases: []string{"unexpected-alias"}},
			},
		},
	}, &dockerImageInspect{ID: "sha256:unsafe"})

	if got.Adoptable {
		t.Fatalf("expected unsupported runtime shape to be non-adoptable")
	}
	joined := strings.Join(got.Warnings, "\n")
	for _, want := range []string{
		"network aliases cannot be represented",
		"multiple host bindings for port 80/tcp are not supported",
		"unsupported mount type tmpfs",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected warning containing %q in %v", want, got.Warnings)
		}
	}
}

func assertStringSlice(t *testing.T, got any, want []string) {
	t.Helper()
	gotSlice, ok := got.([]any)
	if !ok {
		t.Fatalf("expected JSON array %v, got %#v", want, got)
	}
	if len(gotSlice) != len(want) {
		t.Fatalf("expected %v, got %#v", want, gotSlice)
	}
	for i := range want {
		if gotSlice[i] != want[i] {
			t.Fatalf("expected %v, got %#v", want, gotSlice)
		}
	}
}

func assertHostPort(t *testing.T, portBindings map[string]any, containerPort, hostIP, hostPort string) {
	t.Helper()

	bindings, ok := portBindings[containerPort].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("expected one binding for %s, got %#v", containerPort, portBindings[containerPort])
	}
	binding, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("expected binding object for %s, got %#v", containerPort, bindings[0])
	}
	if binding["HostPort"] != hostPort {
		t.Fatalf("expected HostPort %q for %s, got %#v", hostPort, containerPort, binding["HostPort"])
	}
	if hostIP == "" {
		if _, ok := binding["HostIp"]; ok && binding["HostIp"] != "" {
			t.Fatalf("expected empty HostIp for %s, got %#v", containerPort, binding["HostIp"])
		}
		return
	}
	if binding["HostIp"] != hostIP {
		t.Fatalf("expected HostIp %q for %s, got %#v", hostIP, containerPort, binding["HostIp"])
	}
}

type dockerHandlerErrors struct {
	mu     sync.Mutex
	errors []string
}

func newDockerHandlerErrors() *dockerHandlerErrors {
	return &dockerHandlerErrors{}
}

func (e *dockerHandlerErrors) add(message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errors = append(e.errors, message)
}

func (e *dockerHandlerErrors) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.errors...)
}
