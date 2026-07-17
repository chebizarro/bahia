package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestDockerEngineControlClientPullImageSurfacesStreamedError(t *testing.T) {
	t.Parallel()

	const image = "registry.example.com/team/app:tag+build?x=1&y=2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fromImage"); got != image {
			t.Errorf("fromImage = %q, want %q", got, image)
		}
		if got := len(r.URL.Query()); got != 1 {
			t.Errorf("query parameter count = %d, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"Pulling manifest\"}\n"))
		_, _ = w.Write([]byte("{\"errorDetail\":{\"message\":\"manifest unknown\"},\"error\":\"manifest unknown\"}\n"))
	}))
	defer server.Close()

	client := &dockerEngineControlClient{observer: &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}}

	err := client.PullImage(context.Background(), image)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("PullImage error = %v, want streamed manifest error", err)
	}
}

func TestDockerEngineControlClientPullImageRejectsMalformedStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"Pulling\"}\n{not-json"))
	}))
	defer server.Close()

	client := &dockerEngineControlClient{observer: &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}}

	err := client.PullImage(context.Background(), "example.com/app:latest")
	if err == nil || !strings.Contains(err.Error(), "decoding docker pull progress") {
		t.Fatalf("PullImage error = %v, want stream decode error", err)
	}
}
