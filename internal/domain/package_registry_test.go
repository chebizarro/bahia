package domain

import "testing"

func TestPackageRepositoryFormatsAreFirstClassValues(t *testing.T) {
	formats := []PackageRepositoryFormat{
		PackageRepositoryFormatNPM,
		PackageRepositoryFormatPyPI,
		PackageRepositoryFormatConan,
		PackageRepositoryFormatDeb,
		PackageRepositoryFormatRPM,
		PackageRepositoryFormatPub,
		PackageRepositoryFormatGoModules,
		PackageRepositoryFormatGradle,
	}
	for _, format := range formats {
		if !format.IsValid() {
			t.Fatalf("expected format %q to be valid", format)
		}
	}
	if PackageRepositoryFormat("generic").IsValid() {
		t.Fatal("generic must not be treated as a first-class package format in this phase")
	}
}

func TestPackageBackendTypesAreRegisteredValues(t *testing.T) {
	for _, backend := range []PackageBackendType{PackageBackendNexus, PackageBackendPulp, PackageBackendFilesystemMock} {
		if !backend.IsValid() {
			t.Fatalf("expected backend %q to be valid", backend)
		}
	}
	if PackageBackendType("inline_secret_backend").IsValid() {
		t.Fatal("unexpected backend accepted")
	}
}

func TestPackageIntentTerminalStatus(t *testing.T) {
	terminal := []PackageIntentStatus{PackageIntentStatusSucceeded, PackageIntentStatusFailed, PackageIntentStatusSuperseded}
	for _, status := range terminal {
		if !status.Terminal() {
			t.Fatalf("expected %q to be terminal", status)
		}
	}
	for _, status := range []PackageIntentStatus{PackageIntentStatusAccepted, PackageIntentStatusExecuting} {
		if status.Terminal() {
			t.Fatalf("expected %q to be non-terminal", status)
		}
	}
}
