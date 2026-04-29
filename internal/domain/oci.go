// Package domain defines OCI registry domain models.
package domain

import "time"

// OCIBlobUploadState represents the lifecycle state for a blob upload session.
type OCIBlobUploadState string

const (
	OCIBlobUploadStatePending    OCIBlobUploadState = "pending"
	OCIBlobUploadStateUploading  OCIBlobUploadState = "uploading"
	OCIBlobUploadStateFinalizing OCIBlobUploadState = "finalizing"
	OCIBlobUploadStateCompleted  OCIBlobUploadState = "completed"
	OCIBlobUploadStateFailed     OCIBlobUploadState = "failed"
	OCIBlobUploadStateExpired    OCIBlobUploadState = "expired"
)

// OCIRepository stores repository namespace metadata.
type OCIRepository struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OCIManifest stores a content-addressed OCI manifest.
type OCIManifest struct {
	ID             string            `json:"id"`
	RepositoryID   string            `json:"repository_id"`
	Digest         string            `json:"digest"`
	MediaType      string            `json:"media_type"`
	ArtifactType   string            `json:"artifact_type,omitempty"`
	SubjectDigest  string            `json:"subject_digest,omitempty"`
	Content        []byte            `json:"content"`
	SizeBytes      int64             `json:"size_bytes"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// OCIBlob stores blob metadata.
type OCIBlob struct {
	ID         string    `json:"id"`
	Digest     string    `json:"digest"`
	MediaType  string    `json:"media_type,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	StorageRef string    `json:"storage_ref,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// OCIBlobUpload stores OCI blob upload session state.
type OCIBlobUpload struct {
	UploadID     string             `json:"upload_id"`
	RepositoryID string             `json:"repository_id"`
	SpoolPath    string             `json:"spool_path"`
	State        OCIBlobUploadState `json:"state"`
	OffsetBytes  int64              `json:"offset_bytes"`
	Digest       string             `json:"digest,omitempty"`
	StartedAt    time.Time          `json:"started_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	ExpiresAt    time.Time          `json:"expires_at"`
}

// OCIReferrerDescriptor represents one descriptor in the OCI referrers API.
type OCIReferrerDescriptor struct {
	Digest       string            `json:"digest"`
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// RegistryPrincipal is the authenticated principal for registry operations.
type RegistryPrincipal struct {
	Subject        string   `json:"subject"`
	AuthType       string   `json:"auth_type"`
	Pubkey         string   `json:"pubkey,omitempty"`
	ServiceAccount string   `json:"service_account,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
}
