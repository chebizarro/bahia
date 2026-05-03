package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgToolProvisioningRepository is a PostgreSQL implementation of ToolProvisioningRepository.
type PgToolProvisioningRepository struct {
	pool pgQueryer
}

func NewPgToolProvisioningRepository(pool *pgxpool.Pool) *PgToolProvisioningRepository {
	return newPgToolProvisioningRepositoryWithDB(pool)
}

func newPgToolProvisioningRepositoryWithDB(db pgQueryer) *PgToolProvisioningRepository {
	return &PgToolProvisioningRepository{pool: db}
}

const toolIntentColumns = `id, service_id, environment_id, requested_tools, resolved_tools, security_scan_results, toolset_hash, status, approval_required, approval_flags, approved_by, approved_at, nostr_event_id, requester_pubkey, created_at`
const toolRunColumns = `id, intent_id, base_image_digest, built_image_digest, artifact_id, build_log_url, status, started_at, completed_at, error_message`
const toolProfileStateColumns = `service_id, environment_id, current_toolset_hash, current_image_digest, installed_tools, previous_image_digest, updated_at`
const toolDenylistColumns = `package_name, manager, reason, source, blocked_at, blocked_by`

func (r *PgToolProvisioningRepository) CreateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = time.Now().UTC()
	}
	requestedJSON, err := marshalJSON(intent.RequestedTools, "tool provision requested tools")
	if err != nil {
		return err
	}
	resolvedJSON, err := marshalJSON(intent.ResolvedTools, "tool provision resolved tools")
	if err != nil {
		return err
	}
	scanJSON, err := marshalJSON(intent.SecurityScanResults, "tool provision security scan results")
	if err != nil {
		return err
	}
	flagsJSON, err := marshalJSON(intent.ApprovalFlags, "tool provision approval flags")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO tool_provision_intents (`+toolIntentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, intent.ID, intent.ServiceID, intent.EnvironmentID, requestedJSON, resolvedJSON, scanJSON, intent.ToolsetHash,
		intent.Status, intent.ApprovalRequired, flagsJSON, intent.ApprovedBy, intent.ApprovedAt,
		intent.NostrEventID, intent.RequesterPubkey, intent.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting tool provision intent: %w", err)
	}
	return nil
}

func (r *PgToolProvisioningRepository) scanIntent(row pgx.Row) (*domain.ToolProvisionIntent, error) {
	intent := &domain.ToolProvisionIntent{}
	var requestedJSON, resolvedJSON, scanJSON, flagsJSON []byte
	if err := row.Scan(&intent.ID, &intent.ServiceID, &intent.EnvironmentID, &requestedJSON, &resolvedJSON,
		&scanJSON, &intent.ToolsetHash, &intent.Status, &intent.ApprovalRequired, &flagsJSON, &intent.ApprovedBy,
		&intent.ApprovedAt, &intent.NostrEventID, &intent.RequesterPubkey, &intent.CreatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(requestedJSON, &intent.RequestedTools, "tool provision requested tools"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(resolvedJSON, &intent.ResolvedTools, "tool provision resolved tools"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(scanJSON, &intent.SecurityScanResults, "tool provision security scan results"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(flagsJSON, &intent.ApprovalFlags, "tool provision approval flags"); err != nil {
		return nil, err
	}
	return intent, nil
}

func (r *PgToolProvisioningRepository) GetIntent(ctx context.Context, id uuid.UUID) (*domain.ToolProvisionIntent, error) {
	intent, err := r.scanIntent(r.pool.QueryRow(ctx, `SELECT `+toolIntentColumns+` FROM tool_provision_intents WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying tool provision intent by id: %w", err)
	}
	return intent, nil
}

func (r *PgToolProvisioningRepository) UpdateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error {
	requestedJSON, err := marshalJSON(intent.RequestedTools, "tool provision requested tools")
	if err != nil {
		return err
	}
	resolvedJSON, err := marshalJSON(intent.ResolvedTools, "tool provision resolved tools")
	if err != nil {
		return err
	}
	scanJSON, err := marshalJSON(intent.SecurityScanResults, "tool provision security scan results")
	if err != nil {
		return err
	}
	flagsJSON, err := marshalJSON(intent.ApprovalFlags, "tool provision approval flags")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `
		UPDATE tool_provision_intents
		SET service_id = $2,
			environment_id = $3,
			requested_tools = $4,
			resolved_tools = $5,
			security_scan_results = $6,
			toolset_hash = $7,
			status = $8,
			approval_required = $9,
			approval_flags = $10,
			approved_by = $11,
			approved_at = $12,
			nostr_event_id = $13,
			requester_pubkey = $14,
			created_at = $15
		WHERE id = $1
	`, intent.ID, intent.ServiceID, intent.EnvironmentID, requestedJSON, resolvedJSON, scanJSON, intent.ToolsetHash,
		intent.Status, intent.ApprovalRequired, flagsJSON, intent.ApprovedBy, intent.ApprovedAt,
		intent.NostrEventID, intent.RequesterPubkey, intent.CreatedAt)
	if err != nil {
		return fmt.Errorf("updating tool provision intent: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating tool provision intent %s: %w", intent.ID, ErrNotFound)
	}
	return nil
}

func (r *PgToolProvisioningRepository) ListPendingApprovalIntents(ctx context.Context) ([]domain.ToolProvisionIntent, error) {
	return r.ListIntentsByStatus(ctx, domain.ToolProvisionStatusAwaitingApproval)
}

func (r *PgToolProvisioningRepository) ListIntentsByStatus(ctx context.Context, statuses ...domain.ToolProvisionStatus) ([]domain.ToolProvisionIntent, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	params := make([]any, 0, len(statuses))
	placeholders := make([]string, 0, len(statuses))
	for i, status := range statuses {
		params = append(params, status)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
	}
	query := `
		SELECT ` + toolIntentColumns + `
		FROM tool_provision_intents
		WHERE status IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query, params...)
	if err != nil {
		return nil, fmt.Errorf("listing tool provision intents by status: %w", err)
	}
	defer rows.Close()

	var intents []domain.ToolProvisionIntent
	for rows.Next() {
		intent, err := r.scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning tool provision intent: %w", err)
		}
		intents = append(intents, *intent)
	}
	return intents, rows.Err()
}

func (r *PgToolProvisioningRepository) CreateRun(ctx context.Context, run *domain.ToolProvisionRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tool_provision_runs (`+toolRunColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, run.ID, run.IntentID, run.BaseImageDigest, run.BuiltImageDigest, run.ArtifactID, run.BuildLogURL,
		run.Status, run.StartedAt, run.CompletedAt, run.ErrorMessage)
	if err != nil {
		return fmt.Errorf("inserting tool provision run: %w", err)
	}
	return nil
}

func (r *PgToolProvisioningRepository) scanRun(row pgx.Row) (*domain.ToolProvisionRun, error) {
	run := &domain.ToolProvisionRun{}
	if err := row.Scan(&run.ID, &run.IntentID, &run.BaseImageDigest, &run.BuiltImageDigest, &run.ArtifactID,
		&run.BuildLogURL, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ErrorMessage); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *PgToolProvisioningRepository) GetRun(ctx context.Context, id uuid.UUID) (*domain.ToolProvisionRun, error) {
	run, err := r.scanRun(r.pool.QueryRow(ctx, `SELECT `+toolRunColumns+` FROM tool_provision_runs WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying tool provision run by id: %w", err)
	}
	return run, nil
}

func (r *PgToolProvisioningRepository) UpdateRun(ctx context.Context, run *domain.ToolProvisionRun) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE tool_provision_runs
		SET intent_id = $2,
			base_image_digest = $3,
			built_image_digest = $4,
			artifact_id = $5,
			build_log_url = $6,
			status = $7,
			started_at = $8,
			completed_at = $9,
			error_message = $10
		WHERE id = $1
	`, run.ID, run.IntentID, run.BaseImageDigest, run.BuiltImageDigest, run.ArtifactID, run.BuildLogURL,
		run.Status, run.StartedAt, run.CompletedAt, run.ErrorMessage)
	if err != nil {
		return fmt.Errorf("updating tool provision run: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating tool provision run %s: %w", run.ID, ErrNotFound)
	}
	return nil
}

func (r *PgToolProvisioningRepository) GetProfileState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.ToolProfileState, error) {
	state, err := r.scanProfileState(r.pool.QueryRow(ctx, `
		SELECT `+toolProfileStateColumns+` FROM tool_profile_state
		WHERE service_id = $1 AND environment_id = $2
	`, serviceID, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying tool profile state: %w", err)
	}
	return state, nil
}

func (r *PgToolProvisioningRepository) scanProfileState(row pgx.Row) (*domain.ToolProfileState, error) {
	state := &domain.ToolProfileState{}
	var installedJSON []byte
	if err := row.Scan(&state.ServiceID, &state.EnvironmentID, &state.CurrentToolsetHash, &state.CurrentImageDigest,
		&installedJSON, &state.PreviousImageDigest, &state.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(installedJSON, &state.InstalledTools, "tool profile installed tools"); err != nil {
		return nil, err
	}
	return state, nil
}

func (r *PgToolProvisioningRepository) UpsertProfileState(ctx context.Context, state *domain.ToolProfileState) error {
	state.UpdatedAt = time.Now().UTC()
	installedJSON, err := marshalJSON(state.InstalledTools, "tool profile installed tools")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO tool_profile_state (`+toolProfileStateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (service_id, environment_id) DO UPDATE SET
			current_toolset_hash = EXCLUDED.current_toolset_hash,
			current_image_digest = EXCLUDED.current_image_digest,
			installed_tools = EXCLUDED.installed_tools,
			previous_image_digest = EXCLUDED.previous_image_digest,
			updated_at = EXCLUDED.updated_at
	`, state.ServiceID, state.EnvironmentID, state.CurrentToolsetHash, state.CurrentImageDigest,
		installedJSON, state.PreviousImageDigest, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting tool profile state: %w", err)
	}
	return nil
}

func (r *PgToolProvisioningRepository) AddToDenylist(ctx context.Context, entry *domain.ToolDenylistEntry) error {
	if entry.BlockedAt.IsZero() {
		entry.BlockedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tool_denylist (`+toolDenylistColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (package_name, manager) DO UPDATE SET
			reason = EXCLUDED.reason,
			source = EXCLUDED.source,
			blocked_at = EXCLUDED.blocked_at,
			blocked_by = EXCLUDED.blocked_by
	`, entry.PackageName, entry.Manager, entry.Reason, entry.Source, entry.BlockedAt, entry.BlockedBy)
	if err != nil {
		return fmt.Errorf("upserting denylist entry: %w", err)
	}
	return nil
}

func (r *PgToolProvisioningRepository) RemoveFromDenylist(ctx context.Context, packageName, manager string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM tool_denylist WHERE package_name = $1 AND manager = $2`, packageName, manager)
	if err != nil {
		return fmt.Errorf("removing denylist entry: %w", err)
	}
	return nil
}

func (r *PgToolProvisioningRepository) IsDenylisted(ctx context.Context, packageName, manager string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tool_denylist WHERE package_name = $1 AND manager = $2
		)
	`, packageName, manager).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking denylist entry: %w", err)
	}
	return exists, nil
}

func (r *PgToolProvisioningRepository) ListDenylist(ctx context.Context) ([]domain.ToolDenylistEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+toolDenylistColumns+` FROM tool_denylist ORDER BY blocked_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing denylist entries: %w", err)
	}
	defer rows.Close()

	var entries []domain.ToolDenylistEntry
	for rows.Next() {
		entry := domain.ToolDenylistEntry{}
		if err := rows.Scan(&entry.PackageName, &entry.Manager, &entry.Reason, &entry.Source, &entry.BlockedAt, &entry.BlockedBy); err != nil {
			return nil, fmt.Errorf("scanning denylist entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *PgToolProvisioningRepository) LogApproval(ctx context.Context, intentID uuid.UUID, action, actorPubkey, reason string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tool_approval_log (id, intent_id, action, actor_pubkey, reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, uuid.New(), intentID, action, actorPubkey, reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("inserting tool approval log: %w", err)
	}
	return nil
}
