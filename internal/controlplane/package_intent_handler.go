package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// PackageIntentHandler processes kind-30900 intents for the "package" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// Package mutations are fleet-scoped: authorization uses fleet-operator
// identity (config authorized_pubkeys) rather than per-org RBAC. The handler
// implements FleetScopedHandler.
//
// Operations: repository-apply, repository-delete, publish, promote, yank,
// drift-detect.
//
// See docs/architecture/intents-and-authority.md
type PackageIntentHandler struct {
	packageService *service.PackageRegistryService
	projection     repository.PackageControlPlaneRepository
	store          repository.PackageAuthorizationStore
	writer         PackageCPStateWriter
	statePublisher PackageIntentStatePublisher
	status         *IntentStatusPublisher
	gate           *FleetOperatorGate
	logger         *zap.Logger
}

// PackageCPStateWriter publishes canonical cp-state records for package
// mutations through the shared envelope and outbox (controlStateEnvelope,
// fingerprint dedup, monotonic created_at). Implemented by
// nostr.RelayFirstStatePublisher.
type PackageCPStateWriter interface {
	PublishPackageRepositoryRegistry(ctx context.Context, repo *domain.PackageRepository, deleted bool) error
	PublishPackageArtifactRegistry(ctx context.Context, artifact *domain.PackageArtifact, deleted bool) error
	PublishPackagePromotionRegistry(ctx context.Context, publication *domain.PackagePublication, deleted bool) error
}

// PackageIntentStatePublisher emits the fleet-private terminal read model for
// signed package intents. The handler does not persist PackageIntent rows.
type PackageIntentStatePublisher interface {
	PublishSignedPackageIntent(context.Context, *domain.PackageIntentState) error
}

// PackageIntentHandlerConfig configures the package intent handler.
type PackageIntentHandlerConfig struct {
	PackageService *service.PackageRegistryService
	Projection     repository.PackageControlPlaneRepository
	Store          repository.PackageAuthorizationStore
	Writer         PackageCPStateWriter
	StatePublisher PackageIntentStatePublisher
	Status         *IntentStatusPublisher
	Gate           *FleetOperatorGate
	Logger         *zap.Logger
}

// NewPackageIntentHandler constructs the handler.
func NewPackageIntentHandler(cfg PackageIntentHandlerConfig) *PackageIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PackageIntentHandler{
		packageService: cfg.PackageService,
		projection:     cfg.Projection,
		store:          cfg.Store,
		writer:         cfg.Writer,
		statePublisher: cfg.StatePublisher,
		status:         cfg.Status,
		gate:           cfg.Gate,
		logger:         logger.Named("package-intent"),
	}
}

// HandleIntent processes a single package intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *PackageIntentHandler) HandleIntent(ctx context.Context, intent *Intent) (err error) {
	if intent == nil {
		return fmt.Errorf("package intent is nil")
	}
	defer func() {
		if h.statePublisher == nil {
			return
		}
		status, reason := string(domain.PackageIntentStatusSucceeded), ""
		if err != nil {
			status, reason = string(domain.PackageIntentStatusFailed), err.Error()
		}
		requestEventID := ""
		if intent.Event != nil {
			requestEventID = intent.Event.ID.Hex()
		}
		state := &domain.PackageIntentState{RecordType: "signed-intent", ID: intent.IntentID, RequestEventID: requestEventID, Operation: intent.Op, RequesterPubkey: intent.Actor, Status: status, ErrorMessage: reason, UpdatedAt: time.Now().UTC()}
		err = errors.Join(err, h.statePublisher.PublishSignedPackageIntent(ctx, state))
	}()
	if intent.ExpectedUpdatedAt != nil && intent.Op != "repository-apply" && intent.Op != "repository-delete" {
		return fmt.Errorf("expected_updated_at is not supported for package %s", intent.Op)
	}
	switch intent.Op {
	case "repository-apply":
		return h.handleRepositoryApply(ctx, intent)
	case "repository-delete":
		return h.handleRepositoryDelete(ctx, intent)
	case "publish":
		return h.handlePublish(ctx, intent)
	case "promote":
		return h.handlePromote(ctx, intent)
	case "yank":
		return h.handleYank(ctx, intent)
	case "drift-detect":
		return h.handleDriftDetect(ctx, intent)
	default:
		return fmt.Errorf("unsupported package operation: %s", intent.Op)
	}
}

// PermissionFor returns domain.PermManagePackages for all package ops.
func (h *PackageIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermManagePackages
}

// IsFleetScoped implements FleetScopedHandler. Package mutations are
// fleet-scoped: authorized by config authorized_pubkeys, not per-org RBAC.
func (h *PackageIntentHandler) IsFleetScoped() bool { return true }

// handleRepositoryApply creates or updates a package repository. This gives
// repository-apply a real consumer: previously it was wired to a compatibility kind
// (KindPackageRepositoryApply) that had no production subscription.
func (h *PackageIntentHandler) handleRepositoryApply(ctx context.Context, intent *Intent) error {
	repo, err := packageRepoFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse repository-apply intent: %w", err)
	}

	var existing *domain.PackageRepository
	if h.projection != nil {
		if repo.ID != uuid.Nil {
			existing, _ = h.projection.GetRepository(ctx, repo.ID)
		}
		if existing == nil && repo.Name != "" {
			existing, _ = h.projection.GetRepositoryByName(ctx, repo.Name)
		}
	}
	if intent.ExpectedUpdatedAt != nil {
		if existing == nil || !intent.RevisionMatches(existing.UpdatedAt) {
			actual := time.Time{}
			entityID := repo.ID
			if existing != nil {
				actual, entityID = existing.UpdatedAt, existing.ID
			}
			return &revisionConflictError{entityType: "package repository", entityID: entityID, expected: *intent.ExpectedUpdatedAt, actual: actual}
		}
	}

	out, err := h.packageService.EnsureRepository(ctx, repo, existing)
	if err != nil {
		return fmt.Errorf("ensure repository: %w", err)
	}

	if err := h.publishPackageRepositoryRegistry(ctx, out); err != nil {
		h.logger.Warn("publish package repository registry failed",
			zap.String("repository_id", out.ID.String()),
			zap.Error(err),
		)
	}

	// Update the local projection so subsequent reads reflect the mutation.
	if h.projection != nil {
		if err := h.projection.UpsertRepository(ctx, out); err != nil {
			h.logger.Warn("upsert repository projection failed",
				zap.String("repository_id", out.ID.String()),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("package repository applied via intent",
		zap.String("repository_id", out.ID.String()),
		zap.String("repository_name", out.Name),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handleRepositoryDelete deletes a package repository.
func (h *PackageIntentHandler) handleRepositoryDelete(ctx context.Context, intent *Intent) error {
	repoID, repoName := packageRepoIdentFromContent(intent.Content)
	force, _ := intent.Content["force"].(bool)

	repo, err := h.lookupRepository(ctx, repoID, repoName)
	if err != nil {
		return fmt.Errorf("lookup repository for delete: %w", err)
	}
	if intent.ExpectedUpdatedAt != nil && !intent.RevisionMatches(repo.UpdatedAt) {
		return &revisionConflictError{entityType: "package repository", entityID: repo.ID, expected: *intent.ExpectedUpdatedAt, actual: repo.UpdatedAt}
	}

	out, err := h.packageService.DeleteRepository(ctx, repo, force)
	if err != nil {
		return fmt.Errorf("delete repository: %w", err)
	}

	if err := h.publishPackageRepositoryRegistry(ctx, out); err != nil {
		h.logger.Warn("publish package repository registry after delete failed",
			zap.String("repository_id", out.ID.String()),
			zap.Error(err),
		)
	}

	if h.projection != nil {
		if err := h.projection.UpsertRepository(ctx, out); err != nil {
			h.logger.Warn("upsert repository projection after delete failed",
				zap.String("repository_id", out.ID.String()),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("package repository deleted via intent",
		zap.String("repository_id", out.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handlePublish publishes a package artifact.
func (h *PackageIntentHandler) handlePublish(ctx context.Context, intent *Intent) error {
	cmd, err := packagePublishCmdFromContent(intent.Content)
	if err != nil {
		return fmt.Errorf("parse publish intent: %w", err)
	}

	repo, err := h.lookupRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		return fmt.Errorf("lookup repository for publish: %w", err)
	}

	// Check approval if required.
	approvedBy, err := h.checkApproval(ctx, intent, repo.Policy.PublishRequiresApproval, cmd.ApprovalID)
	if err != nil {
		return err
	}

	var existing *domain.PackageArtifact
	if h.projection != nil {
		existing, _ = h.projection.GetArtifact(ctx, repo.ID,
			strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	}

	artifact, err := h.packageService.PublishPackage(ctx, repo, existing, service.PackagePublishRequest{
		Namespace:   cmd.Namespace,
		PackageName: cmd.PackageName,
		Version:     cmd.Version,
		Filename:    cmd.Filename,
		SourceURL:   cmd.SourceURL,
		SHA256:      cmd.SHA256,
		SizeBytes:   cmd.SizeBytes,
		ContentType: cmd.ContentType,
		Metadata:    cmd.Metadata,
	})
	if err != nil {
		return fmt.Errorf("publish package: %w", err)
	}

	if err := h.publishPackageArtifactRegistry(ctx, artifact); err != nil {
		return fmt.Errorf("publish artifact registry: %w", err)
	}

	if h.projection != nil {
		if err := h.projection.UpsertArtifact(ctx, artifact); err != nil {
			h.logger.Warn("upsert artifact projection failed", zap.Error(err))
		}
	}

	now := time.Now().UTC()
	publication := &domain.PackagePublication{
		ID:             domain.NewEntityID(),
		RepositoryID:   repo.ID,
		ArtifactID:     artifact.ID,
		Status:         domain.PackagePublicationStatusSucceeded,
		PolicyDecision: domain.PackagePolicyDecisionAllowed,
		PolicyRef:      cmd.PolicyRef,
		ApprovedBy:     approvedBy,
		PublishedAt:    &now,
		Metadata:       map[string]any{"operation": "artifact_publish"},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.publishPackagePromotionRegistry(ctx, publication); err != nil {
		return fmt.Errorf("publish publication registry: %w", err)
	}

	if h.projection != nil {
		if err := h.projection.UpsertPublication(ctx, publication); err != nil {
			h.logger.Warn("upsert publication projection failed", zap.Error(err))
		}
	}

	h.logger.Info("package published via intent",
		zap.String("artifact_id", artifact.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handlePromote promotes a package artifact to a target repository.
func (h *PackageIntentHandler) handlePromote(ctx context.Context, intent *Intent) error {
	cmd, err := packagePromoteCmdFromContent(intent.Content)
	if err != nil {
		return fmt.Errorf("parse promote intent: %w", err)
	}

	sourceRepo, err := h.lookupRepository(ctx, cmd.SourceRepositoryID, cmd.SourceRepositoryName)
	if err != nil {
		return fmt.Errorf("lookup source repository: %w", err)
	}

	targetRepo, err := h.lookupRepository(ctx, cmd.TargetRepositoryID, cmd.TargetRepositoryName)
	if err != nil {
		return fmt.Errorf("lookup target repository: %w", err)
	}

	// Check approval if required.
	approvedBy, err := h.checkApproval(ctx, intent, targetRepo.Policy.PromotionRequiresApproval, cmd.ApprovalID)
	if err != nil {
		return err
	}

	var artifact *domain.PackageArtifact
	if h.projection != nil {
		artifact, _ = h.projection.GetArtifact(ctx, sourceRepo.ID,
			strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	}
	if artifact == nil {
		return fmt.Errorf("source package artifact not found")
	}

	var existingTarget *domain.PackageArtifact
	if h.projection != nil {
		existingTarget, _ = h.projection.GetArtifact(ctx, targetRepo.ID,
			artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
	}

	target, publication, err := h.packageService.PromotePackage(ctx, sourceRepo, targetRepo, artifact, existingTarget, service.PackagePromotionRequest{
		Environment: cmd.Environment,
		Channel:     cmd.Channel,
		ApprovedBy:  approvedBy,
		PolicyRef:   cmd.PolicyRef,
		Metadata:    cmd.Metadata,
	})
	if err != nil {
		return fmt.Errorf("promote package: %w", err)
	}

	publication.Status = domain.PackagePublicationStatusSucceeded
	if err := h.publishPackageArtifactRegistry(ctx, target); err != nil {
		return fmt.Errorf("publish promoted artifact registry: %w", err)
	}
	if err := h.publishPackagePromotionRegistry(ctx, publication); err != nil {
		return fmt.Errorf("publish promotion registry: %w", err)
	}

	if h.projection != nil {
		if err := h.projection.UpsertArtifact(ctx, target); err != nil {
			h.logger.Warn("upsert promoted artifact projection failed", zap.Error(err))
		}
		if err := h.projection.UpsertPublication(ctx, publication); err != nil {
			h.logger.Warn("upsert promotion projection failed", zap.Error(err))
		}
	}

	h.logger.Info("package promoted via intent",
		zap.String("artifact_id", target.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handleYank yanks or deprecates a package artifact.
func (h *PackageIntentHandler) handleYank(ctx context.Context, intent *Intent) error {
	cmd, err := packageYankCmdFromContent(intent.Content)
	if err != nil {
		return fmt.Errorf("parse yank intent: %w", err)
	}

	repo, err := h.lookupRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		return fmt.Errorf("lookup repository for yank: %w", err)
	}

	var existing *domain.PackageArtifact
	if h.projection != nil {
		existing, _ = h.projection.GetArtifact(ctx, repo.ID,
			strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	}

	var artifact *domain.PackageArtifact
	if cmd.Deprecated {
		if existing == nil || existing.Deleted || existing.Status != domain.PackageArtifactStatusAvailable {
			return fmt.Errorf("only an available package artifact can be deprecated")
		}
		copy := *existing
		copy.Metadata = make(map[string]any, len(existing.Metadata)+2)
		for key, value := range existing.Metadata {
			copy.Metadata[key] = value
		}
		copy.Metadata["deprecated"], copy.Metadata["deprecation_reason"] = true, cmd.Reason
		copy.UpdatedAt = time.Now().UTC()
		artifact = &copy
	} else {
		artifact, err = h.packageService.YankPackage(ctx, repo, existing, service.PackageYankRequest{
			Namespace:   cmd.Namespace,
			PackageName: cmd.PackageName,
			Version:     cmd.Version,
			Filename:    cmd.Filename,
			Reason:      cmd.Reason,
			Metadata:    cmd.Metadata,
		})
		if err != nil {
			return fmt.Errorf("yank package: %w", err)
		}
	}

	if err := h.publishPackageArtifactRegistry(ctx, artifact); err != nil {
		return fmt.Errorf("publish package availability: %w", err)
	}

	if h.projection != nil {
		if err := h.projection.UpsertArtifact(ctx, artifact); err != nil {
			h.logger.Warn("upsert yanked artifact projection failed", zap.Error(err))
		}
	}

	h.logger.Info("package yanked via intent",
		zap.String("artifact_id", artifact.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// handleDriftDetect observes package backend drift.
func (h *PackageIntentHandler) handleDriftDetect(ctx context.Context, intent *Intent) error {
	repoID, repoName := packageRepoIdentFromContent(intent.Content)
	repo, err := h.lookupRepository(ctx, repoID, repoName)
	if err != nil {
		return fmt.Errorf("lookup repository for drift-detect: %w", err)
	}

	obs, err := h.packageService.ObserveRepositoryDrift(ctx, repo)
	if err != nil {
		return fmt.Errorf("observe repository drift: %w", err)
	}

	h.logger.Info("package drift observed via intent",
		zap.String("repository_id", repo.ID.String()),
		zap.Bool("drifted", obs.Drifted),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// lookupRepository looks up a repository by ID and/or name from the projection.
func (h *PackageIntentHandler) lookupRepository(ctx context.Context, id uuid.UUID, name string) (*domain.PackageRepository, error) {
	if h.projection == nil {
		return nil, fmt.Errorf("package projection not configured")
	}
	if id != uuid.Nil {
		repo, err := h.projection.GetRepository(ctx, id)
		if err == nil && repo != nil {
			return repo, nil
		}
	}
	if strings.TrimSpace(name) != "" {
		repo, err := h.projection.GetRepositoryByName(ctx, strings.TrimSpace(name))
		if err == nil && repo != nil {
			return repo, nil
		}
	}
	return nil, fmt.Errorf("package repository not found: %w", repository.ErrNotFound)
}

// checkApproval verifies approval when required. Returns the approver pubkey
// on success, or an error with a bounded rejection for unauthorized principals.
func (h *PackageIntentHandler) checkApproval(ctx context.Context, intent *Intent, approvalRequired bool, approvalID uuid.UUID) (string, error) {
	if !approvalRequired && approvalID == uuid.Nil {
		return "", nil
	}
	if !approvalRequired {
		// Approval provided but not required — allow with empty approver.
		return "", nil
	}
	if approvalID == uuid.Nil {
		return "", fmt.Errorf("approval required but no approval_id provided: %w", service.ErrPackageApprovalRequired)
	}
	if h.store == nil {
		return "", fmt.Errorf("package authorization store not configured: %w", service.ErrPackageApprovalRequired)
	}

	var allowedPubkeys []string
	if h.gate != nil {
		allowedPubkeys = h.gate.authorizedPubkeys
	}

	// Build the plan hash for verification.
	approvedBy, err := h.store.ConsumePackageApproval(ctx, approvalID, intent.Actor, "", "", allowedPubkeys)
	if err != nil {
		return "", fmt.Errorf("approval verification failed: %w", service.ErrPackageApprovalRequired)
	}
	return approvedBy, nil
}

// publishPackageRepositoryRegistry publishes a canonical cp-state record for a repository.
func (h *PackageIntentHandler) publishPackageRepositoryRegistry(ctx context.Context, repo *domain.PackageRepository) error {
	if h.writer == nil {
		return nil
	}
	return h.writer.PublishPackageRepositoryRegistry(ctx, repo, repo.Deleted)
}

// publishPackageArtifactRegistry publishes a canonical cp-state record for an artifact.
func (h *PackageIntentHandler) publishPackageArtifactRegistry(ctx context.Context, artifact *domain.PackageArtifact) error {
	if h.writer == nil {
		return nil
	}
	return h.writer.PublishPackageArtifactRegistry(ctx, artifact, artifact.Deleted)
}

// publishPackagePromotionRegistry publishes a canonical cp-state record for a publication.
func (h *PackageIntentHandler) publishPackagePromotionRegistry(ctx context.Context, publication *domain.PackagePublication) error {
	if h.writer == nil {
		return nil
	}
	return h.writer.PublishPackagePromotionRegistry(ctx, publication, false)
}

// packageRepoFromIntentContent parses a package repository from intent content.
func packageRepoFromIntentContent(intent *Intent) (*domain.PackageRepository, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	repo := &domain.PackageRepository{}

	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid repository id %q: %w", idStr, err)
		}
		repo.ID = id
	}
	if repo.ID == uuid.Nil {
		if id, err := uuid.Parse(intent.Coordinate); err == nil && id != uuid.Nil {
			repo.ID = id
		}
	}

	if name, ok := content["name"].(string); ok {
		repo.Name = strings.TrimSpace(name)
	}
	if format, ok := content["format"].(string); ok {
		repo.Format = domain.PackageRepositoryFormat(strings.TrimSpace(format))
	}
	if backendRef, ok := content["backend_ref"].(string); ok {
		repo.BackendRef = strings.TrimSpace(backendRef)
	}
	if backendType, ok := content["backend_type"].(string); ok {
		repo.BackendType = domain.PackageBackendType(strings.TrimSpace(backendType))
	}
	if externalName, ok := content["external_repository_name"].(string); ok {
		repo.ExternalRepositoryName = strings.TrimSpace(externalName)
	}
	if desc, ok := content["description"].(string); ok {
		repo.Description = strings.TrimSpace(desc)
	}
	if prefix, ok := content["namespace_prefix"].(string); ok {
		repo.NamespacePrefix = strings.TrimSpace(prefix)
	}

	// Parse policy from JSON.
	if policyRaw, ok := content["policy"]; ok && policyRaw != nil {
		policyJSON, err := json.Marshal(policyRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal policy: %w", err)
		}
		if err := json.Unmarshal(policyJSON, &repo.Policy); err != nil {
			return nil, fmt.Errorf("parse policy: %w", err)
		}
	}

	// Parse metadata.
	if metaRaw, ok := content["metadata"]; ok && metaRaw != nil {
		if m, mapOK := metaRaw.(map[string]any); mapOK {
			repo.Metadata = m
		}
	}

	return repo, nil
}

// packageRepoIdentFromContent extracts repository ID and name from intent content.
func packageRepoIdentFromContent(content map[string]interface{}) (uuid.UUID, string) {
	var id uuid.UUID
	if idStr, ok := content["repository_id"].(string); ok {
		id, _ = uuid.Parse(idStr)
	}
	if id == uuid.Nil {
		if idStr, ok := content["id"].(string); ok {
			id, _ = uuid.Parse(idStr)
		}
	}
	name, _ := content["repository_name"].(string)
	if name == "" {
		name, _ = content["name"].(string)
	}
	return id, name
}

// packagePublishCmdFromContent parses a publish command from intent content.
func packagePublishCmdFromContent(content map[string]interface{}) (PackagePublishCommand, error) {
	cmd := PackagePublishCommand{}
	if content == nil {
		return cmd, fmt.Errorf("content is nil")
	}
	if v, ok := content["repository_id"].(string); ok {
		cmd.RepositoryID, _ = uuid.Parse(v)
	}
	cmd.RepositoryName, _ = content["repository_name"].(string)
	cmd.Namespace, _ = content["namespace"].(string)
	cmd.PackageName, _ = content["package_name"].(string)
	cmd.Version, _ = content["version"].(string)
	cmd.Filename, _ = content["filename"].(string)
	cmd.SourceURL, _ = content["source_url"].(string)
	cmd.SHA256, _ = content["sha256"].(string)
	if v, ok := content["size_bytes"].(float64); ok {
		cmd.SizeBytes = int64(v)
	}
	if v, ok := content["size_bytes"].(json.Number); ok {
		cmd.SizeBytes, _ = v.Int64()
	}
	cmd.ContentType, _ = content["content_type"].(string)
	cmd.PolicyRef, _ = content["policy_ref"].(string)
	if v, ok := content["approval_id"].(string); ok {
		cmd.ApprovalID, _ = uuid.Parse(v)
	}
	if v, ok := content["metadata"]; ok {
		if m, mapOK := v.(map[string]any); mapOK {
			cmd.Metadata = m
		}
	}
	return cmd, nil
}

// packagePromoteCmdFromContent parses a promote command from intent content.
func packagePromoteCmdFromContent(content map[string]interface{}) (PackagePromotionCommand, error) {
	cmd := PackagePromotionCommand{}
	if content == nil {
		return cmd, fmt.Errorf("content is nil")
	}
	if v, ok := content["source_repository_id"].(string); ok {
		cmd.SourceRepositoryID, _ = uuid.Parse(v)
	}
	cmd.SourceRepositoryName, _ = content["source_repository_name"].(string)
	if v, ok := content["target_repository_id"].(string); ok {
		cmd.TargetRepositoryID, _ = uuid.Parse(v)
	}
	cmd.TargetRepositoryName, _ = content["target_repository_name"].(string)
	cmd.Namespace, _ = content["namespace"].(string)
	cmd.PackageName, _ = content["package_name"].(string)
	cmd.Version, _ = content["version"].(string)
	cmd.Filename, _ = content["filename"].(string)
	cmd.Environment, _ = content["environment"].(string)
	cmd.Channel, _ = content["channel"].(string)
	cmd.PolicyRef, _ = content["policy_ref"].(string)
	if v, ok := content["approval_id"].(string); ok {
		cmd.ApprovalID, _ = uuid.Parse(v)
	}
	if v, ok := content["metadata"]; ok {
		if m, mapOK := v.(map[string]any); mapOK {
			cmd.Metadata = m
		}
	}
	return cmd, nil
}

// packageYankCmdFromContent parses a yank command from intent content.
func packageYankCmdFromContent(content map[string]interface{}) (PackageYankCommand, error) {
	cmd := PackageYankCommand{}
	if content == nil {
		return cmd, fmt.Errorf("content is nil")
	}
	if v, ok := content["repository_id"].(string); ok {
		cmd.RepositoryID, _ = uuid.Parse(v)
	}
	cmd.RepositoryName, _ = content["repository_name"].(string)
	cmd.Namespace, _ = content["namespace"].(string)
	cmd.PackageName, _ = content["package_name"].(string)
	cmd.Version, _ = content["version"].(string)
	cmd.Filename, _ = content["filename"].(string)
	cmd.Reason, _ = content["reason"].(string)
	if v, ok := content["deprecated"].(bool); ok {
		cmd.Deprecated = v
	}
	if v, ok := content["metadata"]; ok {
		if m, mapOK := v.(map[string]any); mapOK {
			cmd.Metadata = m
		}
	}
	return cmd, nil
}
