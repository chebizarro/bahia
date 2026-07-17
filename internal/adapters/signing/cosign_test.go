package signing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const testImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// mockInspector is a test double for registry.ImageInspector.
type mockInspector struct {
	referrers []registry.Referrer
	err       error
}

func (m *mockInspector) InspectImage(_ context.Context, _, _ string) (*registry.ImageInspection, error) {
	return &registry.ImageInspection{Exists: true}, nil
}

func (m *mockInspector) ListTags(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (m *mockInspector) GetReferrers(_ context.Context, _, _ string) ([]registry.Referrer, error) {
	return m.referrers, m.err
}

type mockCosignRunner struct {
	results        []cosignVerification
	err            error
	calls          int
	imageReference string
	policy         CosignVerificationPolicy
}

func (m *mockCosignRunner) Verify(_ context.Context, imageReference string, policy CosignVerificationPolicy) ([]cosignVerification, error) {
	m.calls++
	m.imageReference = imageReference
	m.policy = policy
	return m.results, m.err
}

func verifiedCosignResult(repo, digest string, optional map[string]any) cosignVerification {
	var result cosignVerification
	result.Critical.Identity.DockerReference = repo
	result.Critical.Image.DockerManifestDigest = digest
	result.Critical.Type = "cosign container image signature"
	result.Optional = optional
	return result
}

func testCosignVerifier(inspector registry.ImageInspector, policy CosignVerificationPolicy, runner cosignRunner) *CosignVerifier {
	verifier := NewCosignVerifierWithPolicy(inspector, policy, zap.NewNop())
	verifier.runner = runner
	return verifier
}

func cosignReferrer() registry.Referrer {
	return registry.Referrer{
		Digest:       "sha256:sig001",
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		ArtifactType: "application/vnd.dev.cosign.simplesigning.v1",
		Size:         100,
		Annotations: map[string]string{
			"dev.sigstore.cosign/identity": "untrusted-annotation@example.com",
		},
	}
}

func testArtifact() *domain.Artifact {
	return &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "registry.example/myorg/myapp",
		ImageDigest: testImageDigest,
	}
}

func TestCosignVerifier_NoDigest(t *testing.T) {
	verifier := NewCosignVerifier(&mockInspector{}, zap.NewNop())
	_, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{ImageRepo: "registry.example/myorg/myapp"})
	if err == nil {
		t.Fatal("expected error for artifact without digest")
	}
}

func TestCosignVerifier_NoReferrers(t *testing.T) {
	runner := &mockCosignRunner{}
	verifier := testCosignVerifier(&mockInspector{}, CosignVerificationPolicy{KeyRef: "cosign.pub"}, runner)
	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("expected no signatures, got %d", len(sigs))
	}
	if runner.calls != 0 {
		t.Fatalf("cosign called %d times without signature referrers", runner.calls)
	}
}

func TestCosignVerifier_CryptographicallyVerifiesKeySignature(t *testing.T) {
	inspector := &mockInspector{referrers: []registry.Referrer{cosignReferrer()}}
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/myorg/myapp", testImageDigest, nil),
	}}
	policy := CosignVerificationPolicy{KeyRef: "kms://trusted/signing-key"}
	verifier := testCosignVerifier(inspector, policy, runner)
	artifact := testArtifact()

	sigs, err := verifier.VerifySignatures(context.Background(), artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}

	sig := sigs[0]
	if sig.SignatureType != domain.SignatureCosign {
		t.Errorf("type = %q, want cosign", sig.SignatureType)
	}
	if sig.SignatureRef != "sha256:sig001" {
		t.Errorf("ref = %q", sig.SignatureRef)
	}
	if sig.SignerIdentity != policy.KeyRef {
		t.Errorf("signer = %q, want trusted key reference", sig.SignerIdentity)
	}
	if !sig.Verified || sig.VerificationStatus != domain.SignatureStatusVerified {
		t.Errorf("verified = %v status = %q, want cryptographically verified", sig.Verified, sig.VerificationStatus)
	}
	if sig.VerifiedAt == nil {
		t.Error("expected verified_at for verified signature")
	}
	if sig.ArtifactID != artifact.ID {
		t.Errorf("artifact_id = %s, want %s", sig.ArtifactID, artifact.ID)
	}
	if got, _ := sig.Metadata["cryptographic_verification"].(bool); !got {
		t.Error("expected cryptographic verification metadata")
	}
	if runner.imageReference != artifact.ImageRepo+"@"+artifact.ImageDigest {
		t.Errorf("cosign reference = %q", runner.imageReference)
	}
	if runner.policy != policy {
		t.Errorf("cosign policy = %#v, want %#v", runner.policy, policy)
	}
}

func TestCosignVerifier_CryptographicallyVerifiesKeylessSignature(t *testing.T) {
	ref := registry.Referrer{
		Digest:       "sha256:sigstore001",
		ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		Size:         200,
	}
	policy := CosignVerificationPolicy{
		CertificateIdentity:   "release@example.com",
		CertificateOIDCIssuer: "https://issuer.example",
	}
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/myorg/myapp", testImageDigest, map[string]any{
			"Subject": policy.CertificateIdentity,
			"Issuer":  policy.CertificateOIDCIssuer,
		}),
	}}
	verifier := testCosignVerifier(&mockInspector{referrers: []registry.Referrer{ref}}, policy, runner)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	if sigs[0].SignatureType != domain.SignatureSigstore {
		t.Errorf("type = %q, want sigstore", sigs[0].SignatureType)
	}
	if sigs[0].SignerIdentity != policy.CertificateIdentity {
		t.Errorf("identity = %q", sigs[0].SignerIdentity)
	}
	if !sigs[0].Verified || sigs[0].VerificationStatus != domain.SignatureStatusVerified {
		t.Errorf("keyless signature was not marked verified")
	}
}

func TestCosignVerifier_SkipsNonSignatureReferrers(t *testing.T) {
	inspector := &mockInspector{referrers: []registry.Referrer{
		{Digest: "sha256:sbom001", ArtifactType: "application/spdx+json", Size: 5000},
		cosignReferrer(),
	}}
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/myorg/myapp", testImageDigest, nil),
	}}
	verifier := testCosignVerifier(inspector, CosignVerificationPolicy{KeyRef: "cosign.pub"}, runner)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature (SBOM should be skipped), got %d", len(sigs))
	}
}

func TestCosignVerifier_ReferrersError_FailsClosed(t *testing.T) {
	verifier := testCosignVerifier(
		&mockInspector{err: context.DeadlineExceeded},
		CosignVerificationPolicy{KeyRef: "cosign.pub"},
		&mockCosignRunner{},
	)
	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil {
		t.Fatal("expected registry lookup error to fail closed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want wrapped deadline error", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures on lookup error, got %v", sigs)
	}
}

func TestCosignVerifier_MissingTrustPolicyFailsClosed(t *testing.T) {
	runner := &mockCosignRunner{}
	verifier := testCosignVerifier(
		&mockInspector{referrers: []registry.Referrer{cosignReferrer()}},
		CosignVerificationPolicy{},
		runner,
	)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil || !strings.Contains(err.Error(), "trust policy is not configured") {
		t.Fatalf("error = %v, want missing policy error", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures, got %v", sigs)
	}
	if runner.calls != 0 {
		t.Fatalf("runner called %d times with invalid policy", runner.calls)
	}
}

func TestCosignVerifier_VerificationErrorReturnsNoVerifiedRecords(t *testing.T) {
	runner := &mockCosignRunner{err: errors.New("signature is invalid")}
	verifier := testCosignVerifier(
		&mockInspector{referrers: []registry.Referrer{cosignReferrer()}},
		CosignVerificationPolicy{KeyRef: "cosign.pub"},
		runner,
	)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil || !strings.Contains(err.Error(), "signature is invalid") {
		t.Fatalf("error = %v, want verification error", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures on verification error, got %v", sigs)
	}
}

func TestCosignVerifier_WrongDigestReturnsNoVerifiedRecords(t *testing.T) {
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/myorg/myapp", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil),
	}}
	verifier := testCosignVerifier(
		&mockInspector{referrers: []registry.Referrer{cosignReferrer()}},
		CosignVerificationPolicy{KeyRef: "cosign.pub"},
		runner,
	)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil || !strings.Contains(err.Error(), "verified digest") {
		t.Fatalf("error = %v, want digest mismatch", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures for wrong digest, got %v", sigs)
	}
}

func TestCosignVerifier_WrongRepositoryReturnsNoVerifiedRecords(t *testing.T) {
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/attacker/image", testImageDigest, nil),
	}}
	verifier := testCosignVerifier(
		&mockInspector{referrers: []registry.Referrer{cosignReferrer()}},
		CosignVerificationPolicy{KeyRef: "cosign.pub"},
		runner,
	)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil || !strings.Contains(err.Error(), "verified repository") {
		t.Fatalf("error = %v, want repository mismatch", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures for wrong repository, got %v", sigs)
	}
}

func TestCanonicalImageRepository_DockerHubAliases(t *testing.T) {
	if got, want := canonicalImageRepository("alpine"), canonicalImageRepository("index.docker.io/library/alpine"); got != want {
		t.Fatalf("canonical alpine = %q, want %q", got, want)
	}
	if got, want := canonicalImageRepository("docker.io/acme/api:latest"), canonicalImageRepository("acme/api"); got != want {
		t.Fatalf("canonical Docker Hub repo = %q, want %q", got, want)
	}
}

func TestCosignVerifier_KeylessIdentityMismatchReturnsNoVerifiedRecords(t *testing.T) {
	policy := CosignVerificationPolicy{
		CertificateIdentity:   "trusted@example.com",
		CertificateOIDCIssuer: "https://issuer.example",
	}
	runner := &mockCosignRunner{results: []cosignVerification{
		verifiedCosignResult("registry.example/myorg/myapp", testImageDigest, map[string]any{
			"Subject": "attacker@example.com",
			"Issuer":  policy.CertificateOIDCIssuer,
		}),
	}}
	verifier := testCosignVerifier(
		&mockInspector{referrers: []registry.Referrer{cosignReferrer()}},
		policy,
		runner,
	)

	sigs, err := verifier.VerifySignatures(context.Background(), testArtifact())
	if err == nil || !strings.Contains(err.Error(), "does not match trusted identity") {
		t.Fatalf("error = %v, want identity mismatch", err)
	}
	if sigs != nil {
		t.Fatalf("expected no signatures for wrong identity, got %v", sigs)
	}
}

func TestParseCosignVerificationOutput_RejectsEmptyAndTrailingOutput(t *testing.T) {
	for _, data := range []string{"[]", "[] {}"} {
		if _, err := parseCosignVerificationOutput([]byte(data)); err == nil {
			t.Fatalf("parseCosignVerificationOutput(%q) unexpectedly succeeded", data)
		}
	}
}

func TestClassifyReferrer(t *testing.T) {
	tests := []struct {
		artifactType string
		want         domain.SignatureType
	}{
		{"application/vnd.dev.cosign.simplesigning.v1", domain.SignatureCosign},
		{"application/vnd.dev.sigstore.bundle.v0.3+json", domain.SignatureSigstore},
		{"application/spdx+json", ""},
		{"application/json", ""},
	}

	for _, tt := range tests {
		t.Run(tt.artifactType, func(t *testing.T) {
			got := classifyReferrer(registry.Referrer{ArtifactType: tt.artifactType})
			if got != tt.want {
				t.Errorf("classifyReferrer(%q) = %q, want %q", tt.artifactType, got, tt.want)
			}
		})
	}
}
