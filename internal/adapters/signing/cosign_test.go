package signing

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

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

func TestCosignVerifier_NoDigest(t *testing.T) {
	verifier := NewCosignVerifier(&mockInspector{}, zap.NewNop())
	_, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{})
	if err == nil {
		t.Fatal("expected error for artifact without digest")
	}
}

func TestCosignVerifier_NoReferrers(t *testing.T) {
	verifier := NewCosignVerifier(&mockInspector{referrers: nil}, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("expected no signatures, got %d", len(sigs))
	}
}

func TestCosignVerifier_FindsCosignSignature(t *testing.T) {
	inspector := &mockInspector{
		referrers: []registry.Referrer{
			{
				Digest:       "sha256:sig001",
				MediaType:    "application/vnd.oci.image.manifest.v1+json",
				ArtifactType: "application/vnd.dev.cosign.simplesigning.v1",
				Size:         100,
				Annotations: map[string]string{
					"dev.sigstore.cosign/identity": "alice@example.com",
				},
			},
		},
	}

	verifier := NewCosignVerifier(inspector, zap.NewNop())
	artifactID := uuid.New()
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{
		ID:          artifactID,
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	})
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
	if sig.SignerIdentity != "alice@example.com" {
		t.Errorf("signer = %q, want alice@example.com", sig.SignerIdentity)
	}
	if sig.Verified {
		t.Error("expected registry referrer discovery to remain unverified")
	}
	if sig.VerificationStatus != domain.SignatureStatusDiscovered {
		t.Errorf("verification_status = %q, want discovered", sig.VerificationStatus)
	}
	if sig.VerifiedAt != nil {
		t.Error("expected verified_at to be nil for discovered-only referrer")
	}
	if sig.ArtifactID != artifactID {
		t.Errorf("artifact_id = %s, want %s", sig.ArtifactID, artifactID)
	}
}

func TestCosignVerifier_FindsSigstoreSignature(t *testing.T) {
	inspector := &mockInspector{
		referrers: []registry.Referrer{
			{
				Digest:       "sha256:sigstore001",
				ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json",
				Size:         200,
			},
		},
	}

	verifier := NewCosignVerifier(inspector, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	if sigs[0].SignatureType != domain.SignatureSigstore {
		t.Errorf("type = %q, want sigstore", sigs[0].SignatureType)
	}
	if sigs[0].Verified || sigs[0].VerificationStatus != domain.SignatureStatusDiscovered {
		t.Errorf("sigstore referrer status = verified:%v status:%q, want discovered only", sigs[0].Verified, sigs[0].VerificationStatus)
	}
}

func TestCosignVerifier_SkipsNonSignatureReferrers(t *testing.T) {
	inspector := &mockInspector{
		referrers: []registry.Referrer{
			{
				Digest:       "sha256:sbom001",
				ArtifactType: "application/spdx+json",
				Size:         5000,
			},
			{
				Digest:       "sha256:sig001",
				ArtifactType: "application/vnd.dev.cosign.simplesigning.v1",
				Size:         100,
			},
		},
	}

	verifier := NewCosignVerifier(inspector, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature (SBOM should be skipped), got %d", len(sigs))
	}
}

func TestCosignVerifier_ReferrersError_ReturnsNil(t *testing.T) {
	inspector := &mockInspector{
		err: context.DeadlineExceeded,
	}

	verifier := NewCosignVerifier(inspector, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error (should be nil): %v", err)
	}
	if sigs != nil {
		t.Fatalf("expected nil sigs on error, got %v", sigs)
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
