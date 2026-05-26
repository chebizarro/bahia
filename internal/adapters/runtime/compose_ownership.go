package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ComposeOwnershipReason classifies why a compose directory is or is not
// considered Bahia-owned.
type ComposeOwnershipReason int

const (
	// OwnershipUnknown indicates ownership could not be determined.
	OwnershipUnknown ComposeOwnershipReason = iota
	// OwnershipBahiaMarker indicates the .bahia/ marker directory and
	// render-state.json metadata file are present and valid.
	OwnershipBahiaMarker
	// OwnershipExplicitConfig indicates the runtime config explicitly
	// declares bahia_owned: true for this environment.
	OwnershipExplicitConfig
	// OwnershipNotOwned indicates no markers or explicit config were found;
	// the directory is assumed to be operator-authored.
	OwnershipNotOwned
	// OwnershipMissingDir indicates the compose_dir path does not exist.
	OwnershipMissingDir
	// OwnershipMalformed indicates the .bahia/ marker directory exists but
	// contains invalid or corrupted metadata.
	OwnershipMalformed
)

// String returns a machine-readable label for the ownership reason.
func (r ComposeOwnershipReason) String() string {
	switch r {
	case OwnershipBahiaMarker:
		return "bahia_marker"
	case OwnershipExplicitConfig:
		return "explicit_config"
	case OwnershipNotOwned:
		return "not_owned"
	case OwnershipMissingDir:
		return "missing_dir"
	case OwnershipMalformed:
		return "malformed"
	default:
		return "unknown"
	}
}

// ComposeOwnershipStatus reports whether a compose directory is Bahia-owned
// and safe for authoritative full-project generation.
type ComposeOwnershipStatus struct {
	// Owned is true when the compose directory is confirmed Bahia-owned
	// either by marker presence or explicit config.
	Owned bool
	// Reason classifies why ownership was determined.
	Reason ComposeOwnershipReason
	// ComposePath is the resolved absolute path to the compose directory.
	ComposePath string
	// MarkerPath is the path to the .bahia/ marker directory, if found.
	MarkerPath string
	// Error holds a non-nil value when ownership validation itself failed.
	// Errors intentionally omit endpoint secrets and raw Docker host
	// values to avoid leaking sensitive material in logs.
	Error error
}

// bahiaMarkerDir is the sentinel directory that marks a compose project as
// Bahia-owned.
const bahiaMarkerDir = ".bahia"

// bahiaRenderStateFile is the metadata file inside .bahia/ that records
// render provenance and state.
const bahiaRenderStateFile = "render-state.json"

// ComposeOwnershipConfig carries the subset of environment runtime
// configuration relevant to ownership validation. It is intentionally
// separate from full runtime config to avoid leaking endpoint secrets
// into validation paths.
type ComposeOwnershipConfig struct {
	// BahiaOwned, when explicitly true, overrides marker detection and
	// declares the compose directory as Bahia-owned. When false or unset,
	// marker detection applies.
	BahiaOwned *bool
}

// ValidateComposeOwnership checks whether the given compose directory is
// Bahia-owned and safe for authoritative full-project generation.
//
// Resolution order:
//  1. If cfg.BahiaOwned is explicitly true, ownership is granted regardless
//     of marker state (OwnershipExplicitConfig).
//  2. If the directory does not exist, OwnershipMissingDir is returned.
//  3. If .bahia/ marker directory and render-state.json exist and are valid,
//     OwnershipBahiaMarker is returned.
//  4. If .bahia/ exists but render-state.json is missing or corrupt,
//     OwnershipMalformed is returned.
//  5. Otherwise OwnershipNotOwned is returned.
//
// Errors returned in ComposeOwnershipStatus.Error never include endpoint
// secrets or raw Docker host values.
func ValidateComposeOwnership(composeDir string, cfg ComposeOwnershipConfig) ComposeOwnershipStatus {
	composeDir = strings.TrimSpace(composeDir)
	if composeDir == "" {
		return ComposeOwnershipStatus{
			Reason: OwnershipMissingDir,
			Error:  fmt.Errorf("compose_dir is empty"),
		}
	}

	absDir, err := filepath.Abs(composeDir)
	if err != nil {
		return ComposeOwnershipStatus{
			ComposePath: composeDir,
			Reason:      OwnershipMissingDir,
			Error:       fmt.Errorf("cannot resolve compose_dir path"),
		}
	}

	// Explicit config override takes precedence over everything.
	if cfg.BahiaOwned != nil && *cfg.BahiaOwned {
		return ComposeOwnershipStatus{
			Owned:       true,
			Reason:      OwnershipExplicitConfig,
			ComposePath: absDir,
		}
	}

	// Check directory existence.
	info, err := os.Stat(absDir)
	if err != nil {
		return ComposeOwnershipStatus{
			ComposePath: absDir,
			Reason:      OwnershipMissingDir,
			Error:       fmt.Errorf("compose_dir does not exist"),
		}
	}
	if !info.IsDir() {
		return ComposeOwnershipStatus{
			ComposePath: absDir,
			Reason:      OwnershipMissingDir,
			Error:       fmt.Errorf("compose_dir is not a directory"),
		}
	}

	markerPath := filepath.Join(absDir, bahiaMarkerDir)
	markerInfo, err := os.Stat(markerPath)
	if err != nil {
		// No .bahia/ directory — not owned.
		return ComposeOwnershipStatus{
			ComposePath: absDir,
			Reason:      OwnershipNotOwned,
		}
	}
	if !markerInfo.IsDir() {
		return ComposeOwnershipStatus{
			ComposePath: absDir,
			MarkerPath:  markerPath,
			Reason:      OwnershipMalformed,
			Error:       fmt.Errorf(".bahia marker exists but is not a directory"),
		}
	}

	// Validate render-state.json inside the marker directory.
	renderStatePath := filepath.Join(markerPath, bahiaRenderStateFile)
	if err := validateRenderStateFile(renderStatePath); err != nil {
		return ComposeOwnershipStatus{
			ComposePath: absDir,
			MarkerPath:  markerPath,
			Reason:      OwnershipMalformed,
			Error:       err,
		}
	}

	return ComposeOwnershipStatus{
		Owned:       true,
		Reason:      OwnershipBahiaMarker,
		ComposePath: absDir,
		MarkerPath:  markerPath,
	}
}

// ComposeOwnershipError is a structured error returned when Compose ownership
// validation fails. It carries machine-readable reason codes for programmatic
// handling and correlation in failure events.
type ComposeOwnershipError struct {
	// Reason is the machine-readable ownership classification.
	Reason ComposeOwnershipReason
	// ReasonCode is the string form of Reason for structured logging/events.
	ReasonCode string
	// Message is a human-readable description of why ownership failed.
	Message string
	// ComposePath is the resolved compose directory path (may be empty).
	ComposePath string
}

func (e *ComposeOwnershipError) Error() string {
	if e.ComposePath != "" {
		return fmt.Sprintf("compose ownership validation failed (%s): %s", e.ReasonCode, e.Message)
	}
	return fmt.Sprintf("compose ownership validation failed (%s): %s", e.ReasonCode, e.Message)
}

// NewComposeOwnershipError creates a ComposeOwnershipError from a
// ComposeOwnershipStatus that represents a failed ownership check.
func NewComposeOwnershipError(status ComposeOwnershipStatus) *ComposeOwnershipError {
	msg := fmt.Sprintf("compose directory is not Bahia-owned (reason: %s)", status.Reason)
	if status.Error != nil {
		msg = status.Error.Error()
	}
	return &ComposeOwnershipError{
		Reason:      status.Reason,
		ReasonCode:  status.Reason.String(),
		Message:     msg,
		ComposePath: status.ComposePath,
	}
}

// IsComposeOwnershipError returns true if err is a *ComposeOwnershipError.
func IsComposeOwnershipError(err error) bool {
	_, ok := err.(*ComposeOwnershipError)
	return ok
}

// AsComposeOwnershipError extracts a *ComposeOwnershipError from err if present.
func AsComposeOwnershipError(err error) (*ComposeOwnershipError, bool) {
	var ownershipErr *ComposeOwnershipError
	if errors.As(err, &ownershipErr) {
		return ownershipErr, true
	}
	return nil, false
}

// validateRenderStateFile checks that the render-state.json file exists and
// contains valid JSON. It does not enforce a specific schema beyond valid
// JSON with at least one key, allowing the renderer to evolve the format.
func validateRenderStateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("render-state.json missing from .bahia marker")
		}
		return fmt.Errorf("cannot read render-state.json")
	}

	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return fmt.Errorf("render-state.json is empty")
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("render-state.json contains invalid JSON")
	}
	if len(parsed) == 0 {
		return fmt.Errorf("render-state.json is an empty object")
	}

	return nil
}
