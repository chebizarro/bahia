package controlplane

import (
	"sort"
	"strconv"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	maxSummaryEnvKeys     = 50
	maxSummaryVolumeSpecs = 50
	maxSummaryPortSpecs   = 50
	maxSummaryLabelKeys   = 50
)

// desiredStateSummary is a compact, non-secret structural summary of a
// DesiredServiceSpec. It omits the full spec and all environment variable
// values so the payload is small enough to traverse Nostr relays even
// for production-scale configurations. The DesiredHash remains the sole
// authoritative identifier — deploy() independently rebuilds desired state
// and compares against ExpectedDesiredStateHash.
//
// Every field is derived from the already-built desiredState. The helper is
// intentionally in the controlplane package (not domain) because the summary
// shape is a transport-level concern, not a domain invariant.
type desiredStateSummary struct {
	ImageRef       string                        `json:"image_ref"`
	Command        []string                      `json:"command,omitempty"`
	Entrypoint     []string                      `json:"entrypoint,omitempty"`
	WorkDir        string                        `json:"work_dir,omitempty"`
	Ports          []string                      `json:"ports"`
	Volumes        []string                      `json:"volumes"`
	LabelsCount    int                           `json:"labels_count"`
	LabelKeys      []string                      `json:"label_keys,omitempty"`
	EnvKeyCount    int                           `json:"env_key_count"`
	EnvKeys        []string                      `json:"env_keys"`
	SecretRefKeys  []string                      `json:"secret_ref_keys"`
	NetworkMode    string                        `json:"network_mode,omitempty"`
	DependsOn      []string                      `json:"depends_on,omitempty"`
	ResourceLimits *domain.RuntimeResourceLimits `json:"resource_limits,omitempty"`
	Healthcheck    *healthcheckSummary           `json:"healthcheck,omitempty"`
	PublicRoute    *publicRouteSummary           `json:"public_route,omitempty"`
	InternalHTTPS  *internalHTTPSSummary         `json:"internal_https,omitempty"`
	RestartPolicy  string                        `json:"restart_policy"`
	PullPolicy     string                        `json:"pull_policy"`

	// Truncation signals — always present when lists are capped.
	EnvKeysTruncated   bool `json:"env_keys_truncated,omitempty"`
	VolumesTruncated   bool `json:"volumes_truncated,omitempty"`
	PortsTruncated     bool `json:"ports_truncated,omitempty"`
	LabelKeysTruncated bool `json:"label_keys_truncated,omitempty"`
}

type healthcheckSummary struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path,omitempty"`
	Port    int    `json:"port"`
}

type publicRouteSummary struct {
	Hostname        string `json:"hostname"`
	DNSName         string `json:"dns_name"`
	DNSValue        string `json:"dns_value"`
	DNSType         string `json:"dns_type"`
	DNSTTL          int    `json:"dns_ttl"`
	Proxied         bool   `json:"proxied"`
	TLSMode         string `json:"tls_mode"`
	TunnelOriginURL string `json:"tunnel_origin_url"`
	ProxyUpstream   string `json:"proxy_upstream"`
	ProxyHealthPath string `json:"proxy_health_path"`
	OperationsCount int    `json:"operations_count"`
	RollbackCount   int    `json:"rollback_count"`
}

type internalHTTPSSummary struct {
	Enabled     bool   `json:"enabled"`
	Hostname    string `json:"hostname"`
	UpstreamURL string `json:"upstream_url"`
	Listen      string `json:"listen,omitempty"`
	CertPath    string `json:"cert_path,omitempty"`
	KeyPath     string `json:"key_path,omitempty"`
}

// buildDesiredStateSummary derives a compact structural summary from the
// built desired state. The summary intentionally excludes all environment
// variable VALUES because managed runtime environments frequently carry
// sensitive data (API keys, database URLs, credentials). The operator verifies
// structural preservation through the authoritative desired_state_hash and uses
// the summary only as a human-readable diagnostic.
//
// Label values are also excluded for the same reason — they can carry
// routing tags, instance identifiers, or other data that should not transit
// every relay. Only sorted key names are included.
func buildDesiredStateSummary(state *domain.DesiredServiceSpec) *desiredStateSummary {
	if state == nil {
		return nil
	}

	envKeys := make([]string, 0, len(state.Env))
	for k := range state.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	labelKeys := make([]string, 0, len(state.Labels))
	for k := range state.Labels {
		labelKeys = append(labelKeys, k)
	}
	sort.Strings(labelKeys)

	secretKeys := make([]string, len(state.SecretRefs))
	for i, ref := range state.SecretRefs {
		secretKeys[i] = ref.Name
	}
	sort.Strings(secretKeys)

	summary := &desiredStateSummary{
		ImageRef:       state.ImageRef,
		Command:        copyStringSlice(state.Command),
		Entrypoint:     copyStringSlice(state.Entrypoint),
		WorkDir:        state.WorkDir,
		Ports:          copyStringSlice(state.Ports),
		Volumes:        copyStringSlice(state.Volumes),
		LabelsCount:    len(state.Labels),
		LabelKeys:      labelKeys,
		EnvKeyCount:    len(state.Env),
		EnvKeys:        envKeys,
		SecretRefKeys:  secretKeys,
		NetworkMode:    state.NetworkMode,
		DependsOn:      copyStringSlice(state.DependsOn),
		ResourceLimits: state.ResourceLimits,
		RestartPolicy:  state.RestartPolicy,
		PullPolicy:     state.PullPolicy,
	}

	if len(summary.EnvKeys) > maxSummaryEnvKeys {
		summary.EnvKeys = summary.EnvKeys[:maxSummaryEnvKeys]
		summary.EnvKeysTruncated = true
	}
	if len(summary.Volumes) > maxSummaryVolumeSpecs {
		summary.Volumes = summary.Volumes[:maxSummaryVolumeSpecs]
		summary.VolumesTruncated = true
	}
	if len(summary.Ports) > maxSummaryPortSpecs {
		summary.Ports = summary.Ports[:maxSummaryPortSpecs]
		summary.PortsTruncated = true
	}
	if len(summary.LabelKeys) > maxSummaryLabelKeys {
		summary.LabelKeys = summary.LabelKeys[:maxSummaryLabelKeys]
		summary.LabelKeysTruncated = true
	}

	if state.Healthcheck != nil {
		summary.Healthcheck = &healthcheckSummary{
			Enabled: true,
			Path:    state.Healthcheck.Path,
			Port:    state.Healthcheck.Port,
		}
	}

	if state.PublicRoute != nil {
		r := state.PublicRoute
		summary.PublicRoute = &publicRouteSummary{
			Hostname:        r.Hostname,
			DNSName:         r.DNS.Name,
			DNSValue:        r.DNS.Value,
			DNSType:         r.DNS.Type,
			DNSTTL:          r.DNS.TTL,
			Proxied:         r.DNS.Proxied,
			TLSMode:         r.TLS.Mode,
			TunnelOriginURL: r.Tunnel.OriginURL,
			ProxyUpstream:   r.Proxy.UpstreamScheme + "://" + r.Proxy.UpstreamHost + ":" + portStr(r.Proxy.UpstreamPort),
			ProxyHealthPath: r.Proxy.HealthPath,
			OperationsCount: len(r.Operations),
			RollbackCount:   len(r.Rollback),
		}
		if r.InternalHTTPS != nil {
			summary.InternalHTTPS = &internalHTTPSSummary{
				Enabled:     true,
				Hostname:    r.InternalHTTPS.Hostname,
				UpstreamURL: r.InternalHTTPS.UpstreamURL,
				Listen:      r.InternalHTTPS.Listen,
				CertPath:    r.InternalHTTPS.CertFile,
				KeyPath:     r.InternalHTTPS.KeyFile,
			}
		}
	}

	return summary
}

func portStr(p int) string {
	if p == 0 {
		return ""
	}
	return strconv.Itoa(p)
}

func copyStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
