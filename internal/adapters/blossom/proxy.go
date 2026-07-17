package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// BlobHead contains metadata returned by a HEAD request.
type BlobHead struct {
	Exists        bool
	URL           string
	ContentLength int64
	ContentType   string
	Header        http.Header
}

// BlobStream is an open HTTP stream for transparent proxying.
type BlobStream struct {
	URL           string
	ContentLength int64
	ContentType   string
	Header        http.Header
	Body          io.ReadCloser
}

// Close closes the underlying stream body.
func (s *BlobStream) Close() error {
	if s == nil || s.Body == nil {
		return nil
	}
	return s.Body.Close()
}

// UploadFile uploads a file without buffering the full contents in memory.
// If expectedHash is empty, it is computed by streaming the file.
func (c *Client) UploadFile(ctx context.Context, path, contentType, expectedHash string) (*BlobDescriptor, error) {
	if len(c.servers) == 0 {
		return nil, fmt.Errorf("no Blossom servers configured")
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory: %s", path)
	}

	size := info.Size()
	hash := strings.TrimSpace(expectedHash)
	if hash == "" {
		hash, err = computeFileSHA256(path)
		if err != nil {
			return nil, err
		}
	}

	exists, server, err := c.Exists(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("checking existing blob: %w", err)
	}
	if exists {
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return &BlobDescriptor{
			URL:      server + "/" + hash,
			SHA256:   hash,
			Size:     size,
			Type:     contentType,
			Uploaded: time.Now(),
		}, nil
	}

	var lastErr error
	for _, server := range c.servers {
		bd, err := c.uploadFileToServer(ctx, server, path, size, contentType, hash)
		if err != nil {
			c.recordUpload(server, false)
			lastErr = err
			c.logger.Warn("file upload failed, trying next server", "server", server, "error", err)
			continue
		}
		c.recordUpload(server, true)
		return bd, nil
	}

	return nil, fmt.Errorf("failed to upload file to any Blossom server: %w", lastErr)
}

func computeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Client) uploadFileToServer(ctx context.Context, server, path string, size int64, contentType, hash string) (*BlobDescriptor, error) {
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

		bd, err := c.doUploadFile(ctx, url, path, size, contentType, hash)
		if err != nil {
			lastErr = err
			continue
		}
		return bd, nil
	}

	return nil, fmt.Errorf("after %d retries: %w", c.maxRetries, lastErr)
}

func (c *Client) doUploadFile(ctx context.Context, url, path string, size int64, contentType, hash string) (*BlobDescriptor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("X-SHA-256", hash)
	req.ContentLength = size

	if err := c.applyAuthHeader(ctx, req, http.MethodPut, hash); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return nil, fmt.Errorf("reading upload response: %w", err)
	}
	if len(body) > 64*1024 {
		return nil, fmt.Errorf("upload response exceeds 65536 bytes")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var bd BlobDescriptor
	if err := json.Unmarshal(body, &bd); err != nil {
		return nil, fmt.Errorf("decoding upload descriptor: %w", err)
	}
	if err := validateUploadDescriptor(&bd, hash, size); err != nil {
		return nil, err
	}
	if bd.Type == "" {
		bd.Type = req.Header.Get("Content-Type")
	}

	return &bd, nil
}

// HeadByURL checks existence and returns metadata including content length.
func (c *Client) HeadByURL(ctx context.Context, url string) (*BlobHead, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if err := c.applyAuthHeader(ctx, req, http.MethodHead, ""); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("head request: %w", err)
	}
	defer resp.Body.Close()

	result := &BlobHead{
		Exists:        resp.StatusCode == http.StatusOK,
		URL:           url,
		ContentLength: resp.ContentLength,
		ContentType:   resp.Header.Get("Content-Type"),
		Header:        resp.Header.Clone(),
	}

	if resp.StatusCode == http.StatusNotFound {
		return result, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return result, nil
}

// OpenStreamByURL opens a GET stream for transparent byte/header proxying.
func (c *Client) OpenStreamByURL(ctx context.Context, url string) (*BlobStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if err := c.applyAuthHeader(ctx, req, http.MethodGet, ""); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opening stream: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return &BlobStream{
		URL:           url,
		ContentLength: resp.ContentLength,
		ContentType:   resp.Header.Get("Content-Type"),
		Header:        resp.Header.Clone(),
		Body:          resp.Body,
	}, nil
}
