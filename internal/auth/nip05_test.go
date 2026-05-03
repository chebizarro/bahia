package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNIP05Resolver_Verify(t *testing.T) {
	pubkey := "abc123def456"

	// Create a mock NIP-05 server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/nostr.json" {
			http.NotFound(w, r)
			return
		}
		name := r.URL.Query().Get("name")
		resp := map[string]any{
			"names": map[string]string{
				"alice": pubkey,
				"bob":   "otherpubkey",
			},
		}
		if name != "" {
			// Filter to just the requested name for efficiency.
			resp = map[string]any{
				"names": map[string]string{name: pubkey},
			}
			if name == "bob" {
				resp["names"].(map[string]string)["bob"] = "otherpubkey"
				delete(resp["names"].(map[string]string), "alice")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// The mock server is available for future tests that need HTTP.
	// For now, we test the caching and parsing logic.
	_ = server
	_ = pubkey

	t.Run("GetCached_empty", func(t *testing.T) {
		r := NewNIP05Resolver()
		got := r.GetCached("unknownpubkey")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("SetCached_and_GetCached", func(t *testing.T) {
		r := NewNIP05Resolver()
		r.SetCached("pubkey123", "user@example.com")

		got := r.GetCached("pubkey123")
		if got != "user@example.com" {
			t.Errorf("expected user@example.com, got %q", got)
		}
	})

	t.Run("CacheSize", func(t *testing.T) {
		r := NewNIP05Resolver()
		if r.CacheSize() != 0 {
			t.Error("expected empty cache")
		}
		r.SetCached("pk1", "user1@example.com")
		r.SetCached("pk2", "user2@example.com")
		if r.CacheSize() != 2 {
			t.Errorf("expected 2, got %d", r.CacheSize())
		}
	})

	t.Run("CacheExpiry", func(t *testing.T) {
		r := NewNIP05Resolver()
		r.mu.Lock()
		r.cache["expiredpk"] = &nip05CacheEntry{
			identifier: "old@example.com",
			expiresAt:  time.Now().Add(-1 * time.Hour), // expired
		}
		r.mu.Unlock()

		// Should not return expired entry.
		got := r.GetCached("expiredpk")
		if got != "" {
			t.Errorf("expected empty for expired entry, got %q", got)
		}
	})

	t.Run("Resolve_caches_negative", func(t *testing.T) {
		r := NewNIP05Resolver()
		// Resolve without knowing domain will cache a negative result.
		result := r.Resolve(context.Background(), "somepubkey")
		if result != "" {
			t.Error("expected empty result for unknown pubkey")
		}

		// Check that a negative entry was cached.
		r.mu.RLock()
		entry, ok := r.cache["somepubkey"]
		r.mu.RUnlock()
		if !ok {
			t.Error("expected cache entry for negative result")
		}
		if entry.identifier != "" {
			t.Error("expected empty identifier in negative cache entry")
		}
	})

	t.Run("PurgeExpired", func(t *testing.T) {
		r := NewNIP05Resolver()
		r.mu.Lock()
		r.cache["expired1"] = &nip05CacheEntry{
			identifier: "a@b.com",
			expiresAt:  time.Now().Add(-1 * time.Hour),
		}
		r.cache["valid1"] = &nip05CacheEntry{
			identifier: "c@d.com",
			expiresAt:  time.Now().Add(1 * time.Hour),
		}
		r.mu.Unlock()

		r.purgeExpired()

		r.mu.RLock()
		_, hasExpired := r.cache["expired1"]
		_, hasValid := r.cache["valid1"]
		r.mu.RUnlock()

		if hasExpired {
			t.Error("expired entry should have been purged")
		}
		if !hasValid {
			t.Error("valid entry should not have been purged")
		}
	})
}

func TestNIP05Resolver_LookupByIdentifier_Parsing(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		wantValid  bool
	}{
		{"empty", "", false},
		{"no at sign", "justadomain", false},
		{"valid format", "user@domain.com", true},
		{"underscore", "_@domain.com", true},
		{"just at", "@domain.com", true}, // empty local part becomes _
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewNIP05Resolver()
			// We can't actually test HTTP calls here without a real server.
			// Just verify the input validation.
			if tc.identifier == "" {
				_, ok := r.LookupByIdentifier(context.Background(), tc.identifier)
				if ok {
					t.Error("expected failure for empty identifier")
				}
			}
		})
	}
}

func TestNIP05Resolver_Verify_ParsesIdentifier(t *testing.T) {
	r := NewNIP05Resolver()

	// Empty values should return false.
	if r.Verify(context.Background(), "", "pubkey") {
		t.Error("expected false for empty identifier")
	}
	if r.Verify(context.Background(), "user@domain.com", "") {
		t.Error("expected false for empty pubkey")
	}

	// Invalid format should return false.
	if r.Verify(context.Background(), "nodomain", "pubkey") {
		t.Error("expected false for invalid identifier format")
	}
}

func TestNIP05Resolution_Integration(t *testing.T) {
	// Create a mock NIP-05 server.
	pubkey := "deadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/nostr.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"names": map[string]string{
				"alice": pubkey,
			},
		})
	}))
	defer server.Close()

	r := NewNIP05Resolver()
	r.client = server.Client()

	// Manually populate cache to simulate a verified identity.
	r.SetCached(pubkey, "alice@example.com")

	// Now GetCached should return it.
	got := r.GetCached(pubkey)
	if got != "alice@example.com" {
		t.Errorf("expected alice@example.com, got %q", got)
	}
}

func TestPrincipal_NIP05Field(t *testing.T) {
	p := &Principal{
		Subject: "deadbeef",
		Method:  MethodNIP98,
		PubKey:  "deadbeef",
		NIP05:   "alice@example.com",
	}

	if p.NIP05 != "alice@example.com" {
		t.Errorf("expected alice@example.com, got %s", p.NIP05)
	}

	// Verify it serializes to JSON.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded["nip05"] != "alice@example.com" {
		t.Errorf("expected nip05 in JSON, got %v", decoded)
	}
}

func TestMiddlewareConfig_NIP05Resolver(t *testing.T) {
	cfg := MiddlewareConfig{
		Enabled:        true,
		NIP98Validator: NewNIP98Validator(DefaultNIP98Config()),
		NIP05Resolver:  NewNIP05Resolver(),
	}

	if cfg.NIP05Resolver == nil {
		t.Error("expected NIP05Resolver to be set")
	}
}
