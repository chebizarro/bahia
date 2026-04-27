package registry

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// RegistryType identifies a known container registry type.
type RegistryType string

const (
	RegistryGHCR      RegistryType = "ghcr"
	RegistryDockerHub RegistryType = "dockerhub"
	RegistryHarbor    RegistryType = "harbor"
	RegistryOCI       RegistryType = "oci" // generic OCI Distribution v2
)

// RegistryConfig holds configuration for creating a registry adapter.
type RegistryConfig struct {
	// Type overrides auto-detection. If empty, DetectRegistry is used.
	Type RegistryType `koanf:"type"`

	// URL is the registry base URL (required for generic OCI and Harbor).
	URL string `koanf:"url"`

	// Credentials (optional for public repos).
	Username string `koanf:"username"`
	Password string `koanf:"password"` // password or PAT
}

// DetectRegistry determines the registry type from an image URL or repository path.
//
// Examples:
//
//	"ghcr.io/myorg/myapp" → GHCR
//	"docker.io/library/nginx" → DockerHub
//	"nginx" → DockerHub (bare image name)
//	"harbor.example.com/project/repo" → Harbor
//	"myregistry.com/repo" → generic OCI
func DetectRegistry(imageURL string) RegistryType {
	// Strip scheme if present.
	clean := imageURL
	if idx := strings.Index(clean, "://"); idx >= 0 {
		clean = clean[idx+3:]
	}

	// Extract the hostname (everything before the first /).
	host := clean
	if idx := strings.Index(clean, "/"); idx >= 0 {
		host = clean[:idx]
	}
	host = strings.ToLower(host)

	// Check if host has a port before stripping it (presence of port implies a real host).
	hasPort := strings.Contains(host, ":")

	// Strip port for hostname matching.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}

	switch {
	case host == "ghcr.io":
		return RegistryGHCR
	case host == "docker.io" || host == "registry-1.docker.io" || host == "index.docker.io":
		return RegistryDockerHub
	case host == "":
		// Empty host (no input at all) defaults to Docker Hub.
		return RegistryDockerHub
	case !hasPort && !strings.Contains(host, "."):
		// No dot and no port in hostname means it's not a real host — it's a
		// Docker Hub org/repo path like "myorg/myapp" or bare name like "nginx".
		return RegistryDockerHub
	case strings.Contains(host, "harbor"):
		return RegistryHarbor
	default:
		return RegistryOCI
	}
}

// NewInspector creates an ImageInspector for the given registry configuration.
// If cfg.Type is empty, it falls back to generic OCI.
func NewInspector(cfg RegistryConfig, logger *zap.Logger) (ImageInspector, error) {
	typ := cfg.Type
	if typ == "" {
		if cfg.URL != "" {
			typ = DetectRegistry(cfg.URL)
		} else {
			typ = RegistryOCI
		}
	}

	switch typ {
	case RegistryGHCR:
		return NewGHCRClient(cfg.Password, logger), nil

	case RegistryDockerHub:
		return NewDockerHubClient(cfg.Username, cfg.Password, logger), nil

	case RegistryHarbor:
		// Harbor uses basic auth via the OCI client.
		if cfg.URL == "" {
			return nil, fmt.Errorf("harbor registry requires a URL")
		}
		var opts []OCIOption
		if cfg.Username != "" {
			opts = append(opts, WithBasicAuth(cfg.Username, cfg.Password))
		}
		return NewOCIClient(cfg.URL, logger, opts...), nil

	case RegistryOCI:
		if cfg.URL == "" {
			return nil, fmt.Errorf("generic OCI registry requires a URL")
		}
		var opts []OCIOption
		if cfg.Username != "" {
			opts = append(opts, WithBasicAuth(cfg.Username, cfg.Password))
		}
		return NewOCIClient(cfg.URL, logger, opts...), nil

	default:
		return nil, fmt.Errorf("unknown registry type: %s", typ)
	}
}

// NewVerifier is a convenience that creates an ImageInspector and wraps it as
// a service.ImageVerifier for use with RegistryService.
func NewVerifier(cfg RegistryConfig, logger *zap.Logger) (service.ImageVerifier, error) {
	inspector, err := NewInspector(cfg, logger)
	if err != nil {
		return nil, err
	}
	return &VerifierAdapter{Inspector: inspector}, nil
}

// InspectorForImage auto-detects the registry from the image URL, creates
// an appropriate inspector on the fly, and returns it. This is useful when
// you need to inspect images across multiple registries without pre-configuring.
func InspectorForImage(imageURL string, logger *zap.Logger) (ImageInspector, error) {
	typ := DetectRegistry(imageURL)

	switch typ {
	case RegistryGHCR:
		// Anonymous access for public repos.
		return NewGHCRClient("", logger), nil
	case RegistryDockerHub:
		// Anonymous access; handles bare names like "nginx" automatically.
		return NewDockerHubClient("", "", logger), nil
	default:
		// Extract host from the image reference.
		host := extractHost(imageURL)
		if host == "" {
			// Shouldn't happen for non-DockerHub types, but be defensive.
			return nil, fmt.Errorf("cannot determine registry URL from %q", imageURL)
		}
		return NewOCIClient("https://"+host, logger), nil
	}
}

// extractHost pulls the hostname from an image reference like "registry.example.com/repo:tag".
func extractHost(image string) string {
	// Strip scheme.
	s := image
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// Get hostname (may include port).
	if idx := strings.Index(s, "/"); idx > 0 {
		return s[:idx]
	}
	return ""
}
