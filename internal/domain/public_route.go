package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	PublicRouteSchemaVersion       = "1"
	InternalHTTPSPlanSchemaVersion = "1"
)

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// PublicRouteRequest is the semantic, provider-neutral route requested by an operator.
type PublicRouteRequest struct {
	Hostname       string `json:"hostname"`
	UpstreamScheme string `json:"upstream_scheme"`
	UpstreamPort   int    `json:"upstream_port"`
	HealthPath     string `json:"health_path"`
	TLS            string `json:"tls"`
}

// DesiredPublicRoutePlan is the exact non-secret edge change included in signed desired state.
type DesiredPublicRoutePlan struct {
	SchemaVersion      string                     `json:"schema_version"`
	ServiceID          uuid.UUID                  `json:"service_id"`
	EnvironmentID      uuid.UUID                  `json:"environment_id"`
	DeploymentUnitID   uuid.UUID                  `json:"deployment_unit_id"`
	Hostname           string                     `json:"hostname"`
	Zone               string                     `json:"zone"`
	BackendRef         string                     `json:"backend_ref"`
	Provider           string                     `json:"provider"`
	ProviderConfigHash string                     `json:"provider_config_hash"`
	DNS                DesiredPublicRouteDNS      `json:"dns"`
	Tunnel             DesiredPublicRouteTunnel   `json:"tunnel"`
	Proxy              DesiredPublicRouteProxy    `json:"proxy"`
	TLS                DesiredPublicRouteTLS      `json:"tls"`
	InternalHTTPS      *DesiredInternalHTTPSPlan  `json:"internal_https,omitempty"`
	Operations         []DesiredPublicRouteChange `json:"operations"`
	Rollback           []DesiredPublicRouteChange `json:"rollback"`
}

type DesiredPublicRouteDNS struct {
	Type             string `json:"type"`
	Name             string `json:"name"`
	Value            string `json:"value"`
	TTL              int    `json:"ttl"`
	Proxied          bool   `json:"proxied"`
	SourceCoordinate string `json:"source_coordinate"`
}

type DesiredPublicRouteTunnel struct {
	TunnelRef string `json:"tunnel_ref"`
	Hostname  string `json:"hostname"`
	OriginURL string `json:"origin_url"`
}

type DesiredPublicRouteProxy struct {
	HostMatch      string `json:"host_match"`
	UpstreamScheme string `json:"upstream_scheme"`
	UpstreamHost   string `json:"upstream_host"`
	UpstreamPort   int    `json:"upstream_port"`
	HealthPath     string `json:"health_path"`
}

type DesiredPublicRouteTLS struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider"`
}

// DesiredInternalHTTPSPlan describes the exact non-secret nginx vhost state
// approved together with the public Cloudflare route. Certificate contents are
// never embedded; only operator-configured absolute file paths are signed.
type DesiredInternalHTTPSPlan struct {
	SchemaVersion string                     `json:"schema_version"`
	Hostname      string                     `json:"hostname"`
	Listen        string                     `json:"listen"`
	UpstreamURL   string                     `json:"upstream_url"`
	CertFile      string                     `json:"cert_file"`
	KeyFile       string                     `json:"key_file"`
	ConfigHash    string                     `json:"config_hash"`
	Apply         []DesiredPublicRouteChange `json:"apply"`
	Rollback      []DesiredPublicRouteChange `json:"rollback"`
}

type DesiredPublicRouteChange struct {
	Order    int    `json:"order"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Summary  string `json:"summary"`
}

func NormalizePublicHostname(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:@* 	\r\n") || net.ParseIP(host) != nil {
		return "", fmt.Errorf("hostname must be a DNS name without scheme, port, wildcard, or path")
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return "", fmt.Errorf("hostname label %q is invalid", label)
		}
	}
	if !strings.Contains(host, ".") {
		return "", fmt.Errorf("hostname must be fully qualified")
	}
	return host, nil
}

func NormalizePublicRouteRequest(in PublicRouteRequest) (PublicRouteRequest, error) {
	host, err := NormalizePublicHostname(in.Hostname)
	if err != nil {
		return PublicRouteRequest{}, err
	}
	in.Hostname = host
	in.UpstreamScheme = strings.ToLower(strings.TrimSpace(in.UpstreamScheme))
	in.TLS = strings.ToLower(strings.TrimSpace(in.TLS))
	in.HealthPath = strings.TrimSpace(in.HealthPath)
	if in.UpstreamScheme != "http" {
		return PublicRouteRequest{}, fmt.Errorf("upstream_scheme must be http; TLS terminates at the managed edge")
	}
	if in.UpstreamPort < 1 || in.UpstreamPort > 65535 {
		return PublicRouteRequest{}, fmt.Errorf("upstream_port must be between 1 and 65535")
	}
	if in.TLS != "managed" {
		return PublicRouteRequest{}, fmt.Errorf("tls must be managed")
	}
	parsed, err := url.ParseRequestURI(in.HealthPath)
	if err != nil || !strings.HasPrefix(in.HealthPath, "/") || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(in.HealthPath, "\r\n\t ") {
		return PublicRouteRequest{}, fmt.Errorf("health_path must be an absolute path without query, fragment, or whitespace")
	}
	return in, nil
}

func HostnameWithinZone(hostname, zone string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	return hostname == zone || strings.HasSuffix(hostname, "."+zone)
}

func PublicRouteCoordinate(serviceID, environmentID, unitID uuid.UUID) string {
	return fmt.Sprintf("public-route:%s:%s:%s", serviceID, environmentID, unitID)
}

func PublicRouteProviderConfigHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(strings.TrimSpace(part)))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func ValidateDesiredPublicRoute(plan *DesiredPublicRoutePlan) error {
	if plan == nil {
		return nil
	}
	if plan.SchemaVersion != PublicRouteSchemaVersion || plan.ServiceID == uuid.Nil || plan.EnvironmentID == uuid.Nil || plan.DeploymentUnitID == uuid.Nil {
		return fmt.Errorf("public route identity and schema version are required")
	}
	req, err := NormalizePublicRouteRequest(PublicRouteRequest{Hostname: plan.Hostname, UpstreamScheme: plan.Proxy.UpstreamScheme, UpstreamPort: plan.Proxy.UpstreamPort, HealthPath: plan.Proxy.HealthPath, TLS: plan.TLS.Mode})
	if err != nil {
		return err
	}
	if req.Hostname != plan.Hostname || !HostnameWithinZone(plan.Hostname, plan.Zone) {
		return fmt.Errorf("public route hostname is not canonical or outside its zone")
	}
	if plan.BackendRef == "" || plan.Provider == "" || plan.ProviderConfigHash == "" || plan.Tunnel.TunnelRef == "" || plan.Tunnel.OriginURL == "" {
		return fmt.Errorf("public route backend, provider, tunnel, origin, and config hash are required")
	}
	if plan.DNS.Name != plan.Hostname || plan.DNS.Value == "" || plan.DNS.Type != "CNAME" || !plan.DNS.Proxied || plan.DNS.SourceCoordinate == "" || plan.TLS.Provider == "" {
		return fmt.Errorf("public route requires owned proxied CNAME DNS and a TLS provider")
	}
	if plan.Tunnel.Hostname != plan.Hostname || plan.Proxy.HostMatch != plan.Hostname {
		return fmt.Errorf("public route hostname must match DNS, tunnel, and proxy resources")
	}
	origin, err := url.Parse(plan.Tunnel.OriginURL)
	if err != nil || origin.Scheme != plan.Proxy.UpstreamScheme || origin.Hostname() != plan.Proxy.UpstreamHost || origin.Port() != fmt.Sprintf("%d", plan.Proxy.UpstreamPort) || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return fmt.Errorf("public route tunnel origin must match the proxy upstream")
	}
	if plan.InternalHTTPS != nil {
		internal := plan.InternalHTTPS
		if internal.SchemaVersion != InternalHTTPSPlanSchemaVersion || internal.Hostname != plan.Hostname {
			return fmt.Errorf("internal HTTPS hostname and schema version must match the public route")
		}
		if internal.Listen != "443 ssl" {
			return fmt.Errorf("internal HTTPS listen must be 443 ssl")
		}
		internalOrigin, err := url.Parse(internal.UpstreamURL)
		if err != nil || internalOrigin.Scheme != plan.Proxy.UpstreamScheme || internalOrigin.Hostname() != plan.Proxy.UpstreamHost || internalOrigin.Port() != fmt.Sprintf("%d", plan.Proxy.UpstreamPort) || internalOrigin.Path != "" || internalOrigin.RawQuery != "" || internalOrigin.Fragment != "" || internalOrigin.User != nil {
			return fmt.Errorf("internal HTTPS upstream must match the public route proxy upstream")
		}
		if !filepath.IsAbs(internal.CertFile) || !filepath.IsAbs(internal.KeyFile) || strings.TrimSpace(internal.ConfigHash) == "" {
			return fmt.Errorf("internal HTTPS certificate path, key path, and config hash are required")
		}
		if err := validateRouteChanges("internal HTTPS apply", internal.Apply); err != nil {
			return err
		}
		if err := validateRouteChanges("internal HTTPS rollback", internal.Rollback); err != nil {
			return err
		}
	}
	if len(plan.Operations) == 0 || len(plan.Rollback) == 0 {
		return fmt.Errorf("public route apply and rollback operations are required")
	}
	for i, operation := range plan.Operations {
		if operation.Order != i+1 || operation.Resource == "" || operation.Action == "" {
			return fmt.Errorf("public route apply operations must be complete and sequential")
		}
	}
	for i, operation := range plan.Rollback {
		if operation.Order != i+1 || operation.Resource == "" || operation.Action == "" {
			return fmt.Errorf("public route rollback operations must be complete and sequential")
		}
	}
	return nil
}

func validateRouteChanges(label string, changes []DesiredPublicRouteChange) error {
	if len(changes) == 0 {
		return fmt.Errorf("%s change descriptions are required", label)
	}
	for i, change := range changes {
		if change.Order != i+1 || strings.TrimSpace(change.Resource) == "" || strings.TrimSpace(change.Action) == "" || strings.TrimSpace(change.Summary) == "" {
			return fmt.Errorf("%s change descriptions must be complete and sequential", label)
		}
	}
	return nil
}
