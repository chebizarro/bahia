// Package signing provides signature verification adapters for container image artifacts.
//
// It supports cosign/sigstore signatures via OCI referrers, Nostr-native
// signatures via event verification, and defines the SignatureVerifier
// interface for policy enforcement.
package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const (
	cosignKeyEnv             = "BAHIA_COSIGN_KEY"
	cosignCertificateIDEnv   = "BAHIA_COSIGN_CERTIFICATE_IDENTITY"
	cosignCertificateOIDCEnv = "BAHIA_COSIGN_CERTIFICATE_OIDC_ISSUER"
)

// SignatureVerifier checks whether an artifact has valid signatures.
type SignatureVerifier interface {
	// VerifySignatures checks for signatures on the given artifact and returns
	// only records that passed the configured verifier's trust policy.
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

// CosignVerificationPolicy defines the trust policy passed to cosign. Exactly
// one mode must be configured: a trusted public key reference, or an exact
// Fulcio certificate identity and OIDC issuer pair.
type CosignVerificationPolicy struct {
	KeyRef                string
	CertificateIdentity   string
	CertificateOIDCIssuer string
}

func (p CosignVerificationPolicy) validate() error {
	keyRef := strings.TrimSpace(p.KeyRef)
	identity := strings.TrimSpace(p.CertificateIdentity)
	issuer := strings.TrimSpace(p.CertificateOIDCIssuer)

	if keyRef != "" {
		if identity != "" || issuer != "" {
			return fmt.Errorf("cosign trust policy cannot combine a public key with keyless identity constraints")
		}
		return nil
	}
	if identity == "" || issuer == "" {
		return fmt.Errorf("cosign trust policy is not configured: set %s, or both %s and %s", cosignKeyEnv, cosignCertificateIDEnv, cosignCertificateOIDCEnv)
	}
	return nil
}

func cosignPolicyFromEnvironment() CosignVerificationPolicy {
	return CosignVerificationPolicy{
		KeyRef:                strings.TrimSpace(os.Getenv(cosignKeyEnv)),
		CertificateIdentity:   strings.TrimSpace(os.Getenv(cosignCertificateIDEnv)),
		CertificateOIDCIssuer: strings.TrimSpace(os.Getenv(cosignCertificateOIDCEnv)),
	}
}

// CosignVerifier performs cryptographic cosign verification for signature
// referrers attached to the artifact's exact immutable digest.
type CosignVerifier struct {
	inspector registry.ImageInspector
	policy    CosignVerificationPolicy
	runner    cosignRunner
	logger    *zap.Logger
}

// NewCosignVerifier creates a verifier using trust policy from the Bahia cosign
// environment variables documented above. Missing or ambiguous policy fails
// closed when a signature referrer is present.
func NewCosignVerifier(inspector registry.ImageInspector, logger *zap.Logger) *CosignVerifier {
	return NewCosignVerifierWithPolicy(inspector, cosignPolicyFromEnvironment(), logger)
}

// NewCosignVerifierWithPolicy creates a verifier with an explicit trusted-key
// or exact keyless identity policy.
func NewCosignVerifierWithPolicy(inspector registry.ImageInspector, policy CosignVerificationPolicy, logger *zap.Logger) *CosignVerifier {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CosignVerifier{
		inspector: inspector,
		policy:    policy,
		runner:    execCosignRunner{binary: "cosign"},
		logger:    logger,
	}
}

type cosignRunner interface {
	Verify(ctx context.Context, imageReference string, policy CosignVerificationPolicy) ([]cosignVerification, error)
}

type execCosignRunner struct {
	binary string
}

func (r execCosignRunner) Verify(ctx context.Context, imageReference string, policy CosignVerificationPolicy) ([]cosignVerification, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}

	args := []string{"verify", "--output=json"}
	if policy.KeyRef != "" {
		args = append(args, "--key", policy.KeyRef)
	} else {
		args = append(args,
			"--certificate-identity", policy.CertificateIdentity,
			"--certificate-oidc-issuer", policy.CertificateOIDCIssuer,
		)
	}
	// These are cosign's secure defaults; pass them explicitly so a future
	// default change cannot silently disable transparency-log or SCT checking.
	args = append(args, "--insecure-ignore-tlog=false", "--insecure-ignore-sct=false", imageReference)

	cmd := exec.CommandContext(ctx, r.binary, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("cosign verify failed: %s", message)
	}

	results, err := parseCosignVerificationOutput(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parsing cosign verification output: %w", err)
	}
	return results, nil
}

type cosignVerification struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

func parseCosignVerificationOutput(data []byte) ([]cosignVerification, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var results []cosignVerification
	if err := decoder.Decode(&results); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("cosign returned no verified signatures")
	}
	return results, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("invalid trailing output: %w", err)
	}
	return nil
}

// VerifySignatures discovers signature referrers, then invokes cosign against
// the exact repository@digest. Registry, policy, execution, parsing, digest,
// identity, and attribution failures all return errors with no verified records.
func (v *CosignVerifier) VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error) {
	if artifact == nil {
		return nil, fmt.Errorf("artifact is required")
	}
	if strings.TrimSpace(artifact.ImageRepo) == "" {
		return nil, fmt.Errorf("artifact has no image repository, cannot verify signatures")
	}
	if err := validateImageDigest(artifact.ImageDigest); err != nil {
		return nil, err
	}
	if v.inspector == nil {
		return nil, fmt.Errorf("registry inspector is not configured")
	}

	referrers, err := v.inspector.GetReferrers(ctx, artifact.ImageRepo, artifact.ImageDigest)
	if err != nil {
		v.logger.Warn("failed to fetch referrers for signature verification",
			zap.String("repo", artifact.ImageRepo),
			zap.String("digest", artifact.ImageDigest),
			zap.Error(err),
		)
		return nil, fmt.Errorf("fetching signature referrers for %s@%s: %w", artifact.ImageRepo, artifact.ImageDigest, err)
	}

	signatureRefs := make([]registry.Referrer, 0, len(referrers))
	for _, ref := range referrers {
		if classifyReferrer(ref) != "" {
			signatureRefs = append(signatureRefs, ref)
		}
	}
	if len(signatureRefs) == 0 {
		return nil, nil
	}
	if err := v.policy.validate(); err != nil {
		return nil, err
	}
	if v.runner == nil {
		return nil, fmt.Errorf("cosign cryptographic verifier is not configured")
	}

	imageReference := artifact.ImageRepo + "@" + artifact.ImageDigest
	verified, err := v.runner.Verify(ctx, imageReference, v.policy)
	if err != nil {
		return nil, fmt.Errorf("verifying signatures for %s: %w", imageReference, err)
	}
	if len(verified) != len(signatureRefs) {
		return nil, fmt.Errorf("cosign verified %d signatures but registry reported %d signature referrers; refusing ambiguous attribution", len(verified), len(signatureRefs))
	}

	now := time.Now().UTC()
	sigs := make([]domain.ArtifactSignature, 0, len(verified))
	for i, result := range verified {
		if result.Critical.Image.DockerManifestDigest != artifact.ImageDigest {
			return nil, fmt.Errorf("cosign result %d verified digest %q, want %q", i, result.Critical.Image.DockerManifestDigest, artifact.ImageDigest)
		}
		if strings.TrimSpace(result.Critical.Identity.DockerReference) == "" {
			return nil, fmt.Errorf("cosign result %d omitted the signed image repository", i)
		}
		if canonicalImageRepository(result.Critical.Identity.DockerReference) != canonicalImageRepository(artifact.ImageRepo) {
			return nil, fmt.Errorf("cosign result %d verified repository %q, want %q", i, result.Critical.Identity.DockerReference, artifact.ImageRepo)
		}
		if strings.TrimSpace(result.Critical.Type) == "" {
			return nil, fmt.Errorf("cosign result %d omitted the signature type", i)
		}

		identity, issuer, err := v.verifiedIdentity(result)
		if err != nil {
			return nil, fmt.Errorf("cosign result %d: %w", i, err)
		}
		ref := signatureRefs[i]
		metadata := map[string]any{
			"artifact_type":              ref.ArtifactType,
			"media_type":                 ref.MediaType,
			"size":                       ref.Size,
			"cryptographic_verification": true,
			"verification_backend":       "cosign",
			"signed_image_repository":    result.Critical.Identity.DockerReference,
			"signed_image_digest":        result.Critical.Image.DockerManifestDigest,
			"cosign_signature_type":      result.Critical.Type,
		}
		if issuer != "" {
			metadata["certificate_oidc_issuer"] = issuer
		}
		if len(ref.Annotations) > 0 {
			metadata["referrer_annotations"] = ref.Annotations
		}

		sig := domain.ArtifactSignature{
			ID:                 uuid.New(),
			ArtifactID:         artifact.ID,
			SignerIdentity:     identity,
			SignatureType:      classifyReferrer(ref),
			SignatureRef:       ref.Digest,
			Verified:           true,
			VerificationStatus: domain.SignatureStatusVerified,
			VerifiedAt:         &now,
			CreatedAt:          now,
			Metadata:           metadata,
		}
		sig.NormalizeVerificationStatus()
		sigs = append(sigs, sig)
	}

	v.logger.Info("cryptographically verified cosign signatures",
		zap.String("artifact_id", artifact.ID.String()),
		zap.String("image", imageReference),
		zap.Int("signatures", len(sigs)),
	)
	return sigs, nil
}

func (v *CosignVerifier) verifiedIdentity(result cosignVerification) (identity string, issuer string, err error) {
	if v.policy.KeyRef != "" {
		return v.policy.KeyRef, "", nil
	}
	identity = optionalString(result.Optional, "Subject", "subject")
	issuer = optionalString(result.Optional, "Issuer", "issuer")
	if identity != v.policy.CertificateIdentity {
		return "", "", fmt.Errorf("certificate identity %q does not match trusted identity %q", identity, v.policy.CertificateIdentity)
	}
	if issuer != v.policy.CertificateOIDCIssuer {
		return "", "", fmt.Errorf("certificate OIDC issuer %q does not match trusted issuer %q", issuer, v.policy.CertificateOIDCIssuer)
	}
	return identity, issuer, nil
}

func optionalString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func canonicalImageRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	repository = strings.TrimPrefix(repository, "https://")
	repository = strings.TrimPrefix(repository, "http://")
	repository = strings.TrimSuffix(repository, "/")
	if at := strings.LastIndex(repository, "@"); at >= 0 {
		repository = repository[:at]
	}
	if slash, colon := strings.LastIndex(repository, "/"), strings.LastIndex(repository, ":"); colon > slash {
		repository = repository[:colon]
	}

	first, _, hasSlash := strings.Cut(repository, "/")
	switch first {
	case "docker.io":
		return "index.docker.io/" + strings.TrimPrefix(repository, "docker.io/")
	case "index.docker.io":
		return repository
	}
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		if !hasSlash {
			return "index.docker.io/library/" + repository
		}
		return "index.docker.io/" + repository
	}
	return repository
}

func validateImageDigest(digest string) error {
	algorithm, encoded, ok := strings.Cut(strings.TrimSpace(digest), ":")
	if !ok || algorithm != "sha256" || len(encoded) != 64 {
		return fmt.Errorf("artifact digest %q is not a canonical sha256 digest", digest)
	}
	for _, r := range encoded {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("artifact digest %q is not a canonical sha256 digest", digest)
		}
	}
	return nil
}

// classifyReferrer determines the signature type from an OCI referrer.
func classifyReferrer(ref registry.Referrer) domain.SignatureType {
	switch {
	case isCosignType(ref.ArtifactType):
		return domain.SignatureCosign
	case isSigstoreType(ref.ArtifactType):
		return domain.SignatureSigstore
	default:
		return ""
	}
}

func isCosignType(t string) bool {
	return t == "application/vnd.dev.cosign.simplesigning.v1"
}

func isSigstoreType(t string) bool {
	return t == "application/vnd.dev.sigstore.bundle.v0.3+json" ||
		t == "application/vnd.dev.sigstore.bundle+json;version=0.3"
}

// Compile-time interface check.
var _ SignatureVerifier = (*CosignVerifier)(nil)
