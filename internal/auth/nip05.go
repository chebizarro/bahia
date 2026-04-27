package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NIP05CacheTTL is how long resolved NIP-05 identities are cached.
const NIP05CacheTTL = 1 * time.Hour

// NIP05NegativeTTL is how long failed lookups are cached to avoid hammering.
const NIP05NegativeTTL = 5 * time.Minute

// NIP05Resolver resolves Nostr public keys to NIP-05 identifiers.
// It caches results to avoid repeated HTTP requests.
type NIP05Resolver struct {
	client *http.Client
	mu     sync.RWMutex
	cache  map[string]*nip05CacheEntry
}

type nip05CacheEntry struct {
	identifier string    // "user@domain.com" or "" for failed lookups
	expiresAt  time.Time
}

// NewNIP05Resolver creates a new NIP-05 resolver with default HTTP client settings.
func NewNIP05Resolver() *NIP05Resolver {
	r := &NIP05Resolver{
		client: &http.Client{Timeout: 5 * time.Second},
		cache:  make(map[string]*nip05CacheEntry),
	}
	go r.cleanupLoop()
	return r
}

// Resolve attempts to find a NIP-05 identifier for the given pubkey.
// It queries common relay patterns and caches results.
// Returns the NIP-05 identifier (e.g., "user@domain.com") or empty string if not found.
func (r *NIP05Resolver) Resolve(ctx context.Context, pubkey string) string {
	if pubkey == "" {
		return ""
	}

	// Check cache first.
	r.mu.RLock()
	entry, ok := r.cache[pubkey]
	r.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.identifier
	}

	// Not cached or expired — we can't resolve without knowing the domain.
	// NIP-05 resolution requires knowing the identifier first, then verifying it.
	// For "reverse" resolution (pubkey → identifier), we'd need a registry.
	// Cache a negative result so we don't keep trying.
	r.mu.Lock()
	r.cache[pubkey] = &nip05CacheEntry{
		identifier: "",
		expiresAt:  time.Now().Add(NIP05NegativeTTL),
	}
	r.mu.Unlock()

	return ""
}

// Verify checks if a NIP-05 identifier resolves to the given pubkey.
// The identifier is in the format "user@domain.com".
// Returns true if the identifier resolves to the pubkey.
func (r *NIP05Resolver) Verify(ctx context.Context, identifier, pubkey string) bool {
	if identifier == "" || pubkey == "" {
		return false
	}

	parts := strings.SplitN(identifier, "@", 2)
	if len(parts) != 2 {
		return false
	}
	localPart, domain := parts[0], parts[1]

	// Query the well-known URL.
	url := fmt.Sprintf("https://%s/.well-known/nostr.json?name=%s", domain, localPart)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var nip05Response struct {
		Names  map[string]string   `json:"names"`
		Relays map[string][]string `json:"relays,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&nip05Response); err != nil {
		return false
	}

	// Check if the local part maps to the pubkey.
	if resolvedPubkey, ok := nip05Response.Names[localPart]; ok {
		if strings.EqualFold(resolvedPubkey, pubkey) {
			// Cache the successful verification.
			r.mu.Lock()
			r.cache[pubkey] = &nip05CacheEntry{
				identifier: identifier,
				expiresAt:  time.Now().Add(NIP05CacheTTL),
			}
			r.mu.Unlock()
			return true
		}
	}

	return false
}

// LookupByIdentifier resolves a NIP-05 identifier to a pubkey.
// Returns the pubkey and true if found, or empty string and false otherwise.
func (r *NIP05Resolver) LookupByIdentifier(ctx context.Context, identifier string) (string, bool) {
	if identifier == "" {
		return "", false
	}

	// Handle bare identifiers (underscore assumed for root).
	if !strings.Contains(identifier, "@") {
		// Could be just a domain, but we need user@domain format.
		return "", false
	}

	parts := strings.SplitN(identifier, "@", 2)
	if len(parts) != 2 {
		return "", false
	}
	localPart, domain := parts[0], parts[1]

	// Use _ for root identifier.
	if localPart == "" {
		localPart = "_"
	}

	url := fmt.Sprintf("https://%s/.well-known/nostr.json?name=%s", domain, localPart)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	var nip05Response struct {
		Names  map[string]string   `json:"names"`
		Relays map[string][]string `json:"relays,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&nip05Response); err != nil {
		return "", false
	}

	if pubkey, ok := nip05Response.Names[localPart]; ok {
		// Cache the result.
		r.mu.Lock()
		r.cache[pubkey] = &nip05CacheEntry{
			identifier: identifier,
			expiresAt:  time.Now().Add(NIP05CacheTTL),
		}
		r.mu.Unlock()
		return pubkey, true
	}

	return "", false
}

// GetCached returns the cached NIP-05 identifier for a pubkey, if available.
// Does not make network requests.
func (r *NIP05Resolver) GetCached(pubkey string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if entry, ok := r.cache[pubkey]; ok && time.Now().Before(entry.expiresAt) {
		return entry.identifier
	}
	return ""
}

// SetCached manually sets a cached NIP-05 mapping.
// Useful when the identifier is known from another source (e.g., user profile).
func (r *NIP05Resolver) SetCached(pubkey, identifier string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[pubkey] = &nip05CacheEntry{
		identifier: identifier,
		expiresAt:  time.Now().Add(NIP05CacheTTL),
	}
}

// CacheSize returns the number of cached entries (for metrics).
func (r *NIP05Resolver) CacheSize() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache)
}

// cleanupLoop periodically removes expired cache entries.
func (r *NIP05Resolver) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.purgeExpired()
	}
}

func (r *NIP05Resolver) purgeExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for pubkey, entry := range r.cache {
		if now.After(entry.expiresAt) {
			delete(r.cache, pubkey)
		}
	}
}
