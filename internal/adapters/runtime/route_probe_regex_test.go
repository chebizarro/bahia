package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const jsonStatusOKPattern = `(?s).*"status"\s*:\s*"ok".*`

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestProbeRouteRegexAssertsJSONFieldRegardlessOfOrder is the bahia-j9liz
// acceptance criterion end to end: a JSON health endpoint is asserted on a
// field without depending on serialization order or formatting.
func TestProbeRouteRegexAssertsJSONFieldRegardlessOfOrder(t *testing.T) {
	tests := []struct {
		name string
		body string
		want domain.RouteCanaryClassification
	}{
		{"field first", `{"status":"ok","version":"1.24.0","db":"up"}`, domain.RouteCanaryClassificationRouteOK},
		{"field last", `{"db":"up","version":"1.24.0","status":"ok"}`, domain.RouteCanaryClassificationRouteOK},
		{"pretty printed", "{\n  \"version\": \"1.24.0\",\n  \"status\": \"ok\"\n}\n", domain.RouteCanaryClassificationRouteOK},
		{"degraded", `{"status":"degraded","version":"1.24.0"}`, domain.RouteCanaryClassificationBodyMismatch},
		{"html shell", `<!doctype html><html><body>ok</body></html>`, domain.RouteCanaryClassificationBodyMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := httpTarget(t, jsonServer(t, test.body))
			target.ExpectedBodyRegex = jsonStatusOKPattern

			observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
			if err != nil {
				t.Fatalf("probe error: %v", err)
			}
			if got := domain.ClassifyRouteObservation(observation, time.Now()); got != test.want {
				t.Fatalf("got %q, want %q (body %q)", got, test.want, observation.Body)
			}
		})
	}
}

// TestProbeRouteRegexMatchesRawBodyNotSanitizedEvidence proves the regex, like
// the substring form, runs against the raw bounded body, so evidence redaction
// can never change whether the assertion passes.
func TestProbeRouteRegexMatchesRawBodyNotSanitizedEvidence(t *testing.T) {
	target := httpTarget(t, jsonServer(t, `{"status":"ok","echo":"Authorization: Bearer supersecrettokenvalue"}`))
	target.ExpectedBodyRegex = `(?s).*Bearer supersecrettokenvalue.*`

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !observation.BodyMatched {
		t.Fatal("regex matching must run against the raw body so redaction cannot change semantics")
	}
	if strings.Contains(observation.Body, "supersecrettokenvalue") {
		t.Fatalf("stored body evidence was not sanitized: %q", observation.Body)
	}
}

// TestProbeRouteRegexSeesOnlyTheBoundedBody pins the documented limit: a marker
// beyond the bounded read is invisible to the assertion, so the anchored
// pattern fails rather than reading an unbounded body.
func TestProbeRouteRegexSeesOnlyTheBoundedBody(t *testing.T) {
	body := `{"padding":"` + strings.Repeat("x", maxRouteProbeBodyBytes) + `","status":"ok"}`
	target := httpTarget(t, jsonServer(t, body))
	target.ExpectedBodyRegex = jsonStatusOKPattern

	observation, err := RouteProber{}.ProbeRoute(context.Background(), target)
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if observation.BodyMatched {
		t.Fatal("a marker beyond the bounded body must not be visible to the assertion")
	}
}

func TestProbeRouteRejectsTargetWithInvalidRegex(t *testing.T) {
	target := httpTarget(t, jsonServer(t, `{"status":"ok"}`))
	target.ExpectedBodyRegex = `a)|(b`
	if _, err := (RouteProber{}).ProbeRoute(context.Background(), target); err == nil {
		t.Fatal("a target with an invalid regex must be rejected as a configuration fault")
	}
}
