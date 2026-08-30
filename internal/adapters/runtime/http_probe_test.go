package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPProberExpectedStatusAndSanitizedBoundedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
		_, _ = w.Write([]byte("token=secret-value " + strings.Repeat("x", 700)))
	}))
	defer server.Close()

	result := (HTTPProber{}).Probe(context.Background(), HTTPProbeConfig{
		Method:            http.MethodPost,
		URL:               server.URL,
		Timeout:           time.Second,
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 204,
	})
	if !result.Successful || result.StatusCode != http.StatusNoContent {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Detail, "secret-value") || len(result.Detail) > maxHTTPProbeBodyBytes {
		t.Fatalf("detail was not sanitized/bounded: len=%d detail=%q", len(result.Detail), result.Detail)
	}
}

func TestHTTPProberEnforcesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	result := (HTTPProber{}).Probe(context.Background(), HTTPProbeConfig{URL: server.URL, Timeout: 20 * time.Millisecond})
	if result.Successful || result.Error == "" {
		t.Fatalf("result = %+v, want timeout error", result)
	}
	if result.Duration > time.Second {
		t.Fatalf("probe timeout was not enforced: %s", result.Duration)
	}
}

func TestHTTPProberLimitsRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()

	result := (HTTPProber{}).Probe(context.Background(), HTTPProbeConfig{URL: server.URL, Timeout: time.Second})
	if result.Successful || !strings.Contains(result.Error, "redirect limit exceeded") {
		t.Fatalf("result = %+v, want redirect limit error", result)
	}
}
