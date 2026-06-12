package controlplane

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PackageCommandPublisher emits canonical package control-plane request events.
type PackageCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewPackageCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *PackageCommandPublisher {
	return &PackageCommandPublisher{publisher: publisher, signer: signer}
}

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

type PackageCommandReceipt struct {
	RequestEventID         string `json:"request_event_id"`
	RequestPubkey          string `json:"request_pubkey"`
	RequestKind            int    `json:"request_kind"`
	StatusKind             int    `json:"status_kind,omitempty"`
	ResultKind             int    `json:"result_kind"`
	RepositoryRegistryKind int    `json:"repository_registry_kind"`
	ArtifactRegistryKind   int    `json:"artifact_registry_kind"`
	PromotionRegistryKind  int    `json:"promotion_registry_kind"`
	DriftEventKind         int    `json:"drift_event_kind,omitempty"`
	PublishedRelays        int    `json:"published_relays"`
	RepositoryID           string `json:"repository_id,omitempty"`
	RepositoryName         string `json:"repository_name,omitempty"`
	PackageName            string `json:"package_name,omitempty"`
	Version                string `json:"version,omitempty"`
	Filename               string `json:"filename,omitempty"`
	ExpectedAuthor         string `json:"expected_author,omitempty"` // when set, reject results not signed by this pubkey
}

func (p *PackageCommandPublisher) PublishPackageRepositoryApplyRequest(ctx context.Context, cmd PackageRepositoryApplyCommand) (*PackageCommandReceipt, error) {
	content := map[string]any{
		"name":                     cmd.Name,
		"format":                   cmd.Format,
		"backend_ref":              cmd.BackendRef,
		"backend_type":             cmd.BackendType,
		"external_repository_name": cmd.ExternalRepositoryName,
		"description":              cmd.Description,
		"namespace_prefix":         cmd.NamespacePrefix,
		"policy":                   cmd.Policy,
		"metadata":                 cmd.Metadata,
	}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	tags := nostr.Tags{{"operation", string(domain.PackageOperationRepositoryApply)}, {"repository_name", cmd.Name}, {"name", cmd.Name}, {"format", string(cmd.Format)}, {"backend_ref", cmd.BackendRef}}
	if cmd.RepositoryID != uuid.Nil {
		tags = append(tags, nostr.Tag{"repository", cmd.RepositoryID.String()})
	}
	return p.publish(ctx, "package/repository-apply", tags, content)
}

func (p *PackageCommandPublisher) PublishPackageRepositoryDeleteRequest(ctx context.Context, cmd PackageRepositoryDeleteCommand) (*PackageCommandReceipt, error) {
	content := map[string]any{"repository_name": cmd.RepositoryName, "force": cmd.Force, "reason": cmd.Reason}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	tags := nostr.Tags{{"operation", string(domain.PackageOperationRepositoryDelete)}, {"repository_name", cmd.RepositoryName}}
	if cmd.RepositoryID != uuid.Nil {
		tags = append(tags, nostr.Tag{"repository", cmd.RepositoryID.String()})
	}
	return p.publish(ctx, "package/repository-delete", tags, content)
}

func (p *PackageCommandPublisher) PublishPackagePublishRequest(ctx context.Context, cmd PackagePublishCommand) (*PackageCommandReceipt, error) {
	content := map[string]any{"repository_name": cmd.RepositoryName, "namespace": cmd.Namespace, "package_name": cmd.PackageName, "version": cmd.Version, "filename": cmd.Filename, "source_url": cmd.SourceURL, "sha256": cmd.SHA256, "size_bytes": cmd.SizeBytes, "content_type": cmd.ContentType, "approved_by": cmd.ApprovedBy, "policy_ref": cmd.PolicyRef, "metadata": cmd.Metadata}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	tags := packageArtifactTags(domain.PackageOperationArtifactPublish, cmd.RepositoryID, cmd.RepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename, cmd.SHA256)
	return p.publish(ctx, "package/publish", tags, content)
}

func (p *PackageCommandPublisher) PublishPackagePromotionRequest(ctx context.Context, cmd PackagePromotionCommand) (*PackageCommandReceipt, error) {
	content := map[string]any{"source_repository_name": cmd.SourceRepositoryName, "target_repository_name": cmd.TargetRepositoryName, "namespace": cmd.Namespace, "package_name": cmd.PackageName, "version": cmd.Version, "filename": cmd.Filename, "environment": cmd.Environment, "channel": cmd.Channel, "approved_by": cmd.ApprovedBy, "policy_ref": cmd.PolicyRef, "metadata": cmd.Metadata}
	if cmd.SourceRepositoryID != uuid.Nil {
		content["source_repository_id"] = cmd.SourceRepositoryID.String()
	}
	if cmd.TargetRepositoryID != uuid.Nil {
		content["target_repository_id"] = cmd.TargetRepositoryID.String()
	}
	tags := packageArtifactTags(domain.PackageOperationPromote, cmd.SourceRepositoryID, cmd.SourceRepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename, "")
	if cmd.TargetRepositoryID != uuid.Nil {
		tags = append(tags, nostr.Tag{"target_repository", cmd.TargetRepositoryID.String()})
	}
	if cmd.TargetRepositoryName != "" {
		tags = append(tags, nostr.Tag{"target_repository_name", cmd.TargetRepositoryName})
	}
	return p.publish(ctx, ContextVMMethodPackagePromote, tags, content)
}

func (p *PackageCommandPublisher) PublishPackageYankRequest(ctx context.Context, cmd PackageYankCommand) (*PackageCommandReceipt, error) {
	operation := domain.PackageOperationYank
	if cmd.Deprecated {
		operation = domain.PackageOperationDeprecate
	}
	content := map[string]any{"repository_name": cmd.RepositoryName, "namespace": cmd.Namespace, "package_name": cmd.PackageName, "version": cmd.Version, "filename": cmd.Filename, "reason": cmd.Reason, "deprecated": cmd.Deprecated, "metadata": cmd.Metadata}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	return p.publish(ctx, "package/yank", packageArtifactTags(operation, cmd.RepositoryID, cmd.RepositoryName, cmd.Namespace, cmd.PackageName, cmd.Version, cmd.Filename, ""), content)
}

func (p *PackageCommandPublisher) PublishPackageDriftDetectRequest(ctx context.Context, cmd PackageDriftDetectCommand) (*PackageCommandReceipt, error) {
	content := map[string]any{"repository_name": cmd.RepositoryName, "include_artifacts": cmd.IncludeArtifacts}
	if cmd.RepositoryID != uuid.Nil {
		content["repository_id"] = cmd.RepositoryID.String()
	}
	tags := nostr.Tags{{"operation", string(domain.PackageOperationDriftDetect)}, {"repository_name", cmd.RepositoryName}}
	if cmd.RepositoryID != uuid.Nil {
		tags = append(tags, nostr.Tag{"repository", cmd.RepositoryID.String()})
	}
	return p.publish(ctx, "package/drift-detect", tags, content)
}

func (p *PackageCommandPublisher) publish(ctx context.Context, method string, tags nostr.Tags, content map[string]any) (*PackageCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("package command publisher is not configured")
	}
	dTag := tagValueNostr(tags, "d")
	if dTag == "" {
		dTag = "package-command:" + method + ":" + uuid.NewString()
	}
	ev, published, _, err := publishContextVMCommand(ctx, p.publisher, p.signer, method, dTag, "", tags, content, "package command")
	if err != nil {
		return nil, err
	}
	receipt := &PackageCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: KindContextVMMessage, ResultKind: KindCASControlState, RepositoryRegistryKind: KindCASControlState, ArtifactRegistryKind: KindCASControlState, PromotionRegistryKind: KindCASControlState, DriftEventKind: KindNIP38Status, PublishedRelays: published}
	receipt.RepositoryID = tagValueNostr(ev.Tags, "repository")
	receipt.RepositoryName = tagValueNostr(ev.Tags, "repository_name")
	receipt.PackageName = tagValueNostr(ev.Tags, "package")
	receipt.Version = tagValueNostr(ev.Tags, "version")
	receipt.Filename = tagValueNostr(ev.Tags, "filename")
	return receipt, nil
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
