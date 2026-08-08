package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateBuildStatus(t *testing.T) {
	valid := []BuildStatus{BuildStatusQueued, BuildStatusRunning, BuildStatusSucceeded, BuildStatusFailed, BuildStatusCancelled, ""}
	for _, s := range valid {
		if err := ValidateBuildStatus(s); err != nil {
			t.Errorf("ValidateBuildStatus(%q) unexpected error: %v", s, err)
		}
	}
	invalid := []BuildStatus{"bogus", "QUEUED", "success", "done"}
	for _, s := range invalid {
		if err := ValidateBuildStatus(s); err == nil {
			t.Errorf("ValidateBuildStatus(%q) expected error, got nil", s)
		}
	}
}

func TestValidateScanStatus(t *testing.T) {
	valid := []ScanStatus{ScanStatusUnknown, ScanStatusPending, ScanStatusClean, ScanStatusWarning, ScanStatusFailed, ""}
	for _, s := range valid {
		if err := ValidateScanStatus(s); err != nil {
			t.Errorf("ValidateScanStatus(%q) unexpected error: %v", s, err)
		}
	}
	if err := ValidateScanStatus("bogus"); err == nil {
		t.Error("expected error for bogus scan status")
	}
}

func TestValidateSourceKind(t *testing.T) {
	valid := []SourceKind{SourceKindManual, SourceKindAutoPromote, SourceKindRollback, SourceKindScheduled, SourceKindEventTriggered, ""}
	for _, s := range valid {
		if err := ValidateSourceKind(s); err != nil {
			t.Errorf("ValidateSourceKind(%q) unexpected error: %v", s, err)
		}
	}
	if err := ValidateSourceKind("api_triggered"); err == nil {
		t.Error("expected error for invalid source kind")
	}
}

func TestValidateDeploymentRunStatus(t *testing.T) {
	valid := []DeploymentRunStatus{RunStatusQueued, RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusTimeout, ""}
	for _, s := range valid {
		if err := ValidateDeploymentRunStatus(s); err != nil {
			t.Errorf("ValidateDeploymentRunStatus(%q) unexpected error: %v", s, err)
		}
	}
	if err := ValidateDeploymentRunStatus("completed"); err == nil {
		t.Error("expected error for 'completed' (not a valid status)")
	}
}

func TestValidateHealthStatus(t *testing.T) {
	valid := []HealthStatus{HealthStatusUnknown, HealthStatusStarting, HealthStatusHealthy, HealthStatusUnhealthy, HealthStatusStopped}
	for _, s := range valid {
		if err := ValidateHealthStatus(s); err != nil {
			t.Errorf("ValidateHealthStatus(%q) unexpected error: %v", s, err)
		}
	}
	invalid := []HealthStatus{"", "ok", "running", "dead"}
	for _, s := range invalid {
		if err := ValidateHealthStatus(s); err == nil {
			t.Errorf("ValidateHealthStatus(%q) expected error, got nil", s)
		}
	}
}

func TestValidateRuntimeType(t *testing.T) {
	valid := []RuntimeType{RuntimeTypeDocker, RuntimeTypeCompose, RuntimeTypeK8s, RuntimeTypePodman, RuntimeTypeVMFirecracker, RuntimeTypeVMQEMU, ""}
	for _, s := range valid {
		if err := ValidateRuntimeType(s); err != nil {
			t.Errorf("ValidateRuntimeType(%q) unexpected error: %v", s, err)
		}
	}
	if err := ValidateRuntimeType("lxc"); err == nil {
		t.Error("expected error for unsupported runtime type")
	}
}

func TestValidateDeployStrategy(t *testing.T) {
	valid := []DeployStrategy{DeployStrategyReplace, DeployStrategyBlueGreen, DeployStrategyCanary, ""}
	for _, s := range valid {
		if err := ValidateDeployStrategy(s); err != nil {
			t.Errorf("ValidateDeployStrategy(%q) unexpected error: %v", s, err)
		}
	}
	if err := ValidateDeployStrategy("rolling"); err == nil {
		t.Error("expected error for unsupported deploy strategy")
	}
}

func TestValidateImageDigest(t *testing.T) {
	valid := []string{
		"sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
		"sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	for _, d := range valid {
		if err := ValidateImageDigest(d); err != nil {
			t.Errorf("ValidateImageDigest(%q) unexpected error: %v", d, err)
		}
	}
	invalid := []string{
		"",             // empty
		"sha256:short", // too short
		"sha512:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",  // wrong algo
		"a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",         // no prefix
		"sha256:AAAA95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",  // uppercase
		"sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4a", // too long (65 chars)
	}
	for _, d := range invalid {
		if err := ValidateImageDigest(d); err == nil {
			t.Errorf("ValidateImageDigest(%q) expected error, got nil", d)
		}
	}
}

func TestValidateGitSHA(t *testing.T) {
	valid := []string{
		"abc1234", // short SHA
		"abc1234567890abcdef1234567890abcdef12345678", // 42 chars... wait, let me use correct ones
	}
	// Actually let me be precise
	valid = []string{
		"abc1234", // 7 chars (short SHA)
		"a3ed95caeb02ffe68cdd9fd84406680ae93d633c", // 40 chars (full SHA)
		"abcdef1", // 7 chars
	}
	for _, s := range valid {
		if err := ValidateGitSHA(s); err != nil {
			t.Errorf("ValidateGitSHA(%q) unexpected error: %v", s, err)
		}
	}
	// Uppercase hex should be valid since we lowercase before matching.
	if err := ValidateGitSHA("ABC1234"); err != nil {
		t.Errorf("ValidateGitSHA(ABC1234) unexpected error: %v", err)
	}

	reallyInvalid := []string{
		"",        // empty
		"abc123",  // 6 chars - too short
		"xyz1234", // non-hex chars (x, y, z are not valid at position 0 and 1)
		"ghijklm", // definitely not hex
	}
	for _, s := range reallyInvalid {
		if err := ValidateGitSHA(s); err == nil {
			t.Errorf("ValidateGitSHA(%q) expected error, got nil", s)
		}
	}
}

func TestValidateRequiredUUID(t *testing.T) {
	if err := ValidateRequiredUUID(uuid.New(), "test_id"); err != nil {
		t.Errorf("unexpected error for valid UUID: %v", err)
	}
	if err := ValidateRequiredUUID(uuid.Nil, "test_id"); err == nil {
		t.Error("expected error for nil UUID")
	}
}

func TestValidateRequiredString(t *testing.T) {
	if err := ValidateRequiredString("hello", "name"); err != nil {
		t.Errorf("unexpected error for valid string: %v", err)
	}
	for _, s := range []string{"", "   ", "\t\n"} {
		if err := ValidateRequiredString(s, "name"); err == nil {
			t.Errorf("ValidateRequiredString(%q) expected error, got nil", s)
		}
	}
}
