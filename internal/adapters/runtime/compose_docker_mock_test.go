//go:build ignore

package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestMockDockerAPIDirect(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		t.Logf("MOCK: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("API-Version", "1.44")
			w.Header().Set("OSType", "linux")
		case "/v1.44/containers/running-id/json":
			fmt.Fprint(w, `{"Id":"running-id","Image":"sha256:runningimage","Config":{"Image":"registry.example/app:v2","Labels":{"bahia.desired_hash":"sha256:reviewed"}}}`)
		case "/v1.44/images/sha256:runningimage/json":
			fmt.Fprint(w, `{"Id":"sha256:runningimage","RepoDigests":["registry.example/app@sha256:runningdigest"]}`)
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := &ComposeRuntime{dockerHost: srv.URL, logger: zap.NewNop()}
	
	imageID, configuredImage, desiredHash, ok := r.inspectComposeContainerImage(context.Background(), r.logger, "running-id")
	t.Logf("inspectComposeContainerImage: imageID=%q configuredImage=%q desiredHash=%q ok=%v", imageID, configuredImage, desiredHash, ok)
	t.Logf("requests: %v", requests)
	
	repo, digest := r.inspectDockerImage(context.Background(), r.logger, "sha256:runningimage", "registry.example/app:v2")
	t.Logf("inspectDockerImage: repo=%q digest=%q", repo, digest)
	t.Logf("requests: %v", requests)
}
