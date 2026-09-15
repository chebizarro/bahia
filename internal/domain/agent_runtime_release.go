package domain

import (
	"time"

	"github.com/google/uuid"
)

// AgentRuntimeSource identifies runtime code independently from a soul's
// workspace/persona repository.
type AgentRuntimeSource struct {
	ID             uuid.UUID `json:"id"`
	OrgID          uuid.UUID `json:"org_id"`
	Repository     string    `json:"repository"`
	Branch         string    `json:"branch"`
	ReleaseChannel string    `json:"release_channel"`
	CreatedAt      time.Time `json:"created_at"`
}

// RuntimeReleaseProvenance is the immutable verification evidence for one
// runtime digest. It describes real producer evidence and is never synthesized
// from an agent binding.
type RuntimeReleaseProvenance struct {
	Provider           string `json:"provider"`
	ReleaseEventID     string `json:"release_event_id"`
	WorkflowRunEventID string `json:"workflow_run_event_id"`
	ManifestDigest     string `json:"manifest_digest"`
	SBOMDigest         string `json:"sbom_digest"`
	ProvenanceDigest   string `json:"provenance_digest"`
	AttestorPubkey     string `json:"attestor_pubkey"`
}

// AgentRuntimeRelease is a tenant-owned immutable verified runtime artifact.
// A single release can be bound to any number of agent services.
type AgentRuntimeRelease struct {
	ID          uuid.UUID                `json:"id"`
	OrgID       uuid.UUID                `json:"org_id"`
	SourceID    uuid.UUID                `json:"source_id"`
	ImageRepo   string                   `json:"image_repo"`
	ImageDigest string                   `json:"image_digest"`
	Provenance  RuntimeReleaseProvenance `json:"provenance"`
	VerifiedAt  time.Time                `json:"verified_at"`
	CreatedAt   time.Time                `json:"created_at"`
}

// AgentServiceReleaseBinding is an append-only promotion record. The previous
// binding provides an exact rollback lookup without mutating release history.
type AgentServiceReleaseBinding struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	AgentID           string     `json:"agent_id"`
	ServiceID         uuid.UUID  `json:"service_id"`
	ReleaseID         uuid.UUID  `json:"release_id"`
	ReleaseChannel    string     `json:"release_channel"`
	SourceEventID     string     `json:"source_event_id"`
	PreviousBindingID *uuid.UUID `json:"previous_binding_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

// AgentServiceRuntimeRelease joins an append-only agent binding to its shared release.
type AgentServiceRuntimeRelease struct {
	Binding AgentServiceReleaseBinding `json:"binding"`
	Release AgentRuntimeRelease        `json:"release"`
	Source  AgentRuntimeSource         `json:"source"`
}

// RuntimeReleaseDeploymentIntent is the durable deployment identity derived
// from a shared verified runtime release. Release is the canonical source of
// image digest and provenance; no service-scoped Artifact is synthesized.
type RuntimeReleaseDeploymentIntent struct {
	Intent  DeploymentIntent           `json:"intent"`
	Binding AgentServiceReleaseBinding `json:"binding"`
	Release AgentRuntimeRelease        `json:"release"`
}
