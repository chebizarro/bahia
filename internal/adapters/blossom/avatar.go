package blossom

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// AvatarRefPrefix is the portable reference scheme used for Blossom-backed avatars.
	AvatarRefPrefix          = "blossom:"
	defaultAvatarContentType = "image/png"
	defaultMaxAvatarBytes    = 10 * 1024 * 1024
)

// AvatarStoreResult describes where a generated avatar was stored and the
// portable reference callers should persist on soul draft/read-model assets.
type AvatarStoreResult struct {
	Ref         string
	Hash        string
	URL         string
	ContentType string
	Size        int64
	Fallback    bool
}

// AvatarPreview contains avatar bytes resolved from either a blossom:<hash>
// reference or a trusted direct URL fallback reference.
type AvatarPreview struct {
	Ref         string
	Hash        string
	URL         string
	ContentType string
	Data        []byte
}

// AvatarResolveOption customizes avatar preview retrieval.
type AvatarResolveOption func(*avatarResolveOptions)

type avatarResolveOptions struct {
	allowDirectURLs bool
	maxBytes        int64
}

// AllowDirectAvatarPreviewURLs allows ResolveAvatarRef to dereference HTTP(S)
// fallback refs. Callers should only enable this for trusted refs; Blossom refs
// do not need this option.
func AllowDirectAvatarPreviewURLs() AvatarResolveOption {
	return func(o *avatarResolveOptions) { o.allowDirectURLs = true }
}

// WithMaxAvatarPreviewBytes sets a response body cap for direct URL previews.
func WithMaxAvatarPreviewBytes(maxBytes int64) AvatarResolveOption {
	return func(o *avatarResolveOptions) {
		if maxBytes > 0 {
			o.maxBytes = maxBytes
		}
	}
}

// StoreAvatar uploads image data to Blossom and returns a blossom:<hash> ref.
// If Blossom is unavailable or upload fails, a non-empty direct fallback URL is
// returned as the ref so callers can still preserve a previewable avatar.
func (c *Client) StoreAvatar(ctx context.Context, data []byte, contentType, fallbackURL string) (*AvatarStoreResult, error) {
	contentType = normalizeAvatarContentType(contentType, data)
	fallbackURL = strings.TrimSpace(fallbackURL)

	var uploadErr error
	if c != nil && len(c.servers) > 0 && len(data) > 0 {
		bd, err := c.Upload(ctx, data, contentType)
		if err == nil {
			stored, err := c.avatarStoreResultFromDescriptor(bd, data, contentType)
			if err == nil {
				return stored, nil
			}
			uploadErr = err
		} else {
			uploadErr = err
		}
	}

	if fallbackURL != "" {
		if err := validateDirectURL(fallbackURL); err != nil {
			if uploadErr != nil {
				return nil, fmt.Errorf("Blossom upload failed (%v) and fallback URL is invalid: %w", uploadErr, err)
			}
			return nil, err
		}
		return &AvatarStoreResult{
			Ref:         fallbackURL,
			URL:         fallbackURL,
			ContentType: contentType,
			Size:        int64(len(data)),
			Fallback:    true,
		}, nil
	}

	if uploadErr != nil {
		return nil, fmt.Errorf("store avatar in Blossom: %w", uploadErr)
	}
	return nil, fmt.Errorf("no Blossom storage configured and no direct avatar URL fallback available")
}

// ResolveAvatarRef retrieves avatar bytes for preview from a blossom:<hash> ref,
// a raw SHA-256 hash, or an explicitly trusted HTTP(S) direct URL fallback.
func (c *Client) ResolveAvatarRef(ctx context.Context, ref string, opts ...AvatarResolveOption) (*AvatarPreview, error) {
	ref = strings.TrimSpace(ref)
	resolveOpts := avatarResolveOptions{maxBytes: defaultMaxAvatarBytes}
	for _, opt := range opts {
		if opt != nil {
			opt(&resolveOpts)
		}
	}
	if ref == "" {
		return nil, fmt.Errorf("avatar ref is empty")
	}

	if hash, err := HashFromRef(ref); err == nil {
		if c == nil {
			return nil, fmt.Errorf("Blossom client is required to resolve %s refs", AvatarRefPrefix)
		}
		data, err := c.DownloadByHash(ctx, hash)
		if err != nil {
			return nil, err
		}
		return &AvatarPreview{Ref: RefFromHash(hash), Hash: hash, ContentType: normalizeAvatarContentType("", data), Data: data}, nil
	}

	if err := validateSHA256Hash(ref); err == nil {
		if c == nil {
			return nil, fmt.Errorf("Blossom client is required to resolve raw hash avatar refs")
		}
		hash := strings.ToLower(ref)
		data, err := c.DownloadByHash(ctx, hash)
		if err != nil {
			return nil, err
		}
		return &AvatarPreview{Ref: RefFromHash(hash), Hash: hash, ContentType: normalizeAvatarContentType("", data), Data: data}, nil
	}

	if err := validateDirectURL(ref); err != nil {
		return nil, fmt.Errorf("unsupported avatar ref %q: %w", ref, err)
	}
	if !resolveOpts.allowDirectURLs {
		return nil, fmt.Errorf("direct avatar URL preview requires explicit opt-in")
	}
	data, contentType, err := c.downloadAvatarURL(ctx, ref, resolveOpts.maxBytes)
	if err != nil {
		return nil, err
	}
	return &AvatarPreview{Ref: ref, URL: ref, ContentType: contentType, Data: data}, nil
}

// RefFromHash returns the canonical Blossom avatar reference for a SHA-256 hash.
func RefFromHash(hash string) string {
	return AvatarRefPrefix + strings.ToLower(strings.TrimSpace(hash))
}

// HashFromRef extracts and validates the hash from a blossom:<hash> ref.
func HashFromRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), AvatarRefPrefix) {
		return "", fmt.Errorf("not a Blossom ref")
	}
	hash := strings.ToLower(strings.TrimSpace(ref[len(AvatarRefPrefix):]))
	if err := validateSHA256Hash(hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (c *Client) avatarStoreResultFromDescriptor(bd *BlobDescriptor, data []byte, contentType string) (*AvatarStoreResult, error) {
	if bd == nil {
		return nil, fmt.Errorf("empty Blossom upload descriptor")
	}
	hash := strings.ToLower(strings.TrimSpace(bd.SHA256))
	if hash == "" {
		hash = ComputeSHA256(data)
	}
	if err := validateSHA256Hash(hash); err != nil {
		return nil, fmt.Errorf("invalid Blossom upload hash: %w", err)
	}
	urlValue := strings.TrimSpace(bd.URL)
	if urlValue == "" && len(c.servers) > 0 {
		urlValue = strings.TrimRight(c.servers[0], "/") + "/" + hash
	}
	resultType := strings.TrimSpace(bd.Type)
	if resultType == "" {
		resultType = contentType
	}
	size := bd.Size
	if size == 0 {
		size = int64(len(data))
	}
	return &AvatarStoreResult{
		Ref:         RefFromHash(hash),
		Hash:        hash,
		URL:         urlValue,
		ContentType: resultType,
		Size:        size,
	}, nil
}

func (c *Client) downloadAvatarURL(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if c != nil && c.httpClient != nil {
		client = c.httpClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating avatar preview request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetching avatar preview: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("avatar preview returned %d: %s", resp.StatusCode, string(body))
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("reading avatar preview: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("avatar preview exceeds %d bytes", maxBytes)
	}
	return data, normalizeAvatarContentType(resp.Header.Get("Content-Type"), data), nil
}

func normalizeAvatarContentType(contentType string, data []byte) string {
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if contentType != "" {
		return contentType
	}
	if len(data) > 0 {
		detected := http.DetectContentType(data)
		if detected != "" {
			return detected
		}
	}
	return defaultAvatarContentType
}

func validateDirectURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("direct avatar URL must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("direct avatar URL must include a host")
	}
	return nil
}

func validateSHA256Hash(hash string) error {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 {
		return fmt.Errorf("SHA-256 hash must be 64 hex characters")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return fmt.Errorf("SHA-256 hash must be hex: %w", err)
	}
	return nil
}
