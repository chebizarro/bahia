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
	inTotoStatementType       = "https://in-toto.io/Statement/v1"
	releaseProvenanceType     = "https://sharegap.net/hiveci/release-provenance/v1"
	signetAttestationType     = "https://sharegap.net/hiveci/signet-artifact-attestation/v1"
	sourceProvenancePrefix    = "hiveci-source-provenance:v1:"
)

var (
	ErrNotRelease                 = errors.New("not a release attestation")
	ErrInvalidRelease             = errors.New("invalid release attestation")
	ErrUntrustedReleaseAttestor   = errors.New("untrusted Hive-CI release attestor")
	ErrReleaseEvidenceUnavailable = errors.New("Hive-CI release evidence unavailable")
	ErrReleaseLineagePending      = errors.New("Hive-CI release is waiting for signed workflow lineage")
	ErrReleasePolicyDenied        = errors.New("Hive-CI release denied by repository policy")
	ErrReleaseWorkerNotAdmitted   = errors.New("Hive-CI release worker is not admitted")
	ErrReleaseArtifactUnavailable = errors.New("Hive-CI release artifact is unavailable")

	hex64Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type ResolvedReleaseArtifact struct {
	Content   []byte
	MediaType string
	Size      int64
}

type WorkerAdmissionEvidence struct {
	WorkerIdentity     string
	WorkerCapability   string
	WorkerAdEventID    string
	WorkerAdvertisedAt time.Time
	DecisionCode       string
	CapacityClass      string
	PressureLevel      string
}

type ReleaseEvidence interface {
	GetWorkflowRunEvent(context.Context, string) (*nostr.Event, error)
	ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error)
	AdmitWorker(context.Context, string, string, string) (WorkerAdmissionEvidence, bool, error)
	ResolveArtifact(context.Context, domain.HiveCIReleaseArtifact) (ResolvedReleaseArtifact, error)
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

// Ingest accepts only a trusted-attestor-signed terminal release attestation:
// kind 4903 with domain=release, type=attestation, and schema
// bahia.audit.release.v1 (cascadia-nips release_attestation). Hive-CI kind
// 5402 has exactly one semantic (the Workflow Result) and is never a release;
// every other event returns ErrNotRelease and never reaches durable release
// state.
func (i *ReleaseIngestor) Ingest(ctx context.Context, event *nostr.Event) (domain.HiveCIReleaseCommitResult, error) {
	if i == nil || i.evidence == nil || i.store == nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: release ingestor dependencies are not configured", ErrReleaseEvidenceUnavailable)
	}
	now := i.now()
	if err := nostradapter.ValidateInboundEvent(event, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: signature boundary: %w", ErrInvalidRelease, err)
	}
	if int(event.Kind) != kinds.CASAudit {
		return domain.HiveCIReleaseCommitResult{}, ErrNotRelease
	}
	if !IsReleaseCandidate(event) {
		return domain.HiveCIReleaseCommitResult{}, ErrNotRelease
	}
	if schema, present, tagErr := uniqueTag(event, "schema", true); tagErr != nil || !present || schema != domain.ReleaseAttestationSchema {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: schema tag must be %s", ErrInvalidRelease, domain.ReleaseAttestationSchema)
	}

	attestor := event.PubKey.Hex()
	if _, ok := i.trustedAttestors[attestor]; !ok {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s", ErrUntrustedReleaseAttestor, attestor)
	}

	envelope, err := decodeReleaseAttestation(event.Content)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	result := envelope.Meta.HiveCIRelease
	if err := validateAttestationPayload(envelope, result); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	if err := validateReleaseEnvelope(event, result); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}

	run, err := i.evidence.GetWorkflowRunEvent(ctx, result.Lineage.WorkflowRunEventID)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: load signed 5401: %w", ErrReleaseEvidenceUnavailable, err)
	}
	if run == nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: signed 5401 %s is missing", ErrReleaseLineagePending, result.Lineage.WorkflowRunEventID)
	}
	if err := nostradapter.ValidateInboundEvent(run, now, nostradapter.InboundEventMaxFutureSkew); err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: stored 5401 signature boundary: %w", ErrInvalidRelease, err)
	}
	if err := validateWorkflowRunReference(run, result.Lineage.WorkflowRunEventID); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
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

	workerAdmission, admitted, err := i.evidence.AdmitWorker(
		ctx, result.Execution.WorkerIdentity, result.Execution.WorkerCapability, runEvidence.workerAd,
	)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: worker admission lookup: %w", ErrReleaseEvidenceUnavailable, err)
	}
	if !admitted {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: %s", ErrReleaseWorkerNotAdmitted, result.Execution.WorkerIdentity)
	}
	resolved := make(map[string]ResolvedReleaseArtifact, 3)
	for _, candidate := range []struct {
		name     string
		artifact domain.HiveCIReleaseArtifact
	}{
		{name: "manifest", artifact: result.Manifest},
		{name: "sbom", artifact: result.SBOM},
		{name: "provenance", artifact: result.Provenance},
	} {
		object, lookupErr := i.evidence.ResolveArtifact(ctx, candidate.artifact)
		if lookupErr != nil {
			return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: resolve %s by digest: %w", ErrReleaseEvidenceUnavailable, candidate.name, lookupErr)
		}
		if err := verifyResolvedArtifact(candidate.name, candidate.artifact, object); err != nil {
			return domain.HiveCIReleaseCommitResult{}, err
		}
		resolved[candidate.name] = object
	}
	if err := verifyOCIManifest(resolved["manifest"].Content); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	if err := verifySBOM(result.SBOM.MediaType, resolved["sbom"].Content); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}
	if err := verifyProvenance(result, resolved["provenance"].Content); err != nil {
		return domain.HiveCIReleaseCommitResult{}, err
	}

	runJSON, err := json.Marshal(run)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: encode signed workflow event: %w", ErrInvalidRelease, err)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return domain.HiveCIReleaseCommitResult{}, fmt.Errorf("%w: encode signed event: %w", ErrInvalidRelease, err)
	}
	contentHash := sha256.Sum256([]byte(event.Content))
	accepted := domain.HiveCIAcceptedRelease{
		Result: result, ResultEventID: event.ID.Hex(), Attestor: attestor,
		Workflow: runEvidence.workflow, Branch: runEvidence.branch, PolicyID: policy.ID.String(), Policy: policy,
		WorkflowRunSignedEvent: string(runJSON),
		WorkerAdmissionEvidence: map[string]any{
			"state": "admitted", "worker_identity": workerAdmission.WorkerIdentity,
			"worker_capability":    workerAdmission.WorkerCapability,
			"worker_ad_event_id":   workerAdmission.WorkerAdEventID,
			"worker_advertised_at": workerAdmission.WorkerAdvertisedAt.UTC().Format(time.RFC3339Nano),
			"decision_code":        workerAdmission.DecisionCode,
			"capacity_class":       workerAdmission.CapacityClass,
			"pressure_level":       workerAdmission.PressureLevel,
			"workflow_issuer":      run.PubKey.Hex(),
		},
		RollbackCompatibility: policyMetadataObject(policy.Metadata, "rollback_compatibility"),
		HealthReadinessContracts: map[string]any{
			"health":    policyMetadataObject(policy.Metadata, "health_contract"),
			"readiness": policyMetadataObject(policy.Metadata, "readiness_contract"),
		},
		ContentDigest: "sha256:" + hex.EncodeToString(contentHash[:]),
		SignedEvent:   string(eventJSON), AcceptedAt: now,
	}
	return i.store.CommitAcceptedRelease(ctx, accepted)
}

func validateWorkflowRunReference(run *nostr.Event, workflowRunEventID string) error {
	if run == nil || int(run.Kind) != kinds.HiveCIWorkflowRun || run.ID.Hex() != workflowRunEventID {
		return fmt.Errorf("%w: lineage event is not the referenced kind 5401", ErrInvalidRelease)
	}
	return nil
}

// IsReleaseCandidate reports whether the event claims to be a terminal
// release attestation: kind 4903 tagged domain=release and type=attestation.
func IsReleaseCandidate(event *nostr.Event) bool {
	if event == nil || int(event.Kind) != kinds.CASAudit {
		return false
	}
	auditDomain, domainPresent, _ := uniqueTag(event, "domain", false)
	auditType, typePresent, _ := uniqueTag(event, "type", false)
	return domainPresent && typePresent &&
		auditDomain == domain.ReleaseAttestationDomain && auditType == domain.ReleaseAttestationAuditType
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

func decodeReleaseAttestation(content string) (domain.ReleaseAttestationEnvelope, error) {
	var envelope domain.ReleaseAttestationEnvelope
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, fmt.Errorf("%w: decode release attestation content: %w", ErrInvalidRelease, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return envelope, fmt.Errorf("%w: release attestation content has trailing JSON", ErrInvalidRelease)
	}
	if envelope.V != 1 || envelope.Type != domain.ReleaseAttestationEnvelopeType {
		return envelope, fmt.Errorf("%w: content must be v1 %s", ErrInvalidRelease, domain.ReleaseAttestationEnvelopeType)
	}
	return envelope, nil
}

// validateAttestationPayload binds the canonical bahia.audit.release.v1
// payload to the full provenance document it summarises. A payload that
// disagrees with meta.hiveci_release is a forged or corrupted attestation.
func validateAttestationPayload(envelope domain.ReleaseAttestationEnvelope, result domain.HiveCIReleaseResult) error {
	payload := envelope.Payload
	expected := map[string][2]string{
		"release_id":      {payload.ReleaseID, result.ReleaseIdentity},
		"workflow_run_id": {payload.WorkflowRunID, result.Lineage.WorkflowRunEventID},
		"source_commit":   {payload.SourceCommit, result.Lineage.Commit},
		"artifact":        {payload.Artifact, releaseArtifactReference(result.Manifest)},
		"digest":          {payload.Digest, result.Manifest.Digest},
		"sbom_ref":        {payload.SBOMRef, result.SBOM.Digest},
		"provenance_ref":  {payload.ProvenanceRef, result.Provenance.Digest},
	}
	for field, pair := range expected {
		if strings.TrimSpace(pair[0]) == "" || pair[0] != pair[1] {
			return fmt.Errorf("%w: payload %s does not match provenance document", ErrInvalidRelease, field)
		}
	}
	if payload.SourceRepo != "" && payload.SourceRepo != result.Lineage.RepoAddress {
		return fmt.Errorf("%w: payload source_repo does not match provenance document", ErrInvalidRelease)
	}
	if payload.ResultID != "" && !hex64Pattern.MatchString(payload.ResultID) {
		return fmt.Errorf("%w: payload result_id must reference a kind-5402 event id", ErrInvalidRelease)
	}
	if _, err := time.Parse(time.RFC3339, payload.AttestedAt); err != nil {
		return fmt.Errorf("%w: payload attested_at must be RFC 3339", ErrInvalidRelease)
	}
	return nil
}

func releaseArtifactReference(artifact domain.HiveCIReleaseArtifact) string {
	return artifact.Repository + "@" + artifact.Digest
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
		"domain": domain.ReleaseAttestationDomain, "type": domain.ReleaseAttestationAuditType,
		"schema": domain.ReleaseAttestationSchema, "run": result.Lineage.WorkflowRunEventID,
		"artifact": releaseArtifactReference(result.Manifest),
		"release":  result.ReleaseIdentity, "trigger-envelope": result.Lineage.TriggerIdentity,
		"trigger-source": result.Lineage.TriggerSource, "trigger-id": result.Lineage.TriggerID,
		"pr": result.Lineage.PREventID, "review": result.Lineage.ReviewEventID,
		"audit": result.Lineage.AuditEventID, "a": result.Lineage.RepoAddress,
		"source-repo":       result.Lineage.SourceRepoIdentity,
		"source-provenance": result.Lineage.SourceProvenanceRef,
		"commit":            result.Lineage.Commit, "tree": result.Lineage.Tree,
		"workflow-digest": result.Lineage.WorkflowDigest,
		"worker":          execution.WorkerIdentity, "worker-capability": execution.WorkerCapability,
		"build-image": execution.BuildEnvironmentImageDigest,
		"log_url":     execution.DurableLogReference, "image_repo": result.Manifest.Repository,
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
	repo, workflow, branch, reviewPolicy, policyDigest, workerAd string
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
		reviewPolicy: reviewPolicy, policyDigest: policyDigest, workerAd: workerAd}, nil
}

func (i *ReleaseIngestor) authorizePolicy(ctx context.Context, run workflowRunEvidence, result domain.HiveCIReleaseResult, attestor string) (domain.HiveCIPipelinePolicy, error) {
	policies, err := i.evidence.ListPipelinePolicies(ctx)
	if err != nil {
		return domain.HiveCIPipelinePolicy{}, fmt.Errorf("%w: load repository policy: %w", ErrReleaseEvidenceUnavailable, err)
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

func policyMetadataObject(metadata map[string]any, key string) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	value, _ := metadata[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
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
		raw, exists := metadata[key]
		value, ok := raw.(string)
		if !exists || !ok || strings.TrimSpace(value) == "" || value != want {
			return false
		}
	}
	raw, exists := metadata["release_attestors"]
	if !exists {
		return false
	}
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
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == attestor {
			return true
		}
	}
	return false
}

func verifyResolvedArtifact(name string, descriptor domain.HiveCIReleaseArtifact, object ResolvedReleaseArtifact) error {
	if len(object.Content) == 0 {
		return fmt.Errorf("%w: %s %s", ErrReleaseArtifactUnavailable, name, descriptor.Digest)
	}
	sum := sha256.Sum256(object.Content)
	if "sha256:"+hex.EncodeToString(sum[:]) != descriptor.Digest {
		return fmt.Errorf("%w: %s bytes do not match signed digest", ErrInvalidRelease, name)
	}
	if object.Size != descriptor.Size || int64(len(object.Content)) != descriptor.Size {
		return fmt.Errorf("%w: %s bytes do not match signed size", ErrInvalidRelease, name)
	}
	if object.MediaType != descriptor.MediaType {
		return fmt.Errorf("%w: %s media type does not match signed descriptor", ErrInvalidRelease, name)
	}
	return nil
}

func verifyOCIManifest(content []byte) error {
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if json.Unmarshal(content, &manifest) != nil || manifest.SchemaVersion != 2 ||
		(manifest.MediaType != "" && manifest.MediaType != ociImageManifestMediaType) ||
		!ociDigestPattern.MatchString(manifest.Config.Digest) || len(manifest.Layers) == 0 {
		return fmt.Errorf("%w: manifest is not a valid OCI image manifest", ErrInvalidRelease)
	}
	for _, layer := range manifest.Layers {
		if !ociDigestPattern.MatchString(layer.Digest) {
			return fmt.Errorf("%w: manifest layer digest is invalid", ErrInvalidRelease)
		}
	}
	return nil
}

func verifySBOM(mediaType string, content []byte) error {
	var document map[string]any
	if json.Unmarshal(content, &document) != nil {
		return fmt.Errorf("%w: SBOM is not valid JSON", ErrInvalidRelease)
	}
	switch mediaType {
	case cycloneDXJSONMediaType:
		if document["bomFormat"] != "CycloneDX" || strings.TrimSpace(fmt.Sprint(document["specVersion"])) == "" {
			return fmt.Errorf("%w: SBOM is not a CycloneDX document", ErrInvalidRelease)
		}
	case spdxJSONMediaType:
		version, _ := document["spdxVersion"].(string)
		id, _ := document["SPDXID"].(string)
		if !strings.HasPrefix(version, "SPDX-") || strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: SBOM is not an SPDX document", ErrInvalidRelease)
		}
	default:
		return fmt.Errorf("%w: unsupported SBOM media type", ErrInvalidRelease)
	}
	return nil
}

func verifyProvenance(result domain.HiveCIReleaseResult, content []byte) error {
	var statement struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
		Predicate struct {
			ReleaseIdentity string                        `json:"release_identity"`
			Lineage         domain.HiveCIReleaseLineage   `json:"lineage"`
			Execution       domain.HiveCIReleaseExecution `json:"execution"`
			SBOMDigest      string                        `json:"sbom_digest"`
		} `json:"predicate"`
	}
	if json.Unmarshal(content, &statement) != nil || statement.Type != inTotoStatementType ||
		statement.PredicateType != releaseProvenanceType || len(statement.Subject) != 1 ||
		statement.Subject[0].Name != result.Manifest.Repository ||
		statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(result.Manifest.Digest, "sha256:") ||
		statement.Predicate.ReleaseIdentity != result.ReleaseIdentity ||
		statement.Predicate.SBOMDigest != result.SBOM.Digest ||
		!reflect.DeepEqual(statement.Predicate.Lineage, result.Lineage) ||
		!reflect.DeepEqual(statement.Predicate.Execution, result.Execution) {
		return fmt.Errorf("%w: provenance statement does not bind the signed release", ErrInvalidRelease)
	}
	return nil
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
