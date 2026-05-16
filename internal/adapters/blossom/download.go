package blossom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Download downloads a blob by URL and verifies its SHA-256 hash.
func (c *Client) Download(ctx context.Context, url string) ([]byte, error) {
	server, expectedHash, err := ParseBlossomURL(url)
	if err != nil {
		return nil, err
	}

	data, err := c.downloadFromServer(ctx, url)
	if err != nil {
		c.recordDownload(server, false)
		return nil, err
	}

	// Verify hash
	if !VerifySHA256(data, expectedHash) {
		c.recordDownload(server, false)
		return nil, fmt.Errorf("SHA-256 mismatch: expected %s", expectedHash)
	}

	c.recordDownload(server, true)
	c.logger.Debug("downloaded from Blossom",
		"url", url,
		"sha256", expectedHash,
		"size", len(data),
	)

	return data, nil
}

// DownloadByHash downloads a blob by its SHA-256 hash, trying all configured servers.
func (c *Client) DownloadByHash(ctx context.Context, hash string) ([]byte, error) {
	if len(c.servers) == 0 {
		return nil, fmt.Errorf("no Blossom servers configured")
	}

	var lastErr error
	for _, server := range c.servers {
		url := server + "/" + hash
		data, err := c.downloadFromServer(ctx, url)
		if err != nil {
			c.recordDownload(server, false)
			lastErr = err
			c.logger.Debug("download failed, trying next server",
				"server", server,
				"hash", hash,
				"error", err,
			)
			continue
		}

		// Verify hash
		if !VerifySHA256(data, hash) {
			c.recordDownload(server, false)
			lastErr = fmt.Errorf("SHA-256 mismatch from %s", server)
			continue
		}

		c.recordDownload(server, true)
		c.logger.Info("downloaded from Blossom",
			"server", server,
			"sha256", hash,
			"size", len(data),
		)

		return data, nil
	}

	return nil, fmt.Errorf("failed to download %s from any server: %w", hash, lastErr)
}

// DownloadWithFallback tries to download from the URL's server first,
// then falls back to configured servers if that fails.
func (c *Client) DownloadWithFallback(ctx context.Context, url string) ([]byte, error) {
	server, hash, err := ParseBlossomURL(url)
	if err != nil {
		return nil, err
	}

	// Try the original server first
	data, err := c.downloadFromServer(ctx, url)
	if err == nil {
		if VerifySHA256(data, hash) {
			c.recordDownload(server, true)
			return data, nil
		}
		c.recordDownload(server, false)
	} else {
		c.recordDownload(server, false)
		c.logger.Debug("primary server failed, trying fallbacks",
			"url", url,
			"error", err,
		)
	}

	// Try configured servers
	return c.DownloadByHash(ctx, hash)
}

func (c *Client) downloadFromServer(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}

		data, err := c.doDownload(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}

	return nil, fmt.Errorf("after %d retries: %w", c.maxRetries, lastErr)
}

func (c *Client) doDownload(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	if err := c.applyAuthHeader(ctx, req, http.MethodGet, ""); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("blob not found: %s", url)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// Exists checks if a blob exists on any configured server.
func (c *Client) Exists(ctx context.Context, hash string) (bool, string, error) {
	for _, server := range c.servers {
		url := server + "/" + hash
		exists, err := c.checkExists(ctx, url)
		if err != nil {
			continue
		}
		if exists {
			return true, server, nil
		}
	}
	return false, "", nil
}

func (c *Client) checkExists(ctx context.Context, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}
