package domain

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecurityTargetSBOMUsesDigestAndIDFallback(t *testing.T) {
	payload := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	withDigest, err := NewSBOMSecurityTarget(SBOMSubject{Type: SBOMSubjectArtifact, ID: "artifact-1", Digest: "sha256:abc"}, SBOMFormatSPDX, payload, "sbom:artifact:1")
	require.NoError(t, err)
	require.Equal(t, SecurityTargetSBOM, withDigest.Type)
	require.Contains(t, withDigest.TargetKey, "sbom:artifact:sha256%3Aabc:spdx:")
	require.Len(t, withDigest.TargetKeyHash, 64)

	withoutDigest, err := NewSBOMSecurityTarget(SBOMSubject{Type: SBOMSubjectRepository, ID: "repo/name"}, SBOMFormatCycloneDX, payload, "ref")
	require.NoError(t, err)
	require.Contains(t, withoutDigest.TargetKey, "sbom:repository:repo%2Fname:cyclonedx:")
	require.NotEqual(t, withDigest.TargetKeyHash, withoutDigest.TargetKeyHash)
}

func TestSecurityTargetPackagePreservesEmptyVersionComponent(t *testing.T) {
	target, err := NewPackageSecurityTarget("NPM", "Lodash", "")
	require.NoError(t, err)
	require.Equal(t, "package:npm:lodash:", target.TargetKey)
	require.Equal(t, CanonicalTargetHash(target.TargetKey), target.TargetKeyHash)
	require.Equal(t, "", target.Package.Version)
}

func TestSecurityTargetPURLCanonicalizesEquivalentInputs(t *testing.T) {
	first, err := NewPURLSecurityTarget(" pkg:PyPI/Requests@2.31.0 ")
	require.NoError(t, err)
	second, err := NewPURLSecurityTarget("pkg:pypi/requests@2.31.0")
	require.NoError(t, err)
	require.Equal(t, "purl:pkg:pypi/requests@2.31.0", first.TargetKey)
	require.Equal(t, first.TargetKey, second.TargetKey)
	require.Equal(t, first.TargetKeyHash, second.TargetKeyHash)
}

func TestSecurityTargetPURLRejectsMalformed(t *testing.T) {
	_, err := NewPURLSecurityTarget("not-a-purl")
	require.Error(t, err)
}

func TestSecurityTargetCommitWithAndWithoutRepository(t *testing.T) {
	commit := "abcdef1234567890abcdef1234567890abcdef12"
	withoutRepo, err := NewCommitSecurityTarget("", strings.ToUpper(commit))
	require.NoError(t, err)
	require.Equal(t, "commit:unknown:"+commit, withoutRepo.TargetKey)

	withRepo, err := NewCommitSecurityTarget("https://github.com/example/repo.git", commit)
	require.NoError(t, err)
	require.Contains(t, withRepo.TargetKey, "commit:")
	require.NotContains(t, withRepo.TargetKey, "github.com")
	require.NotEqual(t, withoutRepo.TargetKeyHash, withRepo.TargetKeyHash)
}

func TestSecurityTargetHashIsLowerHexSHA256(t *testing.T) {
	hash := CanonicalTargetHash("package:npm:lodash:")
	require.Len(t, hash, 64)
	require.True(t, regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash))
}

func TestSecurityScanStatusTerminalHelpers(t *testing.T) {
	tests := []struct {
		status     SecurityScanStatus
		terminal   bool
		successful bool
	}{
		{SecurityScanAccepted, false, false},
		{SecurityScanRunning, false, false},
		{SecurityScanCompleted, true, true},
		{SecurityScanFailed, true, false},
		{SecurityScanCancelled, true, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			require.Equal(t, tt.terminal, tt.status.IsTerminal())
			require.Equal(t, tt.successful, tt.status.IsSuccessful())
		})
	}
}

func TestSecurityTargetsRejectMalformedRequiredFields(t *testing.T) {
	_, err := NewSBOMSecurityTarget(SBOMSubject{Type: SBOMSubjectArtifact}, SBOMFormatSPDX, "bad", "ref")
	require.Error(t, err)
	_, err = NewPackageSecurityTarget("", "name", "1")
	require.Error(t, err)
	_, err = NewCommitSecurityTarget("", "not-hex")
	require.Error(t, err)
	_, err = NewCommitSecurityTarget("", "abcdef1")
	require.Error(t, err)
}
