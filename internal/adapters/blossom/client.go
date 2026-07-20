// Package blossom provides a Blossom media server client for storing and retrieving artifacts.
// Blossom is a simple file storage protocol over HTTP with Nostr-based authentication.
package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config holds Blossom client configuration.
type Config struct {
	Servers       []string      // List of Blossom server URLs for redundancy
	MaxRetries    int           // Max retries per server (default: 3)
	RetryDelay    time.Duration // Delay between retries (default: 1s)
	Timeout       time.Duration // HTTP timeout (default: 30s)
	PrivateKeyHex string        // Nostr private key for auth (optional for public reads)
}

// Client is a Blossom media server client with multi-server redundancy.
type Client struct {
	servers    []string
	maxRetries int
	retryDelay time.Duration
	httpClient *http.Client
	privateKey string
	logger     *slog.Logger

	mu    sync.RWMutex
	stats map[string]*serverStats // server URL -> stats
}

type serverStats struct {
	uploads   int64
	downloads int64
	failures  int64
	lastUsed  time.Time
}

// ErrAuthHeader identifies failures preparing Blossom NIP-98 authorization headers.
var ErrAuthHeader = errors.New("blossom auth header preparation failed")

// BlobDescriptor describes an uploaded blob.
type BlobDescriptor struct {
	URL      string           `json:"url"`
	SHA256   string           `json:"sha256"`
	Size     int64            `json:"size"`
	Type     string           `json:"type"`
	Uploaded BlossomTimestamp `json:"uploaded"`
}

// BlossomTimestamp accepts both the BUD-02 Unix timestamp used by Blossom
// servers and the RFC 3339 form emitted by older Bahia test/fallback paths.
type BlossomTimestamp struct{ time.Time }

func (t *BlossomTimestamp) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		t.Time = time.Time{}
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &t.Time)
	}
	var seconds int64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return err
	}
	t.Time = time.Unix(seconds, 0).UTC()
	return nil
}

func (t BlossomTimestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Time)
}

// NewClient creates a new Blossom client.
func NewClient(cfg Config, logger *slog.Logger) *Client {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Normalize server URLs
	servers := make([]string, len(cfg.Servers))
	for i, s := range cfg.Servers {
		servers[i] = strings.TrimSuffix(s, "/")
	}

	return &Client{
		servers:    servers,
		maxRetries: cfg.MaxRetries,
		retryDelay: cfg.RetryDelay,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		privateKey: cfg.PrivateKeyHex,
		logger:     logger,
		stats:      make(map[string]*serverStats),
	}
}

// Servers returns the list of configured server URLs.
func (c *Client) Servers() []string {
	return c.servers
}

// HealthCheck checks connectivity to all servers.
func (c *Client) HealthCheck(ctx context.Context) map[string]error {
	results := make(map[string]error)
	for _, server := range c.servers {
		err := c.checkServer(ctx, server)
		results[server] = err
	}
	return results
}

func (c *Client) checkServer(ctx context.Context, server string) error {
	req, err := http.NewRequestWithContext(ctx, "HEAD", server, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}

// GetStats returns upload/download statistics for all servers.
func (c *Client) GetStats() map[string]map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]map[string]int64)
	for server, stats := range c.stats {
		result[server] = map[string]int64{
			"uploads":   stats.uploads,
			"downloads": stats.downloads,
			"failures":  stats.failures,
		}
	}
	return result
}

func (c *Client) applyAuthHeader(ctx context.Context, req *http.Request, method, payloadHash string) error {
	if c.privateKey == "" {
		return nil
	}
	authHeader, err := c.createAuthHeader(ctx, req.URL.String(), method, payloadHash)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthHeader, err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return nil
}

func (c *Client) recordUpload(server string, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stats[server] == nil {
		c.stats[server] = &serverStats{}
	}
	c.stats[server].lastUsed = time.Now()
	if success {
		c.stats[server].uploads++
	} else {
		c.stats[server].failures++
	}
}

func (c *Client) recordDownload(server string, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stats[server] == nil {
		c.stats[server] = &serverStats{}
	}
	c.stats[server].lastUsed = time.Now()
	if success {
		c.stats[server].downloads++
	} else {
		c.stats[server].failures++
	}
}

// ComputeSHA256 computes the SHA-256 hash of data.
func ComputeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// VerifySHA256 verifies that data matches the expected SHA-256 hash.
func VerifySHA256(data []byte, expected string) bool {
	actual := ComputeSHA256(data)
	return strings.EqualFold(actual, expected)
}

// ParseBlossomURL extracts the SHA-256 hash from a Blossom URL.
// Blossom URLs have the format: https://server.com/{sha256}[.ext]
func ParseBlossomURL(url string) (server, hash string, err error) {
	// Find the last path segment
	lastSlash := strings.LastIndex(url, "/")
	if lastSlash == -1 {
		return "", "", fmt.Errorf("invalid Blossom URL: %s", url)
	}

	server = url[:lastSlash]
	hashPart := url[lastSlash+1:]

	// Remove extension if present
	if dot := strings.LastIndex(hashPart, "."); dot != -1 {
		hashPart = hashPart[:dot]
	}

	// Validate it's a 64-char hex string (SHA-256)
	if len(hashPart) != 64 {
		return "", "", fmt.Errorf("invalid hash length in URL: %s", url)
	}
	if _, err := hex.DecodeString(hashPart); err != nil {
		return "", "", fmt.Errorf("invalid hex hash in URL: %s", url)
	}

	return server, hashPart, nil
}
