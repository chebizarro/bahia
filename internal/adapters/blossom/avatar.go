package blossom

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
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
	allowedOrigins  map[string]struct{}
}

// AllowDirectAvatarPreviewURLs allows ResolveAvatarRef to dereference HTTP(S)
// fallback refs. Callers should only enable this for trusted refs; Blossom refs
// do not need this option.
func AllowDirectAvatarPreviewURLs() AvatarResolveOption {
	return func(o *avatarResolveOptions) { o.allowDirectURLs = true }
}

// WithAllowedAvatarPreviewOrigins allows exact HTTP(S) origins for direct
// previews. This is intended for explicitly configured generation/CDN origins,
// including trusted private-network services.
func WithAllowedAvatarPreviewOrigins(origins ...string) AvatarResolveOption {
	return func(o *avatarResolveOptions) {
		if o.allowedOrigins == nil {
			o.allowedOrigins = make(map[string]struct{})
		}
		for _, origin := range origins {
			if normalized, err := normalizeHTTPOrigin(origin); err == nil {
				o.allowedOrigins[normalized] = struct{}{}
			}
		}
	}
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
		data, contentType, err := c.downloadAvatarByHash(ctx, hash, resolveOpts.maxBytes)
		if err != nil {
			return nil, err
		}
		return &AvatarPreview{Ref: RefFromHash(hash), Hash: hash, ContentType: contentType, Data: data}, nil
	}

	if err := validateSHA256Hash(ref); err == nil {
		if c == nil {
			return nil, fmt.Errorf("Blossom client is required to resolve raw hash avatar refs")
		}
		hash := strings.ToLower(ref)
		data, contentType, err := c.downloadAvatarByHash(ctx, hash, resolveOpts.maxBytes)
		if err != nil {
			return nil, err
		}
		return &AvatarPreview{Ref: RefFromHash(hash), Hash: hash, ContentType: contentType, Data: data}, nil
	}

	if err := validateDirectURL(ref); err != nil {
		origin, originErr := normalizeHTTPOrigin(ref)
		if originErr != nil {
			return nil, fmt.Errorf("unsupported avatar ref %q: %w", ref, err)
		}
		if _, ok := resolveOpts.allowedOrigins[origin]; !ok {
			return nil, fmt.Errorf("unsupported avatar ref %q: %w", ref, err)
		}
	}
	if !resolveOpts.allowDirectURLs {
		return nil, fmt.Errorf("direct avatar URL preview requires explicit opt-in")
	}
	data, contentType, err := c.downloadAvatarURL(ctx, ref, resolveOpts.maxBytes, resolveOpts.allowedOrigins)
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
	if err := validateSHA256Hash(hash); err != nil {
		return nil, fmt.Errorf("invalid Blossom upload hash: %w", err)
	}
	if !VerifySHA256(data, hash) {
		return nil, fmt.Errorf("Blossom upload hash does not match avatar bytes")
	}
	urlValue := strings.TrimSpace(bd.URL)
	if urlValue == "" {
		return nil, fmt.Errorf("Blossom upload descriptor URL is empty")
	}
	resultType := strings.TrimSpace(bd.Type)
	if resultType == "" {
		resultType = contentType
	}
	size := bd.Size
	if size != int64(len(data)) {
		return nil, fmt.Errorf("Blossom upload size %d does not match avatar size %d", size, len(data))
	}
	return &AvatarStoreResult{
		Ref:         RefFromHash(hash),
		Hash:        hash,
		URL:         urlValue,
		ContentType: resultType,
		Size:        size,
	}, nil
}

func (c *Client) downloadAvatarURL(ctx context.Context, rawURL string, maxBytes int64, allowedOrigins map[string]struct{}) ([]byte, string, error) {
	if err := validateOutboundAvatarURL(ctx, rawURL, allowedOrigins); err != nil {
		return nil, "", err
	}
	baseClient := &http.Client{Timeout: 30 * time.Second}
	if c != nil && c.httpClient != nil {
		baseClient = c.httpClient
	}
	client := *baseClient
	originalRedirectCheck := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if err := validateOutboundAvatarURL(req.Context(), req.URL.String(), allowedOrigins); err != nil {
			return fmt.Errorf("unsafe avatar preview redirect: %w", err)
		}
		if originalRedirectCheck != nil {
			return originalRedirectCheck(req, via)
		}
		return nil
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, "", fmt.Errorf("avatar preview returned %d: %s", resp.StatusCode, string(body))
	}
	return readAndValidateAvatar(resp.Body, resp.Header.Get("Content-Type"), maxBytes)
}

func (c *Client) downloadAvatarByHash(ctx context.Context, hash string, maxBytes int64) ([]byte, string, error) {
	var lastErr error
	for _, server := range c.servers {
		data, err := c.downloadFromServerWithLimit(ctx, server+"/"+hash, maxBytes)
		if err != nil {
			lastErr = err
			continue
		}
		if !VerifySHA256(data, hash) {
			lastErr = fmt.Errorf("SHA-256 mismatch from %s", server)
			continue
		}
		validated, contentType, err := validateAvatarBytes(data, "")
		if err != nil {
			lastErr = err
			continue
		}
		return validated, contentType, nil
	}
	return nil, "", fmt.Errorf("failed to download avatar %s from any Blossom server: %w", hash, lastErr)
}

func readAndValidateAvatar(reader io.Reader, contentType string, maxBytes int64) ([]byte, string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading avatar preview: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("avatar preview exceeds %d bytes", maxBytes)
	}
	return validateAvatarBytes(data, contentType)
}

func validateAvatarBytes(data []byte, contentType string) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", errors.New("avatar image is empty")
	}
	declared := normalizeAvatarContentType(contentType, data)
	detected := strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0])
	allowed := func(value string) bool {
		switch strings.ToLower(value) {
		case "image/png", "image/jpeg", "image/gif":
			return true
		default:
			return false
		}
	}
	if !allowed(declared) || !allowed(detected) || !strings.EqualFold(declared, detected) {
		return nil, "", fmt.Errorf("avatar content type %q does not match allowed detected type %q", declared, detected)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode avatar image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 {
		return nil, "", fmt.Errorf("avatar dimensions %dx%d are outside 1..4096", config.Width, config.Height)
	}
	return data, detected, nil
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
	if parsed.User != nil {
		return fmt.Errorf("direct avatar URL must not include userinfo")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return fmt.Errorf("direct avatar URL host is not allowed")
	}
	if ip := net.ParseIP(hostname); ip != nil && unsafeAvatarIP(ip) {
		return fmt.Errorf("direct avatar URL IP is not allowed")
	}
	return nil
}

func validateOutboundAvatarURL(ctx context.Context, rawURL string, allowedOrigins map[string]struct{}) error {
	if err := validateDirectURL(rawURL); err != nil {
		parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			return err
		}
		origin, originErr := normalizeHTTPOrigin(parsed.Scheme + "://" + parsed.Host)
		if originErr != nil {
			return err
		}
		if _, ok := allowedOrigins[origin]; !ok {
			return err
		}
	}
	origin, err := normalizeHTTPOrigin(rawURL)
	if err != nil {
		return err
	}
	if _, ok := allowedOrigins[origin]; ok {
		return nil
	}
	parsed, _ := url.Parse(rawURL)
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve avatar URL host: %w", err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("avatar URL host resolved to no addresses")
	}
	for _, address := range addresses {
		if unsafeAvatarIP(address.IP) {
			return fmt.Errorf("avatar URL host resolves to a non-public address")
		}
	}
	return nil
}

func normalizeHTTPOrigin(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("invalid HTTP(S) origin")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func unsafeAvatarIP(ip net.IP) bool {
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
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
