package hiveci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	ociImageManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	cycloneDXJSONMediaType    = "application/vnd.cyclonedx+json"
	spdxJSONMediaType         = "application/spdx+json"
	inTotoJSONMediaType       = "application/vnd.in-toto+json"
	signetAttestationType     = "https://sharegap.net/hiveci/signet-artifact-attestation/v1"
	sourceProvenancePrefix    = "hiveci-source-provenance:v1:"
)

var (
	ErrNotRelease                 = errors.New("not a Hive-CI RELEASE result")
	ErrInvalidRelease             = errors.New("invalid Hive-CI RELEASE result")
	ErrUntrustedReleaseAttestor   = errors.New("untrusted Hive-CI release attestor")
	ErrReleaseEvidenceUnavailable = errors.New("Hive-CI release evidence unavailable")
	ErrReleasePolicyDenied        = errors.New("Hive-CI release denied by repository policy")
	ErrReleaseWorkerNotAdmitted   = errors.New("Hive-CI release worker is not admitted")
	ErrReleaseArtifactUnavailable = errors.New("Hive-CI release artifact is unavailable")

	hex64Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type ReleaseEvidence interface {
	GetWorkflowRunEvent(context.Context, string) (*nostr.Event, error)
	ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
	IsWorkerAdmitted(context.Context, string, string) (bool, error)
	ArtifactAvailable(context.Context, domain.HiveCIReleaseArtifact) (bool, error)
}

type ReleaseStore interface {
	CommitAcceptedRelease(context.Context, domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error)
}

type ReleaseIngestor struct {
	evidence         ReleaseEvidence
	store            ReleaseStore
	trustedAttestors map[string]struct{}
	trustedIssuers   map[string]struct{}
	now              func() time.Time
}

func NewReleaseIngestor(evidence ReleaseEvidence, store ReleaseStore, trustedAttestors, trustedWorkflowIssuers []string) *ReleaseIngestor {
	return &ReleaseIngestor{
		evidence:         evidence,
		store:            store,
		trustedAttestors: pubkeySet(trustedAttestors),
		trustedIssuers:   pubkeySet(trustedWorkflowIssuers),
		now:              func() time.Time { return time.Now().UTC() },
	}
}

// Ingest accepts only the bridge/Signet-signed terminal RELEASE 5402. The
// earlier worker-signed 5402 and all ordinary Hive-CI results return
// ErrNotRelease and never reach durable release state.
func (i *ReleaseIngestor) Ingest(ctx context.Context, event *nostr.Event) (domain.HiveCIReleaseCommitResult, error) {
	if i == nil || i.evidence == nil || i.store == nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: release ingestor dependencies are not configured", ErrReleaseEvidenceUnavailable)
	}
	now := i.now()
	if err := nostradapter.ValidateInboundEvent(event, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: signature boundary: %v", ErrInvalidRelease, err)
	}
	if int(event.Kind) != kinds.HiveCIWorkflowResult {
		return domain.HiveCIReleaseCommitResult{}, ErrNotRelease
	}

	tagResult, tagPresent, tagErr := uniqueTag(event, "result", false)
	contentType := releaseContentType(event.Content)
	if !tagPresent && contentType != domain.HiveCIReleaseResultType {
		return domain.HiveCIReleaseCommitResult{}, ErrNotRelease
	}
	if tagErr != nil || !tagPresent || tagResult != domain.HiveCIReleaseResultType ||
		contentType != domain.HiveCIReleaseResultType {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: tag and content must both identify RELEASE", ErrInvalidRelease)
	}

	attestor := event.PubKey.Hex()
	if _, ok := i.trustedAttestors[attestor]; !ok {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s", ErrUntrustedReleaseAttestor, attestor)
	}

	result, err := decodeReleaseContent(event.Content)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	if err := validateReleaseEnvelope(event, result); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}

	run, err := i.evidence.GetWorkflowRunEvent(ctx, result.Lineage.WorkflowRunEventID)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: load signed 5401: %v", ErrReleaseEvidenceUnavailable, err)
	}
	if run == nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: signed 5401 is missing", ErrReleaseEvidenceUnavailable)
	}
	if err := nostradapter.ValidateInboundEvent(run, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: stored 5401 signature boundary: %v", ErrInvalidRelease, err)
	}
	if int(run.Kind) != kinds.HiveCIWorkflowRun || run.ID.Hex() != result.Lineage.WorkflowRunEventID {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: lineage event is not the referenced kind 5401", ErrInvalidRelease)
	}
	if event.CreatedAt < run.CreatedAt {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: release predates its workflow run", ErrInvalidRelease)
	}
	if _, ok := i.trustedIssuers[run.PubKey.Hex()]; !ok {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: 5401 issuer %s", ErrReleasePolicyDenied, run.PubKey.Hex())
	}

	runEvidence, err := validateWorkflowRun(run, result)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	policy, err := i.authorizePolicy(ctx, runEvidence, result, attestor)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}

	admitted, err := i.evidence.IsWorkerAdmitted(ctx, result.Execution.WorkerIdentity, result.Execution.WorkerCapability)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: worker admission lookup: %v", ErrReleaseEvidenceUnavailable, err)
	}
	if !admitted {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s", ErrReleaseWorkerNotAdmitted, result.Execution.WorkerIdentity)
	}
	for _, candidate := range []struct {
		name     string
		artifact domain.HiveCIReleaseArtifact
	}{
		{name: "manifest", artifact: result.Manifest},
		{name: "sbom", artifact: result.SBOM},
		{name: "provenance", artifact: result.Provenance},
	} {
		name, artifact := candidate.name, candidate.artifact
		available, lookupErr := i.evidence.ArtifactAvailable(ctx, artifact)
		if lookupErr != nil {
			return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: verify %s: %v", ErrReleaseEvidenceUnavailable, name, lookupErr)
		}
		if !available {
			return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s %s", ErrReleaseArtifactUnavailable, name, artifact.Digest)
		}
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: encode signed event: %v", ErrInvalidRelease, err)
	}
	contentHash := sha256.Sum256([]byte(event.Content))
	accepted := domain.HiveCIAcceptedRelease{
		Result: result, ResultEventID: event.ID.Hex(), Attestor: attestor,
		Workflow: runEvidence.workflow, Branch: runEvidence.branch, PolicyID: policy.ID.String(),
		ContentDigest: "sha256:" + hex.EncodeToString(contentHash[:]),
		SignedEvent:   string(eventJSON), AcceptedAt: now,
	}
	return i.store.CommitAcceptedRelease(ctx, accepted)
}

func IsReleaseCandidate(event *nostr.Event) bool {
	if event == nil || int(event.Kind) != kinds.HiveCIWorkflowResult {
		return false
	}
	tag, present, _ := uniqueTag(event, "result", false)
	return (present && tag == domain.HiveCIReleaseResultType) ||
		releaseContentType(event.Content) == domain.HiveCIReleaseResultType
}

func pubkeySet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if hex64Pattern.MatchString(value) {
			set[value] = struct{}{}
		}
	}
	return set
}

func releaseContentType(content string) string {
	var marker struct {
		ResultType string `json:"result_type"`
	}
	if json.Unmarshal([]byte(content), &marker) != nil {
		return ""
	}
	return marker.ResultType
}

func decodeReleaseContent(content string) (domain.HiveCIReleaseResult, error) {
	var result domain.HiveCIReleaseResult
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("%w: decode release content: %v", ErrInvalidRelease, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return result, fmt.Errorf("%w: release content has trailing JSON", ErrInvalidRelease)
	}
	return result, nil
}

func validateReleaseEnvelope(event *nostr.Event, result domain.HiveCIReleaseResult) error {
	if result.SchemaVersion != domain.HiveCIReleaseSchemaV1 ||
		result.ResultType != domain.HiveCIReleaseResultType || result.Status != "success" {
		return fmt.Errorf("%w: unsupported schema, type, or status", ErrInvalidRelease)
	}
	expectedIdentity, err := releaseIdentity(result.Lineage)
	if err != nil || result.ReleaseIdentity != expectedIdentity {
		return fmt.Errorf("%w: release identity does not match lineage", ErrInvalidRelease)
	}
	if !validLineage(result.Lineage) {
		return fmt.Errorf("%w: complete canonical trigger and review lineage is required", ErrInvalidRelease)
	}
	execution := result.Execution
	duration, durationErr := strconv.ParseInt(execution.BahiaDuration, 10, 64)
	tests := execution.Tests
	if !execution.Complete || execution.Status != "success" || execution.ExitCode != 0 ||
		execution.DurationMS < 0 || durationErr != nil || duration < 0 || duration != execution.DurationMS/1000 ||
		!hex64Pattern.MatchString(execution.WorkerIdentity) ||
		strings.TrimSpace(execution.WorkerCapability) == "" ||
		!ociDigestPattern.MatchString(execution.BuildEnvironmentImageDigest) ||
		!validDurableReference(execution.DurableLogReference) ||
		tests.Status != "success" || tests.Total <= 0 || tests.Passed <= 0 ||
		tests.Failed != 0 || tests.Skipped < 0 || tests.Passed+tests.Failed+tests.Skipped != tests.Total {
		return fmt.Errorf("%w: execution evidence is incomplete or not green", ErrInvalidRelease)
	}
	if err := validateArtifact("manifest", result.Manifest, ociImageManifestMediaType); err != nil {
		return err
	}
	if result.SBOM.MediaType != cycloneDXJSONMediaType && result.SBOM.MediaType != spdxJSONMediaType {
		return fmt.Errorf("%w: unsupported SBOM media type", ErrInvalidRelease)
	}
	if err := validateArtifact("sbom", result.SBOM, result.SBOM.MediaType); err != nil {
		return err
	}
	if err := validateArtifact("provenance", result.Provenance, inTotoJSONMediaType); err != nil {
		return err
	}
	if result.Manifest.Repository != result.SBOM.Repository ||
		result.Manifest.Repository != result.Provenance.Repository {
		return fmt.Errorf("%w: artifact repositories conflict", ErrInvalidRelease)
	}
	if result.ArtifactAttestation.Type != signetAttestationType ||
		result.ArtifactAttestation.SignerPubkey != event.PubKey.Hex() ||
		!reflect.DeepEqual(result.ArtifactAttestation.Subjects,
			[]domain.HiveCIReleaseArtifact{result.Manifest, result.SBOM, result.Provenance}) {
		return fmt.Errorf("%w: Signet artifact attestation does not cover exact descriptors", ErrInvalidRelease)
	}

	expectedTags := map[string]string{
		"e": result.Lineage.WorkflowRunEventID, "status": "success", "result": domain.HiveCIReleaseResultType,
		"release": result.ReleaseIdentity, "trigger-envelope": result.Lineage.TriggerIdentity,
		"trigger-source": result.Lineage.TriggerSource, "trigger-id": result.Lineage.TriggerID,
		"pr": result.Lineage.PREventID, "review": result.Lineage.ReviewEventID,
		"audit": result.Lineage.AuditEventID, "a": result.Lineage.RepoAddress,
		"source-repo":       result.Lineage.SourceRepoIdentity,
		"source-provenance": result.Lineage.SourceProvenanceRef,
		"commit":            result.Lineage.Commit, "tree": result.Lineage.Tree,
		"workflow-digest": result.Lineage.WorkflowDigest,
		"worker":          execution.WorkerIdentity, "worker-capability": execution.WorkerCapability,
		"build-image": execution.BuildEnvironmentImageDigest,
		"exit_code":   "0", "duration": execution.BahiaDuration,
		"log_url": execution.DurableLogReference, "image_repo": result.Manifest.Repository,
		"image_digest": result.Manifest.Digest, "sbom_digest": result.SBOM.Digest,
		"provenance_digest": result.Provenance.Digest,
	}
	for key, want := range expectedTags {
		got, present, tagErr := uniqueTag(event, key, true)
		if tagErr != nil || !present || got != want {
			return fmt.Errorf("%w: tag %s does not match signed content", ErrInvalidRelease, key)
		}
	}
	imageTag, present, tagErr := uniqueTag(event, "image_tag", false)
	if tagErr != nil || (present && imageTag != result.ImageTag) || (!present && result.ImageTag != "") {
		return fmt.Errorf("%w: image_tag does not match signed content", ErrInvalidRelease)
	}
	return nil
}

type workflowRunEvidence struct {
	repo, workflow, branch, reviewPolicy, policyDigest string
}

func validateWorkflowRun(run *nostr.Event, result domain.HiveCIReleaseResult) (workflowRunEvidence, error) {
	required := map[string]string{
		"t": "hive-ci", "trigger-envelope": result.Lineage.TriggerIdentity,
		"idempotency": result.Lineage.TriggerIdentity, "trigger-source": result.Lineage.TriggerSource,
		"trigger-id": result.Lineage.TriggerID, "pr": result.Lineage.PREventID,
		"pr-event": result.Lineage.PREventID, "review": result.Lineage.ReviewEventID,
		"audit": result.Lineage.AuditEventID, "a": result.Lineage.RepoAddress,
		"repo-address": result.Lineage.RepoAddress, "source-repo": result.Lineage.SourceRepoIdentity,
		"source-provenance": result.Lineage.SourceProvenanceRef, "commit": result.Lineage.Commit,
		"tree": result.Lineage.Tree, "workflow-digest": result.Lineage.WorkflowDigest,
		"worker": result.Execution.WorkerIdentity, "worker-capability": result.Execution.WorkerCapability,
	}
	for key, want := range required {
		got, present, tagErr := uniqueTag(run, key, true)
		if tagErr != nil || !present || got != want {
			return workflowRunEvidence{}, fmt.Errorf("%w: 5401 tag %s conflicts with release lineage", ErrInvalidRelease, key)
		}
	}
	repo, _, _ := uniqueTag(run, "a", true)
	workflow, present, err := uniqueTag(run, "workflow", true)
	if err != nil || !present {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 workflow is missing", ErrInvalidRelease)
	}
	branch, present, err := uniqueTag(run, "branch", true)
	if err != nil || !present {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 branch is missing", ErrInvalidRelease)
	}
	workerAd, present, err := uniqueTag(run, "worker-ad", true)
	if err != nil || !present || !hex64Pattern.MatchString(workerAd) {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 worker advertisement is invalid", ErrInvalidRelease)
	}
	publisher, present, err := uniqueTag(run, "publisher", true)
	if err != nil || !present || !hex64Pattern.MatchString(publisher) {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 delegated publisher is invalid", ErrInvalidRelease)
	}
	reviewPolicy, present, err := uniqueTag(run, "review-policy", true)
	if err != nil || !present {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 review policy is missing", ErrInvalidRelease)
	}
	policyDigest, present, err := uniqueTag(run, "policy-digest", true)
	if err != nil || !present || !hex64Pattern.MatchString(policyDigest) {
		return workflowRunEvidence{}, fmt.Errorf("%w: 5401 policy digest is invalid", ErrInvalidRelease)
	}
	return workflowRunEvidence{repo: repo, workflow: workflow, branch: branch,
		reviewPolicy: reviewPolicy, policyDigest: policyDigest}, nil
}

func (i *ReleaseIngestor) authorizePolicy(ctx context.Context, run workflowRunEvidence, result domain.HiveCIReleaseResult, attestor string) (domain.HiveCIPipelinePolicy, error) {
	policies, err := i.evidence.ListPipelinePolicies(ctx)
	if err != nil {
		return domain.HiveCIPipelinePolicy{}, fmt.Errorf("%w: load repository policy: %v", ErrReleaseEvidenceUnavailable, err)
	}
	for _, policy := range policies {
		if !policy.Enabled || policy.RepoCoordinate != run.repo || policy.WorkflowPath != run.workflow ||
			!branchMatches(policy.BranchPattern, run.branch) {
			continue
		}
		if !policyMetadataMatches(policy.Metadata, run, result, attestor) {
			continue
		}
		return policy, nil
	}
	return domain.HiveCIPipelinePolicy{}, fmt.Errorf("%w: repository %s workflow %s branch %s", ErrReleasePolicyDenied, run.repo, run.workflow, run.branch)
}

func branchMatches(pattern, branch string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	matched, err := path.Match(pattern, branch)
	return err == nil && matched
}

func policyMetadataMatches(metadata map[string]any, run workflowRunEvidence, result domain.HiveCIReleaseResult, attestor string) bool {
	expected := map[string]string{
		"workflow_digest":          result.Lineage.WorkflowDigest,
		"policy_digest":            run.policyDigest,
		"review_policy":            run.reviewPolicy,
		"source_repo_identity":     result.Lineage.SourceRepoIdentity,
		"release_image_repository": result.Manifest.Repository,
	}
	for key, want := range expected {
		if raw, exists := metadata[key]; exists {
			value, ok := raw.(string)
			if !ok || value != want {
				return false
			}
		}
	}
	if raw, exists := metadata["release_attestors"]; exists {
		values, ok := raw.([]string)
		if !ok {
			untyped, untypedOK := raw.([]any)
			if !untypedOK {
				return false
			}
			values = make([]string, 0, len(untyped))
			for _, value := range untyped {
				text, textOK := value.(string)
				if !textOK {
					return false
				}
				values = append(values, text)
			}
		}
		found := false
		for _, value := range values {
			if strings.ToLower(strings.TrimSpace(value)) == attestor {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validateArtifact(name string, artifact domain.HiveCIReleaseArtifact, mediaType string) error {
	if strings.TrimSpace(artifact.Repository) == "" || strings.ContainsAny(artifact.Repository, "\x00\r\n\t") ||
		!ociDigestPattern.MatchString(artifact.Digest) || artifact.MediaType != mediaType || artifact.Size <= 0 {
		return fmt.Errorf("%w: %s descriptor is incomplete", ErrInvalidRelease, name)
	}
	return nil
}

func validLineage(lineage domain.HiveCIReleaseLineage) bool {
	for _, value := range []string{
		lineage.WorkflowRunEventID, lineage.TriggerIdentity, lineage.PREventID,
		lineage.ReviewEventID, lineage.AuditEventID, lineage.WorkflowDigest,
	} {
		if !hex64Pattern.MatchString(value) {
			return false
		}
	}
	parts := strings.SplitN(lineage.RepoAddress, ":", 3)
	validRepo := len(parts) == 3 && parts[0] == "30617" && hex64Pattern.MatchString(parts[1]) &&
		parts[2] != "" && !strings.ContainsAny(parts[2], "\x00\r\n\t")
	return strings.TrimSpace(lineage.TriggerSource) != "" &&
		strings.TrimSpace(lineage.TriggerID) != "" && validRepo &&
		strings.TrimSpace(lineage.SourceRepoIdentity) != "" &&
		strings.HasPrefix(lineage.SourceProvenanceRef, sourceProvenancePrefix) &&
		hex64Pattern.MatchString(strings.TrimPrefix(lineage.SourceProvenanceRef, sourceProvenancePrefix)) &&
		commitPattern.MatchString(lineage.Commit) && commitPattern.MatchString(lineage.Tree)
}

func releaseIdentity(lineage domain.HiveCIReleaseLineage) (string, error) {
	value := struct {
		Schema  string                      `json:"schema"`
		Lineage domain.HiveCIReleaseLineage `json:"lineage"`
	}{Schema: domain.HiveCIReleaseSchemaV1, Lineage: lineage}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return domain.HiveCIReleaseIdentityPrefix + hex.EncodeToString(sum[:]), nil
}

func validDurableReference(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "oci":
		at := strings.LastIndex(u.Path, "@")
		return u.Host != "" && at >= 0 && ociDigestPattern.MatchString(u.Path[at+1:])
	case "cas":
		return u.Host == "sha256" && hex64Pattern.MatchString(strings.TrimPrefix(u.Path, "/"))
	case "https":
		if u.Host == "" {
			return false
		}
		at := strings.LastIndex(u.Path, "@")
		if at >= 0 && ociDigestPattern.MatchString(u.Path[at+1:]) {
			return true
		}
		return hex64Pattern.MatchString(strings.TrimPrefix(path.Base(u.EscapedPath()), "/"))
	default:
		return false
	}
}

func uniqueTag(event *nostr.Event, key string, required bool) (string, bool, error) {
	var value string
	found := false
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != key {
			continue
		}
		if len(tag) != 2 {
			return "", true, fmt.Errorf("tag %s must contain exactly one value", key)
		}
		candidate := strings.TrimSpace(tag[1])
		if candidate == "" {
			return "", true, fmt.Errorf("tag %s is empty", key)
		}
		if found {
			return "", true, fmt.Errorf("tag %s is duplicated", key)
		}
		value, found = candidate, true
	}
	if required && !found {
		return "", false, fmt.Errorf("tag %s is missing", key)
	}
	return value, found, nil
}
