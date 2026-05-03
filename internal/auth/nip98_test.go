package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// testNIP98Key is a throwaway private key for tests only.
const testNIP98Key = "9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b"

func makeNIP98Event(t *testing.T, method, url string, createdAt time.Time) nostr.Event {
	t.Helper()
	ev := nostr.Event{
		Kind:      kindHTTPAuth,
		Content:   "",
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags: nostr.Tags{
			{"u", url},
			{"method", method},
		},
	}
	if err := ev.Sign(testNIP98Key); err != nil {
		t.Fatalf("failed to sign NIP-98 event: %v", err)
	}
	return ev
}

func encodeEvent(t *testing.T, ev nostr.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestNIP98_ValidEvent(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost:8080/api/v1/services"
	ev := makeNIP98Event(t, "GET", url, time.Now())
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	principal, err := v.Validate(token, req)
	if err != nil {
		t.Fatalf("Validate() error: %v", err)
	}

	if principal.Method != MethodNIP98 {
		t.Errorf("Method = %q, want %q", principal.Method, MethodNIP98)
	}
	if principal.PubKey != ev.PubKey {
		t.Errorf("PubKey = %q, want %q", principal.PubKey, ev.PubKey)
	}
	if principal.Subject != ev.PubKey {
		t.Errorf("Subject should equal PubKey for NIP-98")
	}
	if !principal.IsAuthenticated() {
		t.Error("principal should be authenticated")
	}
}

func TestNIP98_WrongKind(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost:8080/api/v1/services"

	ev := nostr.Event{
		Kind:      1, // wrong kind
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"u", url},
			{"method", "GET"},
		},
	}
	_ = ev.Sign(testNIP98Key)
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for wrong kind")
	}
}

func TestNIP98_EventTooOld(t *testing.T) {
	v := NewNIP98Validator(NIP98Config{MaxSkew: 30 * time.Second})
	url := "http://localhost:8080/api/v1/services"
	ev := makeNIP98Event(t, "GET", url, time.Now().Add(-2*time.Minute))
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for stale event")
	}
}

func TestNIP98_URLMismatch(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	ev := makeNIP98Event(t, "GET", "http://localhost:8080/api/v1/services", time.Now())
	token := encodeEvent(t, ev)

	// Request goes to a different URL.
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/v1/builds", nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for URL mismatch")
	}
}

func TestNIP98_MethodMismatch(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost:8080/api/v1/services"
	ev := makeNIP98Event(t, "POST", url, time.Now())
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for method mismatch")
	}
}

func TestNIP98_ReplayProtection(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost:8080/api/v1/services"
	ev := makeNIP98Event(t, "GET", url, time.Now())
	token := encodeEvent(t, ev)

	req1 := httptest.NewRequest(http.MethodGet, url, nil)
	_, err := v.Validate(token, req1)
	if err != nil {
		t.Fatalf("first validation should succeed: %v", err)
	}

	// Same event replayed.
	req2 := httptest.NewRequest(http.MethodGet, url, nil)
	_, err = v.Validate(token, req2)
	if err == nil {
		t.Error("replay of same event should be rejected")
	}
}

func TestNIP98_InvalidBase64(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/test", nil)
	_, err := v.Validate("not-valid-base64!!!", req)
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestNIP98_InvalidJSON(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	token := base64.StdEncoding.EncodeToString([]byte("this is not json"))
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/test", nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestNIP98_MissingURLTag(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())

	ev := nostr.Event{
		Kind:      kindHTTPAuth,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"method", "GET"},
			// no "u" tag
		},
	}
	_ = ev.Sign(testNIP98Key)
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/test", nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for missing 'u' tag")
	}
}

func TestNIP98_MissingMethodTag(t *testing.T) {
	v := NewNIP98Validator(DefaultNIP98Config())
	url := "http://localhost:8080/test"

	ev := nostr.Event{
		Kind:      kindHTTPAuth,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"u", url},
			// no "method" tag
		},
	}
	_ = ev.Sign(testNIP98Key)
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	_, err := v.Validate(token, req)
	if err == nil {
		t.Error("expected error for missing 'method' tag")
	}
}

func TestNIP98_SeenCountAndPurge(t *testing.T) {
	v := NewNIP98Validator(NIP98Config{MaxSkew: 60 * time.Second})
	url := "http://localhost:8080/api/v1/services"

	ev := makeNIP98Event(t, "GET", url, time.Now())
	token := encodeEvent(t, ev)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	_, _ = v.Validate(token, req)

	if v.SeenCount() != 1 {
		t.Errorf("SeenCount() = %d, want 1", v.SeenCount())
	}

	// Manually set all entries as expired and purge.
	v.mu.Lock()
	for id := range v.seen {
		v.seen[id] = time.Now().Add(-time.Minute)
	}
	v.mu.Unlock()

	v.purgeExpired()
	if v.SeenCount() != 0 {
		t.Errorf("SeenCount() after purge = %d, want 0", v.SeenCount())
	}
}

func TestMiddleware_NostrScheme_Integration(t *testing.T) {
	validator := NewNIP98Validator(DefaultNIP98Config())

	url := "http://localhost:8080/api/v1/services"
	ev := makeNIP98Event(t, "GET", url, time.Now())
	token := encodeEvent(t, ev)

	var principal *Principal
	handler := MiddlewareFromConfig(MiddlewareConfig{
		Enabled:        true,
		NIP98Validator: validator,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Nostr "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if principal == nil {
		t.Fatal("expected principal, got nil")
	}
	if principal.Method != MethodNIP98 {
		t.Errorf("Method = %q, want %q", principal.Method, MethodNIP98)
	}
	if principal.PubKey == "" {
		t.Error("expected PubKey to be set")
	}
}

func TestMiddleware_NostrScheme_NoValidator(t *testing.T) {
	// NIP98Validator is nil → auth fails closed before request handling.
	handler := MiddlewareFromConfig(MiddlewareConfig{
		Enabled: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/test", nil)
	req.Header.Set("Authorization", "Nostr sometoken")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when NIP98Validator is nil, got %d", w.Code)
	}
}
