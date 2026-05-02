// Package middleware provides HTTP middleware components.
package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// MetricsRecorder is the interface for recording HTTP metrics.
type MetricsRecorder interface {
	RecordHTTPRequest(method, path string, status int, duration time.Duration)
}

// Metrics returns a middleware that records HTTP request metrics.
func Metrics(recorder MetricsRecorder) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap the response writer to capture status code
			wrapped := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			// Get the route pattern if available (for chi router)
			path := r.URL.Path
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if pattern := routeCtx.RoutePattern(); pattern != "" {
					path = pattern
				}
			}

			recorder.RecordHTTPRequest(r.Method, path, wrapped.status, duration)
		})
	}
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Flush forwards flush support for streaming responses such as SSE.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController support.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// StatusCode returns the recorded status code.
func (r *statusRecorder) StatusCode() int {
	return r.status
}

// ParseStatusBucket returns a status code bucket (2xx, 3xx, 4xx, 5xx).
func ParseStatusBucket(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return strconv.Itoa(status)
	}
}
