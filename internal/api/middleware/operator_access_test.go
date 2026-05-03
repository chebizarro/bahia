package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/auth"
)

func TestRequireOperator(t *testing.T) {
	tests := []struct {
		name      string
		principal *auth.Principal
		cfg       OperatorAccessConfig
		want      int
	}{
		{
			name: "missing principal",
			cfg:  OperatorAccessConfig{AllowedSubjects: []string{"ops"}},
			want: http.StatusUnauthorized,
		},
		{
			name:      "subject allowed",
			principal: &auth.Principal{Subject: "ops", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{AllowedSubjects: []string{"ops"}},
			want:      http.StatusOK,
		},
		{
			name:      "pubkey allowed case insensitive",
			principal: &auth.Principal{Subject: "npub-user", PubKey: "ABC123", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{AllowedPubkeys: []string{"abc123"}},
			want:      http.StatusOK,
		},
		{
			name:      "nip05 email allowed",
			principal: &auth.Principal{Subject: "user", NIP05: "Ops@Example.com", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{AllowedEmails: []string{"ops@example.com"}},
			want:      http.StatusOK,
		},
		{
			name:      "subject email allowed",
			principal: &auth.Principal{Subject: "ops@example.com", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{AllowedEmails: []string{"ops@example.com"}},
			want:      http.StatusOK,
		},
		{
			name:      "authenticated but not allowed",
			principal: &auth.Principal{Subject: "developer", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{AllowedSubjects: []string{"ops"}},
			want:      http.StatusForbidden,
		},
		{
			name:      "empty allowlist denies",
			principal: &auth.Principal{Subject: "ops", Method: auth.MethodNIP98},
			cfg:       OperatorAccessConfig{},
			want:      http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.principal != nil {
				req = req.WithContext(auth.ContextWithPrincipal(req.Context(), tt.principal))
			}
			w := httptest.NewRecorder()

			RequireOperator(tt.cfg)(next).ServeHTTP(w, req)

			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.want, w.Body.String())
			}
			if called != (tt.want == http.StatusOK) {
				t.Fatalf("next called = %v, want %v", called, tt.want == http.StatusOK)
			}
		})
	}
}
