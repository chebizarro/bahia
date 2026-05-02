package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// GatewayHTTPEndpointConfig describes one gateway admin endpoint.
type GatewayHTTPEndpointConfig struct {
	Type      string        `json:"type" koanf:"type"`
	BaseURL   string        `json:"base_url" koanf:"base_url"`
	AuthToken string        `json:"auth_token" koanf:"auth_token"`
	Timeout   time.Duration `json:"timeout" koanf:"timeout"`
}

// GatewayHTTPConfig maps stable gateway refs to admin endpoints.
type GatewayHTTPConfig struct {
	Endpoints map[string]GatewayHTTPEndpointConfig `json:"endpoints" koanf:"endpoints"`
}

// HTTPGatewayRouteManager implements GatewayRouteManager against a small Bahia
// admin API:
//   - PUT    /api/v1/routes/{routeName}
//   - GET    /api/v1/routes/{routeName}
//   - DELETE /api/v1/routes/{routeName}
//
// The request and response body use GatewayRouteSpec-compatible JSON. This
// keeps Bahia's adapter interface stable while the gateway implementation can
// evolve behind this admin shape.
type HTTPGatewayRouteManager struct {
	endpoints map[string]GatewayHTTPEndpointConfig
	client    *http.Client
}

// NewHTTPGatewayRouteManager creates an HTTP-backed route manager.
func NewHTTPGatewayRouteManager(cfg GatewayHTTPConfig, client *http.Client) *HTTPGatewayRouteManager {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoints := make(map[string]GatewayHTTPEndpointConfig, len(cfg.Endpoints))
	for ref, ep := range cfg.Endpoints {
		if ep.Timeout > 0 {
			copied := *client
			copied.Timeout = ep.Timeout
			_ = copied
		}
		endpoints[strings.TrimSpace(ref)] = ep
	}
	return &HTTPGatewayRouteManager{endpoints: endpoints, client: client}
}

func (m *HTTPGatewayRouteManager) UpsertRoute(ctx context.Context, gatewayRef string, spec GatewayRouteSpec) (*GatewayRouteObservation, error) {
	spec.RouteName = strings.TrimSpace(spec.RouteName)
	if spec.RouteName == "" {
		return nil, fmt.Errorf("gateway route name is required")
	}
	if spec.TargetURL == "" {
		return nil, fmt.Errorf("gateway route target_url is required")
	}
	spec.Path = spec.CanonicalPath()

	body, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal gateway route spec: %w", err)
	}
	respBody, err := m.do(ctx, http.MethodPut, gatewayRef, spec.RouteName, body)
	if err != nil {
		return nil, err
	}
	obs, err := decodeGatewayObservation(respBody, spec)
	if err != nil {
		return nil, err
	}
	if obs.GatewayConfigHash == "" {
		obs.GatewayConfigHash = spec.ManagedConfigHash()
	}
	if obs.Status == "" || obs.Status == domain.GatewayRouteStatusUnknown {
		obs.Status = domain.GatewayRouteStatusSynced
	}
	return obs, nil
}

func (m *HTTPGatewayRouteManager) GetRoute(ctx context.Context, gatewayRef, routeName string) (*GatewayRouteObservation, error) {
	routeName = strings.TrimSpace(routeName)
	if routeName == "" {
		return nil, fmt.Errorf("gateway route name is required")
	}
	respBody, err := m.do(ctx, http.MethodGet, gatewayRef, routeName, nil)
	if err != nil {
		if errors.Is(err, ErrGatewayRouteNotFound) {
			return &GatewayRouteObservation{RouteName: routeName, Status: domain.GatewayRouteStatusMissing}, nil
		}
		return nil, err
	}
	obs, err := decodeGatewayObservation(respBody, GatewayRouteSpec{RouteName: routeName})
	if obs != nil && (obs.Status == "" || obs.Status == domain.GatewayRouteStatusUnknown) {
		obs.Status = domain.GatewayRouteStatusSynced
	}
	return obs, err
}

func (m *HTTPGatewayRouteManager) DeleteRoute(ctx context.Context, gatewayRef, routeName string) error {
	routeName = strings.TrimSpace(routeName)
	if routeName == "" {
		return fmt.Errorf("gateway route name is required")
	}
	_, err := m.do(ctx, http.MethodDelete, gatewayRef, routeName, nil)
	if errors.Is(err, ErrGatewayRouteNotFound) {
		return nil
	}
	return err
}

// ErrGatewayRouteNotFound marks a missing route.
var ErrGatewayRouteNotFound = errors.New("gateway route not found")

func (m *HTTPGatewayRouteManager) do(ctx context.Context, method, gatewayRef, routeName string, body []byte) ([]byte, error) {
	ep, err := m.endpoint(gatewayRef)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(strings.TrimRight(ep.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse gateway %q base_url: %w", gatewayRef, err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/routes/" + url.PathEscape(routeName)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("create gateway request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if ep.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+ep.AuthToken)
	}

	client := m.client
	if ep.Timeout > 0 {
		copied := *m.client
		copied.Timeout = ep.Timeout
		client = &copied
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway %s %s: %w", method, routeName, err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read gateway response: %w", readErr)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrGatewayRouteNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gateway %s %s returned %d: %s", method, routeName, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func (m *HTTPGatewayRouteManager) endpoint(gatewayRef string) (GatewayHTTPEndpointConfig, error) {
	ref := strings.TrimSpace(gatewayRef)
	if ref == "" && len(m.endpoints) == 1 {
		for _, ep := range m.endpoints {
			return ep, validateGatewayEndpoint(ref, ep)
		}
	}
	ep, ok := m.endpoints[ref]
	if !ok {
		return GatewayHTTPEndpointConfig{}, fmt.Errorf("gateway ref %q is not configured", gatewayRef)
	}
	return ep, validateGatewayEndpoint(ref, ep)
}

func validateGatewayEndpoint(ref string, ep GatewayHTTPEndpointConfig) error {
	if typ := strings.TrimSpace(ep.Type); typ != "" && typ != "http" {
		return fmt.Errorf("gateway ref %q has unsupported type %q", ref, ep.Type)
	}
	if strings.TrimSpace(ep.BaseURL) == "" {
		return fmt.Errorf("gateway ref %q has no base_url", ref)
	}
	return nil
}

func decodeGatewayObservation(body []byte, fallback GatewayRouteSpec) (*GatewayRouteObservation, error) {
	obs := &GatewayRouteObservation{
		RouteName:   fallback.RouteName,
		PublicModel: fallback.PublicModel,
		Path:        fallback.CanonicalPath(),
		TargetURL:   fallback.TargetURL,
		Status:      domain.GatewayRouteStatusUnknown,
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		obs.Status = domain.GatewayRouteStatusSynced
		obs.GatewayConfigHash = fallback.ManagedConfigHash()
		return obs, nil
	}

	var raw json.RawMessage = append([]byte(nil), body...)
	var envelope struct {
		RouteName         string                    `json:"route_name"`
		Name              string                    `json:"name"`
		PublicModel       string                    `json:"public_model"`
		Path              string                    `json:"path"`
		TargetURL         string                    `json:"target_url"`
		Status            domain.GatewayRouteStatus `json:"status"`
		GatewayConfigHash string                    `json:"gateway_config_hash"`
		ConfigHash        string                    `json:"config_hash"`
		Metadata          map[string]any            `json:"metadata"`
		Route             *GatewayRouteObservation  `json:"route"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode gateway route observation: %w", err)
	}
	if envelope.Route != nil {
		*obs = *envelope.Route
	} else {
		obs.RouteName = firstNonEmpty(envelope.RouteName, envelope.Name, obs.RouteName)
		obs.PublicModel = firstNonEmpty(envelope.PublicModel, obs.PublicModel)
		obs.Path = firstNonEmpty(envelope.Path, obs.Path)
		obs.TargetURL = firstNonEmpty(envelope.TargetURL, obs.TargetURL)
		obs.Status = envelope.Status
		obs.GatewayConfigHash = firstNonEmpty(envelope.GatewayConfigHash, envelope.ConfigHash)
		obs.Metadata = envelope.Metadata
	}
	obs.ObservedRaw = raw
	if obs.Path == "" {
		obs.Path = fallback.CanonicalPath()
	}
	if obs.GatewayConfigHash == "" && fallback.TargetURL != "" {
		obs.GatewayConfigHash = fallback.ManagedConfigHash()
	}
	if obs.Status == "" {
		obs.Status = domain.GatewayRouteStatusUnknown
	}
	return obs, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
