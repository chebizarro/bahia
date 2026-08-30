package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestDockerObserverObserveInstanceInspectsExactContainerAndStats(t *testing.T) {
	serviceID, environmentID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.44/containers/json":
			_, _ = fmt.Fprintf(w, `[{"Id":"container-1","Names":["/api"],"State":"running"}]`)
		case "/v1.44/containers/container-1/json":
			_, _ = fmt.Fprint(w, `{"Id":"container-1","RestartCount":7,"State":{"Status":"running","ExitCode":0,"Error":"token=secret-value","StartedAt":"2026-08-29T10:11:12.123456789Z","FinishedAt":"0001-01-01T00:00:00Z","Health":{"Status":"unhealthy"}}}`)
		case "/v1.44/containers/container-1/stats":
			if r.URL.Query().Get("stream") != "false" {
				t.Errorf("stats stream = %q, want false", r.URL.Query().Get("stream"))
			}
			_, _ = fmt.Fprint(w, `{"memory_stats":{"usage":1234,"max_usage":2345,"limit":4096,"stats":{"peak":3456}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
	key := domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID, RuntimeTargetName: "api"}
	observation, err := observer.ObserveInstance(context.Background(), key)
	if err != nil {
		t.Fatalf("ObserveInstance() error = %v", err)
	}
	if observation.Status != domain.InstanceHealthStatusUnhealthy || observation.HealthStatus != "unhealthy" {
		t.Fatalf("status = %q health = %q", observation.Status, observation.HealthStatus)
	}
	if observation.RestartCount != 7 || observation.MemoryCurrentBytes != 1234 || observation.MemoryPeakBytes != 3456 || observation.MemoryLimitBytes != 4096 {
		t.Fatalf("unexpected counters/memory: %+v", observation)
	}
	if observation.StartedAt == nil || observation.FinishedAt != nil {
		t.Fatalf("unexpected timestamps: started=%v finished=%v", observation.StartedAt, observation.FinishedAt)
	}
	if observation.Detail != "[REDACTED]" {
		t.Fatalf("Detail = %q, want redacted", observation.Detail)
	}
}

func TestDockerObserverObserveInstanceMapsOOMKilledAndRestarting(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		wantStatus domain.InstanceHealthStatus
	}{
		{name: "oom", state: `{"Status":"exited","OOMKilled":true,"ExitCode":137}`, wantStatus: domain.InstanceHealthStatusOOMKilled},
		{name: "restarting", state: `{"Status":"restarting","Restarting":true,"ExitCode":1}`, wantStatus: domain.InstanceHealthStatusRestartLoop},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1.44/containers/json":
					_, _ = fmt.Fprint(w, `[{"Id":"target","Names":["/target"],"State":"exited"}]`)
				case "/v1.44/containers/target/json":
					_, _ = fmt.Fprintf(w, `{"Id":"target","RestartCount":4,"State":%s}`, test.state)
				case "/v1.44/containers/target/stats":
					http.Error(w, "unavailable", http.StatusNotFound)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			observer := &DockerObserver{httpClient: server.Client(), host: server.URL, logger: zap.NewNop()}
			observation, err := observer.ObserveInstance(context.Background(), domain.ManagedInstanceKey{RuntimeTargetName: "target"})
			if err != nil {
				t.Fatalf("ObserveInstance() error = %v", err)
			}
			if observation.Status != test.wantStatus || observation.RestartCount != 4 {
				t.Fatalf("observation = %+v, want status %q restart count 4", observation, test.wantStatus)
			}
		})
	}
}
