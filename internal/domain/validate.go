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
	ErrEmptyField     = fmt.Errorf("field must not be empty")
	ErrInvalidFormat  = fmt.Errorf("invalid format")
	ErrInvalidValue   = fmt.Errorf("invalid value")
	ErrNilUUID        = fmt.Errorf("UUID must not be nil")
)

// digestRegex matches OCI content-addressable digests: algorithm:hex
var digestRegex = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// gitSHARegex matches full or short git SHA hashes (7-40 hex chars).
var gitSHARegex = regexp.MustCompile(`^[a-f0-9]{7,40}$`)

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
	case RuntimeTypeDocker, RuntimeTypeCompose, RuntimeTypeK8s:
		return nil
	case "":
		return nil // defaults to "docker"
	default:
		return fmt.Errorf("%w: runtime type %q is not valid (allowed: docker, compose, kubernetes)", ErrInvalidValue, s)
	}
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
