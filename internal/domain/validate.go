// Package domain validation functions for Bahia types.
package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Validation errors.
var (
	ErrEmptyField    = fmt.Errorf("field must not be empty")
	ErrInvalidFormat = fmt.Errorf("invalid format")
	ErrInvalidValue  = fmt.Errorf("invalid value")
	ErrNilUUID       = fmt.Errorf("UUID must not be nil")
)

// digestRegex matches OCI content-addressable digests: algorithm:hex
var digestRegex = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// gitSHARegex matches full or short git SHA hashes (7-40 hex chars).
var gitSHARegex = regexp.MustCompile(`^[a-f0-9]{7,40}$`)

// llmRouteNameRegex matches DNS/route-safe LLM route names.
var llmRouteNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidateBuildStatus checks that a BuildStatus is a known value.
func ValidateBuildStatus(s BuildStatus) error {
	switch s {
	case BuildStatusQueued, BuildStatusRunning, BuildStatusSucceeded, BuildStatusFailed, BuildStatusCancelled:
		return nil
	case "":
		return nil // empty is allowed — defaults are applied by service layer
	default:
		return fmt.Errorf("%w: build status %q is not valid (allowed: queued, running, succeeded, failed, cancelled)", ErrInvalidValue, s)
	}
}

// ValidateScanStatus checks that a ScanStatus is a known value.
func ValidateScanStatus(s ScanStatus) error {
	switch s {
	case ScanStatusUnknown, ScanStatusPending, ScanStatusClean, ScanStatusWarning, ScanStatusFailed:
		return nil
	case "":
		return nil // empty is allowed — defaults to "unknown"
	default:
		return fmt.Errorf("%w: scan status %q is not valid (allowed: unknown, pending, clean, warning, failed)", ErrInvalidValue, s)
	}
}

// ValidateSourceKind checks that a SourceKind is a known value.
func ValidateSourceKind(s SourceKind) error {
	switch s {
	case SourceKindManual, SourceKindAutoPromote, SourceKindRollback, SourceKindScheduled, SourceKindEventTriggered:
		return nil
	case "":
		return nil // empty defaults to "manual"
	default:
		return fmt.Errorf("%w: source kind %q is not valid (allowed: manual, auto_promote, rollback, scheduled, event_triggered)", ErrInvalidValue, s)
	}
}

// ValidateDeploymentRunStatus checks that a DeploymentRunStatus is a known value.
func ValidateDeploymentRunStatus(s DeploymentRunStatus) error {
	switch s {
	case RunStatusQueued, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusTimeout:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: deployment run status %q is not valid (allowed: queued, running, succeeded, failed, cancelled, timeout)", ErrInvalidValue, s)
	}
}

// ValidateHealthStatus checks that a HealthStatus is a known value.
func ValidateHealthStatus(s HealthStatus) error {
	switch s {
	case HealthStatusUnknown, HealthStatusStarting, HealthStatusHealthy, HealthStatusUnhealthy, HealthStatusStopped:
		return nil
	default:
		return fmt.Errorf("%w: health status %q is not valid (allowed: unknown, starting, healthy, unhealthy, stopped)", ErrInvalidValue, s)
	}
}

// ValidateRuntimeType checks that a RuntimeType is a known value.
func ValidateRuntimeType(s RuntimeType) error {
	switch s {
	case RuntimeTypeDocker, RuntimeTypeCompose, RuntimeTypeK8s, RuntimeTypePodman:
		return nil
	case "":
		return nil // defaults to "docker"
	default:
		return fmt.Errorf("%w: runtime type %q is not valid (allowed: docker, compose, kubernetes)", ErrInvalidValue, s)
	}
}

// ValidateLLMRouteName checks that a route name is stable and route-safe.
func ValidateLLMRouteName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: LLM route name must not be empty", ErrEmptyField)
	}
	if !llmRouteNameRegex.MatchString(name) || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%w: LLM route name %q must be lower-case, route-safe, and 1-63 chars", ErrInvalidFormat, name)
	}
	return nil
}

// ValidateLLMBackendKind checks that a backend kind is supported.
func ValidateLLMBackendKind(kind LLMBackendKind) error {
	switch kind {
	case LLMBackendKindVLLM, LLMBackendKindOllama, LLMBackendKindLlamaCPP, LLMBackendKindExternalAPI:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: LLM backend kind %q is not valid (allowed: vllm, ollama, llama_cpp, external_api)", ErrInvalidValue, kind)
	}
}

// ValidateGatewayRouteStatus checks that a gateway route status is supported.
func ValidateGatewayRouteStatus(status GatewayRouteStatus) error {
	switch status {
	case GatewayRouteStatusUnknown, GatewayRouteStatusPending, GatewayRouteStatusSynced, GatewayRouteStatusMissing, GatewayRouteStatusError:
		return nil
	case "":
		return nil
	default:
		return fmt.Errorf("%w: gateway route status %q is not valid (allowed: unknown, pending, synced, missing, error)", ErrInvalidValue, status)
	}
}

// ValidateModelSource checks that a model source is supported.
func ValidateModelSource(source string) error {
	switch strings.TrimSpace(source) {
	case ModelSourceHuggingFace, ModelSourceOCI, ModelSourceExternal:
		return nil
	case "":
		return fmt.Errorf("%w: model source must not be empty", ErrEmptyField)
	default:
		return fmt.Errorf("%w: model source %q is not valid (allowed: huggingface, oci, external)", ErrInvalidValue, source)
	}
}

// ValidateLLMPromotionGateConfig checks an optional LLM promotion gate config.
func ValidateLLMPromotionGateConfig(gate *LLMPromotionGateConfig) error {
	if gate == nil {
		return nil
	}
	if gate.IntervalSeconds <= 0 {
		return fmt.Errorf("%w: promotion gate interval_seconds must be > 0", ErrInvalidValue)
	}
	if gate.TimeoutSeconds <= 0 {
		return fmt.Errorf("%w: promotion gate timeout_seconds must be > 0", ErrInvalidValue)
	}
	if gate.SuccessThreshold <= 0 {
		return fmt.Errorf("%w: promotion gate success_threshold must be > 0", ErrInvalidValue)
	}
	if gate.FailureThreshold <= 0 {
		return fmt.Errorf("%w: promotion gate failure_threshold must be > 0", ErrInvalidValue)
	}
	return nil
}

// ValidateLLMReleaseConfig checks backend-specific release configuration.
func ValidateLLMReleaseConfig(release *LLMRelease) error {
	if release == nil {
		return fmt.Errorf("%w: LLM release must not be nil", ErrInvalidValue)
	}
	if err := ValidateRequiredUUID(release.RouteID, "route_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(release.Version, "version"); err != nil {
		return err
	}
	if err := ValidateRequiredString(release.ModelRef, "model_ref"); err != nil {
		return err
	}
	if err := ValidateModelSource(release.ModelSource); err != nil {
		return err
	}
	if err := ValidateLLMPromotionGateConfig(release.PromotionGate); err != nil {
		return err
	}

	needsRuntime := false
	needsExternal := false
	for _, kind := range release.BackendPreferences {
		if err := ValidateLLMBackendKind(kind); err != nil {
			return err
		}
		switch kind {
		case LLMBackendKindVLLM, LLMBackendKindOllama, LLMBackendKindLlamaCPP:
			needsRuntime = true
		case LLMBackendKindExternalAPI:
			needsExternal = true
		}
	}
	if len(release.BackendPreferences) == 0 {
		needsRuntime = release.RuntimeBackend != nil
		needsExternal = release.ExternalBackend != nil
		if !needsRuntime && !needsExternal {
			return fmt.Errorf("%w: at least one runtime_backend or external_backend config is required", ErrInvalidValue)
		}
	}

	if needsRuntime {
		if release.RuntimeBackend == nil {
			return fmt.Errorf("%w: runtime_backend is required for runtime-managed LLM backend preferences", ErrInvalidValue)
		}
		if strings.TrimSpace(release.RuntimeBackend.Image) == "" {
			return fmt.Errorf("%w: runtime_backend.image must not be empty", ErrEmptyField)
		}
		if release.RuntimeBackend.ContainerPort <= 0 {
			return fmt.Errorf("%w: runtime_backend.container_port must be > 0", ErrInvalidValue)
		}
		if release.RuntimeBackend.HostPort <= 0 {
			return fmt.Errorf("%w: runtime_backend.host_port must be > 0", ErrInvalidValue)
		}
		if strings.TrimSpace(release.RuntimeBackend.HealthPath) == "" {
			return fmt.Errorf("%w: runtime_backend.health_path must not be empty", ErrEmptyField)
		}
	}
	if needsExternal && release.ExternalBackend == nil {
		return fmt.Errorf("%w: external_backend is required for external_api LLM backend preference", ErrInvalidValue)
	}
	if release.ExternalBackend != nil && strings.TrimSpace(release.ExternalBackend.BaseURL) == "" {
		return fmt.Errorf("%w: external_backend.base_url must not be empty", ErrEmptyField)
	}
	return nil
}

// ValidateDeployStrategy checks that a DeployStrategy is a known value.
func ValidateDeployStrategy(s DeployStrategy) error {
	switch s {
	case DeployStrategyReplace, DeployStrategyBlueGreen, DeployStrategyCanary:
		return nil
	case "":
		return nil // defaults to "replace"
	default:
		return fmt.Errorf("%w: deploy strategy %q is not valid (allowed: replace, blue_green, canary)", ErrInvalidValue, s)
	}
}

// ValidateImageDigest checks that a digest has the OCI sha256:hex format.
func ValidateImageDigest(digest string) error {
	if digest == "" {
		return fmt.Errorf("%w: image digest must not be empty", ErrEmptyField)
	}
	if !digestRegex.MatchString(digest) {
		return fmt.Errorf("%w: image digest must match sha256:<64 hex chars>, got %q", ErrInvalidFormat, digest)
	}
	return nil
}

// ValidateGitSHA checks that a git SHA is a valid hex hash (7-40 chars).
func ValidateGitSHA(sha string) error {
	if sha == "" {
		return fmt.Errorf("%w: git SHA must not be empty", ErrEmptyField)
	}
	lower := strings.ToLower(sha)
	if !gitSHARegex.MatchString(lower) {
		return fmt.Errorf("%w: git SHA must be 7-40 hex characters, got %q", ErrInvalidFormat, sha)
	}
	return nil
}

// ValidateRequiredUUID checks that a UUID is not the nil/zero value.
func ValidateRequiredUUID(id uuid.UUID, fieldName string) error {
	if id == uuid.Nil {
		return fmt.Errorf("%w: %s must not be empty/nil", ErrNilUUID, fieldName)
	}
	return nil
}

// ValidateRequiredString checks that a string field is non-empty.
func ValidateRequiredString(s, fieldName string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: %s must not be empty", ErrEmptyField, fieldName)
	}
	return nil
}
