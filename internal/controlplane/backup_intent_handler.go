package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// BackupIntentPublisher publishes canonical 30900 cp-state records for backup
// entities. The intent handler uses this interface for all publish operations.
// The nostr.BackupCanonicalPublisher implements it.
type BackupIntentPublisher interface {
	PublishRecipe(ctx context.Context, recipe *domain.BackupRecipe) error
	PublishPolicy(ctx context.Context, policy *domain.BackupPolicy) error
	PublishRepository(ctx context.Context, repo *domain.BackupRepository) error
	PublishDefinition(ctx context.Context, def *domain.BackupDefinition) error
	PublishDeleted(ctx context.Context, legacyKind int, dTag string, tags gonostr.Tags, content string, entityType string, entityID *uuid.UUID) error
}

// BackupIntentCRUD is the read/write contract the backup intent handler uses
// for level-triggered reconciliation. service.BackupRegistryService satisfies
// the config entity methods; the definition registry extends it.
type BackupIntentCRUD interface {
	// Config entities (operator-authored):
	CreateOrUpdateRecipe(ctx context.Context, recipe *domain.BackupRecipe) error
	GetRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error)
	CreateOrUpdatePolicy(ctx context.Context, policy *domain.BackupPolicy) error
	GetPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error)
	CreateOrUpdateRepository(ctx context.Context, repo *domain.BackupRepository) error
	GetRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error)
	GetRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error)
	GetPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error)

	// Run lifecycle (daemon-authored, intent handler creates the record):
	CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error)
	GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error)

	// Restore (daemon-authored):
	CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error)
	GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error)

	// Verification:
	RecordBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error

	// Retention:
	CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error)
}

// BackupIntentDefinitionCRUD extends BackupIntentCRUD with definition
// upsert, needed only for the definition-apply operation.
type BackupIntentDefinitionCRUD interface {
	UpsertBackupDefinition(ctx context.Context, definition *domain.BackupDefinition) error
	GetBackupDefinitionByName(ctx context.Context, name string) (*domain.BackupDefinition, error)
}

// BackupIntentExecutors groups the execution triggers the intent handler
// fires after creating a run/restore/retention/verification record, replacing
// the reactor's go-routine launches.
type BackupIntentExecutors struct {
	RunExecutor          BackupRunControlPlaneExecutor
	RestoreExecutor      BackupRestoreControlPlaneExecutor
	RetentionExecutor    BackupRetentionControlPlaneExecutor
	VerificationExecutor BackupVerificationControlPlaneExecutor
	ProbeExecutor        BackupRepositoryProbeControlPlaneExecutor
}

// BackupIntentHandler processes kind-30900 intents for the "backup" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// Backup intents are fleet-scoped: the handler implements FleetScopedHandler
// so the intent processor authorizes via FleetOperatorGate rather than
// per-org membership.
//
// Operations (intent op tag):
//   - recipe-apply: create/update a backup recipe
//   - policy-apply: create/update a backup policy
//   - repository-register: create/update a backup repository
//   - definition-apply: create/update a backup definition
//   - run: request a backup run
//   - restore: request a backup restore
//   - restore-approval: approve/reject a pending restore
//   - verification: request a backup verification
//   - retention: request a retention run
//   - repository-probe: probe a backup repository
//   - delete: delete an entity (recipe, policy, repository, or definition)
//
// See docs/architecture/intents-and-authority.md.
type BackupIntentHandler struct {
	registry    BackupIntentCRUD
	definitions BackupIntentDefinitionCRUD
	publisher   BackupIntentPublisher
	runReceipts BackupRunReceiptReader
	executors   BackupIntentExecutors
	status      *IntentStatusPublisher
	logger      *zap.Logger
}

// BackupIntentHandlerConfig configures the backup intent handler.
type BackupIntentHandlerConfig struct {
	Registry    BackupIntentCRUD
	Definitions BackupIntentDefinitionCRUD
	Publisher   BackupIntentPublisher
	RunReceipts BackupRunReceiptReader
	Executors   BackupIntentExecutors
	Status      *IntentStatusPublisher
	Logger      *zap.Logger
}

// NewBackupIntentHandler constructs the handler.
func NewBackupIntentHandler(cfg BackupIntentHandlerConfig) *BackupIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupIntentHandler{
		registry:    cfg.Registry,
		definitions: cfg.Definitions,
		publisher:   cfg.Publisher,
		runReceipts: cfg.RunReceipts,
		executors:   cfg.Executors,
		status:      cfg.Status,
		logger:      logger.Named("backup-intent"),
	}
}

// HandleIntent processes a single backup intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *BackupIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "recipe-apply":
		return h.handleRecipeApply(ctx, intent)
	case "policy-apply":
		return h.handlePolicyApply(ctx, intent)
	case "repository-register":
		return h.handleRepositoryRegister(ctx, intent)
	case "definition-apply":
		return h.handleDefinitionApply(ctx, intent)
	case "run":
		return h.handleRun(ctx, intent)
	case "restore":
		return h.handleRestore(ctx, intent)
	case "restore-approval":
		return h.handleRestoreApproval(ctx, intent)
	case "verification":
		return h.handleVerification(ctx, intent)
	case "retention":
		return h.handleRetention(ctx, intent)
	case "repository-probe":
		return h.handleRepositoryProbe(ctx, intent)
	case "delete":
		return h.handleDelete(ctx, intent)
	default:
		return fmt.Errorf("unknown backup operation %q", intent.Op)
	}
}

// PermissionFor returns the permission required for backup operations.
func (h *BackupIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermManageBackups
}

// IsFleetScoped returns true because all backup operations are fleet-scoped.
// The intent processor authorizes via FleetOperatorGate.
func (h *BackupIntentHandler) IsFleetScoped() bool {
	return true
}

// --- Config entity handlers (operator-authored) ---

func (h *BackupIntentHandler) handleRecipeApply(ctx context.Context, intent *Intent) error {
	recipe, err := backupRecipeFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse recipe from intent: %w", err)
	}

	// Level-triggered: look up by ID for upsert.
	if recipe.ID != uuid.Nil {
		if existing, err := h.registry.GetRecipe(ctx, recipe.ID); err == nil && existing != nil {
			// Merge: intent content is full desired state, just keep the ID.
			_ = existing
		}
	}
	if recipe.ID == uuid.Nil {
		recipe.ID = domain.NewEntityID()
	}

	// Validate references exist.
	if _, err := h.registry.GetRepository(ctx, recipe.RepositoryID); err != nil {
		return fmt.Errorf("repository %s not found: %w", recipe.RepositoryID, err)
	}
	if recipe.PolicyID != nil {
		if _, err := h.registry.GetPolicy(ctx, *recipe.PolicyID); err != nil {
			return fmt.Errorf("policy %s not found: %w", *recipe.PolicyID, err)
		}
	}

	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return fmt.Errorf("validate recipe: %w", err)
	}
	if err := h.registry.CreateOrUpdateRecipe(ctx, recipe); err != nil {
		return fmt.Errorf("apply recipe: %w", err)
	}

	// Publish canonical state.
	return h.publishRecipe(ctx, recipe, false)
}

func (h *BackupIntentHandler) handlePolicyApply(ctx context.Context, intent *Intent) error {
	policy, err := backupPolicyFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse policy from intent: %w", err)
	}

	if policy.ID == uuid.Nil {
		if existing, err := h.registry.GetPolicyByName(ctx, policy.Name); err == nil && existing != nil {
			policy.ID = existing.ID
		}
	}
	if policy.ID == uuid.Nil {
		policy.ID = domain.NewEntityID()
	}

	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return fmt.Errorf("validate policy: %w", err)
	}
	if err := h.registry.CreateOrUpdatePolicy(ctx, policy); err != nil {
		return fmt.Errorf("apply policy: %w", err)
	}

	return h.publishPolicy(ctx, policy, false)
}

func (h *BackupIntentHandler) handleRepositoryRegister(ctx context.Context, intent *Intent) error {
	repo, err := backupRepositoryFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse repository from intent: %w", err)
	}

	if repo.ID == uuid.Nil {
		if existing, err := h.registry.GetRepositoryByName(ctx, repo.Name); err == nil && existing != nil {
			repo.ID = existing.ID
		}
	}
	if repo.ID == uuid.Nil {
		repo.ID = domain.NewEntityID()
	}

	if err := domain.ValidateBackupRepository(repo); err != nil {
		return fmt.Errorf("validate repository: %w", err)
	}
	if err := h.registry.CreateOrUpdateRepository(ctx, repo); err != nil {
		return fmt.Errorf("apply repository: %w", err)
	}

	return h.publishRepository(ctx, repo, false)
}

func (h *BackupIntentHandler) handleDefinitionApply(ctx context.Context, intent *Intent) error {
	if h.definitions == nil {
		return fmt.Errorf("backup definition registry is not configured")
	}

	def, err := backupDefinitionFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse definition from intent: %w", err)
	}

	if def.ID == uuid.Nil {
		if existing, err := h.definitions.GetBackupDefinitionByName(ctx, def.Name); err == nil && existing != nil {
			def.ID = existing.ID
		}
	}
	if def.ID == uuid.Nil {
		def.ID = domain.NewEntityID()
	}

	// Resolve references to get display names.
	repo, err := h.registry.GetRepository(ctx, def.RepositoryID)
	if err != nil || repo == nil {
		return fmt.Errorf("repository %s not found", def.RepositoryID)
	}
	policy, err := h.registry.GetPolicy(ctx, def.PolicyID)
	if err != nil || policy == nil {
		return fmt.Errorf("policy %s not found", def.PolicyID)
	}
	recipe, err := h.registry.GetRecipe(ctx, def.RecipeID)
	if err != nil || recipe == nil {
		return fmt.Errorf("recipe %s not found", def.RecipeID)
	}
	def.RepositoryName = repo.Name
	def.PolicyName = policy.Name
	def.RecipeName = recipe.Name
	def.RecipeVersion = recipe.Version

	if def.CreatedBy == "" {
		def.CreatedBy = intent.Actor
	}

	if err := domain.ValidateBackupDefinition(def); err != nil {
		return fmt.Errorf("validate definition: %w", err)
	}
	if err := h.definitions.UpsertBackupDefinition(ctx, def); err != nil {
		return fmt.Errorf("apply definition: %w", err)
	}

	return h.publishDefinition(ctx, def, false)
}

// --- Daemon-triggered handlers (run, restore, verification, retention) ---

func (h *BackupIntentHandler) handleRun(ctx context.Context, intent *Intent) error {
	if h.runReceipts == nil {
		return fmt.Errorf("backup run request intake paused: canonical acceptance receipts are unavailable")
	}
	requested, err := backupRunFromIntentContent(intent)
	if err != nil || requested.ID == uuid.Nil {
		return fmt.Errorf("backup run request intake paused: a valid run id is required")
	}
	receipt, err := h.runReceipts.GetBackupRunReceipt(ctx, requested.ID)
	if err != nil {
		return fmt.Errorf("backup run request intake paused: %w", err)
	}
	if receipt != nil {
		if intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() ||
			receipt.RequestedBy != intent.Actor || receipt.RequestKind != int(intent.Event.Kind) ||
			receipt.RequestEventID != intent.Event.ID.Hex() || receipt.RequestDTag != intent.Coordinate ||
			receipt.RecipeID != requested.RecipeID {
			return fmt.Errorf("backup run request intake paused: run id conflicts with an ACKed canonical request")
		}
		signed, err := ParseIntent(intent.Event)
		if err != nil || signed.Domain != "backup" || signed.Op != "run" || signed.Coordinate != intent.Coordinate ||
			signed.IntentID != intent.IntentID || signed.OrgID != intent.OrgID || intent.Event.PubKey.Hex() != intent.Actor ||
			!backupRunContentEqual(signed.Content, intent.Content) {
			return fmt.Errorf("backup run request intake paused: request fields do not match the signed intent")
		}
		if requested.RepositoryID == uuid.Nil || requested.Backend == "" || strings.TrimSpace(requested.TargetRef) == "" ||
			requested.VerificationMode == "" || (requested.PolicyID == nil && receipt.PolicyID != nil) {
			return fmt.Errorf("backup run request intake paused: execution inputs are not bound in the signed request")
		}
		if receipt.RepositoryID != requested.RepositoryID || receipt.Backend != requested.Backend ||
			receipt.TargetRef != requested.TargetRef || receipt.VerificationMode != requested.VerificationMode ||
			!backupRunPolicyEqual(receipt.PolicyID, requested.PolicyID) ||
			!backupRunMetadataContains(receipt.Metadata, requested.Metadata) {
			return fmt.Errorf("backup run request intake paused: execution inputs conflict with the signed request")
		}
		// Legacy run states remain inspectable, but never establish execution
		// eligibility without the exact signed and ACK-proven config version.
		if err := validateBackupExecutionSnapshot(requested, receipt); err != nil {
			return fmt.Errorf("backup run request intake paused: %w", err)
		}
		proof, ok := h.runReceipts.(backupExecutionConfigProof)
		if !ok {
			return fmt.Errorf("backup run request intake paused: canonical configuration proof is unavailable")
		}
		if err := proof.VerifyBackupExecutionConfig(ctx, requested.ExecutionSnapshot); err != nil {
			return fmt.Errorf("backup run request intake paused: %w", err)
		}
		if receipt.Status == domain.RunStatusQueued || receipt.Status == domain.RunStatusRunning {
			return fmt.Errorf("backup run request intake paused: an ACKed canonical run is %s but canonical execution recovery is unavailable", receipt.Status)
		}
		return fmt.Errorf("backup run request intake paused: an ACKed canonical terminal run exists but request replay remains unavailable")
	}
	return fmt.Errorf("backup run request intake paused: no ACKed canonical run-state receipt exists")
}

// validateBackupExecutionSnapshot rejects any drift between the operator's
// signed request, the service's ACKed run state, and the config tuple itself.
func validateBackupExecutionSnapshot(requested, receipt *domain.BackupRun) error {
	if requested.ExecutionSnapshot == nil || receipt.ExecutionSnapshot == nil {
		return fmt.Errorf("execution snapshot is missing from the signed request or run receipt")
	}
	snapshot := requested.ExecutionSnapshot
	recipe, repository := snapshot.Recipe, snapshot.Repository
	if err := domain.ValidateBackupRecipe(&recipe); err != nil {
		return fmt.Errorf("execution snapshot recipe is invalid: %w", err)
	}
	if err := domain.ValidateBackupRepository(&repository); err != nil {
		return fmt.Errorf("execution snapshot repository is invalid: %w", err)
	}
	if snapshot.Policy != nil {
		policy := *snapshot.Policy
		if err := domain.ValidateBackupPolicy(&policy); err != nil {
			return fmt.Errorf("execution snapshot policy is invalid: %w", err)
		}
		if policy.RequireVerification && policy.VerificationMode != requested.VerificationMode {
			return fmt.Errorf("execution snapshot verification conflicts with policy")
		}
	}
	if snapshot.Recipe.ID != requested.RecipeID || snapshot.Repository.ID != requested.RepositoryID ||
		snapshot.Recipe.RepositoryID != snapshot.Repository.ID || snapshot.Recipe.Backend != requested.Backend ||
		snapshot.Repository.Backend != requested.Backend || snapshot.Recipe.TargetRef != requested.TargetRef ||
		snapshot.Recipe.VerificationMode != requested.VerificationMode ||
		!backupRunPolicyEqual(snapshot.Recipe.PolicyID, requested.PolicyID) ||
		snapshot.RecipeEventID == "" || snapshot.RepositoryEventID == "" {
		return fmt.Errorf("execution snapshot conflicts with signed run inputs")
	}
	if snapshot.Policy == nil {
		if requested.PolicyID != nil || snapshot.PolicyEventID != "" {
			return fmt.Errorf("execution snapshot policy is incomplete")
		}
	} else if requested.PolicyID == nil || snapshot.Policy.ID != *requested.PolicyID || snapshot.PolicyEventID == "" {
		return fmt.Errorf("execution snapshot policy conflicts with signed run inputs")
	}
	left, leftErr := json.Marshal(snapshot)
	right, rightErr := json.Marshal(receipt.ExecutionSnapshot)
	if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
		return fmt.Errorf("execution snapshot conflicts with the ACKed run receipt")
	}
	return nil
}

func backupRunContentEqual(a, b map[string]any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func backupRunPolicyEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func backupRunMetadataContains(actual, requested map[string]any) bool {
	for key, want := range requested {
		got, ok := actual[key]
		if !ok {
			return false
		}
		encodedGot, gotErr := json.Marshal(got)
		encodedWant, wantErr := json.Marshal(want)
		if gotErr != nil || wantErr != nil || !bytes.Equal(encodedGot, encodedWant) {
			return false
		}
	}
	return true
}

func (h *BackupIntentHandler) handleRestore(_ context.Context, _ *Intent) error {
	return fmt.Errorf("backup restore request intake paused: canonical acceptance receipts are unavailable")
}

func (h *BackupIntentHandler) handleRestoreApproval(_ context.Context, _ *Intent) error {
	return fmt.Errorf("backup restore approval paused: canonical execution inputs and atomic approval are unavailable")
}

func (h *BackupIntentHandler) handleVerification(ctx context.Context, intent *Intent) error {
	record, err := backupVerificationFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse verification from intent: %w", err)
	}
	if record.ID == uuid.Nil {
		record.ID = domain.NewEntityID()
	}

	if err := h.registry.RecordBackupVerification(ctx, record); err != nil {
		return fmt.Errorf("record backup verification: %w", err)
	}

	if h.executors.VerificationExecutor != nil {
		go func() {
			if err := h.executors.VerificationExecutor.ProcessBackupVerification(context.Background(), record.ID); err != nil {
				h.logger.Warn("backup verification execution failed", zap.String("verification_id", record.ID.String()), zap.Error(err))
			}
		}()
	}
	return nil
}

func (h *BackupIntentHandler) handleRetention(_ context.Context, _ *Intent) error {
	return fmt.Errorf("backup retention request intake paused: canonical acceptance receipts are unavailable")
}

func (h *BackupIntentHandler) handleRepositoryProbe(ctx context.Context, intent *Intent) error {
	content := intent.Content
	repoIDStr, _ := content["repository_id"].(string)
	repoName, _ := content["repository"].(string)

	var repo *domain.BackupRepository
	if repoIDStr != "" {
		id, err := uuid.Parse(repoIDStr)
		if err != nil {
			return fmt.Errorf("invalid repository_id %q: %w", repoIDStr, err)
		}
		repo, err = h.registry.GetRepository(ctx, id)
		if err != nil {
			return fmt.Errorf("repository %s not found: %w", repoIDStr, err)
		}
	} else if repoName != "" {
		var err error
		repo, err = h.registry.GetRepositoryByName(ctx, repoName)
		if err != nil {
			return fmt.Errorf("repository %q not found: %w", repoName, err)
		}
	}
	if repo == nil {
		return fmt.Errorf("no repository_id or repository name specified")
	}

	if h.executors.ProbeExecutor != nil {
		go func() {
			if err := h.executors.ProbeExecutor.ProcessBackupRepositoryProbe(context.Background(), repo.ID, intent.Event.ID.Hex()); err != nil {
				h.logger.Warn("backup repository probe failed", zap.String("repository_id", repo.ID.String()), zap.Error(err))
			}
		}()
	}
	return nil
}

func (h *BackupIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	entityType, entityID, err := backupParseDeleteCoordinate(intent)
	if err != nil {
		return fmt.Errorf("parse delete coordinate: %w", err)
	}

	switch entityType {
	case "recipe":
		recipe, err := h.registry.GetRecipe(ctx, entityID)
		if err != nil {
			return fmt.Errorf("recipe %s not found: %w", entityID, err)
		}
		return h.publishRecipe(ctx, recipe, true)
	case "policy":
		policy, err := h.registry.GetPolicy(ctx, entityID)
		if err != nil {
			return fmt.Errorf("policy %s not found: %w", entityID, err)
		}
		return h.publishPolicy(ctx, policy, true)
	case "repository":
		repo, err := h.registry.GetRepository(ctx, entityID)
		if err != nil {
			return fmt.Errorf("repository %s not found: %w", entityID, err)
		}
		return h.publishRepository(ctx, repo, true)
	case "definition":
		if h.definitions == nil {
			return fmt.Errorf("definition registry not configured")
		}
		def, err := h.definitions.GetBackupDefinitionByName(ctx, entityID.String())
		if err != nil {
			return fmt.Errorf("definition %s not found: %w", entityID, err)
		}
		return h.publishDefinition(ctx, def, true)
	default:
		return fmt.Errorf("unknown backup entity type for delete: %q", entityType)
	}
}

// --- Publish helpers -------------------------------------------------------

func (h *BackupIntentHandler) publishRecipe(ctx context.Context, recipe *domain.BackupRecipe, deleted bool) error {
	if h.publisher == nil {
		return nil
	}
	if deleted {
		tags, content := nostradapter.BackupRecipeRegistryRecord(recipe, true)
		return h.publisher.PublishDeleted(ctx, nostradapter.KindBackupRecipeRegistry, nostradapter.BackupRecipeDTag(recipe.ID), tags, content, "backup_recipe", &recipe.ID)
	}
	return h.publisher.PublishRecipe(ctx, recipe)
}

func (h *BackupIntentHandler) publishPolicy(ctx context.Context, policy *domain.BackupPolicy, deleted bool) error {
	if h.publisher == nil {
		return nil
	}
	if deleted {
		tags, content := nostradapter.BackupPolicyRegistryRecord(policy, true)
		return h.publisher.PublishDeleted(ctx, nostradapter.KindBackupPolicyRegistry, nostradapter.BackupPolicyDTag(policy.ID), tags, content, "backup_policy", &policy.ID)
	}
	return h.publisher.PublishPolicy(ctx, policy)
}

func (h *BackupIntentHandler) publishRepository(ctx context.Context, repo *domain.BackupRepository, deleted bool) error {
	if h.publisher == nil {
		return nil
	}
	if deleted {
		tags, content := nostradapter.BackupRepositoryRegistryRecord(repo, true)
		return h.publisher.PublishDeleted(ctx, nostradapter.KindBackupRepositoryRegistry, nostradapter.BackupRepositoryDTag(repo.ID), tags, content, "backup_repository", &repo.ID)
	}
	return h.publisher.PublishRepository(ctx, repo)
}

func (h *BackupIntentHandler) publishDefinition(ctx context.Context, def *domain.BackupDefinition, deleted bool) error {
	if h.publisher == nil {
		return nil
	}
	if deleted {
		tags, content := nostradapter.BackupDefinitionRegistryRecord(def, true)
		return h.publisher.PublishDeleted(ctx, nostradapter.KindBackupDefinitionRegistry, nostradapter.BackupDefinitionDTag(def.ID), tags, content, "backup_definition", &def.ID)
	}
	return h.publisher.PublishDefinition(ctx, def)
}

// --- Content parsers -------------------------------------------------------

func backupRecipeFromIntentContent(intent *Intent) (*domain.BackupRecipe, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var recipe domain.BackupRecipe
	if err := json.Unmarshal(data, &recipe); err != nil {
		return nil, fmt.Errorf("unmarshal recipe: %w", err)
	}
	return &recipe, nil
}

func backupPolicyFromIntentContent(intent *Intent) (*domain.BackupPolicy, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var policy domain.BackupPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("unmarshal policy: %w", err)
	}
	return &policy, nil
}

func backupRepositoryFromIntentContent(intent *Intent) (*domain.BackupRepository, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var repo domain.BackupRepository
	if err := json.Unmarshal(data, &repo); err != nil {
		return nil, fmt.Errorf("unmarshal repository: %w", err)
	}
	return &repo, nil
}

func backupDefinitionFromIntentContent(intent *Intent) (*domain.BackupDefinition, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var def domain.BackupDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("unmarshal definition: %w", err)
	}
	return &def, nil
}

func backupRunFromIntentContent(intent *Intent) (*domain.BackupRun, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var run domain.BackupRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("unmarshal backup run: %w", err)
	}
	return &run, nil
}

func backupRestoreFromIntentContent(intent *Intent) (*domain.BackupRestoreRun, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var restore domain.BackupRestoreRun
	if err := json.Unmarshal(data, &restore); err != nil {
		return nil, fmt.Errorf("unmarshal backup restore: %w", err)
	}
	return &restore, nil
}

func backupVerificationFromIntentContent(intent *Intent) (*domain.BackupVerificationRecord, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var record domain.BackupVerificationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal backup verification: %w", err)
	}
	return &record, nil
}

func backupRetentionFromIntentContent(intent *Intent) (*domain.BackupRetentionRun, error) {
	data, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, err
	}
	var run domain.BackupRetentionRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("unmarshal backup retention: %w", err)
	}
	return &run, nil
}

func backupParseDeleteCoordinate(intent *Intent) (entityType string, entityID uuid.UUID, err error) {
	content := intent.Content
	typ, _ := content["entity_type"].(string)
	idStr, _ := content["entity_id"].(string)
	if typ == "" {
		return "", uuid.Nil, fmt.Errorf("missing entity_type in delete intent")
	}
	id, parseErr := uuid.Parse(idStr)
	if parseErr != nil {
		return "", uuid.Nil, fmt.Errorf("invalid entity_id %q: %w", idStr, parseErr)
	}
	return typ, id, nil
}
