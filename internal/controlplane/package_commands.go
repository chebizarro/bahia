package controlplane

import (
	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type PackageRepositoryApplyCommand struct {
	RepositoryID           uuid.UUID                      `json:"repository_id,omitempty"`
	Name                   string                         `json:"name"`
	Format                 domain.PackageRepositoryFormat `json:"format"`
	BackendRef             string                         `json:"backend_ref"`
	BackendType            domain.PackageBackendType      `json:"backend_type,omitempty"`
	ExternalRepositoryName string                         `json:"external_repository_name"`
	Description            string                         `json:"description,omitempty"`
	NamespacePrefix        string                         `json:"namespace_prefix,omitempty"`
	Policy                 domain.PackageRepositoryPolicy `json:"policy"`
	Metadata               map[string]any                 `json:"metadata,omitempty"`
}

type PackageRepositoryDeleteCommand struct {
	RepositoryID   uuid.UUID `json:"repository_id,omitempty"`
	RepositoryName string    `json:"repository_name,omitempty"`
	Force          bool      `json:"force,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}

type PackagePublishCommand struct {
	RepositoryID   uuid.UUID      `json:"repository_id,omitempty"`
	RepositoryName string         `json:"repository_name,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	PackageName    string         `json:"package_name"`
	Version        string         `json:"version"`
	Filename       string         `json:"filename"`
	SourceURL      string         `json:"source_url"`
	SHA256         string         `json:"sha256"`
	SizeBytes      int64          `json:"size_bytes"`
	ContentType    string         `json:"content_type,omitempty"`
	ApprovedBy     string         `json:"approved_by,omitempty"`
	ApprovalID     uuid.UUID      `json:"approval_id,omitempty"`
	PolicyRef      string         `json:"policy_ref,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type PackagePromotionCommand struct {
	SourceRepositoryID   uuid.UUID      `json:"source_repository_id,omitempty"`
	SourceRepositoryName string         `json:"source_repository_name,omitempty"`
	TargetRepositoryID   uuid.UUID      `json:"target_repository_id,omitempty"`
	TargetRepositoryName string         `json:"target_repository_name,omitempty"`
	Namespace            string         `json:"namespace,omitempty"`
	PackageName          string         `json:"package_name"`
	Version              string         `json:"version"`
	Filename             string         `json:"filename"`
	Environment          string         `json:"environment,omitempty"`
	Channel              string         `json:"channel,omitempty"`
	ApprovedBy           string         `json:"approved_by,omitempty"`
	ApprovalID           uuid.UUID      `json:"approval_id,omitempty"`
	PolicyRef            string         `json:"policy_ref,omitempty"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

type PackageYankCommand struct {
	RepositoryID   uuid.UUID      `json:"repository_id,omitempty"`
	RepositoryName string         `json:"repository_name,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	PackageName    string         `json:"package_name"`
	Version        string         `json:"version"`
	Filename       string         `json:"filename"`
	Reason         string         `json:"reason,omitempty"`
	Deprecated     bool           `json:"deprecated,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type PackageDriftDetectCommand struct {
	RepositoryID     uuid.UUID `json:"repository_id,omitempty"`
	RepositoryName   string    `json:"repository_name,omitempty"`
	IncludeArtifacts bool      `json:"include_artifacts,omitempty"`
}

func packageArtifactTags(operation domain.PackageOperation, repositoryID uuid.UUID, repositoryName, namespace, packageName, version, filename, sha256 string) nostr.Tags {
	tags := nostr.Tags{{"operation", string(operation)}, {"repository_name", repositoryName}, {"package", packageName}, {"version", version}, {"filename", filename}}
	if repositoryID != uuid.Nil {
		tags = append(tags, nostr.Tag{"repository", repositoryID.String()})
	}
	if namespace != "" {
		tags = append(tags, nostr.Tag{"namespace", namespace})
	}
	if sha256 != "" {
		tags = append(tags, nostr.Tag{"sha256", sha256})
	}
	return compactTags(tags)
}

func compactTags(tags nostr.Tags) nostr.Tags {
	out := make(nostr.Tags, 0, len(tags))
	for _, tag := range tags {
		if len(tag) >= 2 && tag[1] == "" {
			continue
		}
		out = append(out, tag)
	}
	return out
}
