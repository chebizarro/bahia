// Package client provides Bahia Nostr reads, intents, and keyed ContextVM requests.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// Phase 5 F2: retained until F3 lands.
// Client is the CLI's temporary HTTP fallback client.
type Client struct {
	baseURL               string
	httpClient            *http.Client
	authorizationProvider AuthorizationProvider
}

// AuthorizationProvider returns an Authorization header value for one HTTP request.
// Implementations must create a fresh value for each call; NIP-98 validators reject
// replayed event IDs.
// Phase 5 F2: retained until F3 lands.
type AuthorizationProvider interface {
	AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error)
}

type payloadAuthorizationProvider interface {
	AuthorizationHeaderWithPayload(ctx context.Context, method, absoluteURL string, payload []byte) (string, error)
}

// New creates a new Bahia API client.
// Phase 5 F2: retained until F3 lands.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Phase 5 F2: retained until F3 lands.
// SetAuthorizationProvider configures the CLI compatibility requests.
func (c *Client) SetAuthorizationProvider(provider AuthorizationProvider) {
	c.authorizationProvider = provider
}

// Phase 5 F2: retained until F3 lands.
// NIP98PrivateKeyProvider signs HTTP requests with local key material.
type NIP98PrivateKeyProvider struct {
	PrivateKey string
	Clock      func() time.Time
}

// Phase 5 F2: retained until F3 lands.
// NIP98SignerProvider signs Bahia HTTP requests through a remote or local
// canonical Nostr signer without requiring private key material in-process.
type NIP98SignerProvider struct {
	Signer nostr.Signer
	Clock  func() time.Time
}

// NewNIP98SignerProvider returns a NIP-98 provider backed by signer.
// Phase 5 F2: retained until F3 lands.
func NewNIP98SignerProvider(signer nostr.Signer) (*NIP98SignerProvider, error) {
	if signer == nil {
		return nil, fmt.Errorf("NIP-98 signer is required")
	}
	return &NIP98SignerProvider{Signer: signer}, nil
}

// AuthorizationHeader returns a fresh NIP-98 Authorization header signed by
// the configured canonical signer.
func (p *NIP98SignerProvider) AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error) {
	return p.AuthorizationHeaderWithPayload(ctx, method, absoluteURL, nil)
}

// AuthorizationHeaderWithPayload includes the mandatory NIP-98 payload hash
// when the HTTP request has a body.
func (p *NIP98SignerProvider) AuthorizationHeaderWithPayload(ctx context.Context, method, absoluteURL string, payload []byte) (string, error) {
	if p == nil || p.Signer == nil {
		return "", fmt.Errorf("NIP-98 signer is required")
	}
	createdAt := nostr.Now()
	if p.Clock != nil {
		createdAt = nostr.Timestamp(p.Clock().Unix())
	}
	nonce, err := randomNonce()
	if err != nil {
		return "", fmt.Errorf("generate NIP-98 nonce: %w", err)
	}
	event := nostr.Event{
		Kind:      kinds.HTTPAuth,
		CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"u", absoluteURL},
			{"method", strings.ToUpper(method)},
			{"nonce", nonce},
		},
		Content: "",
	}
	if len(payload) > 0 {
		digest := sha256.Sum256(payload)
		event.Tags = append(event.Tags, nostr.Tag{"payload", hex.EncodeToString(digest[:])})
	}
	if err := p.Signer.SignEvent(ctx, &event); err != nil {
		return "", fmt.Errorf("sign NIP-98 event: %w", err)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("encode NIP-98 event: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(eventJSON), nil
}

// NewNIP98PrivateKeyProvider validates key material and returns a NIP-98 signer.
// Phase 5 F2: retained until F3 lands.
func NewNIP98PrivateKeyProvider(privateKey string) (*NIP98PrivateKeyProvider, error) {
	provider := &NIP98PrivateKeyProvider{PrivateKey: privateKey}
	if _, err := provider.normalizedPrivateKey(); err != nil {
		return nil, err
	}
	return provider, nil
}

// AuthorizationHeader returns a fresh NIP-98 Authorization header for method and absoluteURL.
func (p *NIP98PrivateKeyProvider) AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error) {
	return p.AuthorizationHeaderWithPayload(ctx, method, absoluteURL, nil)
}

// AuthorizationHeaderWithPayload includes the mandatory NIP-98 payload hash
// when the HTTP request has a body.
func (p *NIP98PrivateKeyProvider) AuthorizationHeaderWithPayload(ctx context.Context, method, absoluteURL string, payload []byte) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	privateKey, err := p.normalizedPrivateKey()
	if err != nil {
		return "", err
	}
	createdAt := nostr.Now()
	if p.Clock != nil {
		createdAt = nostr.Timestamp(p.Clock().Unix())
	}
	nonce, err := randomNonce()
	if err != nil {
		return "", fmt.Errorf("generate NIP-98 nonce: %w", err)
	}

	event := nostr.Event{
		Kind:      kinds.HTTPAuth,
		CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"u", absoluteURL},
			{"method", strings.ToUpper(method)},
			{"nonce", nonce},
		},
		Content: "",
	}
	if len(payload) > 0 {
		digest := sha256.Sum256(payload)
		event.Tags = append(event.Tags, nostr.Tag{"payload", hex.EncodeToString(digest[:])})
	}
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		return "", fmt.Errorf("parse Nostr private key: %w", err)
	}
	if err := event.Sign(secret); err != nil {
		return "", fmt.Errorf("sign NIP-98 event: %w", err)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("encode NIP-98 event: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(eventJSON), nil
}

// PublicKey returns the hex public key derived from the provider's private key.
func (p *NIP98PrivateKeyProvider) PublicKey() (string, error) {
	privateKey, err := p.normalizedPrivateKey()
	if err != nil {
		return "", err
	}
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		return "", fmt.Errorf("parse Nostr private key: %w", err)
	}
	return secret.Public().Hex(), nil
}

// Npub returns the NIP-19 npub form of the provider's public key.
func (p *NIP98PrivateKeyProvider) Npub() (string, error) {
	pubkey, err := p.PublicKey()
	if err != nil {
		return "", err
	}
	parsed, err := nostr.PubKeyFromHex(pubkey)
	if err != nil {
		return "", fmt.Errorf("parse Nostr public key: %w", err)
	}
	return nip19.EncodeNpub(parsed), nil
}

func (p *NIP98PrivateKeyProvider) normalizedPrivateKey() (string, error) {
	return NormalizeNostrPrivateKey(p.PrivateKey)
}

func randomNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(nonce[:]), nil
}

type apiResponse struct {
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

// Phase 5 F2: retained until F3 lands.
func (c *Client) do(ctx context.Context, method, path string, body any, result any) error {
	var reqBody io.Reader
	var requestBody []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		requestBody = b
		reqBody = bytes.NewReader(requestBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.applyAuthorization(ctx, req, requestBody); err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if apiResp.Error != "" {
		return fmt.Errorf("API error: %s", apiResp.Error)
	}

	if result != nil && apiResp.Data != nil {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			return fmt.Errorf("decoding data: %w", err)
		}
	}

	return nil
}

// Phase 5 F2: retained until F3 lands.
func (c *Client) applyAuthorization(ctx context.Context, req *http.Request, payload []byte) error {
	if c.authorizationProvider == nil {
		return nil
	}
	var header string
	var err error
	if provider, ok := c.authorizationProvider.(payloadAuthorizationProvider); ok {
		header, err = provider.AuthorizationHeaderWithPayload(ctx, req.Method, req.URL.String(), payload)
	} else {
		header, err = c.authorizationProvider.AuthorizationHeader(ctx, req.Method, req.URL.String())
	}
	if err != nil {
		return fmt.Errorf("creating authorization header: %w", err)
	}
	if strings.TrimSpace(header) != "" {
		req.Header.Set("Authorization", header)
	}
	return nil
}

// --- Services ---

// ListServices returns all registered services.
// Deprecated: Use NostrClient for service reads. Retained only until Phase 5 F3.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListServices(ctx context.Context) ([]domain.Service, error) {
	var services []domain.Service
	if err := c.do(ctx, http.MethodGet, "/api/v1/services", nil, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// GetService returns a service by ID.
// Deprecated: Use NostrClient for service reads. Retained only until Phase 5 F3.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetService(ctx context.Context, id string) (*domain.Service, error) {
	var svc domain.Service
	if err := c.do(ctx, http.MethodGet, "/api/v1/services/"+id, nil, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

// --- Environments ---

// ListEnvironments returns all environments.
// Deprecated: Use NostrClient for environment reads. Retained only until Phase 5 F3.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	var envs []domain.Environment
	if err := c.do(ctx, http.MethodGet, "/api/v1/environments", nil, &envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// GetEnvironmentDetails returns an environment with its deployment units.
// Deprecated: Use NostrClient for environment reads. Retained only until Phase 5 F3.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetEnvironmentDetails(ctx context.Context, id string) (*EnvironmentDetails, error) {
	var env EnvironmentDetails
	if err := c.do(ctx, http.MethodGet, "/api/v1/environments/"+id, nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// --- State ---

// ListStates returns all environment service states.
// Deprecated: use NostrClient state reads; REST compatibility is removed in Wave 6.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	var states []domain.EnvironmentServiceState
	if err := c.do(ctx, http.MethodGet, "/api/v1/state", nil, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// ListDriftedStates returns all drifted states.
// Deprecated: use NostrClient state reads; REST compatibility is removed in Wave 6.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	var states []domain.EnvironmentServiceState
	if err := c.do(ctx, http.MethodGet, "/api/v1/state/drifted", nil, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// --- Logs ---

// RunLogs contains stdout and stderr from a deployment run.
// Phase 5 F2: retained until F3 lands.
type RunLogs struct {
	RunID    string `json:"run_id"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode *int   `json:"exit_code"`
	Duration string `json:"duration"`
}

// GetRunLogs retrieves logs for a completed deployment run.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetRunLogs(ctx context.Context, runID string, tail int, stream string) (*RunLogs, error) {
	path := fmt.Sprintf("/api/v1/deployments/runs/%s/logs?tail=%d", runID, tail)
	if stream != "" {
		path += "&stream=" + stream
	}
	var logs RunLogs
	if err := c.do(ctx, http.MethodGet, path, nil, &logs); err != nil {
		return nil, err
	}
	return &logs, nil
}

// LogLine represents a single log entry from live streaming.
// Phase 5 F2: retained until F3 lands.
type LogLine struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Message   string `json:"message"`
	Service   string `json:"service"`
}

// StreamLiveLogs streams live logs via SSE. The callback is called for each log line.
// Phase 5 F2: retained until F3 lands.
func (c *Client) StreamLiveLogs(ctx context.Context, serviceID, envID string, tail int, callback func(LogLine)) error {
	path := fmt.Sprintf("/api/v1/services/%s/environments/%s/logs?follow=true&tail=%d", serviceID, envID, tail)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.applyAuthorization(ctx, req, nil); err != nil {
		return err
	}

	streamClient := *c.httpClient
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SSE error: %s", string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var logLine LogLine
			if err := json.Unmarshal([]byte(data), &logLine); err == nil {
				callback(logLine)
			}
		}
	}
	return scanner.Err()
}

// --- Policies ---

// ListPolicies returns all deployment policies.
// Deprecated: use NostrClient policy reads; REST compatibility is removed in Wave 6.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListPolicies(ctx context.Context) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	if err := c.do(ctx, http.MethodGet, "/api/v1/policies", nil, &policies); err != nil {
		return nil, err
	}
	return policies, nil
}

// GetPolicy returns a policy by ID.
// Deprecated: use NostrClient policy reads; REST compatibility is removed in Wave 6.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetPolicy(ctx context.Context, id string) (*domain.DeploymentPolicy, error) {
	var policy domain.DeploymentPolicy
	if err := c.do(ctx, http.MethodGet, "/api/v1/policies/"+id, nil, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// --- Secrets ---

// ListSecrets returns secrets for a service.
// Deprecated: use the NostrClient confidential secret read path.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListSecrets(ctx context.Context, serviceID string) ([]SecretRef, error) {
	var secrets []SecretRef
	if err := c.do(ctx, http.MethodGet, "/api/v1/services/"+serviceID+"/secrets", nil, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// --- Organizations ---

// ListOrgs returns organizations the current user is a member of.
// Deprecated: use the NostrClient confidential organization read path.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListOrgs(ctx context.Context) ([]domain.Organization, error) {
	var orgs []domain.Organization
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs", nil, &orgs); err != nil {
		return nil, err
	}
	return orgs, nil
}

// GetOrg returns an organization by ID or name.
// Deprecated: use the NostrClient confidential organization read path.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetOrg(ctx context.Context, idOrName string) (*domain.Organization, error) {
	var org domain.Organization
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+idOrName, nil, &org); err != nil {
		return nil, err
	}
	return &org, nil
}

// ListOrgMembers returns members of an organization.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListOrgMembers(ctx context.Context, orgID string) ([]domain.OrgMember, error) {
	var members []domain.OrgMember
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+orgID+"/members", nil, &members); err != nil {
		return nil, err
	}
	return members, nil
}
