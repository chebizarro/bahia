package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	ghcrRegistryURL = "https://ghcr.io"
	ghcrTokenURL    = "https://ghcr.io/token"
)

// GHCRAuth provides token authentication for GitHub Container Registry.
// Supports both anonymous and PAT-based authentication.
type GHCRAuth struct {
	pat string // personal access token (empty for anonymous)

	mu    sync.Mutex
	cache map[string]*tokenEntry
}

type tokenEntry struct {
	token   string
	expires time.Time
}

// NewGHCRAuth creates a GHCR auth provider.
// If pat is empty, anonymous authentication is used (public repos only).
func NewGHCRAuth(pat string) *GHCRAuth {
	return &GHCRAuth{
		pat:   pat,
		cache: make(map[string]*tokenEntry),
	}
}

// Token returns a bearer token for the given scope.
func (a *GHCRAuth) Token(ctx context.Context, scope string) (string, error) {
	a.mu.Lock()
	if entry, ok := a.cache[scope]; ok && time.Now().Before(entry.expires) {
		token := entry.token
		a.mu.Unlock()
		return token, nil
	}
	a.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ghcrTokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}

	q := req.URL.Query()
	q.Set("scope", scope)
	req.URL.RawQuery = q.Encode()

	// Use PAT as basic auth if provided (for private repos).
	if a.pat != "" {
		req.SetBasicAuth("_token", a.pat)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting GHCR token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GHCR token endpoint returned %d", resp.StatusCode)
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding GHCR token response: %w", err)
	}

	// Cache with a safety margin.
	ttl := time.Duration(result.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}

	a.mu.Lock()
	a.cache[scope] = &tokenEntry{
		token:   result.Token,
		expires: time.Now().Add(ttl - 10*time.Second),
	}
	a.mu.Unlock()

	return result.Token, nil
}

// NewGHCRClient creates an OCI client configured for GitHub Container Registry.
// pat can be empty for public repository access.
func NewGHCRClient(pat string, logger *zap.Logger, opts ...OCIOption) *OCIClient {
	auth := NewGHCRAuth(pat)
	allOpts := append([]OCIOption{WithAuth(auth)}, opts...)
	return NewOCIClient(ghcrRegistryURL, logger, allOpts...)
}

// Compile-time interface check.
var _ AuthProvider = (*GHCRAuth)(nil)
