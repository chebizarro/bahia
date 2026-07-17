package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// MaxBodyBytes bounds the request body buffered for payload-tag validation.
	// Default: 1 MiB.
	MaxBodyBytes int64
}

// DefaultNIP98Config returns sensible defaults for NIP-98 validation.
func DefaultNIP98Config() NIP98Config {
	return NIP98Config{MaxSkew: 60 * time.Second, MaxBodyBytes: 1 << 20}
}

// NIP98ReplayStore atomically claims signed HTTP event IDs across validators.
type NIP98ReplayStore interface {
	Claim(ctx context.Context, eventID string, expiresAt time.Time) (bool, error)
}

// NIP98Validator validates NIP-98 HTTP Auth events and provides replay protection.
type NIP98Validator struct {
	cfg     NIP98Config
	replays NIP98ReplayStore
}

// NewNIP98Validator creates a validator. Production callers should pass a
// durable shared replay store; the in-memory default exists for isolated tests.
func NewNIP98Validator(cfg NIP98Config, stores ...NIP98ReplayStore) *NIP98Validator {
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = 60 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	store := NIP98ReplayStore(newInMemoryNIP98ReplayStore())
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	}
	return &NIP98Validator{cfg: cfg, replays: store}
}

// Validate parses and verifies a base64-encoded NIP-98 event.
func (v *NIP98Validator) Validate(authToken string, r *http.Request) (*Principal, error) {
	eventJSON, err := base64.StdEncoding.DecodeString(authToken)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 encoding")
	}

	var ev nostr.Event
	if err := json.Unmarshal(eventJSON, &ev); err != nil {
		return nil, fmt.Errorf("invalid event JSON")
	}
	if ev.Kind != kindHTTPAuth {
		return nil, fmt.Errorf("invalid event kind %d, expected %d", ev.Kind, kindHTTPAuth)
	}
	if !ev.CheckID() {
		return nil, fmt.Errorf("event ID mismatch")
	}
	if !ev.VerifySignature() {
		return nil, fmt.Errorf("invalid signature")
	}

	eventTime := ev.CreatedAt.Time()
	now := time.Now()
	age := now.Sub(eventTime)
	if age < 0 {
		age = -age
	}
	if age > v.cfg.MaxSkew {
		return nil, fmt.Errorf("event too old or too far in the future (age %s, max %s)", age.Round(time.Second), v.cfg.MaxSkew)
	}

	urlTag := getEventTagValue(ev.Tags, "u")
	if urlTag == "" {
		return nil, fmt.Errorf("missing 'u' tag")
	}
	requestURL := requestAbsoluteURL(r)
	if urlTag != requestURL {
		return nil, fmt.Errorf("URL mismatch: event=%q request=%q", urlTag, requestURL)
	}

	methodTag := getEventTagValue(ev.Tags, "method")
	if methodTag == "" {
		return nil, fmt.Errorf("missing 'method' tag")
	}
	if methodTag != r.Method {
		return nil, fmt.Errorf("method mismatch: event=%q request=%q", methodTag, r.Method)
	}

	body, err := readBoundedRequestBody(r, v.cfg.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	payloadTag := getEventTagValue(ev.Tags, "payload")
	if len(body) > 0 && payloadTag == "" {
		return nil, fmt.Errorf("missing 'payload' tag for request body")
	}
	if payloadTag != "" {
		digest := sha256.Sum256(body)
		if !strings.EqualFold(payloadTag, hex.EncodeToString(digest[:])) {
			return nil, fmt.Errorf("payload hash mismatch")
		}
	}

	claimed, err := v.replays.Claim(r.Context(), ev.ID.Hex(), eventTime.Add(2*v.cfg.MaxSkew))
	if err != nil {
		return nil, fmt.Errorf("claiming NIP-98 replay ID: %w", err)
	}
	if !claimed {
		return nil, fmt.Errorf("event ID already used (replay)")
	}

	pubkey := nostrutil.PubKeyHex(ev.PubKey)
	return &Principal{Subject: pubkey, Method: MethodNIP98, PubKey: pubkey}, nil
}

func readBoundedRequestBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("request body exceeds NIP-98 limit of %d bytes", limit)
	}
	return body, nil
}

type inMemoryNIP98ReplayStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newInMemoryNIP98ReplayStore() *inMemoryNIP98ReplayStore {
	return &inMemoryNIP98ReplayStore{seen: make(map[string]time.Time)}
}

func (s *inMemoryNIP98ReplayStore) Claim(_ context.Context, eventID string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(time.Now())
	if _, exists := s.seen[eventID]; exists {
		return false, nil
	}
	s.seen[eventID] = expiresAt
	return true, nil
}

func (s *inMemoryNIP98ReplayStore) purgeExpiredLocked(now time.Time) {
	for id, expires := range s.seen {
		if !expires.After(now) {
			delete(s.seen, id)
		}
	}
}

// SeenCount returns the in-memory replay count for tests and metrics.
func (v *NIP98Validator) SeenCount() int {
	store, ok := v.replays.(*inMemoryNIP98ReplayStore)
	if !ok {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.seen)
}

// purgeExpired is retained as a test hook for the in-memory store.
func (v *NIP98Validator) purgeExpired() {
	store, ok := v.replays.(*inMemoryNIP98ReplayStore)
	if !ok {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.purgeExpiredLocked(time.Now())
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
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	return scheme + "://" + host + r.URL.RequestURI()
}
