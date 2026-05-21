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
const backupRunColumns = `id, recipe_id, repository_id, policy_id, requested_by, request_event_id, request_kind, request_d_tag, status, backend, target_ref, snapshot_created, snapshot_id, verification_status, publish_summary, error, metadata, started_at, finished_at, created_at, updated_at`
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

func marshalBackupRunJSON(run *domain.BackupRun) ([]byte, []byte, error) {
	publishJSON, err := marshalJSON(run.PublishSummary, "backup run publish summary")
	if err != nil {
		return nil, nil, err
	}
	metadataJSON, err := marshalJSON(run.Metadata, "backup run metadata")
	if err != nil {
		return nil, nil, err
	}
	return publishJSON, metadataJSON, nil
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
