package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// NIP98 event kind for HTTP Auth (RFC 7235 reference).
const kindHTTPAuth = 27235

// NIP98Config holds configuration for NIP-98 HTTP Auth validation.
type NIP98Config struct {
	// MaxSkew is the maximum age of a NIP-98 event. Events older than this
	// are rejected. Default: 60 seconds.
	MaxSkew time.Duration
}

// DefaultNIP98Config returns sensible defaults for NIP-98 validation.
func DefaultNIP98Config() NIP98Config {
	return NIP98Config{MaxSkew: 60 * time.Second}
}

// NIP98Validator validates NIP-98 HTTP Auth events and provides replay protection.
// Clients must sign a fresh event for each request attempt; include fresh entropy
// such as a nonce tag when repeating the same method/URL within the same second.
type NIP98Validator struct {
	cfg NIP98Config

	// In-memory replay protection. Keyed by event ID → expiry time.
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewNIP98Validator creates a validator and starts a background goroutine
// that periodically purges expired replay entries.
func NewNIP98Validator(cfg NIP98Config) *NIP98Validator {
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = 60 * time.Second
	}
	v := &NIP98Validator{
		cfg:  cfg,
		seen: make(map[string]time.Time),
	}
	go v.cleanupLoop()
	return v
}

// Validate parses the base64-encoded Nostr event from the Authorization header
// value (after stripping the "Nostr " prefix) and verifies it per NIP-98.
// On success it returns a Principal whose Subject and PubKey are the event author.
func (v *NIP98Validator) Validate(authToken string, r *http.Request) (*Principal, error) {
	// Decode base64.
	eventJSON, err := base64.StdEncoding.DecodeString(authToken)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 encoding")
	}

	// Parse the Nostr event.
	var ev nostr.Event
	if err := json.Unmarshal(eventJSON, &ev); err != nil {
		return nil, fmt.Errorf("invalid event JSON")
	}

	// 1. Kind MUST be 27235.
	if ev.Kind != kindHTTPAuth {
		return nil, fmt.Errorf("invalid event kind %d, expected %d", ev.Kind, kindHTTPAuth)
	}

	// 2. Verify Schnorr signature and event ID.
	if !ev.CheckID() {
		return nil, fmt.Errorf("event ID mismatch")
	}
	if !ev.VerifySignature() {
		return nil, fmt.Errorf("invalid signature")
	}

	// 3. created_at within skew window.
	eventTime := ev.CreatedAt.Time()
	now := time.Now()
	age := now.Sub(eventTime)
	if age < 0 {
		age = -age // future event
	}
	if age > v.cfg.MaxSkew {
		return nil, fmt.Errorf("event too old or too far in the future (age %s, max %s)", age.Round(time.Second), v.cfg.MaxSkew)
	}

	// 4. URL tag must match.
	urlTag := getEventTagValue(ev.Tags, "u")
	if urlTag == "" {
		return nil, fmt.Errorf("missing 'u' tag")
	}
	requestURL := requestAbsoluteURL(r)
	if urlTag != requestURL {
		return nil, fmt.Errorf("URL mismatch: event=%q request=%q", urlTag, requestURL)
	}

	// 5. Method tag must match.
	methodTag := getEventTagValue(ev.Tags, "method")
	if methodTag == "" {
		return nil, fmt.Errorf("missing 'method' tag")
	}
	if methodTag != r.Method {
		return nil, fmt.Errorf("method mismatch: event=%q request=%q", methodTag, r.Method)
	}

	// 6. Replay protection.
	if !v.markSeen(ev.ID.Hex(), eventTime) {
		return nil, fmt.Errorf("event ID already used (replay)")
	}

	return &Principal{
		Subject: nostrutil.PubKeyHex(ev.PubKey),
		Method:  MethodNIP98,
		PubKey:  nostrutil.PubKeyHex(ev.PubKey),
	}, nil
}

// markSeen atomically checks and records an event ID. Returns true if this is
// the first time the ID is seen (i.e. not a replay).
func (v *NIP98Validator) markSeen(eventID string, createdAt time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, exists := v.seen[eventID]; exists {
		return false
	}
	// Entry expires after 2× the skew window to be safe.
	v.seen[eventID] = createdAt.Add(2 * v.cfg.MaxSkew)
	return true
}

// cleanupLoop runs every MaxSkew duration and purges expired replay entries.
func (v *NIP98Validator) cleanupLoop() {
	ticker := time.NewTicker(v.cfg.MaxSkew)
	defer ticker.Stop()
	for range ticker.C {
		v.purgeExpired()
	}
}

// purgeExpired removes entries whose expiry has passed.
func (v *NIP98Validator) purgeExpired() {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	for id, expires := range v.seen {
		if now.After(expires) {
			delete(v.seen, id)
		}
	}
}

// SeenCount returns the number of tracked event IDs (for testing/metrics).
func (v *NIP98Validator) SeenCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.seen)
}

// getEventTagValue returns the first value for the given tag key.
func getEventTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

// requestAbsoluteURL reconstructs the absolute URL from an *http.Request.
func requestAbsoluteURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	// Honour X-Forwarded-Proto if present (behind reverse proxy).
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}

	host := r.Host
	if host == "" {
		host = r.URL.Host
	}

	path := r.URL.RequestURI() // path + query + fragment
	return scheme + "://" + host + path
}
