// Package client provides an HTTP client for the Bahia API.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Client is an HTTP client for the Bahia API.
type Client struct {
	baseURL               string
	httpClient            *http.Client
	authorizationProvider AuthorizationProvider
}

// AuthorizationProvider returns an Authorization header value for one HTTP request.
// Implementations must create a fresh value for each call; NIP-98 validators reject
// replayed event IDs.
type AuthorizationProvider interface {
	AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error)
}

// New creates a new Bahia API client.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetAuthorizationProvider sets the per-request authorization provider.
func (c *Client) SetAuthorizationProvider(provider AuthorizationProvider) {
	c.authorizationProvider = provider
}

// NIP98PrivateKeyProvider signs Bahia HTTP requests with NIP-98 using local key material.
// PrivateKey may be a 64-character hex private key or an nsec value.
type NIP98PrivateKeyProvider struct {
	PrivateKey string
	Clock      func() time.Time
}

// NewNIP98PrivateKeyProvider validates key material and returns a NIP-98 signer.
func NewNIP98PrivateKeyProvider(privateKey string) (*NIP98PrivateKeyProvider, error) {
	provider := &NIP98PrivateKeyProvider{PrivateKey: privateKey}
	if _, err := provider.normalizedPrivateKey(); err != nil {
		return nil, err
	}
	return provider, nil
}

// AuthorizationHeader returns a fresh NIP-98 Authorization header for method and absoluteURL.
func (p *NIP98PrivateKeyProvider) AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error) {
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
		Kind:      27235,
		CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"u", absoluteURL},
			{"method", strings.ToUpper(method)},
			{"nonce", nonce},
		},
		Content: "",
	}
	if err := event.Sign(privateKey); err != nil {
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
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("derive Nostr public key: %w", err)
	}
	return pubkey, nil
}

// Npub returns the NIP-19 npub form of the provider's public key.
func (p *NIP98PrivateKeyProvider) Npub() (string, error) {
	pubkey, err := p.PublicKey()
	if err != nil {
		return "", err
	}
	npub, err := nip19.EncodePublicKey(pubkey)
	if err != nil {
		return "", fmt.Errorf("encode npub: %w", err)
	}
	return npub, nil
}

func (p *NIP98PrivateKeyProvider) normalizedPrivateKey() (string, error) {
	return NormalizeNostrPrivateKey(p.PrivateKey)
}

// NormalizeNostrPrivateKey returns a 64-character hex private key from hex or nsec input.
func NormalizeNostrPrivateKey(privateKey string) (string, error) {
	key := strings.TrimSpace(privateKey)
	if key == "" {
		return "", fmt.Errorf("nostr private key is required")
	}
	if strings.HasPrefix(key, "nsec") {
		prefix, value, err := nip19.Decode(key)
		if err != nil {
			return "", fmt.Errorf("decode nsec: %w", err)
		}
		if prefix != "nsec" {
			return "", fmt.Errorf("expected nsec key, got %s", prefix)
		}
		sk, ok := value.(string)
		if !ok || sk == "" {
			return "", fmt.Errorf("decoded nsec did not contain a private key")
		}
		key = sk
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("nostr private key must be 32 bytes of hex or nsec")
	}
	if _, err := nostr.GetPublicKey(key); err != nil {
		return "", fmt.Errorf("derive Nostr public key: %w", err)
	}
	return strings.ToLower(key), nil
}

func randomNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(nonce[:]), nil
}

// AdoptionTarget identifies one Docker host for adoption scan/import.
type AdoptionTarget struct {
	Name            string `json:"name"`
	EndpointRef     string `json:"endpoint_ref,omitempty"`
	DockerHost      string `json:"docker_host,omitempty"`
	EnvironmentName string `json:"environment_name,omitempty"`
}

// AdoptionScanRequest requests an adoption preview scan.
type AdoptionScanRequest struct {
	Targets []AdoptionTarget `json:"targets"`
}

// AdoptionSelection selects one discovered container for import.
type AdoptionSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

// AdoptionImportRequest requests adoption import for selected or all containers.
type AdoptionImportRequest struct {
	Targets    []AdoptionTarget    `json:"targets"`
	Selections []AdoptionSelection `json:"selections,omitempty"`
	ImportAll  bool                `json:"import_all,omitempty"`
}

// DiscoveredContainer is a normalized container preview returned by adoption scan.
type DiscoveredContainer struct {
	TargetName              string            `json:"target_name"`
	EnvironmentName         string            `json:"environment_name"`
	ContainerID             string            `json:"container_id"`
	ContainerName           string            `json:"container_name"`
	ImageRef                string            `json:"image_ref"`
	ImageRepo               string            `json:"image_repo"`
	ImageTag                string            `json:"image_tag"`
	ImageDigest             string            `json:"image_digest"`
	SourceRuntime           string            `json:"source_runtime"`
	Labels                  map[string]string `json:"labels,omitempty"`
	Environment             map[string]string `json:"environment,omitempty"`
	RedactedEnvironmentKeys []string          `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string          `json:"redacted_label_keys,omitempty"`
	Ports                   []string          `json:"ports,omitempty"`
	Volumes                 []string          `json:"volumes,omitempty"`
	Restart                 string            `json:"restart,omitempty"`
	Command                 []string          `json:"command,omitempty"`
	Entrypoint              []string          `json:"entrypoint,omitempty"`
	WorkingDir              string            `json:"working_dir,omitempty"`
	NetworkMode             string            `json:"network_mode,omitempty"`
	Compose                 *ComposeMetadata  `json:"compose,omitempty"`
	HealthStatus            string            `json:"health_status"`
	Warnings                []string          `json:"warnings,omitempty"`
	Adoptable               bool              `json:"adoptable"`
}

// ComposeMetadata preserves public Docker Compose origin metadata.
type ComposeMetadata struct {
	ProjectName string   `json:"project_name,omitempty"`
	ServiceName string   `json:"service_name,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files,omitempty"`
}

// AdoptionPreviewContainer is one discovered container plus import proposal metadata.
type AdoptionPreviewContainer struct {
	Discovered          DiscoveredContainer `json:"discovered"`
	ProposedServiceName string              `json:"proposed_service_name"`
	ExistingServiceID   *string             `json:"existing_service_id,omitempty"`
	WillUpdate          bool                `json:"will_update"`
	Warnings            []string            `json:"warnings,omitempty"`
	Adoptable           bool                `json:"adoptable"`
}

// AdoptionPreview groups discovered containers for one target.
type AdoptionPreview struct {
	Target     AdoptionTarget             `json:"target"`
	Containers []AdoptionPreviewContainer `json:"containers"`
	Error      string                     `json:"error,omitempty"`
}

// AdoptionImportResult reports one import candidate outcome.
type AdoptionImportResult struct {
	TargetName              string   `json:"target_name"`
	ContainerID             string   `json:"container_id,omitempty"`
	ContainerName           string   `json:"container_name,omitempty"`
	ServiceName             string   `json:"service_name,omitempty"`
	ServiceID               *string  `json:"service_id,omitempty"`
	EnvironmentID           *string  `json:"environment_id,omitempty"`
	BuildID                 *string  `json:"build_id,omitempty"`
	ArtifactID              *string  `json:"artifact_id,omitempty"`
	Status                  string   `json:"status"`
	Warnings                []string `json:"warnings,omitempty"`
	RedactedEnvironmentKeys []string `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string `json:"redacted_label_keys,omitempty"`
	Error                   string   `json:"error,omitempty"`
}

// RuntimeActionResult reports a direct runtime action result.
type RuntimeActionResult struct {
	Action        string              `json:"action"`
	ServiceID     string              `json:"service_id"`
	EnvironmentID string              `json:"environment_id"`
	Observation   *RuntimeObservation `json:"observation,omitempty"`
}

// RuntimeObservation is the public runtime observation response shape.
type RuntimeObservation struct {
	ID                  string         `json:"id"`
	ServiceID           string         `json:"service_id"`
	EnvironmentID       string         `json:"environment_id"`
	ObservedImageDigest string         `json:"observed_image_digest"`
	ObservedImageRepo   string         `json:"observed_image_repo,omitempty"`
	ObservedContainerID string         `json:"observed_container_id,omitempty"`
	ObservedHost        string         `json:"observed_host,omitempty"`
	ObservedVersion     string         `json:"observed_version,omitempty"`
	HealthStatus        string         `json:"health_status"`
	Source              string         `json:"source"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	ObservedAt          time.Time      `json:"observed_at"`
}

// SystemInfo contains the narrow system discovery fields needed by clients.
type SystemInfo struct {
	Nostr    SystemInfoNostr `json:"nostr"`
	Features map[string]bool `json:"features,omitempty"`
}

// SystemInfoNostr contains browser-safe relay discovery fields.
type SystemInfoNostr struct {
	BrowserRelays                 []string `json:"browser_relays,omitempty"`
	SidecarURL                    string   `json:"sidecar_url,omitempty"`
	BrowserEncryptedRequestRelays []string `json:"browser_encrypted_request_relays,omitempty"`
	// PrivateBrowserRelays is a deprecated alias for BrowserEncryptedRequestRelays.
	PrivateBrowserRelays []string `json:"private_browser_relays,omitempty"`
	ServicePubkey        string   `json:"service_pubkey,omitempty"`
}

type apiResponse struct {
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, result any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.applyAuthorization(ctx, req); err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

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

func (c *Client) applyAuthorization(ctx context.Context, req *http.Request) error {
	if c.authorizationProvider == nil {
		return nil
	}
	header, err := c.authorizationProvider.AuthorizationHeader(ctx, req.Method, req.URL.String())
	if err != nil {
		return fmt.Errorf("creating authorization header: %w", err)
	}
	if strings.TrimSpace(header) != "" {
		req.Header.Set("Authorization", header)
	}
	return nil
}

// GetSystemInfo returns narrow system discovery information needed by clients.
func (c *Client) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := c.do(ctx, http.MethodGet, "/api/v1/system/info", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// --- Adoption ---

// ScanAdoption previews adoptable containers on one or more Docker targets.
func (c *Client) ScanAdoption(ctx context.Context, req AdoptionScanRequest) ([]AdoptionPreview, error) {
	var previews []AdoptionPreview
	if err := c.do(ctx, http.MethodPost, "/api/v1/adoption/scan", req, &previews); err != nil {
		return nil, err
	}
	return previews, nil
}

// ImportAdoption imports selected or all discovered containers into Bahia models.
func (c *Client) ImportAdoption(ctx context.Context, req AdoptionImportRequest) ([]AdoptionImportResult, error) {
	var results []AdoptionImportResult
	if err := c.do(ctx, http.MethodPost, "/api/v1/adoption/import", req, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// --- Services ---

// ListServices returns all registered services.
func (c *Client) ListServices(ctx context.Context) ([]domain.Service, error) {
	var services []domain.Service
	if err := c.do(ctx, http.MethodGet, "/api/v1/services", nil, &services); err != nil {
		return nil, err
	}
	return services, nil
}

// GetService returns a service by ID.
func (c *Client) GetService(ctx context.Context, id string) (*domain.Service, error) {
	var svc domain.Service
	if err := c.do(ctx, http.MethodGet, "/api/v1/services/"+id, nil, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

// CreateService registers a new service.
func (c *Client) CreateService(ctx context.Context, name, artifactRepo string, runtimeType domain.RuntimeType) (*domain.Service, error) {
	body := map[string]string{
		"name":          name,
		"artifact_repo": artifactRepo,
		"runtime_type":  string(runtimeType),
	}
	var svc domain.Service
	if err := c.do(ctx, http.MethodPost, "/api/v1/services", body, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

// DeployServiceRuntime deploys the desired or explicit artifact directly through the resolved runtime.
func (c *Client) DeployServiceRuntime(ctx context.Context, serviceID, envID string, artifactID *string) (*RuntimeActionResult, error) {
	body := map[string]any{}
	if artifactID != nil && *artifactID != "" {
		body["artifact_id"] = *artifactID
	}
	var result RuntimeActionResult
	path := "/api/v1/services/" + serviceID + "/environments/" + envID + "/deploy"
	if err := c.do(ctx, http.MethodPost, path, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// RestartServiceRuntime restarts a service directly through the resolved runtime.
func (c *Client) RestartServiceRuntime(ctx context.Context, serviceID, envID string) (*RuntimeActionResult, error) {
	var result RuntimeActionResult
	path := "/api/v1/services/" + serviceID + "/environments/" + envID + "/restart"
	if err := c.do(ctx, http.MethodPost, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// StopServiceRuntime stops a service directly through the resolved runtime.
func (c *Client) StopServiceRuntime(ctx context.Context, serviceID, envID string) (*RuntimeActionResult, error) {
	var result RuntimeActionResult
	path := "/api/v1/services/" + serviceID + "/environments/" + envID + "/stop"
	if err := c.do(ctx, http.MethodPost, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// --- Environments ---

// ListEnvironments returns all environments.
func (c *Client) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	var envs []domain.Environment
	if err := c.do(ctx, http.MethodGet, "/api/v1/environments", nil, &envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// GetEnvironment returns an environment by ID.
func (c *Client) GetEnvironment(ctx context.Context, id string) (*domain.Environment, error) {
	var env domain.Environment
	if err := c.do(ctx, http.MethodGet, "/api/v1/environments/"+id, nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// CreateEnvironment registers a new environment.
func (c *Client) CreateEnvironment(ctx context.Context, name string, strategy domain.DeployStrategy, protected bool) (*domain.Environment, error) {
	body := map[string]any{
		"name":            name,
		"deploy_strategy": string(strategy),
		"protected":       protected,
	}
	var env domain.Environment
	if err := c.do(ctx, http.MethodPost, "/api/v1/environments", body, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// --- State ---

// ListStates returns all environment service states.
func (c *Client) ListStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	var states []domain.EnvironmentServiceState
	if err := c.do(ctx, http.MethodGet, "/api/v1/state", nil, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// ListDriftedStates returns all drifted states.
func (c *Client) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	var states []domain.EnvironmentServiceState
	if err := c.do(ctx, http.MethodGet, "/api/v1/state/drifted", nil, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// --- Deployments ---

// CreateDeploymentIntent creates a new deployment intent.
func (c *Client) CreateDeploymentIntent(ctx context.Context, serviceID, envID, artifactID, requestedBy string) (*domain.DeploymentIntent, error) {
	body := map[string]string{
		"service_id":     serviceID,
		"environment_id": envID,
		"artifact_id":    artifactID,
		"requested_by":   requestedBy,
	}
	var intent domain.DeploymentIntent
	if err := c.do(ctx, http.MethodPost, "/api/v1/deployments/intents", body, &intent); err != nil {
		return nil, err
	}
	return &intent, nil
}

// Rollback creates a rollback deployment intent.
func (c *Client) Rollback(ctx context.Context, serviceID, envID, requestedBy string) (*domain.DeploymentIntent, error) {
	body := map[string]string{
		"service_id":     serviceID,
		"environment_id": envID,
		"requested_by":   requestedBy,
	}
	var intent domain.DeploymentIntent
	if err := c.do(ctx, http.MethodPost, "/api/v1/rollback", body, &intent); err != nil {
		return nil, err
	}
	return &intent, nil
}

// GetDeploymentRun retrieves a deployment run by ID.
func (c *Client) GetDeploymentRun(ctx context.Context, id string) (*domain.DeploymentRun, error) {
	var run domain.DeploymentRun
	if err := c.do(ctx, http.MethodGet, "/api/v1/deployments/runs/"+id, nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// --- Workers ---

// Worker represents a Loom worker advertisement.
type Worker struct {
	Pubkey       string   `json:"pubkey"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	PricePerSec  int      `json:"price_per_sec"`
	MintURL      string   `json:"mint_url"`
	Relays       []string `json:"relays"`
	LastSeen     string   `json:"last_seen"`
}

// ListWorkers returns all discovered workers.
func (c *Client) ListWorkers(ctx context.Context) ([]Worker, error) {
	var workers []Worker
	if err := c.do(ctx, http.MethodGet, "/api/v1/workers", nil, &workers); err != nil {
		return nil, err
	}
	return workers, nil
}

// GetWorker returns a worker by pubkey.
func (c *Client) GetWorker(ctx context.Context, pubkey string) (*Worker, error) {
	var worker Worker
	if err := c.do(ctx, http.MethodGet, "/api/v1/workers/"+pubkey, nil, &worker); err != nil {
		return nil, err
	}
	return &worker, nil
}

// --- Logs ---

// RunLogs contains stdout and stderr from a deployment run.
type RunLogs struct {
	RunID    string `json:"run_id"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode *int   `json:"exit_code"`
	Duration string `json:"duration"`
}

// GetRunLogs retrieves logs for a completed deployment run.
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
type LogLine struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Message   string `json:"message"`
	Service   string `json:"service"`
}

// StreamLiveLogs streams live logs via SSE. The callback is called for each log line.
func (c *Client) StreamLiveLogs(ctx context.Context, serviceID, envID string, tail int, callback func(LogLine)) error {
	path := fmt.Sprintf("/api/v1/services/%s/environments/%s/logs?follow=true&tail=%d", serviceID, envID, tail)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.applyAuthorization(ctx, req); err != nil {
		return err
	}

	streamClient := *c.httpClient
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
func (c *Client) ListPolicies(ctx context.Context) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	if err := c.do(ctx, http.MethodGet, "/api/v1/policies", nil, &policies); err != nil {
		return nil, err
	}
	return policies, nil
}

// GetPolicy returns a policy by ID.
func (c *Client) GetPolicy(ctx context.Context, id string) (*domain.DeploymentPolicy, error) {
	var policy domain.DeploymentPolicy
	if err := c.do(ctx, http.MethodGet, "/api/v1/policies/"+id, nil, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// CreatePolicy creates a new deployment policy.
func (c *Client) CreatePolicy(ctx context.Context, name string, envID string, rules map[string]any, enforcement string, enabled bool) (*domain.DeploymentPolicy, error) {
	body := map[string]any{
		"name":           name,
		"environment_id": envID,
		"rules":          rules,
		"enforcement":    enforcement,
		"enabled":        enabled,
	}
	var policy domain.DeploymentPolicy
	if err := c.do(ctx, http.MethodPost, "/api/v1/policies", body, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// --- Secrets ---

// SecretRef is a reference to a secret (without the actual value).
type SecretRef struct {
	ID               string `json:"id"`
	ServiceID        string `json:"service_id"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	Name             string `json:"name"`
	EncryptionMethod string `json:"encryption_method"`
	Version          int    `json:"version"`
}

// ListSecrets returns secrets for a service.
func (c *Client) ListSecrets(ctx context.Context, serviceID string) ([]SecretRef, error) {
	var secrets []SecretRef
	if err := c.do(ctx, http.MethodGet, "/api/v1/services/"+serviceID+"/secrets", nil, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// SetSecret creates or updates a secret.
func (c *Client) SetSecret(ctx context.Context, serviceID, name, value string, envID string) (*SecretRef, error) {
	body := map[string]string{
		"name":  name,
		"value": value,
	}
	if envID != "" {
		body["environment_id"] = envID
	}
	var secret SecretRef
	if err := c.do(ctx, http.MethodPost, "/api/v1/services/"+serviceID+"/secrets", body, &secret); err != nil {
		return nil, err
	}
	return &secret, nil
}

// DeleteSecret deletes a secret by ID.
func (c *Client) DeleteSecret(ctx context.Context, serviceID, secretID string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/services/"+serviceID+"/secrets/"+secretID, nil, nil)
}

// --- Organizations ---

// ListOrgs returns organizations the current user is a member of.
func (c *Client) ListOrgs(ctx context.Context) ([]domain.Organization, error) {
	var orgs []domain.Organization
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs", nil, &orgs); err != nil {
		return nil, err
	}
	return orgs, nil
}

// GetOrg returns an organization by ID or name.
func (c *Client) GetOrg(ctx context.Context, idOrName string) (*domain.Organization, error) {
	var org domain.Organization
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+idOrName, nil, &org); err != nil {
		return nil, err
	}
	return &org, nil
}

// CreateOrg creates a new organization.
func (c *Client) CreateOrg(ctx context.Context, name, displayName string) (*domain.Organization, error) {
	body := map[string]string{
		"name":         name,
		"display_name": displayName,
	}
	var org domain.Organization
	if err := c.do(ctx, http.MethodPost, "/api/v1/orgs", body, &org); err != nil {
		return nil, err
	}
	return &org, nil
}

// ListOrgMembers returns members of an organization.
func (c *Client) ListOrgMembers(ctx context.Context, orgID string) ([]domain.OrgMember, error) {
	var members []domain.OrgMember
	if err := c.do(ctx, http.MethodGet, "/api/v1/orgs/"+orgID+"/members", nil, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// AddOrgMember adds a member to an organization.
func (c *Client) AddOrgMember(ctx context.Context, orgID, pubkey string, role domain.Role) (*domain.OrgMember, error) {
	body := map[string]string{
		"pubkey": pubkey,
		"role":   string(role),
	}
	var member domain.OrgMember
	if err := c.do(ctx, http.MethodPost, "/api/v1/orgs/"+orgID+"/members", body, &member); err != nil {
		return nil, err
	}
	return &member, nil
}

// RemoveOrgMember removes a member from an organization.
func (c *Client) RemoveOrgMember(ctx context.Context, orgID, pubkey string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/orgs/"+orgID+"/members/"+pubkey, nil, nil)
}
