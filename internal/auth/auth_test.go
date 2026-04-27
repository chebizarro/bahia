package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testSecret = "test-secret-key-for-bahia"

func TestMiddleware_Disabled(t *testing.T) {
	handler := Middleware(false, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := Middleware(false, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestMiddleware_EnabledNoSecret(t *testing.T) {
	handler := Middleware(true, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("enabled auth with no secret should fail closed, got status %d", w.Code)
	}
}

func TestMiddleware_MissingAuthHeader(t *testing.T) {
	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing auth header should return 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidScheme(t *testing.T) {
	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsupported scheme should return 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidToken(t *testing.T) {
	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid token should return 401, got %d", w.Code)
	}
}

func TestMiddleware_WrongSecret(t *testing.T) {
	token, err := GenerateToken("testuser", "different-secret", time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret should return 401, got %d", w.Code)
	}
}

func TestMiddleware_ExpiredToken(t *testing.T) {
	token, err := GenerateToken("testuser", testSecret, -time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired token should return 401, got %d", w.Code)
	}
}

func TestMiddleware_ValidToken_SetsPrincipalAndClaims(t *testing.T) {
	token, err := GenerateToken("testuser", testSecret, time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	var principal *Principal
	var claims *Claims
	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = GetPrincipal(r.Context())
		claims = GetClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid token should return 200, got %d", w.Code)
	}

	// Verify Principal.
	if principal == nil {
		t.Fatal("expected Principal in context, got nil")
	}
	if principal.Subject != "testuser" {
		t.Errorf("Principal.Subject = %q, want %q", principal.Subject, "testuser")
	}
	if principal.Method != MethodJWT {
		t.Errorf("Principal.Method = %q, want %q", principal.Method, MethodJWT)
	}
	if !principal.IsAuthenticated() {
		t.Error("Principal should be authenticated")
	}

	// Verify legacy Claims are still set.
	if claims == nil {
		t.Fatal("expected Claims in context, got nil")
	}
	if claims.Subject != "testuser" {
		t.Errorf("Claims.Subject = %q, want %q", claims.Subject, "testuser")
	}
	if claims.Issuer != "bahia" {
		t.Errorf("Claims.Issuer = %q, want %q", claims.Issuer, "bahia")
	}
}

func TestMiddleware_NostrScheme_NotYetImplemented(t *testing.T) {
	handler := Middleware(true, testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Nostr base64event")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("NIP-98 auth should return 401 (not yet implemented), got %d", w.Code)
	}
}

func TestValidateToken_MalformedToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no dots", "abcdef"},
		{"one dot", "abc.def"},
		{"four dots", "a.b.c.d"},
		{"empty bearer", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateToken(tc.token, testSecret)
			if err == nil {
				t.Error("expected error for malformed token")
			}
		})
	}
}

func TestGenerateAndValidateToken(t *testing.T) {
	token, err := GenerateToken("deploy-bot", testSecret, 5*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate: %v", err)
	}

	claims, err := ValidateToken(token, testSecret)
	if err != nil {
		t.Fatalf("failed to validate: %v", err)
	}

	if claims.Subject != "deploy-bot" {
		t.Errorf("expected subject 'deploy-bot', got '%s'", claims.Subject)
	}

	if claims.ExpiresAt <= claims.IssuedAt {
		t.Error("expected expiry after issued time")
	}
}

func TestGetClaims_NoClaims(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	claims := GetClaims(req.Context())
	if claims != nil {
		t.Error("expected nil claims when no auth context")
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
		{"jwt", &Principal{Subject: "x", Method: MethodJWT}, true},
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
	p := &Principal{Subject: "alice", Method: MethodJWT, Roles: []string{"deployer", "viewer"}}

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

func TestMiddlewareFromConfig(t *testing.T) {
	token, err := GenerateToken("configuser", testSecret, time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	var principal *Principal
	handler := MiddlewareFromConfig(MiddlewareConfig{
		Enabled:   true,
		JWTSecret: testSecret,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if principal == nil || principal.Subject != "configuser" {
		t.Errorf("MiddlewareFromConfig should set principal, got %+v", principal)
	}
}
