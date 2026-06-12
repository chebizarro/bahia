package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddleware_Disabled(t *testing.T) {
	handler := Middleware(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("disabled auth should pass through, got status %d", w.Code)
	}
}

func TestMiddleware_DisabledNoPrincipal(t *testing.T) {
	var principal *Principal
	handler := Middleware(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if principal != nil {
		t.Error("disabled auth should not set a Principal")
	}
}

func TestMiddleware_EnabledNoValidator(t *testing.T) {
	handler := Middleware(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("enabled auth with no NIP-98 validator should fail closed, got status %d", w.Code)
	}
}

func TestMiddleware_MissingAuthHeader(t *testing.T) {
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: NewNIP98Validator(DefaultNIP98Config())})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing auth header should return 401, got %d", w.Code)
	}
}

func TestMiddleware_RejectsBearerScheme(t *testing.T) {
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: NewNIP98Validator(DefaultNIP98Config())})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	req.Header.Set("Authorization", "Bearer legacy.jwt.token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Bearer auth should return 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidScheme(t *testing.T) {
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: NewNIP98Validator(DefaultNIP98Config())})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsupported scheme should return 401, got %d", w.Code)
	}
}

func TestMiddleware_EmptyNostrToken(t *testing.T) {
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: NewNIP98Validator(DefaultNIP98Config())})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	req.Header.Set("Authorization", "Nostr ")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty Nostr token should return 401, got %d", w.Code)
	}
}

func TestMiddleware_ValidNIP98SetsPrincipal(t *testing.T) {
	validator := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost/test"
	ev := makeNIP98Event(t, http.MethodGet, url, time.Now())
	token := encodeEvent(t, ev)

	var principal *Principal
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: validator})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Nostr "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("valid NIP-98 should return 200, got %d: %s", w.Code, w.Body.String())
	}
	if principal == nil {
		t.Fatal("expected Principal in context, got nil")
	}
	wantPubkey := ev.PubKey.Hex()
	if principal.Subject != wantPubkey || principal.PubKey != wantPubkey {
		t.Errorf("principal should use NIP-98 pubkey, got %+v want %q", principal, wantPubkey)
	}
	if principal.Method != MethodNIP98 {
		t.Errorf("Principal.Method = %q, want %q", principal.Method, MethodNIP98)
	}
	if !principal.IsAuthenticated() {
		t.Error("Principal should be authenticated")
	}
}

func TestMiddlewareFromConfig_RejectsInvalidNIP98(t *testing.T) {
	handler := MiddlewareFromConfig(MiddlewareConfig{Enabled: true, NIP98Validator: NewNIP98Validator(DefaultNIP98Config())})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	req.Header.Set("Authorization", "Nostr not-valid-base64")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid NIP-98 should return 401, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Principal unit tests
// ---------------------------------------------------------------------------

func TestPrincipal_IsAuthenticated(t *testing.T) {
	tests := []struct {
		name string
		p    *Principal
		want bool
	}{
		{"nil", nil, false},
		{"no method", &Principal{Subject: "x"}, false},
		{"nip98", &Principal{Subject: "x", Method: MethodNIP98}, true},
		{"system", &Principal{Subject: "x", Method: MethodSystem}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.IsAuthenticated(); got != tc.want {
				t.Errorf("IsAuthenticated() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrincipal_HasRole(t *testing.T) {
	p := &Principal{Subject: "alice", Method: MethodNIP98, Roles: []string{"deployer", "viewer"}}

	if !p.HasRole("deployer") {
		t.Error("expected HasRole('deployer') = true")
	}
	if !p.HasRole("viewer") {
		t.Error("expected HasRole('viewer') = true")
	}
	if p.HasRole("admin") {
		t.Error("expected HasRole('admin') = false")
	}

	var nilP *Principal
	if nilP.HasRole("admin") {
		t.Error("nil principal should not have any role")
	}
}

func TestSystemPrincipal(t *testing.T) {
	p := SystemPrincipal("reconciler")
	if p.Subject != "reconciler" {
		t.Errorf("Subject = %q, want %q", p.Subject, "reconciler")
	}
	if p.Method != MethodSystem {
		t.Errorf("Method = %q, want %q", p.Method, MethodSystem)
	}
	if !p.HasRole("admin") {
		t.Error("system principal should have admin role")
	}
}

func TestGetPrincipal_RoundTrip(t *testing.T) {
	p := &Principal{Subject: "bob", Method: MethodNIP98, PubKey: "deadbeef"}
	ctx := ContextWithPrincipal(context.Background(), p)

	got := GetPrincipal(ctx)
	if got == nil {
		t.Fatal("expected principal, got nil")
	}
	if got.Subject != "bob" {
		t.Errorf("Subject = %q, want %q", got.Subject, "bob")
	}
	if got.PubKey != "deadbeef" {
		t.Errorf("PubKey = %q, want %q", got.PubKey, "deadbeef")
	}
	if got.Method != MethodNIP98 {
		t.Errorf("Method = %q, want %q", got.Method, MethodNIP98)
	}
}

func TestGetPrincipal_NoContext(t *testing.T) {
	p := GetPrincipal(context.Background())
	if p != nil {
		t.Error("expected nil principal from empty context")
	}
}
