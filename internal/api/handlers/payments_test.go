package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaymentHandlerFailsClosedWithoutService(t *testing.T) {
	h := NewPaymentHandler(nil)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "run cost",
			method: http.MethodGet,
			path:   "/deployments/runs/00000000-0000-0000-0000-000000000001/cost",
			call:   h.GetRunCost,
		},
		{
			name:   "history",
			method: http.MethodGet,
			path:   "/payments/history?worker=worker-pubkey",
			call:   h.GetPaymentHistory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			tt.call(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "payment service is not configured") {
				t.Fatalf("body = %q, want explicit payment service configuration error", rec.Body.String())
			}
		})
	}
}
