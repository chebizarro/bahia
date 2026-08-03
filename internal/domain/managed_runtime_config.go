package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const ManagedRuntimeConfigSchemaVersion = "1"

var (
	managedServiceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	environmentNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ManagedSecretReference maps an environment variable to an opaque Bahia
// secret identifier. Secret values are deliberately absent from this contract.
type ManagedSecretReference struct {
	EnvVar   string    `json:"env_var"`
	SecretID uuid.UUID `json:"secret_id"`
}

// ManagedHTTPHealthcheck is a semantic HTTP healthcheck. The renderer owns the
// fixed probe command, so browser input never becomes shell syntax.
type ManagedHTTPHealthcheck struct {
	Protocol    string `json:"protocol"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Port        int    `json:"port"`
	Interval    string `json:"interval,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
	Retries     int    `json:"retries,omitempty"`
	StartPeriod string `json:"start_period,omitempty"`
}

// RuntimeResourceLimits is the portable resource ceiling for a managed service.
// CPU is expressed in millicores and memory in bytes to avoid ambiguous units.
type RuntimeResourceLimits struct {
	CPUMillis   int64 `json:"cpu_millis,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
}

// ManagedRuntimeConfig is the persisted, browser-authored runtime definition
// for a normal Bahia-managed Compose service.
type ManagedRuntimeConfig struct {
	SchemaVersion  string                   `json:"schema_version"`
	ServiceName    string                   `json:"service_name"`
	Ports          []string                 `json:"ports,omitempty"`
	Command        []string                 `json:"command,omitempty"`
	Environment    map[string]string        `json:"environment,omitempty"`
	SecretRefs     []ManagedSecretReference `json:"secret_refs,omitempty"`
	Healthcheck    *ManagedHTTPHealthcheck  `json:"healthcheck,omitempty"`
	RestartPolicy  string                   `json:"restart_policy,omitempty"`
	Volumes        []string                 `json:"volumes,omitempty"`
	ResourceLimits *RuntimeResourceLimits   `json:"resource_limits,omitempty"`
	PullPolicy     string                   `json:"pull_policy,omitempty"`
}

// NormalizeManagedRuntimeConfig returns a canonical deep copy suitable for
// persistence, hashing, and desired-state construction.
func NormalizeManagedRuntimeConfig(input *ManagedRuntimeConfig) *ManagedRuntimeConfig {
	if input == nil {
		return nil
	}
	out := *input
	out.SchemaVersion = strings.TrimSpace(out.SchemaVersion)
	if out.SchemaVersion == "" {
		out.SchemaVersion = ManagedRuntimeConfigSchemaVersion
	}
	out.ServiceName = strings.TrimSpace(out.ServiceName)
	out.RestartPolicy = strings.ToLower(strings.TrimSpace(out.RestartPolicy))
	out.PullPolicy = strings.ToLower(strings.TrimSpace(out.PullPolicy))
	if out.PullPolicy == "" {
		out.PullPolicy = "always"
	}
	out.Ports = canonicalStringSlice(out.Ports)
	out.Volumes = canonicalStringSlice(out.Volumes)
	out.Command = append([]string(nil), out.Command...)
	out.Environment = copyStringMapDomain(out.Environment)
	out.SecretRefs = append([]ManagedSecretReference(nil), out.SecretRefs...)
	for i := range out.SecretRefs {
		out.SecretRefs[i].EnvVar = strings.TrimSpace(out.SecretRefs[i].EnvVar)
	}
	sort.Slice(out.SecretRefs, func(i, j int) bool {
		if out.SecretRefs[i].EnvVar != out.SecretRefs[j].EnvVar {
			return out.SecretRefs[i].EnvVar < out.SecretRefs[j].EnvVar
		}
		return out.SecretRefs[i].SecretID.String() < out.SecretRefs[j].SecretID.String()
	})
	if out.Healthcheck != nil {
		hc := *out.Healthcheck
		hc.Protocol = strings.ToLower(strings.TrimSpace(hc.Protocol))
		hc.Method = strings.ToUpper(strings.TrimSpace(hc.Method))
		hc.Path = strings.TrimSpace(hc.Path)
		hc.Interval = strings.TrimSpace(hc.Interval)
		hc.Timeout = strings.TrimSpace(hc.Timeout)
		hc.StartPeriod = strings.TrimSpace(hc.StartPeriod)
		out.Healthcheck = &hc
	}
	if out.ResourceLimits != nil {
		limits := *out.ResourceLimits
		if limits.CPUMillis == 0 && limits.MemoryBytes == 0 {
			out.ResourceLimits = nil
		} else {
			out.ResourceLimits = &limits
		}
	}
	return &out
}

// ValidateManagedRuntimeConfig rejects unsafe or ambiguous managed Compose
// configuration. It validates references without resolving secret values.
func ValidateManagedRuntimeConfig(config *ManagedRuntimeConfig) error {
	if config == nil {
		return fmt.Errorf("managed runtime configuration is required")
	}
	if config.SchemaVersion != ManagedRuntimeConfigSchemaVersion {
		return fmt.Errorf("managed runtime schema_version must be %q", ManagedRuntimeConfigSchemaVersion)
	}
	if !managedServiceNamePattern.MatchString(config.ServiceName) {
		return fmt.Errorf("service_name must be a Compose-safe name")
	}
	if len(config.Command) > 64 {
		return fmt.Errorf("command must contain at most 64 arguments")
	}
	for _, argument := range config.Command {
		if err := validateRuntimeText(argument, 4096, "command argument"); err != nil {
			return err
		}
	}
	if len(config.Environment) > 128 {
		return fmt.Errorf("environment must contain at most 128 entries")
	}
	for name, value := range config.Environment {
		if !environmentNamePattern.MatchString(name) {
			return fmt.Errorf("environment variable name %q is invalid", name)
		}
		if err := validateRuntimeText(value, 16384, "environment value"); err != nil {
			return fmt.Errorf("environment variable %q: %w", name, err)
		}
	}
	if len(config.SecretRefs) > 128 {
		return fmt.Errorf("secret_refs must contain at most 128 entries")
	}
	seenSecretEnv := make(map[string]struct{}, len(config.SecretRefs))
	seenSecretIDs := make(map[uuid.UUID]struct{}, len(config.SecretRefs))
	for _, ref := range config.SecretRefs {
		if !environmentNamePattern.MatchString(ref.EnvVar) {
			return fmt.Errorf("secret environment variable name %q is invalid", ref.EnvVar)
		}
		if ref.SecretID == uuid.Nil {
			return fmt.Errorf("secret reference for %q must include secret_id", ref.EnvVar)
		}
		if _, exists := config.Environment[ref.EnvVar]; exists {
			return fmt.Errorf("environment variable %q cannot be both literal and secret-backed", ref.EnvVar)
		}
		if _, exists := seenSecretEnv[ref.EnvVar]; exists {
			return fmt.Errorf("secret environment variable %q is duplicated", ref.EnvVar)
		}
		if _, exists := seenSecretIDs[ref.SecretID]; exists {
			return fmt.Errorf("a secret may be referenced by only one environment variable")
		}
		seenSecretEnv[ref.EnvVar] = struct{}{}
		seenSecretIDs[ref.SecretID] = struct{}{}
	}
	if len(config.Ports) > 64 || len(config.Volumes) > 64 {
		return fmt.Errorf("ports and volumes must each contain at most 64 entries")
	}
	for _, port := range config.Ports {
		if err := validateRuntimeText(port, 256, "port mapping"); err != nil {
			return err
		}
	}
	for _, volume := range config.Volumes {
		if err := validateRuntimeText(volume, 1024, "volume mapping"); err != nil {
			return err
		}
	}
	switch config.RestartPolicy {
	case "", "no", "always", "on-failure", "unless-stopped":
	default:
		return fmt.Errorf("restart_policy is invalid")
	}
	switch config.PullPolicy {
	case "always", "if-not-present", "never":
	default:
		return fmt.Errorf("pull_policy is invalid")
	}
	if config.Healthcheck != nil {
		if err := validateManagedHTTPHealthcheck(config.Healthcheck); err != nil {
			return err
		}
	}
	if config.ResourceLimits != nil {
		if config.ResourceLimits.CPUMillis < 0 || config.ResourceLimits.MemoryBytes < 0 {
			return fmt.Errorf("resource limits must not be negative")
		}
		if config.ResourceLimits.CPUMillis == 0 && config.ResourceLimits.MemoryBytes == 0 {
			return fmt.Errorf("resource_limits must set cpu_millis or memory_bytes")
		}
	}
	return nil
}

func validateManagedHTTPHealthcheck(healthcheck *ManagedHTTPHealthcheck) error {
	if healthcheck.Protocol != "http" || healthcheck.Method != "GET" {
		return fmt.Errorf("healthcheck supports only HTTP GET")
	}
	if healthcheck.Port < 1 || healthcheck.Port > 65535 {
		return fmt.Errorf("healthcheck port must be between 1 and 65535")
	}
	if !strings.HasPrefix(healthcheck.Path, "/") || strings.ContainsAny(healthcheck.Path, "?#\\\x00\r\n\t ") {
		return fmt.Errorf("healthcheck path must be an absolute path without query, fragment, whitespace, or control characters")
	}
	if len(healthcheck.Path) > 2048 {
		return fmt.Errorf("healthcheck path is too long")
	}
	if healthcheck.Retries < 0 || healthcheck.Retries > 100 {
		return fmt.Errorf("healthcheck retries must be between 0 and 100")
	}
	for label, value := range map[string]string{
		"interval":     healthcheck.Interval,
		"timeout":      healthcheck.Timeout,
		"start_period": healthcheck.StartPeriod,
	} {
		if value != "" {
			if err := validateRuntimeText(value, 32, "healthcheck "+label); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRuntimeText(value string, maxBytes int, label string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s contains invalid characters", label)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return nil
}

func canonicalStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func copyStringMapDomain(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[strings.TrimSpace(key)] = value
	}
	return out
}
