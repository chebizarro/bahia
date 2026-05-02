package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// GatewayRouteSpec is the Bahia-owned desired route shape sent to an inference gateway.
type GatewayRouteSpec struct {
	RouteName      string            `json:"route_name"`
	PublicModel    string            `json:"public_model"`
	Path           string            `json:"path,omitempty"`
	TargetURL      string            `json:"target_url"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}

// CanonicalPath returns the route path managed by Bahia.
func (s GatewayRouteSpec) CanonicalPath() string {
	if path := strings.TrimSpace(s.Path); path != "" {
		if strings.HasPrefix(path, "/") {
			return path
		}
		return "/" + path
	}
	name := strings.Trim(strings.TrimSpace(s.RouteName), "/")
	if name == "" {
		return ""
	}
	return "/v1/models/" + name
}

// ManagedConfigHash returns a stable hash over Bahia-managed route fields.
func (s GatewayRouteSpec) ManagedConfigHash() string {
	headers := make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		headers[strings.ToLower(strings.TrimSpace(k))] = v
	}
	headerKeys := make([]string, 0, len(headers))
	for k := range headers {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)
	orderedHeaders := make([][2]string, 0, len(headerKeys))
	for _, k := range headerKeys {
		orderedHeaders = append(orderedHeaders, [2]string{k, headers[k]})
	}
	payload := struct {
		RouteName      string         `json:"route_name"`
		PublicModel    string         `json:"public_model"`
		Path           string         `json:"path"`
		TargetURL      string         `json:"target_url"`
		TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
		Headers        [][2]string    `json:"headers,omitempty"`
		Metadata       map[string]any `json:"metadata,omitempty"`
	}{
		RouteName:      strings.TrimSpace(s.RouteName),
		PublicModel:    strings.TrimSpace(s.PublicModel),
		Path:           s.CanonicalPath(),
		TargetURL:      strings.TrimRight(strings.TrimSpace(s.TargetURL), "/"),
		TimeoutSeconds: s.TimeoutSeconds,
		Headers:        orderedHeaders,
		Metadata:       s.Metadata,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// GatewayRouteObservation is a normalized view of route state returned by a gateway.
type GatewayRouteObservation struct {
	RouteName         string                    `json:"route_name"`
	PublicModel       string                    `json:"public_model,omitempty"`
	Path              string                    `json:"path,omitempty"`
	TargetURL         string                    `json:"target_url,omitempty"`
	Status            domain.GatewayRouteStatus `json:"status"`
	GatewayConfigHash string                    `json:"gateway_config_hash,omitempty"`
	Metadata          map[string]any            `json:"metadata,omitempty"`
	ObservedRaw       json.RawMessage           `json:"observed_raw,omitempty"`
}

// GatewayRouteManager reconciles Bahia-owned LLM route state into an inference gateway.
type GatewayRouteManager interface {
	UpsertRoute(ctx context.Context, gatewayRef string, spec GatewayRouteSpec) (*GatewayRouteObservation, error)
	GetRoute(ctx context.Context, gatewayRef, routeName string) (*GatewayRouteObservation, error)
	DeleteRoute(ctx context.Context, gatewayRef, routeName string) error
}
