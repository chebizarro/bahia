package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgBackupControlPlaneRepository stores authoritative backup control-plane state.
type PgBackupControlPlaneRepository struct {
	pool pgQueryer
}

func NewPgBackupControlPlaneRepository(pool *pgxpool.Pool) *PgBackupControlPlaneRepository {
	return newPgBackupControlPlaneRepositoryWithDB(pool)
}

func newPgBackupControlPlaneRepositoryWithDB(db pgQueryer) *PgBackupControlPlaneRepository {
	return &PgBackupControlPlaneRepository{pool: db}
}

const backupRecipeColumns = `id, name, version, backend, repository_id, policy_id, target_ref, include_paths, exclude_paths, verification_mode, metadata, created_at, updated_at`
const backupPolicyColumns = `id, name, require_verification, verification_mode, metadata, created_at, updated_at`
const backupRepositoryColumns = `id, name, backend, repository_uri, credential_profile, metadata, created_at, updated_at`
const backupDefinitionColumns = `id, name, repository_id, repository_name, policy_id, policy_name, recipe_id, recipe_name, recipe_version, schedule_expression, schedule_enabled, schedule_jitter_window, tenant_id, tenant_name, environment_id, environment_name, owner_pubkey, requires_approval, approval_policy, restore_target_rules, executor_labels, capability_requirements, labels, group_name, metadata, created_at, updated_at, created_by`
const backupRunColumns = `id, recipe_id, repository_id, policy_id, requested_by, request_event_id, request_kind, request_d_tag, status, backend, target_ref, snapshot_created, snapshot_id, verification_status, publish_summary, error, metadata, started_at, finished_at, created_at, updated_at`
const backupRestoreColumns = `id, backup_run_id, recipe_id, repository_id, policy_id, snapshot_id, restore_target_ref, requested_by, request_event_id, request_kind, request_d_tag, approval_status, approval_event_id, approved_by, approved_at, approval_message, status, backend, verification_status, evidence, publish_summary, error, metadata, started_at, finished_at, created_at, updated_at`
const backupRetentionRunColumns = `id, repository_id, policy_id, requested_by, request_event_id, request_kind, request_d_tag, status, backend, dry_run, evidence, publish_summary, error, metadata, started_at, finished_at, created_at, updated_at`
const backupVerificationColumns = `id, backup_run_id, mode, status, verified, evidence, error, publish_summary, verified_at, created_at, updated_at`

func (r *PgBackupControlPlaneRepository) UpsertBackupRecipe(ctx context.Context, recipe *domain.BackupRecipe) error {
	if err := domain.ValidateBackupRecipe(recipe); err != nil {
		return err
	}
	if recipe.ID == uuid.Nil {
		recipe.ID = uuid.New()
	}
	setBackupTimes(&recipe.CreatedAt, &recipe.UpdatedAt)
	includeJSON, err := marshalJSON(recipe.Include, "backup recipe include paths")
	if err != nil {
		return err
	}
	excludeJSON, err := marshalJSON(recipe.Exclude, "backup recipe exclude paths")
	if err != nil {
		return err
	}
	metadataJSON, err := marshalJSON(recipe.Metadata, "backup recipe metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_recipes (`+backupRecipeColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11, '{}'::jsonb),$12,$13)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			version = EXCLUDED.version,
			backend = EXCLUDED.backend,
			repository_id = EXCLUDED.repository_id,
			policy_id = EXCLUDED.policy_id,
			target_ref = EXCLUDED.target_ref,
			include_paths = EXCLUDED.include_paths,
			exclude_paths = EXCLUDED.exclude_paths,
			verification_mode = EXCLUDED.verification_mode,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, recipe.ID, recipe.Name, recipe.Version, recipe.Backend, recipe.RepositoryID, uuidPtrArg(recipe.PolicyID), recipe.TargetRef, includeJSON, excludeJSON, recipe.VerificationMode, metadataJSON, recipe.CreatedAt, recipe.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup recipe: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupRecipe(ctx context.Context, id uuid.UUID) (*domain.BackupRecipe, error) {
	return r.scanBackupRecipe(r.pool.QueryRow(ctx, `SELECT `+backupRecipeColumns+` FROM backup_recipes WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupRecipeByNameVersion(ctx context.Context, name, version string) (*domain.BackupRecipe, error) {
	return r.scanBackupRecipe(r.pool.QueryRow(ctx, `SELECT `+backupRecipeColumns+` FROM backup_recipes WHERE name = $1 AND version = $2`, strings.TrimSpace(name), strings.TrimSpace(version)))
}

func (r *PgBackupControlPlaneRepository) ListBackupRecipes(ctx context.Context, limit, offset int) ([]domain.BackupRecipe, error) {
	limit, offset = backupLimitOffset(limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+backupRecipeColumns+` FROM backup_recipes ORDER BY name ASC, version ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing backup recipes: %w", err)
	}
	defer rows.Close()
	return scanBackupRecipeRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupPolicy(ctx context.Context, policy *domain.BackupPolicy) error {
	if err := domain.ValidateBackupPolicy(policy); err != nil {
		return err
	}
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	setBackupTimes(&policy.CreatedAt, &policy.UpdatedAt)
	metadataJSON, err := marshalJSON(policy.Metadata, "backup policy metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_policies (`+backupPolicyColumns+`)
		VALUES ($1,$2,$3,$4,COALESCE($5, '{}'::jsonb),$6,$7)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			require_verification = EXCLUDED.require_verification,
			verification_mode = EXCLUDED.verification_mode,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, policy.ID, policy.Name, policy.RequireVerification, policy.VerificationMode, metadataJSON, policy.CreatedAt, policy.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup policy: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupPolicy(ctx context.Context, id uuid.UUID) (*domain.BackupPolicy, error) {
	return r.scanBackupPolicy(r.pool.QueryRow(ctx, `SELECT `+backupPolicyColumns+` FROM backup_policies WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupPolicyByName(ctx context.Context, name string) (*domain.BackupPolicy, error) {
	return r.scanBackupPolicy(r.pool.QueryRow(ctx, `SELECT `+backupPolicyColumns+` FROM backup_policies WHERE name = $1`, strings.TrimSpace(name)))
}

func (r *PgBackupControlPlaneRepository) ListBackupPolicies(ctx context.Context, limit, offset int) ([]domain.BackupPolicy, error) {
	limit, offset = backupLimitOffset(limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+backupPolicyColumns+` FROM backup_policies ORDER BY name ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing backup policies: %w", err)
	}
	defer rows.Close()
	return scanBackupPolicyRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupRepository(ctx context.Context, repo *domain.BackupRepository) error {
	if err := domain.ValidateBackupRepository(repo); err != nil {
		return err
	}
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	setBackupTimes(&repo.CreatedAt, &repo.UpdatedAt)
	metadataJSON, err := marshalJSON(repo.Metadata, "backup repository metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_repositories (`+backupRepositoryColumns+`)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6, '{}'::jsonb),$7,$8)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			backend = EXCLUDED.backend,
			repository_uri = EXCLUDED.repository_uri,
			credential_profile = EXCLUDED.credential_profile,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, repo.ID, repo.Name, repo.Backend, repo.RepositoryURI, repo.CredentialProfile, metadataJSON, repo.CreatedAt, repo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup repository: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupRepository(ctx context.Context, id uuid.UUID) (*domain.BackupRepository, error) {
	return r.scanBackupRepository(r.pool.QueryRow(ctx, `SELECT `+backupRepositoryColumns+` FROM backup_repositories WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupRepositoryByName(ctx context.Context, name string) (*domain.BackupRepository, error) {
	return r.scanBackupRepository(r.pool.QueryRow(ctx, `SELECT `+backupRepositoryColumns+` FROM backup_repositories WHERE name = $1`, strings.TrimSpace(name)))
}

func (r *PgBackupControlPlaneRepository) ListBackupRepositories(ctx context.Context, limit, offset int) ([]domain.BackupRepository, error) {
	limit, offset = backupLimitOffset(limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+backupRepositoryColumns+` FROM backup_repositories ORDER BY name ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing backup repositories: %w", err)
	}
	defer rows.Close()
	return scanBackupRepositoryRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupDefinition(ctx context.Context, definition *domain.BackupDefinition) error {
	if err := domain.ValidateBackupDefinition(definition); err != nil {
		return err
	}
	if definition.ID == uuid.Nil {
		definition.ID = uuid.New()
	}
	setBackupTimes(&definition.CreatedAt, &definition.UpdatedAt)
	restoreTargetRulesJSON, executorLabelsJSON, capabilityRequirementsJSON, labelsJSON, metadataJSON, err := marshalBackupDefinitionJSON(definition)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_definitions (`+backupDefinitionColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,COALESCE($20, '{}'::jsonb),COALESCE($21, '[]'::jsonb),COALESCE($22, '[]'::jsonb),COALESCE($23, '{}'::jsonb),$24,COALESCE($25, '{}'::jsonb),$26,$27,$28)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			repository_id = EXCLUDED.repository_id,
			repository_name = EXCLUDED.repository_name,
			policy_id = EXCLUDED.policy_id,
			policy_name = EXCLUDED.policy_name,
			recipe_id = EXCLUDED.recipe_id,
			recipe_name = EXCLUDED.recipe_name,
			recipe_version = EXCLUDED.recipe_version,
			schedule_expression = EXCLUDED.schedule_expression,
			schedule_enabled = EXCLUDED.schedule_enabled,
			schedule_jitter_window = EXCLUDED.schedule_jitter_window,
			tenant_id = EXCLUDED.tenant_id,
			tenant_name = EXCLUDED.tenant_name,
			environment_id = EXCLUDED.environment_id,
			environment_name = EXCLUDED.environment_name,
			owner_pubkey = EXCLUDED.owner_pubkey,
			requires_approval = EXCLUDED.requires_approval,
			approval_policy = EXCLUDED.approval_policy,
			restore_target_rules = EXCLUDED.restore_target_rules,
			executor_labels = EXCLUDED.executor_labels,
			capability_requirements = EXCLUDED.capability_requirements,
			labels = EXCLUDED.labels,
			group_name = EXCLUDED.group_name,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, definition.ID, definition.Name, definition.RepositoryID, definition.RepositoryName, definition.PolicyID, definition.PolicyName, definition.RecipeID, definition.RecipeName,
		definition.RecipeVersion, definition.ScheduleExpression, definition.ScheduleEnabled, definition.ScheduleJitterWindow, uuidPtrArg(definition.TenantID), definition.TenantName,
		uuidPtrArg(definition.EnvironmentID), definition.EnvironmentName, definition.OwnerPubkey, definition.RequiresApproval, definition.ApprovalPolicy, restoreTargetRulesJSON,
		executorLabelsJSON, capabilityRequirementsJSON, labelsJSON, definition.Group, metadataJSON, definition.CreatedAt, definition.UpdatedAt, definition.CreatedBy)
	if err != nil {
		return fmt.Errorf("upserting backup definition: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupDefinition(ctx context.Context, id uuid.UUID) (*domain.BackupDefinition, error) {
	return r.scanBackupDefinition(r.pool.QueryRow(ctx, `SELECT `+backupDefinitionColumns+` FROM backup_definitions WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupDefinitionByName(ctx context.Context, name string) (*domain.BackupDefinition, error) {
	return r.scanBackupDefinition(r.pool.QueryRow(ctx, `SELECT `+backupDefinitionColumns+` FROM backup_definitions WHERE name = $1`, strings.TrimSpace(name)))
}

func (r *PgBackupControlPlaneRepository) ListBackupDefinitions(ctx context.Context, limit, offset int) ([]domain.BackupDefinition, error) {
	limit, offset = backupLimitOffset(limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+backupDefinitionColumns+` FROM backup_definitions ORDER BY name ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing backup definitions: %w", err)
	}
	defer rows.Close()
	return scanBackupDefinitionRows(rows)
}

func (r *PgBackupControlPlaneRepository) DeleteBackupDefinition(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM backup_definitions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting backup definition: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting backup definition %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) UpsertBackupRun(ctx context.Context, run *domain.BackupRun) error {
	if err := domain.ValidateBackupRun(run); err != nil {
		return err
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTimes(&run.CreatedAt, &run.UpdatedAt)
	publishJSON, metadataJSON, err := marshalBackupRunJSON(run)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_runs (`+backupRunColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15, '{}'::jsonb),$16,COALESCE($17, '{}'::jsonb),$18,$19,$20,$21)
		ON CONFLICT (id) DO UPDATE SET
			recipe_id = EXCLUDED.recipe_id,
			repository_id = EXCLUDED.repository_id,
			policy_id = EXCLUDED.policy_id,
			requested_by = EXCLUDED.requested_by,
			request_event_id = EXCLUDED.request_event_id,
			request_kind = EXCLUDED.request_kind,
			request_d_tag = EXCLUDED.request_d_tag,
			status = EXCLUDED.status,
			backend = EXCLUDED.backend,
			target_ref = EXCLUDED.target_ref,
			snapshot_created = EXCLUDED.snapshot_created,
			snapshot_id = EXCLUDED.snapshot_id,
			verification_status = EXCLUDED.verification_status,
			publish_summary = EXCLUDED.publish_summary,
			error = EXCLUDED.error,
			metadata = EXCLUDED.metadata,
			started_at = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			updated_at = EXCLUDED.updated_at
	`, run.ID, run.RecipeID, run.RepositoryID, uuidPtrArg(run.PolicyID), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
		run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus, publishJSON, run.Error,
		metadataJSON, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup run: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupRun(ctx context.Context, id uuid.UUID) (*domain.BackupRun, error) {
	return r.scanBackupRun(r.pool.QueryRow(ctx, `SELECT `+backupRunColumns+` FROM backup_runs WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRun, error) {
	return r.scanBackupRun(r.pool.QueryRow(ctx, `
		SELECT `+backupRunColumns+`
		FROM backup_runs
		WHERE requested_by = $1 AND request_kind = $2 AND request_d_tag = $3
	`, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag)))
}

func (r *PgBackupControlPlaneRepository) CreateBackupRunIfAbsent(ctx context.Context, run *domain.BackupRun) (*domain.BackupRun, bool, error) {
	if err := domain.ValidateBackupRun(run); err != nil {
		return nil, false, err
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTimes(&run.CreatedAt, &run.UpdatedAt)
	publishJSON, metadataJSON, err := marshalBackupRunJSON(run)
	if err != nil {
		return nil, false, err
	}
	createdRun, err := scanBackupRun(r.pool.QueryRow(ctx, `
		INSERT INTO backup_runs (`+backupRunColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15, '{}'::jsonb),$16,COALESCE($17, '{}'::jsonb),$18,$19,$20,$21)
		ON CONFLICT (requested_by, request_kind, request_d_tag) DO NOTHING
		RETURNING `+backupRunColumns,
		run.ID, run.RecipeID, run.RepositoryID, uuidPtrArg(run.PolicyID), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
		run.Status, run.Backend, run.TargetRef, run.SnapshotCreated, run.SnapshotID, run.VerificationStatus, publishJSON, run.Error,
		metadataJSON, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt))
	if err == nil {
		return createdRun, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, fmt.Errorf("creating backup run if absent: %w", err)
	}
	existing, err := r.GetBackupRunByRequestCoordinate(ctx, run.RequestedBy, run.RequestKind, run.RequestDTag)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		return nil, false, fmt.Errorf("backup run idempotency conflict without existing row")
	}
	return existing, false, nil
}

func (r *PgBackupControlPlaneRepository) ClaimNextQueuedBackupRun(ctx context.Context) (*domain.BackupRun, error) {
	now := time.Now().UTC()
	run, err := r.scanBackupRun(r.pool.QueryRow(ctx, `
		UPDATE backup_runs
		SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
		WHERE id = (
			SELECT id FROM backup_runs
			WHERE status = 'queued'
			ORDER BY created_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+backupRunColumns, now))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claiming next queued backup run: %w", err)
	}
	return run, nil
}

func (r *PgBackupControlPlaneRepository) RequeueStaleBackupRuns(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	cmd, err := r.pool.Exec(ctx, `
		UPDATE backup_runs
		SET status = 'queued', started_at = NULL,
			metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{lease_recovered}', 'true'::jsonb, true),
			updated_at = NOW()
		WHERE status = 'running' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale backup runs: %w", err)
	}
	return int(cmd.RowsAffected()), nil
}

func (r *PgBackupControlPlaneRepository) ListBackupRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRun, error) {
	limit, offset = backupLimitOffset(limit, offset)
	query := `SELECT ` + backupRunColumns + ` FROM backup_runs ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`
	args := []any{limit, offset}
	if status != "" {
		query = `SELECT ` + backupRunColumns + ` FROM backup_runs WHERE status = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`
		args = []any{status, limit, offset}
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing backup runs: %w", err)
	}
	defer rows.Close()
	return scanBackupRunRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupRestore(ctx context.Context, restore *domain.BackupRestoreRun) error {
	if err := domain.ValidateBackupRestoreRun(restore); err != nil {
		return err
	}
	if restore.ID == uuid.Nil {
		restore.ID = uuid.New()
	}
	setBackupTimes(&restore.CreatedAt, &restore.UpdatedAt)
	evidenceJSON, publishJSON, metadataJSON, err := marshalBackupRestoreJSON(restore)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_restores (`+backupRestoreColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,COALESCE($20, '{}'::jsonb),COALESCE($21, '{}'::jsonb),$22,COALESCE($23, '{}'::jsonb),$24,$25,$26,$27)
		ON CONFLICT (id) DO UPDATE SET
			backup_run_id = EXCLUDED.backup_run_id,
			recipe_id = EXCLUDED.recipe_id,
			repository_id = EXCLUDED.repository_id,
			policy_id = EXCLUDED.policy_id,
			snapshot_id = EXCLUDED.snapshot_id,
			restore_target_ref = EXCLUDED.restore_target_ref,
			requested_by = EXCLUDED.requested_by,
			request_event_id = EXCLUDED.request_event_id,
			request_kind = EXCLUDED.request_kind,
			request_d_tag = EXCLUDED.request_d_tag,
			approval_status = EXCLUDED.approval_status,
			approval_event_id = EXCLUDED.approval_event_id,
			approved_by = EXCLUDED.approved_by,
			approved_at = EXCLUDED.approved_at,
			approval_message = EXCLUDED.approval_message,
			status = EXCLUDED.status,
			backend = EXCLUDED.backend,
			verification_status = EXCLUDED.verification_status,
			evidence = EXCLUDED.evidence,
			publish_summary = EXCLUDED.publish_summary,
			error = EXCLUDED.error,
			metadata = EXCLUDED.metadata,
			started_at = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			updated_at = EXCLUDED.updated_at
	`, restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, uuidPtrArg(restore.PolicyID), restore.SnapshotID, restore.RestoreTargetRef,
		restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalEventID, restore.ApprovedBy,
		restore.ApprovedAt, restore.ApprovalMessage, restore.Status, restore.Backend, restore.VerificationStatus, evidenceJSON, publishJSON, restore.Error,
		metadataJSON, restore.StartedAt, restore.FinishedAt, restore.CreatedAt, restore.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup restore: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupRestore(ctx context.Context, id uuid.UUID) (*domain.BackupRestoreRun, error) {
	return r.scanBackupRestore(r.pool.QueryRow(ctx, `SELECT `+backupRestoreColumns+` FROM backup_restores WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupRestoreByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRestoreRun, error) {
	return r.scanBackupRestore(r.pool.QueryRow(ctx, `
		SELECT `+backupRestoreColumns+`
		FROM backup_restores
		WHERE requested_by = $1 AND request_kind = $2 AND request_d_tag = $3
	`, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag)))
}

func (r *PgBackupControlPlaneRepository) CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error) {
	if err := domain.ValidateBackupRestoreRun(restore); err != nil {
		return nil, false, err
	}
	if restore.ID == uuid.Nil {
		restore.ID = uuid.New()
	}
	setBackupTimes(&restore.CreatedAt, &restore.UpdatedAt)
	evidenceJSON, publishJSON, metadataJSON, err := marshalBackupRestoreJSON(restore)
	if err != nil {
		return nil, false, err
	}
	created, err := scanBackupRestore(r.pool.QueryRow(ctx, `
		INSERT INTO backup_restores (`+backupRestoreColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,COALESCE($20, '{}'::jsonb),COALESCE($21, '{}'::jsonb),$22,COALESCE($23, '{}'::jsonb),$24,$25,$26,$27)
		ON CONFLICT (requested_by, request_kind, request_d_tag) DO NOTHING
		RETURNING `+backupRestoreColumns,
		restore.ID, restore.BackupRunID, restore.RecipeID, restore.RepositoryID, uuidPtrArg(restore.PolicyID), restore.SnapshotID, restore.RestoreTargetRef,
		restore.RequestedBy, restore.RequestEventID, restore.RequestKind, restore.RequestDTag, restore.ApprovalStatus, restore.ApprovalEventID, restore.ApprovedBy,
		restore.ApprovedAt, restore.ApprovalMessage, restore.Status, restore.Backend, restore.VerificationStatus, evidenceJSON, publishJSON, restore.Error,
		metadataJSON, restore.StartedAt, restore.FinishedAt, restore.CreatedAt, restore.UpdatedAt))
	if err == nil {
		return created, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, fmt.Errorf("creating backup restore if absent: %w", err)
	}
	existing, err := r.GetBackupRestoreByRequestCoordinate(ctx, restore.RequestedBy, restore.RequestKind, restore.RequestDTag)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		return nil, false, fmt.Errorf("backup restore idempotency conflict without existing row")
	}
	return existing, false, nil
}

func (r *PgBackupControlPlaneRepository) ClaimNextQueuedBackupRestore(ctx context.Context) (*domain.BackupRestoreRun, error) {
	now := time.Now().UTC()
	restore, err := r.scanBackupRestore(r.pool.QueryRow(ctx, `
		UPDATE backup_restores
		SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
		WHERE id = (
			SELECT id FROM backup_restores
			WHERE status = 'queued' AND approval_status IN ('approved', 'not_required')
			ORDER BY created_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+backupRestoreColumns, now))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claiming next queued backup restore: %w", err)
	}
	return restore, nil
}

func (r *PgBackupControlPlaneRepository) RequeueStaleBackupRestores(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	cmd, err := r.pool.Exec(ctx, `
		UPDATE backup_restores
		SET status = 'queued', started_at = NULL,
			metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{lease_recovered}', 'true'::jsonb, true),
			updated_at = NOW()
		WHERE status = 'running' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale backup restores: %w", err)
	}
	return int(cmd.RowsAffected()), nil
}

func (r *PgBackupControlPlaneRepository) ListBackupRestores(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRestoreRun, error) {
	limit, offset = backupLimitOffset(limit, offset)
	query := `SELECT ` + backupRestoreColumns + ` FROM backup_restores ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`
	args := []any{limit, offset}
	if status != "" {
		query = `SELECT ` + backupRestoreColumns + ` FROM backup_restores WHERE status = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`
		args = []any{status, limit, offset}
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing backup restores: %w", err)
	}
	defer rows.Close()
	return scanBackupRestoreRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupRetentionRun(ctx context.Context, run *domain.BackupRetentionRun) error {
	if err := domain.ValidateBackupRetentionRun(run); err != nil {
		return err
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTimes(&run.CreatedAt, &run.UpdatedAt)
	evidenceJSON, publishJSON, metadataJSON, err := marshalBackupRetentionRunJSON(run)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO backup_retention_runs (`+backupRetentionRunColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11, '{}'::jsonb),COALESCE($12, '{}'::jsonb),$13,COALESCE($14, '{}'::jsonb),$15,$16,$17,$18)
		ON CONFLICT (id) DO UPDATE SET
			repository_id = EXCLUDED.repository_id,
			policy_id = EXCLUDED.policy_id,
			requested_by = EXCLUDED.requested_by,
			request_event_id = EXCLUDED.request_event_id,
			request_kind = EXCLUDED.request_kind,
			request_d_tag = EXCLUDED.request_d_tag,
			status = EXCLUDED.status,
			backend = EXCLUDED.backend,
			dry_run = EXCLUDED.dry_run,
			evidence = EXCLUDED.evidence,
			publish_summary = EXCLUDED.publish_summary,
			error = EXCLUDED.error,
			metadata = EXCLUDED.metadata,
			started_at = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			updated_at = EXCLUDED.updated_at
	`, run.ID, run.RepositoryID, uuidPtrArg(run.PolicyID), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
		run.Status, run.Backend, run.DryRun, evidenceJSON, publishJSON, run.Error, metadataJSON, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting backup retention run: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupRetentionRun(ctx context.Context, id uuid.UUID) (*domain.BackupRetentionRun, error) {
	return r.scanBackupRetentionRun(r.pool.QueryRow(ctx, `SELECT `+backupRetentionRunColumns+` FROM backup_retention_runs WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupRetentionRunByRequestCoordinate(ctx context.Context, pubkey string, kind int, dTag string) (*domain.BackupRetentionRun, error) {
	return r.scanBackupRetentionRun(r.pool.QueryRow(ctx, `
		SELECT `+backupRetentionRunColumns+`
		FROM backup_retention_runs
		WHERE requested_by = $1 AND request_kind = $2 AND request_d_tag = $3
	`, strings.TrimSpace(pubkey), kind, strings.TrimSpace(dTag)))
}

func (r *PgBackupControlPlaneRepository) CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error) {
	if err := domain.ValidateBackupRetentionRun(run); err != nil {
		return nil, false, err
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	setBackupTimes(&run.CreatedAt, &run.UpdatedAt)
	evidenceJSON, publishJSON, metadataJSON, err := marshalBackupRetentionRunJSON(run)
	if err != nil {
		return nil, false, err
	}
	created, err := scanBackupRetentionRun(r.pool.QueryRow(ctx, `
		INSERT INTO backup_retention_runs (`+backupRetentionRunColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11, '{}'::jsonb),COALESCE($12, '{}'::jsonb),$13,COALESCE($14, '{}'::jsonb),$15,$16,$17,$18)
		ON CONFLICT (requested_by, request_kind, request_d_tag) DO NOTHING
		RETURNING `+backupRetentionRunColumns,
		run.ID, run.RepositoryID, uuidPtrArg(run.PolicyID), run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag,
		run.Status, run.Backend, run.DryRun, evidenceJSON, publishJSON, run.Error, metadataJSON, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt))
	if err == nil {
		return created, true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, false, fmt.Errorf("creating backup retention run if absent: %w", err)
	}
	existing, err := r.GetBackupRetentionRunByRequestCoordinate(ctx, run.RequestedBy, run.RequestKind, run.RequestDTag)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		return nil, false, fmt.Errorf("backup retention run idempotency conflict without existing row")
	}
	return existing, false, nil
}

func (r *PgBackupControlPlaneRepository) ClaimNextQueuedBackupRetentionRun(ctx context.Context) (*domain.BackupRetentionRun, error) {
	now := time.Now().UTC()
	run, err := r.scanBackupRetentionRun(r.pool.QueryRow(ctx, `
		UPDATE backup_retention_runs
		SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
		WHERE id = (
			SELECT id FROM backup_retention_runs
			WHERE status = 'queued'
			ORDER BY created_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+backupRetentionRunColumns, now))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claiming next queued backup retention run: %w", err)
	}
	return run, nil
}

func (r *PgBackupControlPlaneRepository) RequeueStaleBackupRetentionRuns(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	cmd, err := r.pool.Exec(ctx, `
		UPDATE backup_retention_runs
		SET status = 'queued', started_at = NULL,
			metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{lease_recovered}', 'true'::jsonb, true),
			updated_at = NOW()
		WHERE status = 'running' AND updated_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("requeueing stale backup retention runs: %w", err)
	}
	return int(cmd.RowsAffected()), nil
}

func (r *PgBackupControlPlaneRepository) ListBackupRetentionRuns(ctx context.Context, status domain.DeploymentRunStatus, limit, offset int) ([]domain.BackupRetentionRun, error) {
	limit, offset = backupLimitOffset(limit, offset)
	query := `SELECT ` + backupRetentionRunColumns + ` FROM backup_retention_runs ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`
	args := []any{limit, offset}
	if status != "" {
		query = `SELECT ` + backupRetentionRunColumns + ` FROM backup_retention_runs WHERE status = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`
		args = []any{status, limit, offset}
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing backup retention runs: %w", err)
	}
	defer rows.Close()
	return scanBackupRetentionRunRows(rows)
}

func (r *PgBackupControlPlaneRepository) UpsertBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error {
	if err := domain.ValidateBackupVerificationRecord(record); err != nil {
		return err
	}
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	setBackupTimes(&record.CreatedAt, &record.UpdatedAt)
	evidenceJSON, err := marshalJSON(record.Evidence, "backup verification evidence")
	if err != nil {
		return err
	}
	publishJSON, err := marshalJSON(record.PublishSummary, "backup verification publish summary")
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO backup_verifications (`+backupVerificationColumns+`)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6, '{}'::jsonb),$7,COALESCE($8, '{}'::jsonb),$9,$10,$11)
		ON CONFLICT (backup_run_id) DO UPDATE SET
			mode = EXCLUDED.mode,
			status = EXCLUDED.status,
			verified = EXCLUDED.verified,
			evidence = EXCLUDED.evidence,
			error = EXCLUDED.error,
			publish_summary = EXCLUDED.publish_summary,
			verified_at = EXCLUDED.verified_at,
			updated_at = EXCLUDED.updated_at
		RETURNING id
	`, record.ID, record.BackupRunID, record.Mode, record.Status, record.Verified, evidenceJSON, record.Error, publishJSON, record.VerifiedAt, record.CreatedAt, record.UpdatedAt).Scan(&record.ID)
	if err != nil {
		return fmt.Errorf("upserting backup verification: %w", err)
	}
	return nil
}

func (r *PgBackupControlPlaneRepository) GetBackupVerification(ctx context.Context, id uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return r.scanBackupVerification(r.pool.QueryRow(ctx, `SELECT `+backupVerificationColumns+` FROM backup_verifications WHERE id = $1`, id))
}

func (r *PgBackupControlPlaneRepository) GetBackupVerificationByRunID(ctx context.Context, runID uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return r.scanBackupVerification(r.pool.QueryRow(ctx, `SELECT `+backupVerificationColumns+` FROM backup_verifications WHERE backup_run_id = $1`, runID))
}

func (r *PgBackupControlPlaneRepository) scanBackupRecipe(row pgx.Row) (*domain.BackupRecipe, error) {
	recipe, err := scanBackupRecipe(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup recipe: %w", err)
	}
	return recipe, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupPolicy(row pgx.Row) (*domain.BackupPolicy, error) {
	policy, err := scanBackupPolicy(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup policy: %w", err)
	}
	return policy, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupRepository(row pgx.Row) (*domain.BackupRepository, error) {
	repo, err := scanBackupRepository(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup repository: %w", err)
	}
	return repo, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupDefinition(row pgx.Row) (*domain.BackupDefinition, error) {
	definition, err := scanBackupDefinition(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup definition: %w", err)
	}
	return definition, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupRun(row pgx.Row) (*domain.BackupRun, error) {
	run, err := scanBackupRun(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup run: %w", err)
	}
	return run, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupRestore(row pgx.Row) (*domain.BackupRestoreRun, error) {
	restore, err := scanBackupRestore(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup restore: %w", err)
	}
	return restore, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupRetentionRun(row pgx.Row) (*domain.BackupRetentionRun, error) {
	run, err := scanBackupRetentionRun(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup retention run: %w", err)
	}
	return run, nil
}

func (r *PgBackupControlPlaneRepository) scanBackupVerification(row pgx.Row) (*domain.BackupVerificationRecord, error) {
	record, err := scanBackupVerification(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning backup verification: %w", err)
	}
	return record, nil
}

func scanBackupRecipe(row pgx.Row) (*domain.BackupRecipe, error) {
	recipe := &domain.BackupRecipe{}
	var policyID pgtype.UUID
	var includeJSON, excludeJSON, metadataJSON []byte
	if err := row.Scan(&recipe.ID, &recipe.Name, &recipe.Version, &recipe.Backend, &recipe.RepositoryID, &policyID, &recipe.TargetRef, &includeJSON, &excludeJSON, &recipe.VerificationMode, &metadataJSON, &recipe.CreatedAt, &recipe.UpdatedAt); err != nil {
		return nil, err
	}
	recipe.PolicyID = uuidPtrFromPG(policyID)
	if err := unmarshalJSON(includeJSON, &recipe.Include, "backup recipe include paths"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(excludeJSON, &recipe.Exclude, "backup recipe exclude paths"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &recipe.Metadata, "backup recipe metadata"); err != nil {
		return nil, err
	}
	return recipe, nil
}

func scanBackupRecipeRows(rows pgx.Rows) ([]domain.BackupRecipe, error) {
	out := make([]domain.BackupRecipe, 0)
	for rows.Next() {
		recipe, err := scanBackupRecipe(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup recipe row: %w", err)
		}
		out = append(out, *recipe)
	}
	return out, rows.Err()
}

func scanBackupPolicy(row pgx.Row) (*domain.BackupPolicy, error) {
	policy := &domain.BackupPolicy{}
	var metadataJSON []byte
	if err := row.Scan(&policy.ID, &policy.Name, &policy.RequireVerification, &policy.VerificationMode, &metadataJSON, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &policy.Metadata, "backup policy metadata"); err != nil {
		return nil, err
	}
	return policy, nil
}

func scanBackupPolicyRows(rows pgx.Rows) ([]domain.BackupPolicy, error) {
	out := make([]domain.BackupPolicy, 0)
	for rows.Next() {
		policy, err := scanBackupPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup policy row: %w", err)
		}
		out = append(out, *policy)
	}
	return out, rows.Err()
}

func scanBackupRepository(row pgx.Row) (*domain.BackupRepository, error) {
	repo := &domain.BackupRepository{}
	var metadataJSON []byte
	if err := row.Scan(&repo.ID, &repo.Name, &repo.Backend, &repo.RepositoryURI, &repo.CredentialProfile, &metadataJSON, &repo.CreatedAt, &repo.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &repo.Metadata, "backup repository metadata"); err != nil {
		return nil, err
	}
	return repo, nil
}

func scanBackupRepositoryRows(rows pgx.Rows) ([]domain.BackupRepository, error) {
	out := make([]domain.BackupRepository, 0)
	for rows.Next() {
		repo, err := scanBackupRepository(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup repository row: %w", err)
		}
		out = append(out, *repo)
	}
	return out, rows.Err()
}

func scanBackupDefinition(row pgx.Row) (*domain.BackupDefinition, error) {
	definition := &domain.BackupDefinition{}
	var tenantID, environmentID pgtype.UUID
	var restoreTargetRulesJSON, executorLabelsJSON, capabilityRequirementsJSON, labelsJSON, metadataJSON []byte
	if err := row.Scan(&definition.ID, &definition.Name, &definition.RepositoryID, &definition.RepositoryName, &definition.PolicyID, &definition.PolicyName,
		&definition.RecipeID, &definition.RecipeName, &definition.RecipeVersion, &definition.ScheduleExpression, &definition.ScheduleEnabled,
		&definition.ScheduleJitterWindow, &tenantID, &definition.TenantName, &environmentID, &definition.EnvironmentName, &definition.OwnerPubkey,
		&definition.RequiresApproval, &definition.ApprovalPolicy, &restoreTargetRulesJSON, &executorLabelsJSON, &capabilityRequirementsJSON,
		&labelsJSON, &definition.Group, &metadataJSON, &definition.CreatedAt, &definition.UpdatedAt, &definition.CreatedBy); err != nil {
		return nil, err
	}
	definition.TenantID = uuidPtrFromPG(tenantID)
	definition.EnvironmentID = uuidPtrFromPG(environmentID)
	if err := unmarshalJSON(restoreTargetRulesJSON, &definition.RestoreTargetRules, "backup definition restore target rules"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(executorLabelsJSON, &definition.ExecutorLabels, "backup definition executor labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(capabilityRequirementsJSON, &definition.CapabilityRequirements, "backup definition capability requirements"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &definition.Labels, "backup definition labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &definition.Metadata, "backup definition metadata"); err != nil {
		return nil, err
	}
	return definition, nil
}

func scanBackupDefinitionRows(rows pgx.Rows) ([]domain.BackupDefinition, error) {
	out := make([]domain.BackupDefinition, 0)
	for rows.Next() {
		definition, err := scanBackupDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup definition row: %w", err)
		}
		out = append(out, *definition)
	}
	return out, rows.Err()
}

func scanBackupRun(row pgx.Row) (*domain.BackupRun, error) {
	run := &domain.BackupRun{}
	var policyID pgtype.UUID
	var publishJSON, metadataJSON []byte
	if err := row.Scan(&run.ID, &run.RecipeID, &run.RepositoryID, &policyID, &run.RequestedBy, &run.RequestEventID, &run.RequestKind, &run.RequestDTag,
		&run.Status, &run.Backend, &run.TargetRef, &run.SnapshotCreated, &run.SnapshotID, &run.VerificationStatus, &publishJSON, &run.Error,
		&metadataJSON, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, err
	}
	run.PolicyID = uuidPtrFromPG(policyID)
	if err := unmarshalJSON(publishJSON, &run.PublishSummary, "backup run publish summary"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &run.Metadata, "backup run metadata"); err != nil {
		return nil, err
	}
	return run, nil
}

func scanBackupRunRows(rows pgx.Rows) ([]domain.BackupRun, error) {
	out := make([]domain.BackupRun, 0)
	for rows.Next() {
		run, err := scanBackupRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup run row: %w", err)
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func scanBackupRestore(row pgx.Row) (*domain.BackupRestoreRun, error) {
	restore := &domain.BackupRestoreRun{}
	var policyID pgtype.UUID
	var evidenceJSON, publishJSON, metadataJSON []byte
	if err := row.Scan(&restore.ID, &restore.BackupRunID, &restore.RecipeID, &restore.RepositoryID, &policyID, &restore.SnapshotID, &restore.RestoreTargetRef,
		&restore.RequestedBy, &restore.RequestEventID, &restore.RequestKind, &restore.RequestDTag, &restore.ApprovalStatus, &restore.ApprovalEventID,
		&restore.ApprovedBy, &restore.ApprovedAt, &restore.ApprovalMessage, &restore.Status, &restore.Backend, &restore.VerificationStatus,
		&evidenceJSON, &publishJSON, &restore.Error, &metadataJSON, &restore.StartedAt, &restore.FinishedAt, &restore.CreatedAt, &restore.UpdatedAt); err != nil {
		return nil, err
	}
	restore.PolicyID = uuidPtrFromPG(policyID)
	if err := unmarshalJSON(evidenceJSON, &restore.Evidence, "backup restore evidence"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(publishJSON, &restore.PublishSummary, "backup restore publish summary"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &restore.Metadata, "backup restore metadata"); err != nil {
		return nil, err
	}
	return restore, nil
}

func scanBackupRestoreRows(rows pgx.Rows) ([]domain.BackupRestoreRun, error) {
	out := make([]domain.BackupRestoreRun, 0)
	for rows.Next() {
		restore, err := scanBackupRestore(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup restore row: %w", err)
		}
		out = append(out, *restore)
	}
	return out, rows.Err()
}

func scanBackupRetentionRun(row pgx.Row) (*domain.BackupRetentionRun, error) {
	run := &domain.BackupRetentionRun{}
	var policyID pgtype.UUID
	var evidenceJSON, publishJSON, metadataJSON []byte
	if err := row.Scan(&run.ID, &run.RepositoryID, &policyID, &run.RequestedBy, &run.RequestEventID, &run.RequestKind, &run.RequestDTag,
		&run.Status, &run.Backend, &run.DryRun, &evidenceJSON, &publishJSON, &run.Error, &metadataJSON, &run.StartedAt, &run.FinishedAt,
		&run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, err
	}
	run.PolicyID = uuidPtrFromPG(policyID)
	if err := unmarshalJSON(evidenceJSON, &run.Evidence, "backup retention run evidence"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(publishJSON, &run.PublishSummary, "backup retention run publish summary"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &run.Metadata, "backup retention run metadata"); err != nil {
		return nil, err
	}
	return run, nil
}

func scanBackupRetentionRunRows(rows pgx.Rows) ([]domain.BackupRetentionRun, error) {
	out := make([]domain.BackupRetentionRun, 0)
	for rows.Next() {
		run, err := scanBackupRetentionRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup retention run row: %w", err)
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func scanBackupVerification(row pgx.Row) (*domain.BackupVerificationRecord, error) {
	record := &domain.BackupVerificationRecord{}
	var evidenceJSON, publishJSON []byte
	if err := row.Scan(&record.ID, &record.BackupRunID, &record.Mode, &record.Status, &record.Verified, &evidenceJSON, &record.Error, &publishJSON, &record.VerifiedAt, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(evidenceJSON, &record.Evidence, "backup verification evidence"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(publishJSON, &record.PublishSummary, "backup verification publish summary"); err != nil {
		return nil, err
	}
	return record, nil
}

func marshalBackupDefinitionJSON(definition *domain.BackupDefinition) ([]byte, []byte, []byte, []byte, []byte, error) {
	restoreTargetRulesJSON, err := marshalJSONObject(definition.RestoreTargetRules, "backup definition restore target rules")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	executorLabelsJSON, err := marshalJSONArray(definition.ExecutorLabels, "backup definition executor labels")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	capabilityRequirementsJSON, err := marshalJSONArray(definition.CapabilityRequirements, "backup definition capability requirements")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	labelsJSON, err := marshalJSONObject(definition.Labels, "backup definition labels")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	metadataJSON, err := marshalJSONObject(definition.Metadata, "backup definition metadata")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return restoreTargetRulesJSON, executorLabelsJSON, capabilityRequirementsJSON, labelsJSON, metadataJSON, nil
}

func marshalBackupRunJSON(run *domain.BackupRun) ([]byte, []byte, error) {
	publishJSON, err := marshalJSONObject(run.PublishSummary, "backup run publish summary")
	if err != nil {
		return nil, nil, err
	}
	metadataJSON, err := marshalJSONObject(run.Metadata, "backup run metadata")
	if err != nil {
		return nil, nil, err
	}
	return publishJSON, metadataJSON, nil
}

func marshalBackupRestoreJSON(restore *domain.BackupRestoreRun) ([]byte, []byte, []byte, error) {
	evidenceJSON, err := marshalJSONObject(restore.Evidence, "backup restore evidence")
	if err != nil {
		return nil, nil, nil, err
	}
	publishJSON, err := marshalJSONObject(restore.PublishSummary, "backup restore publish summary")
	if err != nil {
		return nil, nil, nil, err
	}
	metadataJSON, err := marshalJSONObject(restore.Metadata, "backup restore metadata")
	if err != nil {
		return nil, nil, nil, err
	}
	return evidenceJSON, publishJSON, metadataJSON, nil
}

func marshalBackupRetentionRunJSON(run *domain.BackupRetentionRun) ([]byte, []byte, []byte, error) {
	evidenceJSON, err := marshalJSONObject(run.Evidence, "backup retention run evidence")
	if err != nil {
		return nil, nil, nil, err
	}
	publishJSON, err := marshalJSONObject(run.PublishSummary, "backup retention run publish summary")
	if err != nil {
		return nil, nil, nil, err
	}
	metadataJSON, err := marshalJSONObject(run.Metadata, "backup retention run metadata")
	if err != nil {
		return nil, nil, nil, err
	}
	return evidenceJSON, publishJSON, metadataJSON, nil
}

func marshalJSONObject(value map[string]any, fieldName string) ([]byte, error) {
	if value == nil {
		return []byte(`{}`), nil
	}
	return marshalJSON(value, fieldName)
}

func marshalJSONArray(value []string, fieldName string) ([]byte, error) {
	if value == nil {
		return []byte(`[]`), nil
	}
	return marshalJSON(value, fieldName)
}

func setBackupTimes(createdAt, updatedAt *time.Time) {
	now := time.Now().UTC()
	if createdAt.IsZero() {
		*createdAt = now
	}
	*updatedAt = now
}

func backupLimitOffset(limit, offset int) (int, int) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
