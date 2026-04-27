package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

func TestDockerDeployAddsPortBindingsAndVolumes(t *testing.T) {
	t.Parallel()

	createBodies := make(chan map[string]any, 1)
	handlerErrors := newDockerHandlerErrors()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.43/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1.43/containers/create":
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1.43/containers/container-123/start":
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
		Ports:   []string{"8080:80", "5353:53/udp", " 8443:443 "},
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
	assertHostPort(t, portBindings, "80/tcp", "8080")
	assertHostPort(t, portBindings, "53/udp", "5353")
	assertHostPort(t, portBindings, "443/tcp", "8443")

	binds, ok := hostConfig["Binds"].([]any)
	if !ok {
		t.Fatalf("expected HostConfig.Binds array, got %#v", hostConfig["Binds"])
	}
	if len(binds) != 2 || binds[0] != "/var/lib/api:/data:ro" || binds[1] != "/tmp/api-cache:/cache" {
		t.Fatalf("unexpected binds: %#v", binds)
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

func assertHostPort(t *testing.T, portBindings map[string]any, containerPort, hostPort string) {
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
