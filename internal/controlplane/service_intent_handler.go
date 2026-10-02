package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ServiceReader is the read contract the service intent handler uses for
// level-triggered reconciliation. Both repository.ServiceRepository and
// service.RegistryService satisfy it.
type ServiceReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error)
}

// ServiceIntentHandler processes service create/update/delete intents.
// It is the reference F2 domain handler for the Phase 3 intent framework.
//
// Level-triggered: the newest trusted intent's full desired state wins.
// An update on a cold daemon (no prior create seen) creates the service.
// Exactly one canonical 30900 per mutation, through the shared record builder
// and the existing relay-first PublishBeforeCommit semantics (the
// RegistryMutationBackend publishes before the DB write).
//
// See design §7 Wave 1 F2 and §10.
type ServiceIntentHandler struct {
	registry RegistryMutationBackend
	reader   ServiceReader
	status   *IntentStatusPublisher
	logger   *zap.Logger
}

// ServiceIntentHandlerConfig configures the service intent handler.
type ServiceIntentHandlerConfig struct {
	// Registry is the mutation backend. When relay-first is configured, this
	// is the RelayFirstRegistry which publishes the canonical 30900 before
	// writing to the database. When not, it is the plain RegistryService.
	Registry RegistryMutationBackend
	// Reader is the service repository for level-triggered entity lookups.
	Reader ServiceReader
	Status *IntentStatusPublisher
	Logger *zap.Logger
}

// NewServiceIntentHandler creates a service intent handler.
func NewServiceIntentHandler(cfg ServiceIntentHandlerConfig) *ServiceIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ServiceIntentHandler{
		registry: cfg.Registry,
		reader:   cfg.Reader,
		status:   cfg.Status,
		logger:   logger.Named("service-intent"),
	}
}

// HandleIntent processes a service intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *ServiceIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.handleDelete(ctx, intent)
	default:
		// Level-triggered: create and update both reconcile toward desired state.
		return h.handleCreateOrUpdate(ctx, intent)
	}
}

// PermissionFor returns domain.PermWriteServices for all service ops.
func (h *ServiceIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermWriteServices
}

// handleCreateOrUpdate reconciles a service toward the intent's desired state.
// Level-triggered: if the entity doesn't exist, it is created regardless of
// whether the op tag says "create" or "update" (design §1.2).
func (h *ServiceIntentHandler) handleCreateOrUpdate(ctx context.Context, intent *Intent) error {
	svc, err := serviceFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse service intent content: %w", err)
	}

	// Check expected_updated_at revision if present.
	if intent.ExpectedUpdatedAt != nil {
		expectedTime := time.Unix(0, *intent.ExpectedUpdatedAt)
		// Try parsing as RFC3339Nano from content first (higher precision).
		if raw, ok := intent.Content["expected_updated_at"]; ok {
			if v, ok := raw.(string); ok {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, v); parseErr == nil {
					expectedTime = parsed
				}
			}
		}
		return h.updateWithRevision(ctx, svc, expectedTime, intent)
	}

	// Level-triggered: try to load existing, create or update accordingly.
	existing, _ := h.reader.GetByID(ctx, svc.ID)
	if existing == nil {
		// Entity doesn't exist: create it.
		return h.createService(ctx, svc, intent)
	}

	// Entity exists: update it.
	mergeServiceOntoExisting(existing, svc)
	return h.updateService(ctx, existing, intent)
}

// handleDelete processes a delete intent.
func (h *ServiceIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		// Use coordinate as fallback.
		id, err = uuid.Parse(intent.Coordinate)
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("delete intent must carry entity id")
		}
	}

	force := false
	if f, ok := intent.Content["force"].(bool); ok {
		force = f
	}

	if err := h.registry.DeleteService(ctx, id, force); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	h.logger.Info("service deleted via intent",
		zap.String("service_id", id.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// createService creates a new service. The RegistryMutationBackend (when
// relay-first is configured) publishes the canonical 30900 before writing
// to the database, ensuring exactly one signature per mutation.
func (h *ServiceIntentHandler) createService(ctx context.Context, svc *domain.Service, intent *Intent) error {
	if err := h.registry.CreateService(ctx, svc); err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	h.logger.Info("service created via intent",
		zap.String("service_id", svc.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// updateService updates an existing service. The RegistryMutationBackend
// publishes the canonical 30900 before writing to the database.
func (h *ServiceIntentHandler) updateService(ctx context.Context, svc *domain.Service, intent *Intent) error {
	if err := h.registry.UpdateService(ctx, svc); err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	h.logger.Info("service updated via intent",
		zap.String("service_id", svc.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// updateWithRevision updates a service with expected_updated_at revision check.
func (h *ServiceIntentHandler) updateWithRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time, intent *Intent) error {
	existing, _ := h.reader.GetByID(ctx, svc.ID)
	if existing == nil {
		// No entity for a revisioned update — conflict.
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return fmt.Errorf("service %s not found for revisioned update", svc.ID)
	}

	mergeServiceOntoExisting(existing, svc)

	if err := h.registry.UpdateServiceWithExpectedRevision(ctx, existing, expectedUpdatedAt); err != nil {
		// Check if this is a revision conflict.
		if strings.Contains(err.Error(), "revision conflict") {
			if h.status != nil {
				h.status.PublishConflict(ctx, intent)
			}
		}
		return fmt.Errorf("update service with revision: %w", err)
	}

	h.logger.Info("service updated via intent (revisioned)",
		zap.String("service_id", svc.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// serviceFromIntentContent parses intent content into a domain.Service.
func serviceFromIntentContent(intent *Intent) (*domain.Service, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	svc := &domain.Service{
		OrgID: intent.OrgID,
	}

	// Parse ID from content or coordinate.
	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid service id %q: %w", idStr, err)
		}
		svc.ID = id
	}
	if svc.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			return nil, fmt.Errorf("cannot derive service id from coordinate %q: %w", intent.Coordinate, err)
		}
		svc.ID = id
	}

	// Name.
	if name, ok := content["name"].(string); ok {
		svc.Name = strings.TrimSpace(name)
	}

	// Repo URL.
	if repoURL, ok := content["repo_url"].(string); ok {
		svc.RepoURL = strings.TrimSpace(repoURL)
	}

	// Artifact repo.
	if artifactRepo, ok := content["artifact_repo"].(string); ok {
		svc.ArtifactRepo = strings.TrimSpace(artifactRepo)
	}

	// Default branch.
	if branch, ok := content["default_branch"].(string); ok {
		svc.DefaultBranch = strings.TrimSpace(branch)
	}

	// Runtime type.
	if rt, ok := content["runtime_type"].(string); ok && rt != "" {
		svc.RuntimeType = domain.RuntimeType(strings.TrimSpace(rt))
	}

	// Repository ref (structured).
	if repoData, ok := content["repository"]; ok && repoData != nil {
		repoBytes, err := json.Marshal(repoData)
		if err == nil {
			var ref domain.RepositoryRef
			if err := json.Unmarshal(repoBytes, &ref); err == nil {
				svc.Repository = &ref
			}
		}
	}

	// OrgID from content overrides tag when present.
	if orgStr, ok := content["org_id"].(string); ok && orgStr != "" {
		if orgID, err := uuid.Parse(orgStr); err == nil && orgID != uuid.Nil {
			svc.OrgID = orgID
		}
	}

	return svc, nil
}

// mergeServiceOntoExisting applies the intent's desired state fields onto the
// loaded entity. The intent carries the complete desired state (§1.2), so
// every non-zero field replaces the existing one.
func mergeServiceOntoExisting(existing, intent *domain.Service) {
	if intent.Name != "" {
		existing.Name = intent.Name
	}
	if intent.RepoURL != "" {
		existing.RepoURL = intent.RepoURL
	}
	if intent.Repository != nil {
		existing.Repository = intent.Repository
		if existing.Repository.CloneURL != "" {
			existing.RepoURL = existing.Repository.CloneURL
		}
	}
	if intent.ArtifactRepo != "" {
		existing.ArtifactRepo = intent.ArtifactRepo
	}
	if intent.DefaultBranch != "" {
		existing.DefaultBranch = intent.DefaultBranch
	}
	if intent.RuntimeType != "" {
		existing.RuntimeType = intent.RuntimeType
	}
	if intent.OrgID != uuid.Nil {
		existing.OrgID = intent.OrgID
	}
}
