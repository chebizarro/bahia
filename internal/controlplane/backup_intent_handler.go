package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	backupAdapter "github.com/openagentsinc/bahia/internal/adapters/backup"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
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

// BackupConfigIndex mirrors canonical records; errors cannot veto a signed
// mutation and the index is never read for authorization or references.
type BackupConfigIndex interface {
	ResetBackupConfigIndex(context.Context) error
	UpsertBackupRecipe(context.Context, *domain.BackupRecipe) error
	UpsertBackupPolicy(context.Context, *domain.BackupPolicy) error
	UpsertBackupRepository(context.Context, *domain.BackupRepository) error
	UpsertBackupDefinition(context.Context, *domain.BackupDefinition) error
	MarkBackupConfigDeleted(context.Context, int, uuid.UUID) error
}

// BackupRunAdmissionWriter persists the initial service-signed run state and
// request identity in one local outbox transaction. It never starts a backup.
type BackupRunAdmissionWriter interface {
	LookupRunAdmission(context.Context, string, string, string) (string, bool, bool, error)
	StageRunAdmission(context.Context, string, string, *domain.BackupRun) (string, error)
}

// BackupRunPendingWriter records the signed operator request before any
// relay-history I/O. The asynchronous worker owns history; it cannot sign a
// run until a separate cross-process service-key fence is configured.
type BackupRunPendingWriter interface {
	LookupPendingRun(context.Context, string, string, string) (*localstore.BackupRunPending, error)
	StagePendingRun(context.Context, string, string, gonostr.Event, string, time.Time) (*localstore.BackupRunPending, error)
}

// BackupRunCredentialRecords reads the current canonical Signet binding.
// Admission never accepts a pin inferred from PostgreSQL or a stale request.
type BackupRunCredentialRecords interface {
	CurrentReference(context.Context, string) (domain.SecretCredentialRef, error)
}

// ErrBackupRunPending means the operator-signed request is durably queued but
// no service-signed run state or accepted outcome exists yet.
var ErrBackupRunPending = errors.New("backup run state is pending relay acceptance")

// ErrBackupRunAdmissionConflict preserves the existing request's final status
// when another signed request attempts to claim its immutable run identity.
var ErrBackupRunAdmissionConflict = errors.New("backup run admission conflict")

// ErrBackupRepositoryUnavailable refuses SQL-backed backup mutations when
// the daemon is serving canonical state without a database.
var ErrBackupRepositoryUnavailable = errors.New("backup repository unavailable")

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
// Backup intents are org-scoped: the processor requires backups:manage for
// the signed org and handlers bind resources to service-signed canonical owners.
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
	registry                 BackupIntentCRUD
	sqlRepositoryUnavailable bool
	definitions              BackupIntentDefinitionCRUD
	publisher                BackupIntentPublisher
	runReceipts              BackupRunReceiptReader
	runAdmission             BackupRunAdmissionWriter
	runPending               BackupRunPendingWriter
	runCredentialRecords     BackupRunCredentialRecords
	ownerReader              BackupOwnershipReader
	configReader             BackupConfigReader
	configIndex              BackupConfigIndex
	runAsyncIntakeEnabled    bool
	executors                BackupIntentExecutors
	status                   *IntentStatusPublisher
	logger                   *zap.Logger
}

// BackupIntentHandlerConfig configures the backup intent handler.
type BackupIntentHandlerConfig struct {
	Registry                 BackupIntentCRUD
	SQLRepositoryUnavailable bool
	Definitions              BackupIntentDefinitionCRUD
	Publisher                BackupIntentPublisher
	RunReceipts              BackupRunReceiptReader
	RunAdmission             BackupRunAdmissionWriter
	RunPending               BackupRunPendingWriter
	RunCredentialRecords     BackupRunCredentialRecords
	OwnerReader              BackupOwnershipReader
	ConfigReader             BackupConfigReader
	ConfigIndex              BackupConfigIndex
	RunAsyncIntakeEnabled    bool
	Executors                BackupIntentExecutors
	Status                   *IntentStatusPublisher
	Logger                   *zap.Logger
}

// NewBackupIntentHandler constructs the handler.
func NewBackupIntentHandler(cfg BackupIntentHandlerConfig) *BackupIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupIntentHandler{
		registry:                 cfg.Registry,
		sqlRepositoryUnavailable: cfg.SQLRepositoryUnavailable,
		definitions:              cfg.Definitions,
		publisher:                cfg.Publisher,
		runReceipts:              cfg.RunReceipts,
		runAdmission:             cfg.RunAdmission,
		runPending:               cfg.RunPending,
		runCredentialRecords:     cfg.RunCredentialRecords,
		ownerReader:              cfg.OwnerReader,
		configReader:             cfg.ConfigReader,
		configIndex:              cfg.ConfigIndex,
		runAsyncIntakeEnabled:    cfg.RunAsyncIntakeEnabled,
		executors:                cfg.Executors,
		status:                   cfg.Status,
		logger:                   logger.Named("backup-intent"),
	}
}

// HandleIntent processes a single backup intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *BackupIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent == nil {
		return fmt.Errorf("backup intent is missing")
	}
	if intent.Op != "run" && intent.Op != "recipe-apply" && intent.Op != "policy-apply" &&
		intent.Op != "repository-register" && intent.Op != "definition-apply" && intent.Op != "delete" &&
		(h.registry == nil || h.sqlRepositoryUnavailable) {
		return fmt.Errorf("%w: %s requires a SQL-backed registry", ErrBackupRepositoryUnavailable, intent.Op)
	}
	switch intent.Op {
	case "recipe-apply", "policy-apply", "repository-register", "definition-apply", "delete":
		if h.publisher == nil {
			return fmt.Errorf("canonical backup publisher is unavailable")
		}
		if h.configReader == nil || h.ownerReader == nil {
			return fmt.Errorf("canonical backup configuration reader is unavailable")
		}
	}
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

func readBackupConfig[T any](ctx context.Context, reader BackupConfigReader, family int, id uuid.UUID) (*T, error) {
	raw, found, err := reader.ConfigOf(ctx, family, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("backup reference %s has no canonical state", id)
	}
	var value T
	if err := decodeBackupConfig(raw, &value); err != nil {
		return nil, fmt.Errorf("decode canonical backup %s: %w", id, err)
	}
	return &value, nil
}

func decodeBackupConfig(raw json.RawMessage, value any) error {
	var content map[string]json.RawMessage
	if err := json.Unmarshal(raw, &content); err != nil {
		return err
	}
	for _, field := range []string{"created_at", "updated_at"} {
		if string(content[field]) == `""` {
			delete(content, field)
		}
	}
	clean, err := json.Marshal(content)
	if err != nil {
		return err
	}
	return json.Unmarshal(clean, value)
}

func (h *BackupIntentHandler) configTimes(ctx context.Context, family int, id uuid.UUID, created, updated *time.Time) error {
	raw, found, err := h.configReader.ConfigOf(ctx, family, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	*created = now
	if found {
		var prior struct {
			CreatedAt string `json:"created_at"`
		}
		if err := json.Unmarshal(raw, &prior); err != nil {
			return err
		}
		if prior.CreatedAt != "" {
			date, err := time.Parse(time.RFC3339, prior.CreatedAt)
			if err != nil {
				return err
			}
			*created = date
		}
	}
	*updated = now
	return nil
}

// PermissionFor returns the permission required for backup operations.
func (h *BackupIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermManageBackups
}

// IsFleetScoped returns false: backup mutations require backups:manage in the
// org bound to the canonical resource, not fleet-operator identity alone.
func (h *BackupIntentHandler) IsFleetScoped() bool {
	return false
}

func (h *BackupIntentHandler) requireOwner(ctx context.Context, intent *Intent, family int, id uuid.UUID, orgID uuid.UUID) error {
	if intent == nil || intent.OrgID == uuid.Nil || orgID == uuid.Nil || orgID != intent.OrgID || id == uuid.Nil {
		return fmt.Errorf("backup owner must match the signed org tag and entity id; republish ownerless legacy state")
	}
	if h.ownerReader == nil {
		return fmt.Errorf("canonical backup ownership reader is unavailable")
	}
	owner, found, err := h.ownerReader.OwnerOf(ctx, family, id)
	if err != nil {
		return err
	}
	if found && owner != orgID {
		return fmt.Errorf("backup %s belongs to another org; ownership is immutable", id)
	}
	return nil
}

func (h *BackupIntentHandler) requireReferenceOwner(ctx context.Context, family int, id, orgID uuid.UUID) error {
	if h.ownerReader == nil || id == uuid.Nil || orgID == uuid.Nil {
		return fmt.Errorf("canonical backup ownership reader or reference is unavailable")
	}
	owner, found, err := h.ownerReader.OwnerOf(ctx, family, id)
	if err != nil {
		return err
	}
	if !found || owner != orgID {
		return fmt.Errorf("backup reference %s has no canonical state owned by org %s; republish ownerless legacy state", id, orgID)
	}
	return nil
}

// --- Config entity handlers (operator-authored) ---

func (h *BackupIntentHandler) handleRecipeApply(ctx context.Context, intent *Intent) error {
	recipe, err := backupRecipeFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse recipe from intent: %w", err)
	}
	if err := h.requireOwner(ctx, intent, kinds.BackupRecipeRegistry, recipe.ID, recipe.OrgID); err != nil {
		return err
	}
	if err := h.requireReferenceOwner(ctx, kinds.BackupRepositoryRegistry, recipe.RepositoryID, recipe.OrgID); err != nil {
		return err
	}
	if recipe.PolicyID != nil {
		if err := h.requireReferenceOwner(ctx, kinds.BackupPolicyRegistry, *recipe.PolicyID, recipe.OrgID); err != nil {
			return err
		}
	}

	if _, err := readBackupConfig[domain.BackupRepository](ctx, h.configReader, kinds.BackupRepositoryRegistry, recipe.RepositoryID); err != nil {
		return fmt.Errorf("repository %s not found: %w", recipe.RepositoryID, err)
	}
	if recipe.PolicyID != nil {
		if _, err := readBackupConfig[domain.BackupPolicy](ctx, h.configReader, kinds.BackupPolicyRegistry, *recipe.PolicyID); err != nil {
			return fmt.Errorf("policy %s not found: %w", *recipe.PolicyID, err)
		}
	}

	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return fmt.Errorf("validate recipe: %w", err)
	}
	if err := h.configTimes(ctx, kinds.BackupRecipeRegistry, recipe.ID, &recipe.CreatedAt, &recipe.UpdatedAt); err != nil {
		return err
	}
	if err := h.publishRecipe(ctx, recipe, false); err != nil {
		return err
	}
	h.mirrorPublishedConfig(ctx, kinds.BackupRecipeRegistry, recipe.ID, false)
	return nil
}

func (h *BackupIntentHandler) handlePolicyApply(ctx context.Context, intent *Intent) error {
	policy, err := backupPolicyFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse policy from intent: %w", err)
	}
	if err := h.requireOwner(ctx, intent, kinds.BackupPolicyRegistry, policy.ID, policy.OrgID); err != nil {
		return err
	}

	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return fmt.Errorf("validate policy: %w", err)
	}
	if err := h.configTimes(ctx, kinds.BackupPolicyRegistry, policy.ID, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
		return err
	}
	if err := h.publishPolicy(ctx, policy, false); err != nil {
		return err
	}
	h.mirrorPublishedConfig(ctx, kinds.BackupPolicyRegistry, policy.ID, false)
	return nil
}

func (h *BackupIntentHandler) handleRepositoryRegister(ctx context.Context, intent *Intent) error {
	repo, err := backupRepositoryFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse repository from intent: %w", err)
	}
	if err := h.requireOwner(ctx, intent, kinds.BackupRepositoryRegistry, repo.ID, repo.OrgID); err != nil {
		return err
	}

	if err := domain.ValidateBackupRepository(repo); err != nil {
		return fmt.Errorf("validate repository: %w", err)
	}
	if err := h.configTimes(ctx, kinds.BackupRepositoryRegistry, repo.ID, &repo.CreatedAt, &repo.UpdatedAt); err != nil {
		return err
	}
	if err := h.publishRepository(ctx, repo, false); err != nil {
		return err
	}
	h.mirrorPublishedConfig(ctx, kinds.BackupRepositoryRegistry, repo.ID, false)
	return nil
}

func (h *BackupIntentHandler) handleDefinitionApply(ctx context.Context, intent *Intent) error {
	def, err := backupDefinitionFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse definition from intent: %w", err)
	}
	if err := h.requireOwner(ctx, intent, kinds.BackupDefinitionRegistry, def.ID, def.OrgID); err != nil {
		return err
	}
	for _, ref := range []struct {
		family int
		id     uuid.UUID
	}{{kinds.BackupRepositoryRegistry, def.RepositoryID}, {kinds.BackupPolicyRegistry, def.PolicyID}, {kinds.BackupRecipeRegistry, def.RecipeID}} {
		if err := h.requireReferenceOwner(ctx, ref.family, ref.id, def.OrgID); err != nil {
			return err
		}
	}

	repo, err := readBackupConfig[domain.BackupRepository](ctx, h.configReader, kinds.BackupRepositoryRegistry, def.RepositoryID)
	if err != nil {
		return fmt.Errorf("repository %s not found: %w", def.RepositoryID, err)
	}
	policy, err := readBackupConfig[domain.BackupPolicy](ctx, h.configReader, kinds.BackupPolicyRegistry, def.PolicyID)
	if err != nil {
		return fmt.Errorf("policy %s not found: %w", def.PolicyID, err)
	}
	recipe, err := readBackupConfig[domain.BackupRecipe](ctx, h.configReader, kinds.BackupRecipeRegistry, def.RecipeID)
	if err != nil {
		return fmt.Errorf("recipe %s not found: %w", def.RecipeID, err)
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
	if err := h.configTimes(ctx, kinds.BackupDefinitionRegistry, def.ID, &def.CreatedAt, &def.UpdatedAt); err != nil {
		return err
	}
	if err := h.publishDefinition(ctx, def, false); err != nil {
		return err
	}
	h.mirrorPublishedConfig(ctx, kinds.BackupDefinitionRegistry, def.ID, false)
	return nil
}

// --- Daemon-triggered handlers (run, restore, verification, retention) ---

func (h *BackupIntentHandler) handleRun(ctx context.Context, intent *Intent) error {
	if h.runAdmission == nil || h.runPending == nil || h.runReceipts == nil {
		return fmt.Errorf("backup run request intake paused: canonical acceptance is unavailable")
	}
	if intent == nil || intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() ||
		intent.Event.PubKey.Hex() != intent.Actor {
		return fmt.Errorf("backup run request intake requires an operator-signed event")
	}
	signed, err := ParseIntent(intent.Event)
	if err != nil || signed.Domain != "backup" || signed.Op != "run" || signed.Coordinate != intent.Coordinate ||
		signed.IntentID != intent.IntentID || signed.OrgID != intent.OrgID ||
		!backupRunContentEqual(signed.Content, intent.Content) {
		return fmt.Errorf("backup run request fields do not match the signed intent")
	}
	if err := rejectLegacyBackupCredentialVersion(signed.Content); err != nil {
		return err
	}
	requested, err := backupRunFromIntentContent(signed)
	if err != nil || requested.ID == uuid.Nil || intent.Coordinate != "backup-run:"+requested.ID.String() {
		return fmt.Errorf("backup run request requires an author-minted run coordinate")
	}
	requestID := intent.Event.ID.Hex()
	stateID, found, delivered, err := h.runAdmission.LookupRunAdmission(ctx, intent.IntentID, intent.Coordinate, requestID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackupRunAdmissionConflict, err)
	}
	if found {
		intent.Result = map[string]any{"run_id": requested.ID.String(), "state_event_id": stateID, "execution": "queued"}
		if !delivered {
			return ErrBackupRunPending
		}
		return nil
	}
	pending, err := h.runPending.LookupPendingRun(ctx, intent.IntentID, intent.Coordinate, requestID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackupRunAdmissionConflict, err)
	}
	if pending != nil {
		if pending.State == localstore.BackupRunRefusedState || !time.Now().UTC().Before(pending.ExpiresAt) {
			if pending.StatusDelivered {
				return fmt.Errorf("%w: signed backup run rejected: %s", ErrBackupRunAdmissionConflict, pending.LastFailure)
			}
			return fmt.Errorf("%w: signed request expired or refused; terminal status awaits operator-relay quorum", ErrBackupRunAdmissionConflict)
		}
		if !h.runAsyncIntakeEnabled {
			return fmt.Errorf("%w: backup run request intake paused: credential recovery and exactly-once execution are unavailable", ErrBackupRunAdmissionConflict)
		}
		intent.Result = map[string]any{"run_id": requested.ID.String(), "request_event_id": requestID, "execution": "queued"}
		return ErrBackupRunPending
	}
	if !h.runAsyncIntakeEnabled {
		return fmt.Errorf("backup run request intake paused: credential recovery and exactly-once execution are unavailable")
	}
	if _, err := ValidateSignedBackupRunRequest(intent.Event, intent.Actor); err != nil {
		return fmt.Errorf("backup run request is invalid: %w", err)
	}
	if err := h.verifyRunInputs(ctx, requested); err != nil {
		return fmt.Errorf("backup run request intake paused: %w", err)
	}
	expiresAt, err := signedBackupRunExpiration(intent.Event)
	if err != nil {
		return fmt.Errorf("backup run request expiration: %w", err)
	}
	if _, err := h.runPending.StagePendingRun(ctx, intent.IntentID, intent.Coordinate, *intent.Event, intent.Actor, expiresAt); err != nil {
		return fmt.Errorf("stage signed backup run pending request: %w", err)
	}
	intent.Result = map[string]any{"run_id": requested.ID.String(), "request_event_id": requestID, "execution": "queued"}
	return ErrBackupRunPending
}

// AdmitPendingRun revalidates the original signed request immediately before
// staging, while the history worker holds its live relay observation.
func (h *BackupIntentHandler) AdmitPendingRun(ctx context.Context, pending localstore.BackupRunPending, history *nostradapter.BackupRunHistoryProof) error {
	if h == nil || !h.runAsyncIntakeEnabled || h.runAdmission == nil || history == nil {
		return fmt.Errorf("backup run admission is paused")
	}
	intent, err := ValidateSignedBackupRunRequest(&pending.RequestEvent, pending.Actor)
	if err != nil {
		return err
	}
	if intent.IntentID != pending.IntentID || intent.Coordinate != pending.Coordinate || pending.ServicePubkey != history.ServicePubkey.Hex() {
		return fmt.Errorf("backup run pending request identity changed")
	}
	if err := rejectLegacyBackupCredentialVersion(intent.Content); err != nil {
		return err
	}
	run, err := backupRunFromIntentContent(intent)
	if err != nil {
		return err
	}
	if run.ID == uuid.Nil || intent.Coordinate != "backup-run:"+run.ID.String() {
		return fmt.Errorf("backup run coordinate is invalid")
	}
	if err := h.verifyRunInputs(ctx, run); err != nil {
		return err
	}
	now := time.Now().UTC()
	run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag = pending.Actor, pending.RequestEvent.ID.Hex(), int(pending.RequestEvent.Kind), pending.Coordinate
	run.Status, run.CreatedAt, run.UpdatedAt = domain.RunStatusQueued, now, now
	stager, ok := h.runAdmission.(interface {
		StageRunAdmissionWithHistory(context.Context, string, string, *domain.BackupRun, *nostradapter.BackupRunHistoryProof) (string, error)
	})
	if !ok {
		return fmt.Errorf("backup run fenced admission writer is unavailable")
	}
	_, err = stager.StageRunAdmissionWithHistory(ctx, pending.IntentID, pending.RequestEvent.ID.Hex(), run, history)
	return err
}

func (h *BackupIntentHandler) verifyRunInputs(ctx context.Context, requested *domain.BackupRun) error {
	if err := validateBackupExecutionSnapshot(requested, requested); err != nil {
		return err
	}
	policy := requested.ExecutionSnapshot.Policy
	if requested.Backend != domain.BackupBackendKopia || requested.VerificationMode != domain.BackupVerificationNone ||
		(policy != nil && (policy.RequireVerification || policy.VerificationMode != domain.BackupVerificationNone)) ||
		len(requested.ExecutionSnapshot.Recipe.Include) != 0 ||
		len(requested.ExecutionSnapshot.Recipe.Exclude) != 0 {
		return fmt.Errorf("bahia-cd0wr: only a single unfiltered Kopia snapshot without verification effects is enabled")
	}
	if _, err := backupAdapter.CanonicalSnapshotEffectArgs(&requested.ExecutionSnapshot.Recipe, requested.ID); err != nil {
		return fmt.Errorf("bahia-cd0wr: unsupported Kopia snapshot input: %w", err)
	}
	proof, ok := h.runReceipts.(backupExecutionConfigProof)
	if !ok {
		return fmt.Errorf("canonical configuration proof is unavailable")
	}
	if err := proof.VerifyBackupExecutionConfig(ctx, requested.ExecutionSnapshot); err != nil {
		return err
	}
	if requested.ExecutionSnapshot.CredentialRef == nil || h.runCredentialRecords == nil {
		return fmt.Errorf("pinned Signet credential proof is unavailable")
	}
	currentRef, err := h.runCredentialRecords.CurrentReference(ctx, requested.ExecutionSnapshot.Repository.CredentialProfile)
	if err != nil {
		return fmt.Errorf("read canonical Signet credential: %w", err)
	}
	if currentRef != *requested.ExecutionSnapshot.CredentialRef {
		return fmt.Errorf("Signet credential version moved; fresh signed run required")
	}
	prior, err := h.runReceipts.GetBackupRunReceipt(ctx, requested.ID)
	if err != nil {
		return err
	}
	if prior != nil {
		return fmt.Errorf("run id already has an ACKed canonical state without an admission record")
	}
	return nil
}

// validateBackupExecutionSnapshot rejects any drift between the operator's
// signed request, the service's ACKed run state, and the config tuple itself.
func validateBackupExecutionSnapshot(requested, receipt *domain.BackupRun) error {
	if requested.ExecutionSnapshot == nil || receipt.ExecutionSnapshot == nil {
		return fmt.Errorf("execution snapshot is missing from the signed request or run receipt")
	}
	snapshot := requested.ExecutionSnapshot
	recipe, repository := snapshot.Recipe, snapshot.Repository
	if requested.OrgID == uuid.Nil || requested.OrgID != receipt.OrgID ||
		requested.DefinitionID == uuid.Nil || requested.DefinitionID != receipt.DefinitionID ||
		snapshot.Definition.OrgID != requested.OrgID || snapshot.Definition.ID != requested.DefinitionID ||
		recipe.OrgID != requested.OrgID || repository.OrgID != requested.OrgID {
		return fmt.Errorf("execution snapshot ownership conflicts with signed run inputs; republish ownerless legacy backup state")
	}
	if snapshot.DefinitionEventID == "" || snapshot.Definition.RepositoryID != requested.RepositoryID ||
		snapshot.Definition.RecipeID != requested.RecipeID ||
		(requested.PolicyID == nil && snapshot.Definition.PolicyID != uuid.Nil) ||
		(requested.PolicyID != nil && snapshot.Definition.PolicyID != *requested.PolicyID) {
		return fmt.Errorf("execution snapshot definition conflicts with signed run inputs")
	}
	if err := domain.ValidateBackupRecipe(&recipe); err != nil {
		return fmt.Errorf("execution snapshot recipe is invalid: %w", err)
	}
	if err := domain.ValidateBackupRepository(&repository); err != nil {
		return fmt.Errorf("execution snapshot repository is invalid: %w", err)
	}
	if snapshot.Policy != nil {
		policy := *snapshot.Policy
		if policy.OrgID != requested.OrgID {
			return fmt.Errorf("execution snapshot policy belongs to a different org")
		}
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
	if profile := strings.TrimSpace(snapshot.Repository.CredentialProfile); profile != "" {
		if id, err := uuid.Parse(profile); err != nil || id == uuid.Nil {
			return fmt.Errorf("execution snapshot repository credential_profile must be a canonical secret UUID")
		}
		if snapshot.CredentialRef == nil {
			return fmt.Errorf("execution snapshot requires a pinned Signet credential reference")
		}
		if err := secretstore.ValidateRef(*snapshot.CredentialRef); err != nil {
			return fmt.Errorf("execution snapshot credential reference: %w", err)
		}
	} else if snapshot.CredentialRef != nil {
		return fmt.Errorf("execution snapshot credential reference has no repository secret")
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

func rejectLegacyBackupCredentialVersion(content map[string]any) error {
	raw, ok := content["execution_snapshot"].(map[string]any)
	if !ok {
		return nil
	}
	if _, legacy := raw["credential_version_id"]; legacy {
		return fmt.Errorf("legacy credential_version_id is unsupported: use a pinned Signet credential_ref and a canonical secret UUID in credential_profile")
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
	if h.runReceipts == nil {
		return fmt.Errorf("backup verification requires canonical run ownership receipt")
	}
	run, err := h.runReceipts.GetBackupRunReceipt(ctx, record.BackupRunID)
	if err != nil {
		return err
	}
	if run == nil || run.OrgID == uuid.Nil || run.OrgID != intent.OrgID {
		return fmt.Errorf("backup verification run has no canonical state owned by the signed org")
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
	if err := h.requireReferenceOwner(ctx, kinds.BackupRepositoryRegistry, repo.ID, intent.OrgID); err != nil {
		return err
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
	family := 0
	switch entityType {
	case "recipe":
		family = kinds.BackupRecipeRegistry
	case "policy":
		family = kinds.BackupPolicyRegistry
	case "repository":
		family = kinds.BackupRepositoryRegistry
	case "definition":
		family = kinds.BackupDefinitionRegistry
	default:
		return fmt.Errorf("unknown backup entity type for delete: %q", entityType)
	}
	if err := h.requireReferenceOwner(ctx, family, entityID, intent.OrgID); err != nil {
		return err
	}
	var publishErr error
	switch entityType {
	case "recipe":
		recipe, err := readBackupConfig[domain.BackupRecipe](ctx, h.configReader, family, entityID)
		if err != nil {
			return fmt.Errorf("recipe %s not found: %w", entityID, err)
		}
		publishErr = h.publishRecipe(ctx, recipe, true)
	case "policy":
		policy, err := readBackupConfig[domain.BackupPolicy](ctx, h.configReader, family, entityID)
		if err != nil {
			return fmt.Errorf("policy %s not found: %w", entityID, err)
		}
		publishErr = h.publishPolicy(ctx, policy, true)
	case "repository":
		repo, err := readBackupConfig[domain.BackupRepository](ctx, h.configReader, family, entityID)
		if err != nil {
			return fmt.Errorf("repository %s not found: %w", entityID, err)
		}
		publishErr = h.publishRepository(ctx, repo, true)
	case "definition":
		def, err := readBackupConfig[domain.BackupDefinition](ctx, h.configReader, family, entityID)
		if err != nil {
			return fmt.Errorf("definition %s not found: %w", entityID, err)
		}
		publishErr = h.publishDefinition(ctx, def, true)
	default:
		return fmt.Errorf("unknown backup entity type for delete: %q", entityType)
	}
	if publishErr != nil {
		return publishErr
	}
	h.mirrorPublishedConfig(ctx, family, entityID, true)
	return nil
}

// mirrorPublishedConfig reads back the signed local head. A queued projection
// that could not be retained locally must not make its unsigned input visible
// in SQL; the post-warm canonical replay can repair the disposable index.
func (h *BackupIntentHandler) mirrorPublishedConfig(ctx context.Context, family int, id uuid.UUID, deleted bool) {
	if h.configIndex == nil {
		return
	}
	raw, found, err := h.configReader.ConfigOf(ctx, family, id)
	if deleted && errors.Is(err, ErrBackupConfigDeleted) {
		err = h.configIndex.MarkBackupConfigDeleted(ctx, family, id)
	} else if err == nil && found && !deleted {
		switch family {
		case kinds.BackupRecipeRegistry:
			var value domain.BackupRecipe
			err = decodeBackupConfig(raw, &value)
			if err == nil {
				err = h.configIndex.UpsertBackupRecipe(ctx, &value)
			}
		case kinds.BackupPolicyRegistry:
			var value domain.BackupPolicy
			err = decodeBackupConfig(raw, &value)
			if err == nil {
				err = h.configIndex.UpsertBackupPolicy(ctx, &value)
			}
		case kinds.BackupRepositoryRegistry:
			var value domain.BackupRepository
			err = decodeBackupConfig(raw, &value)
			if err == nil {
				err = h.configIndex.UpsertBackupRepository(ctx, &value)
			}
		case kinds.BackupDefinitionRegistry:
			var value domain.BackupDefinition
			err = decodeBackupConfig(raw, &value)
			if err == nil {
				err = h.configIndex.UpsertBackupDefinition(ctx, &value)
			}
		default:
			err = fmt.Errorf("unknown backup family %d", family)
		}
	} else if err == nil {
		err = fmt.Errorf("published backup %s has no matching retained canonical head", id)
	}
	if err != nil {
		h.logger.Warn("backup SQL index delayed", zap.String("id", id.String()), zap.Error(err))
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
