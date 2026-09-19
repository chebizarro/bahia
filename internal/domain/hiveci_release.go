package domain

import "time"

const (
	// HiveCIReleaseSchemaV1 is the inner provenance document carried under
	// meta.hiveci_release of a release attestation. Its result_type value is
	// that document's own vocabulary; it is NOT a Hive-CI kind-5402 subtype.
	HiveCIReleaseSchemaV1       = "hiveci.release-provenance.v1"
	HiveCIReleaseResultType     = "RELEASE"
	HiveCIReleaseIdentityPrefix = "hiveci-release:v1:"

	// Terminal release attestation (cascadia-nips release_attestation):
	// kind 4903 with domain=release, type=attestation, schema
	// bahia.audit.release.v1, signed by a trusted attestor. hive-ci-protocol
	// defines a single kind-5402 semantic, so release evidence never rides 5402.
	ReleaseAttestationDomain       = "release"
	ReleaseAttestationAuditType    = "attestation"
	ReleaseAttestationSchema       = "bahia.audit.release.v1"
	ReleaseAttestationEnvelopeType = "release.attestation"
)

// ReleaseAttestationPayload is the canonical bahia.audit.release.v1 payload.
type ReleaseAttestationPayload struct {
	ReleaseID      string         `json:"release_id"`
	WorkflowRunID  string         `json:"workflow_run_id"`
	ResultID       string         `json:"result_id,omitempty"`
	SourceCommit   string         `json:"source_commit"`
	SourceRepo     string         `json:"source_repo,omitempty"`
	Artifact       string         `json:"artifact"`
	Digest         string         `json:"digest"`
	SBOMRef        string         `json:"sbom_ref,omitempty"`
	ProvenanceRef  string         `json:"provenance_ref,omitempty"`
	SignetEvidence map[string]any `json:"signet_evidence,omitempty"`
	AttestedAt     string         `json:"attested_at"`
}

// ReleaseAttestationMeta carries the full Hive-CI provenance document that
// Bahia verifies byte-for-byte against registry evidence.
type ReleaseAttestationMeta struct {
	HiveCIRelease HiveCIReleaseResult `json:"hiveci_release"`
}

// ReleaseAttestationEnvelope is the kind-4903 content of a release attestation.
type ReleaseAttestationEnvelope struct {
	V       int                       `json:"v"`
	Type    string                    `json:"type"`
	Payload ReleaseAttestationPayload `json:"payload"`
	Meta    ReleaseAttestationMeta    `json:"meta"`
}

type HiveCIReleaseLineage struct {
	WorkflowRunEventID  string `json:"workflow_run_event_id"`
	TriggerIdentity     string `json:"trigger_identity"`
	TriggerSource       string `json:"trigger_source"`
	TriggerID           string `json:"trigger_id"`
	PREventID           string `json:"pr_event_id"`
	ReviewEventID       string `json:"review_event_id"`
	AuditEventID        string `json:"audit_event_id"`
	RepoAddress         string `json:"repo_address"`
	SourceRepoIdentity  string `json:"source_repo_identity"`
	SourceProvenanceRef string `json:"source_provenance_ref"`
	Commit              string `json:"commit"`
	Tree                string `json:"tree"`
	WorkflowDigest      string `json:"workflow_digest"`
}

type HiveCIReleaseTestSummary struct {
	Status  string `json:"status"`
	Total   int    `json:"total"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
}

type HiveCIReleaseExecution struct {
	Complete                    bool                     `json:"complete"`
	Status                      string                   `json:"status"`
	ExitCode                    int                      `json:"exit_code"`
	DurationMS                  int64                    `json:"duration_ms"`
	BahiaDuration               string                   `json:"duration,omitempty"`
	WorkerIdentity              string                   `json:"worker_identity"`
	WorkerCapability            string                   `json:"worker_capability"`
	BuildEnvironmentImageDigest string                   `json:"build_environment_image_digest"`
	Tests                       HiveCIReleaseTestSummary `json:"tests"`
	DurableLogReference         string                   `json:"durable_log_reference"`
}

type HiveCIReleaseArtifact struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	MediaType  string `json:"media_type"`
	Size       int64  `json:"size"`
}

type HiveCISignetArtifactAttestation struct {
	Type         string                  `json:"type"`
	SignerPubkey string                  `json:"signer_pubkey"`
	Subjects     []HiveCIReleaseArtifact `json:"subjects"`
}

type HiveCIReleaseResult struct {
	SchemaVersion       string                          `json:"schema_version"`
	ResultType          string                          `json:"result_type"`
	Status              string                          `json:"status"`
	ReleaseIdentity     string                          `json:"release_identity"`
	Lineage             HiveCIReleaseLineage            `json:"lineage"`
	Execution           HiveCIReleaseExecution          `json:"execution"`
	ImageTag            string                          `json:"image_tag,omitempty"`
	Manifest            HiveCIReleaseArtifact           `json:"manifest"`
	SBOM                HiveCIReleaseArtifact           `json:"sbom"`
	Provenance          HiveCIReleaseArtifact           `json:"provenance"`
	ArtifactAttestation HiveCISignetArtifactAttestation `json:"artifact_attestation"`
}

// HiveCIAcceptedRelease is the validated durable ingest boundary. ImageTag is
// retained only as producer evidence; consumers must use Manifest.Digest.
// ResultEventID is the id of the accepted kind-4903 release attestation.
type HiveCIAcceptedRelease struct {
	Result                   HiveCIReleaseResult  `json:"result"`
	ResultEventID            string               `json:"result_event_id"`
	Attestor                 string               `json:"attestor"`
	Workflow                 string               `json:"workflow"`
	Branch                   string               `json:"branch"`
	PolicyID                 string               `json:"policy_id"`
	Policy                   HiveCIPipelinePolicy `json:"policy"`
	WorkflowRunSignedEvent   string               `json:"workflow_run_signed_event"`
	WorkerAdmissionEvidence  map[string]any       `json:"worker_admission_evidence"`
	RollbackCompatibility    map[string]any       `json:"rollback_compatibility"`
	HealthReadinessContracts map[string]any       `json:"health_readiness_contracts"`
	ContentDigest            string               `json:"content_digest"`
	SignedEvent              string               `json:"signed_event"`
	AcceptedAt               time.Time            `json:"accepted_at"`
}

type HiveCIReleaseCommitResult struct {
	Release HiveCIAcceptedRelease
	Replay  bool
}
