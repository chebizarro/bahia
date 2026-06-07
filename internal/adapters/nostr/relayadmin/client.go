package relayadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	ContentType = "application/nostr+json+rpc"

	MethodSupportedMethods            = "supportedmethods"
	MethodBanPubkey                   = "banpubkey"
	MethodUnbanPubkey                 = "unbanpubkey"
	MethodListBannedPubkeys           = "listbannedpubkeys"
	MethodAllowPubkey                 = "allowpubkey"
	MethodUnallowPubkey               = "unallowpubkey"
	MethodListAllowedPubkeys          = "listallowedpubkeys"
	MethodListEventsNeedingModeration = "listeventsneedingmoderation"
	MethodAllowEvent                  = "allowevent"
	MethodBanEvent                    = "banevent"
	MethodListBannedEvents            = "listbannedevents"
	MethodChangeRelayName             = "changerelayname"
	MethodChangeRelayDescription      = "changerelaydescription"
	MethodChangeRelayIcon             = "changerelayicon"
	MethodAllowKind                   = "allowkind"
	MethodDisallowKind                = "disallowkind"
	MethodListAllowedKinds            = "listallowedkinds"
	MethodBlockIP                     = "blockip"
	MethodUnblockIP                   = "unblockip"
	MethodListBlockedIPs              = "listblockedips"
)

var (
	ErrDisabled           = errors.New("nip-86 relay administration is disabled")
	ErrUnauthorizedTarget = errors.New("nip-86 relay administration target is not authorized")
	ErrUnsupportedMethod  = errors.New("unsupported nip-86 relay administration method")
	ErrMissingPrivateKey  = errors.New("nip-86 relay administration private key is required")
	ErrAuthHeader         = errors.New("nip-98 authorization header preparation failed")
	ErrRelayError         = errors.New("nip-86 relay returned error")
)

var allowedMethods = map[string]struct{}{
	MethodSupportedMethods:            {},
	MethodBanPubkey:                   {},
	MethodUnbanPubkey:                 {},
	MethodListBannedPubkeys:           {},
	MethodAllowPubkey:                 {},
	MethodUnallowPubkey:               {},
	MethodListAllowedPubkeys:          {},
	MethodListEventsNeedingModeration: {},
	MethodAllowEvent:                  {},
	MethodBanEvent:                    {},
	MethodListBannedEvents:            {},
	MethodChangeRelayName:             {},
	MethodChangeRelayDescription:      {},
	MethodChangeRelayIcon:             {},
	MethodAllowKind:                   {},
	MethodDisallowKind:                {},
	MethodListAllowedKinds:            {},
	MethodBlockIP:                     {},
	MethodUnblockIP:                   {},
	MethodListBlockedIPs:              {},
}

// Config constructs an opt-in NIP-86 relay administration client. PrivateKeyHex
// is the resolved secret value from configuration's administrator private-key
// reference; do not store plaintext private keys in static configuration.
type Config struct {
	Enabled       bool
	PrivateKeyHex string
	Targets       []Target
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Target is one explicitly configured Bahia-owned or Bahia-authorized relay
// management endpoint. RelayURL is the canonical Nostr relay URL used in the
// NIP-98 `u` tag. HTTPURL is optional; when absent, ws/wss relay URLs are
// converted to http/https for the POST request.
type Target struct {
	Ref                  string
	RelayURL             string
	HTTPURL              string
	AdministratorPubkeys []string
}

type Client struct {
	enabled    bool
	privateKey string
	pubkey     string
	targets    map[string]Target
	httpClient *http.Client
	now        func() time.Time
}

type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
}

// Response is the JSON-RPC-like NIP-86 response. Result is left raw so callers
// can decode relay-specific result shapes without widening this client into an
// application mutation transport.
type Response struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

// HTTPStatusError reports explicit non-2xx relay-management HTTP failures.
type HTTPStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("nip-86 relay management HTTP status %d: %s", e.StatusCode, e.Body)
}

// NewClient validates and constructs a NIP-86 client. Disabled clients are
// constructible so production code can wire the dependency safely; operations
// still fail closed with ErrDisabled.
func NewClient(cfg Config) (*Client, error) {
	client := &Client{
		enabled: cfg.Enabled,
		targets: make(map[string]Target),
		now:     cfg.Now,
	}
	if client.now == nil {
		client.now = time.Now
	}
	if cfg.HTTPClient != nil {
		client.httpClient = cfg.HTTPClient
	} else {
		client.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if !cfg.Enabled {
		return client, nil
	}
	client.privateKey = strings.TrimSpace(cfg.PrivateKeyHex)
	if client.privateKey == "" {
		return nil, ErrMissingPrivateKey
	}
	pubkey, err := nostr.GetPublicKey(client.privateKey)
	if err != nil {
		return nil, fmt.Errorf("deriving relay administrator pubkey: %w", err)
	}
	client.pubkey = strings.ToLower(pubkey)
	if len(cfg.Targets) == 0 {
		return nil, fmt.Errorf("%w: at least one target is required", ErrUnauthorizedTarget)
	}
	for _, target := range cfg.Targets {
		normalized, err := normalizeTarget(target)
		if err != nil {
			return nil, err
		}
		if !containsPubkey(normalized.AdministratorPubkeys, client.pubkey) {
			return nil, fmt.Errorf("%w: target %q does not authorize administrator pubkey %s", ErrUnauthorizedTarget, normalized.Ref, client.pubkey)
		}
		if _, exists := client.targets[normalized.Ref]; exists {
			return nil, fmt.Errorf("%w: duplicate target ref %q", ErrUnauthorizedTarget, normalized.Ref)
		}
		client.targets[normalized.Ref] = normalized
	}
	return client, nil
}

// Call sends one NIP-86 relay-owner administration request to an explicitly
// configured target. Only NIP-86 method names are accepted; ContextVM and Bahia
// app/control-plane methods are rejected before any HTTP request is made.
func (c *Client) Call(ctx context.Context, targetRef, method string, params []any) (*Response, error) {
	if c == nil || !c.enabled {
		return nil, ErrDisabled
	}
	method = strings.TrimSpace(method)
	if _, ok := allowedMethods[method]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMethod, method)
	}
	target, ok := c.targets[strings.TrimSpace(targetRef)]
	if !ok {
		return nil, fmt.Errorf("%w: target %q is not configured", ErrUnauthorizedTarget, targetRef)
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshaling nip-86 request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.HTTPURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating nip-86 request: %w", err)
	}
	req.Header.Set("Content-Type", ContentType)
	authHeader, err := c.createAuthHeader(target.RelayURL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthHeader, err)
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting nip-86 request to target %q: %w", target.Ref, err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, fmt.Errorf("reading nip-86 response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(respBody))}
	}
	var decoded Response
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decoding nip-86 response: %w", err)
	}
	if decoded.Error != "" {
		return &decoded, fmt.Errorf("%w: %s", ErrRelayError, decoded.Error)
	}
	return &decoded, nil
}

func (c *Client) SupportedMethods(ctx context.Context, targetRef string) ([]string, error) {
	resp, err := c.Call(ctx, targetRef, MethodSupportedMethods, nil)
	if err != nil {
		return nil, err
	}
	var methods []string
	if err := json.Unmarshal(resp.Result, &methods); err != nil {
		return nil, fmt.Errorf("decoding supportedmethods result: %w", err)
	}
	return methods, nil
}

func (c *Client) AllowPubkey(ctx context.Context, targetRef, pubkey, reason string) error {
	_, err := c.Call(ctx, targetRef, MethodAllowPubkey, reasonedParams(pubkey, reason))
	return err
}

func (c *Client) BanPubkey(ctx context.Context, targetRef, pubkey, reason string) error {
	_, err := c.Call(ctx, targetRef, MethodBanPubkey, reasonedParams(pubkey, reason))
	return err
}

func (c *Client) ChangeRelayName(ctx context.Context, targetRef, name string) error {
	_, err := c.Call(ctx, targetRef, MethodChangeRelayName, []any{name})
	return err
}

func (c *Client) ChangeRelayDescription(ctx context.Context, targetRef, description string) error {
	_, err := c.Call(ctx, targetRef, MethodChangeRelayDescription, []any{description})
	return err
}

func (c *Client) AllowKind(ctx context.Context, targetRef string, kind int) error {
	_, err := c.Call(ctx, targetRef, MethodAllowKind, []any{kind})
	return err
}

func (c *Client) DisallowKind(ctx context.Context, targetRef string, kind int) error {
	_, err := c.Call(ctx, targetRef, MethodDisallowKind, []any{kind})
	return err
}

func (c *Client) createAuthHeader(relayURL string, body []byte) (string, error) {
	payloadHash := sha256.Sum256(body)
	event := &nostr.Event{
		Kind:      27235,
		PubKey:    c.pubkey,
		CreatedAt: nostr.Timestamp(c.now().Unix()),
		Tags: nostr.Tags{
			{"u", relayURL},
			{"method", http.MethodPost},
			{"payload", hex.EncodeToString(payloadHash[:])},
		},
		Content: "",
	}
	if err := event.Sign(c.privateKey); err != nil {
		return "", fmt.Errorf("signing nip-98 event: %w", err)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshaling nip-98 event: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(eventJSON), nil
}

func reasonedParams(value, reason string) []any {
	if strings.TrimSpace(reason) == "" {
		return []any{value}
	}
	return []any{value, reason}
}

func normalizeTarget(target Target) (Target, error) {
	target.Ref = strings.TrimSpace(target.Ref)
	if target.Ref == "" {
		return Target{}, fmt.Errorf("%w: target ref is required", ErrUnauthorizedTarget)
	}
	target.RelayURL = strings.TrimSpace(target.RelayURL)
	if err := validateRelayURL(target.RelayURL); err != nil {
		return Target{}, fmt.Errorf("%w: target %q relay_url: %v", ErrUnauthorizedTarget, target.Ref, err)
	}
	target.HTTPURL = strings.TrimSpace(target.HTTPURL)
	if target.HTTPURL == "" {
		target.HTTPURL = relayHTTPURL(target.RelayURL)
	}
	if err := validateHTTPURL(target.HTTPURL); err != nil {
		return Target{}, fmt.Errorf("%w: target %q http_url: %v", ErrUnauthorizedTarget, target.Ref, err)
	}
	pubkeys, err := normalizePubkeys(target.AdministratorPubkeys)
	if err != nil {
		return Target{}, fmt.Errorf("%w: target %q administrator_pubkeys: %v", ErrUnauthorizedTarget, target.Ref, err)
	}
	if len(pubkeys) == 0 {
		return Target{}, fmt.Errorf("%w: target %q requires administrator_pubkeys", ErrUnauthorizedTarget, target.Ref)
	}
	target.AdministratorPubkeys = pubkeys
	return target, nil
}

func validateRelayURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute ws/wss relay URL")
	}
	switch parsed.Scheme {
	case "wss":
		return nil
	case "ws":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("ws relay administration URLs are allowed only for localhost or loopback targets; use wss")
	default:
		return fmt.Errorf("scheme must be ws or wss")
	}
}

func validateHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute http/https URL")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("http relay administration URLs are allowed only for localhost or loopback targets; use https")
	default:
		return fmt.Errorf("scheme must be http or https")
	}
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func relayHTTPURL(relayURL string) string {
	parsed, err := url.Parse(relayURL)
	if err != nil {
		return relayURL
	}
	if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	} else if parsed.Scheme == "ws" {
		parsed.Scheme = "http"
	}
	return parsed.String()
}

func normalizePubkeys(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		pk := strings.ToLower(strings.TrimSpace(raw))
		if pk == "" {
			continue
		}
		if len(pk) != 64 {
			return nil, fmt.Errorf("pubkey %q must be 64 hex characters", raw)
		}
		if _, err := hex.DecodeString(pk); err != nil {
			return nil, fmt.Errorf("pubkey %q must be valid hex", raw)
		}
		if _, exists := seen[pk]; exists {
			continue
		}
		seen[pk] = struct{}{}
		out = append(out, pk)
	}
	return out, nil
}

func containsPubkey(values []string, pubkey string) bool {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	for _, value := range values {
		if value == pubkey {
			return true
		}
	}
	return false
}
