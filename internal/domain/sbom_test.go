package domain

import "testing"

func TestArtifactSBOM_HasVulnerabilities(t *testing.T) {
	s := &ArtifactSBOM{VulnerabilityCount: 0}
	if s.HasVulnerabilities() {
		t.Error("expected no vulnerabilities")
	}

	s.VulnerabilityCount = 5
	if !s.HasVulnerabilities() {
		t.Error("expected vulnerabilities")
	}
}

func TestArtifactSBOM_HasCriticalVulnerabilities(t *testing.T) {
	s := &ArtifactSBOM{CriticalCount: 0}
	if s.HasCriticalVulnerabilities() {
		t.Error("expected no critical vulnerabilities")
	}

	s.CriticalCount = 1
	if !s.HasCriticalVulnerabilities() {
		t.Error("expected critical vulnerabilities")
	}
}
