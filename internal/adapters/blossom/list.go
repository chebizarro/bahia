package blossom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// List retrieves all blobs owned by the configured private key's pubkey.
// Requires a private key to be configured for authentication.
func (c *Client) List(ctx context.Context) ([]BlobDescriptor, error) {
	if c.privateKey == "" {
		return nil, fmt.Errorf("private key required for listing own blobs")
	}

	pubkey, err := nostr.GetPublicKey(c.privateKey)
	if err != nil {
		return nil, fmt.Errorf("deriving public key: %w", err)
	}

	return c.ListByPubkey(ctx, pubkey)
}

// ListByPubkey retrieves all blobs owned by a specific pubkey.
// This queries all configured Blossom servers and deduplicates results.
func (c *Client) ListByPubkey(ctx context.Context, pubkey string) ([]BlobDescriptor, error) {
	if len(c.servers) == 0 {
		return nil, fmt.Errorf("no Blossom servers configured")
	}

	// Validate pubkey format (64 hex chars)
	if len(pubkey) != 64 {
		return nil, fmt.Errorf("invalid pubkey length: expected 64 hex chars, got %d", len(pubkey))
	}

	// Track unique blobs by SHA256 hash
	seen := make(map[string]bool)
	var results []BlobDescriptor
	var lastErr error

	for _, server := range c.servers {
		blobs, err := c.listFromServer(ctx, server, pubkey)
		if err != nil {
			lastErr = err
			c.logger.Warn("list failed from server",
				"server", server,
				"pubkey", pubkey[:16]+"...",
				"error", err,
			)
			continue
		}

		// Deduplicate by SHA256
		for _, blob := range blobs {
			if !seen[blob.SHA256] {
				seen[blob.SHA256] = true
				results = append(results, blob)
			}
		}

		c.logger.Debug("listed blobs from server",
			"server", server,
			"pubkey", pubkey[:16]+"...",
			"count", len(blobs),
		)
	}

	// Return results if we got any, even if some servers failed
	if len(results) > 0 || lastErr == nil {
		return results, nil
	}

	return nil, fmt.Errorf("failed to list blobs from any server: %w", lastErr)
}

// listFromServer fetches the blob list from a single Blossom server.
func (c *Client) listFromServer(ctx context.Context, server, pubkey string) ([]BlobDescriptor, error) {
	url := fmt.Sprintf("%s/list/%s", server, pubkey)

	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}

		blobs, err := c.doList(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		return blobs, nil
	}

	return nil, fmt.Errorf("after %d retries: %w", c.maxRetries, lastErr)
}

// doList performs the actual HTTP request to list blobs.
func (c *Client) doList(ctx context.Context, url string) ([]BlobDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	// Add Nostr auth if private key is configured
	// The "t: list" tag is required per Blossom spec
	if c.privateKey != "" {
		authHeader, err := c.createListAuthHeader(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("creating auth header: %w", err)
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting list: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var blobs []BlobDescriptor
	if err := json.Unmarshal(body, &blobs); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return blobs, nil
}

// createListAuthHeader creates a NIP-98 authorization header with the "t: list" tag.
// This is required by Blossom servers for list operations.
func (c *Client) createListAuthHeader(ctx context.Context, url string) (string, error) {
	if c.privateKey == "" {
		return "", nil
	}

	pubkey, err := nostr.GetPublicKey(c.privateKey)
	if err != nil {
		return "", fmt.Errorf("deriving public key: %w", err)
	}

	// Build NIP-98 event (kind 27235) with list tag
	event := &nostr.Event{
		Kind:      27235,
		PubKey:    pubkey,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"u", url},
			{"method", "GET"},
			{"t", "list"}, // Required for Blossom list operations
		},
		Content: "",
	}

	if err := event.Sign(c.privateKey); err != nil {
		return "", fmt.Errorf("signing event: %w", err)
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(eventJSON)
	return "Nostr " + encoded, nil
}
