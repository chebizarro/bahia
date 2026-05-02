package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type mockMetricsRecorder struct {
	calls []metricsCall
}

type metricsCall struct {
	method   string
	path     string
	status   int
	duration time.Duration
}

func (m *mockMetricsRecorder) RecordHTTPRequest(method, path string, status int, duration time.Duration) {
	m.calls = append(m.calls, metricsCall{method, path, status, duration})
}

func TestMetrics_RecordsRequest(t *testing.T) {
	recorder := &mockMetricsRecorder{}
	handler := Metrics(recorder)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(recorder.calls))
	}

	call := recorder.calls[0]
	if call.method != "GET" {
		t.Errorf("expected method GET, got %s", call.method)
	}
	if call.path != "/test" {
		t.Errorf("expected path /test, got %s", call.path)
	}
	if call.status != http.StatusOK {
		t.Errorf("expected status 200, got %d", call.status)
	}
	if call.duration <= 0 {
		t.Errorf("expected positive duration, got %v", call.duration)
	}
}

func TestMetrics_Records500Status(t *testing.T) {
	recorder := &mockMetricsRecorder{}
	handler := Metrics(recorder)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest("POST", "/error", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(recorder.calls))
	}

	if recorder.calls[0].status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", recorder.calls[0].status)
	}
}

func TestMetrics_DefaultStatus(t *testing.T) {
	recorder := &mockMetricsRecorder{}
	handler := Metrics(recorder)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write without explicit status -> defaults to 200
		w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if recorder.calls[0].status != http.StatusOK {
		t.Errorf("expected default status 200, got %d", recorder.calls[0].status)
	}
}

func TestStatusRecorder_Write(t *testing.T) {
	w := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	n, err := sr.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if sr.status != http.StatusOK {
		t.Errorf("expected status 200, got %d", sr.status)
	}
}

func TestStatusRecorder_WriteHeader(t *testing.T) {
	w := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	sr.WriteHeader(http.StatusNotFound)
	sr.WriteHeader(http.StatusOK) // Second call should be ignored

	if sr.status != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", sr.status)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

func TestStatusRecorder_Flush(t *testing.T) {
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	flusher, ok := any(sr).(http.Flusher)
	if !ok {
		t.Fatalf("statusRecorder should implement http.Flusher")
	}
	flusher.Flush()

	if !w.flushed {
		t.Fatalf("expected underlying flusher to be called")
	}
}

func TestParseStatusBucket(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{200, "2xx"},
		{201, "2xx"},
		{299, "2xx"},
		{301, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{500, "5xx"},
		{503, "5xx"},
		{100, "100"},
	}

	for _, tt := range tests {
		got := ParseStatusBucket(tt.status)
		if got != tt.want {
			t.Errorf("ParseStatusBucket(%d) = %s, want %s", tt.status, got, tt.want)
		}
	}
}
