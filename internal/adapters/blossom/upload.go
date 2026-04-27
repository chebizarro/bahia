package blossom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Upload uploads data to the first available Blossom server.
// Returns the blob descriptor with URL and hash.
func (c *Client) Upload(ctx context.Context, data []byte, contentType string) (*BlobDescriptor, error) {
	if len(c.servers) == 0 {
		return nil, fmt.Errorf("no Blossom servers configured")
	}

	hash := ComputeSHA256(data)

	var lastErr error
	for _, server := range c.servers {
		bd, err := c.uploadToServer(ctx, server, data, contentType, hash)
		if err != nil {
			c.recordUpload(server, false)
			lastErr = err
			c.logger.Warn("upload failed, trying next server",
				"server", server,
				"error", err,
			)
			continue
		}

		c.recordUpload(server, true)
		c.logger.Info("uploaded to Blossom",
			"server", server,
			"sha256", hash,
			"size", len(data),
		)
		return bd, nil
	}

	return nil, fmt.Errorf("failed to upload to any Blossom server: %w", lastErr)
}

// UploadWithRedundancy uploads data to multiple servers for redundancy.
// Returns blob descriptors from all successful uploads.
func (c *Client) UploadWithRedundancy(ctx context.Context, data []byte, contentType string, minServers int) ([]*BlobDescriptor, error) {
	if len(c.servers) == 0 {
		return nil, fmt.Errorf("no Blossom servers configured")
	}

	hash := ComputeSHA256(data)
	var results []*BlobDescriptor
	var errors []error

	for _, server := range c.servers {
		bd, err := c.uploadToServer(ctx, server, data, contentType, hash)
		if err != nil {
			c.recordUpload(server, false)
			errors = append(errors, fmt.Errorf("%s: %w", server, err))
			continue
		}

		c.recordUpload(server, true)
		results = append(results, bd)

		c.logger.Debug("uploaded to Blossom server",
			"server", server,
			"sha256", hash,
		)
	}

	if len(results) < minServers {
		return results, fmt.Errorf("uploaded to %d servers, minimum required: %d. Errors: %v",
			len(results), minServers, errors)
	}

	return results, nil
}

func (c *Client) uploadToServer(ctx context.Context, server string, data []byte, contentType string, hash string) (*BlobDescriptor, error) {
	url := server + "/upload"

	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}

		bd, err := c.doUpload(ctx, url, data, contentType, hash)
		if err != nil {
			lastErr = err
			continue
		}
		return bd, nil
	}

	return nil, fmt.Errorf("after %d retries: %w", c.maxRetries, lastErr)
}

func (c *Client) doUpload(ctx context.Context, url string, data []byte, contentType string, hash string) (*BlobDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("X-SHA-256", hash)
	req.ContentLength = int64(len(data))

	// Add Nostr auth if private key is configured
	if c.privateKey != "" {
		authHeader, err := c.createAuthHeader(ctx, url, "PUT", hash)
		if err != nil {
			return nil, fmt.Errorf("creating auth header: %w", err)
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var bd BlobDescriptor
	if err := json.Unmarshal(body, &bd); err != nil {
		// If response isn't JSON, construct descriptor manually
		bd = BlobDescriptor{
			URL:      url[:len(url)-7] + "/" + hash, // Remove "/upload", add hash
			SHA256:   hash,
			Size:     int64(len(data)),
			Uploaded: time.Now(),
		}
	}

	return &bd, nil
}

// createAuthHeader creates a NIP-98 Nostr-signed authorization header.
// Returns "Nostr <base64-encoded-signed-event>" for authenticated requests.
func (c *Client) createAuthHeader(ctx context.Context, url, method, contentHash string) (string, error) {
	if c.privateKey == "" {
		return "", nil
	}

	// Get public key from private key
	pubkey, err := nostr.GetPublicKey(c.privateKey)
	if err != nil {
		return "", fmt.Errorf("deriving public key: %w", err)
	}

	// Build NIP-98 event (kind 27235)
	event := &nostr.Event{
		Kind:      27235,
		PubKey:    pubkey,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"u", url},
			{"method", method},
		},
		Content: "",
	}

	// Add payload hash if provided (SHA-256 of request body)
	if contentHash != "" {
		event.Tags = append(event.Tags, nostr.Tag{"payload", contentHash})
	}

	// Sign the event
	if err := event.Sign(c.privateKey); err != nil {
		return "", fmt.Errorf("signing event: %w", err)
	}

	// Serialize to JSON and base64 encode
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(eventJSON)
	return "Nostr " + encoded, nil
}
