package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestComposeRuntimeObserveInstanceUsesConcreteContainerID(t *testing.T) {
	composeBin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' '{"ID":"compose-container","Service":"api","Image":"example/api:latest","State":"running","Status":"Up"}'
`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.44/containers/compose-container/json":
			_, _ = fmt.Fprint(w, `{"Id":"compose-container","RestartCount":2,"State":{"Status":"running","StartedAt":"2026-08-29T10:00:00Z","Health":{"Status":"healthy"}}}`)
		case "/v1.44/containers/compose-container/stats":
			_, _ = fmt.Fprint(w, `{"memory_stats":{"usage":100,"max_usage":200,"limit":300}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime := &ComposeRuntime{binary: composeBin, dockerHost: server.URL, logger: zap.NewNop()}
	observation, err := runtime.ObserveInstance(context.Background(), domain.ManagedInstanceKey{RuntimeTargetName: "api"})
	if err != nil {
		t.Fatalf("ObserveInstance() error = %v", err)
	}
	if observation.Status != domain.InstanceHealthStatusHealthy || observation.RestartCount != 2 {
		t.Fatalf("observation = %+v", observation)
	}
	if observation.MemoryCurrentBytes != 100 || observation.MemoryPeakBytes != 200 || observation.MemoryLimitBytes != 300 {
		t.Fatalf("memory observation = %+v", observation)
	}
}

func TestComposeRuntimeUsesArgumentSeparatorForPsAndRestart(t *testing.T) {
	composeBin := writeFakeComposeBinary(t, `#!/bin/sh
case "$*" in
	"ps --format json -- api") printf '%s' '{"ID":"compose-container","Service":"api","State":"running","Status":"Up"}' ;;
	"restart -- api") exit 0 ;;
	"stop -- api") exit 0 ;;
	*) echo "unexpected args: $*" >&2; exit 64 ;;
esac
`)
	runtime := &ComposeRuntime{binary: composeBin, logger: zap.NewNop()}
	key := domain.ManagedInstanceKey{RuntimeTargetName: "api"}
	if err := runtime.RestartInstance(context.Background(), key); err != nil {
		t.Fatalf("RestartInstance() error = %v", err)
	}
	if err := runtime.StopInstance(context.Background(), key); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
}

func TestComposeRuntimeRejectsMismatchedAndOptionShapedTargets(t *testing.T) {
	composeBin := writeFakeComposeBinary(t, `#!/bin/sh
printf '%s' '{"ID":"decoy-container","Service":"decoy","State":"running","Status":"Up"}'
`)
	runtime := &ComposeRuntime{binary: composeBin, logger: zap.NewNop()}

	_, err := runtime.ObserveInstance(context.Background(), domain.ManagedInstanceKey{RuntimeTargetName: "api"})
	if err == nil {
		t.Fatal("ObserveInstance() accepted a compose decoy")
	}
	_, err = runtime.ObserveInstance(context.Background(), domain.ManagedInstanceKey{RuntimeTargetName: "--all"})
	if err == nil {
		t.Fatal("ObserveInstance() accepted an option-shaped service name")
	}
}
