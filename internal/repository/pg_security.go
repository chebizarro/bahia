package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

type securityDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgSecurityRepository implements SecurityRepository using PostgreSQL.
type PgSecurityRepository struct {
	pool securityDB
}

func NewPgSecurityRepository(pool *pgxpool.Pool) *PgSecurityRepository {
	return newPgSecurityRepositoryWithDB(pool)
}

func newPgSecurityRepositoryWithDB(db securityDB) *PgSecurityRepository {
	return &PgSecurityRepository{pool: db}
}

const securityTargetColumns = `id, target_type, target_key, target_key_hash, display, subject, package, purl, repository_url, commit_hash, metadata, created_at, updated_at`
const securityRunColumns = `id, target_id, target_key_hash, status, trigger_kind, requested_by, request_event_id, request_d_tag, osv_query_count, finding_count, critical_count, high_count, moderate_count, low_count, unknown_count, unsupported_count, unsupported_reasons, publish_state, error, metadata, started_at, finished_at, created_at, updated_at`
const securityLatestColumns = `target_key_hash, target_id, run_id, status, finding_count, critical_count, high_count, moderate_count, low_count, unknown_count, scanned_at, updated_at`
const securityFindingColumns = `id, run_id, target_key_hash, finding_key, finding_key_hash, osv_id, cve, summary, details, severity, package, aliases, references, withdrawn_at, raw_modified, metadata, created_at, updated_at`
const securityScheduleColumns = `id, policy_id, target_id, target_key_hash, enabled, interval_seconds, next_due_at, lease_until, leased_by, last_dispatched_at, last_run_id, metadata, created_at, updated_at`
const securityBreachColumns = `id, policy_id, target_key_hash, fingerprint, previous_fingerprint, enforcement, violated_rules, critical_count, high_count, moderate_count, low_count, unknown_count, osv_ids, notification_status, first_seen_at, last_seen_at, resolved_at, metadata, created_at, updated_at`
const securityCacheColumns = `osv_id, summary, severity, aliases, raw, cached_at, expires_at, withdrawn_at`
const securityPublicationColumns = `id, observable_type, run_id, target_key_hash, finding_id, breach_id, event_kind, d_tag, schema, publish_state, event_id, attempt_count, last_error, next_retry_at, published_at, created_at, updated_at`

func (r *PgSecurityRepository) UpsertSecurityTarget(ctx context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error) {
	if target == nil {
		return nil, fmt.Errorf("security target is required")
	}
	if target.ID == uuid.Nil {
		target.ID = uuid.New()
	}
	if strings.TrimSpace(target.TargetKey) == "" || strings.TrimSpace(target.TargetKeyHash) == "" {
		return nil, fmt.Errorf("security target key and hash are required")
	}
	now := time.Now().UTC()
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.UpdatedAt = now
	subjectJSON, err := marshalJSON(target.Subject, "security target subject")
	if err != nil {
		return nil, err
	}
	packageJSON, err := marshalJSON(target.Package, "security target package")
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalJSON(target.Metadata, "security target metadata")
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO security_scan_targets (`+securityTargetColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11, '{}'::jsonb),$12,$13)
		ON CONFLICT (target_key_hash) DO UPDATE SET
			target_key = EXCLUDED.target_key,
			display = EXCLUDED.display,
			subject = EXCLUDED.subject,
			package = EXCLUDED.package,
			purl = EXCLUDED.purl,
			repository_url = EXCLUDED.repository_url,
			commit_hash = EXCLUDED.commit_hash,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
		RETURNING `+securityTargetColumns,
		target.ID, target.Type, target.TargetKey, target.TargetKeyHash, target.Display, subjectJSON, packageJSON,
		nilIfEmpty(target.PURL), nilIfEmpty(target.RepositoryURL), nilIfEmpty(target.CommitHash), metadataJSON, target.CreatedAt, target.UpdatedAt)
	stored, err := scanSecurityTarget(row)
	if err != nil {
		return nil, fmt.Errorf("upserting security target: %w", err)
	}
	*target = *stored
	return stored, nil
}

func (r *PgSecurityRepository) GetSecurityTargetByHash(ctx context.Context, targetKeyHash string) (*domain.SecurityTarget, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+securityTargetColumns+` FROM security_scan_targets WHERE target_key_hash = $1`, strings.TrimSpace(targetKeyHash))
	return scanSecurityTarget(row)
}

func (r *PgSecurityRepository) ListSecurityTargets(ctx context.Context, targetType domain.SecurityTargetType, limit int) ([]domain.SecurityTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if targetType == "" {
		rows, err = r.pool.Query(ctx, `SELECT `+securityTargetColumns+` FROM security_scan_targets ORDER BY created_at DESC LIMIT $1`, limit)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+securityTargetColumns+` FROM security_scan_targets WHERE target_type = $1 ORDER BY created_at DESC LIMIT $2`, targetType, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("listing security targets: %w", err)
	}
	defer rows.Close()
	return scanSecurityTargetRows(rows)
}

func (r *PgSecurityRepository) CreateSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error {
	if run == nil {
		return fmt.Errorf("security scan run is required")
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.Status == "" {
		run.Status = domain.SecurityScanAccepted
	}
	if run.PublishState == "" {
		run.PublishState = domain.SecurityPublicationPending
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	unsupportedJSON, err := marshalJSON(run.UnsupportedReasons, "security scan run unsupported reasons")
	if err != nil {
		return err
	}
	metadataJSON, err := marshalJSON(run.Metadata, "security scan run metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO security_scan_runs (`+securityRunColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,COALESCE($17, '{}'::jsonb),$18,$19,COALESCE($20, '{}'::jsonb),$21,$22,$23,$24)
	`, run.ID, run.TargetID, run.TargetKeyHash, run.Status, run.Trigger, run.RequestedBy, run.RequestEventID, run.RequestDTag,
		run.OSVQueryCount, run.FindingCount, run.SeverityCounts.Critical, run.SeverityCounts.High, run.SeverityCounts.Moderate,
		run.SeverityCounts.Low, run.SeverityCounts.Unknown, run.UnsupportedCount, unsupportedJSON, run.PublishState,
		nilIfEmpty(run.Error), metadataJSON, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating security scan run: %w", err)
	}
	return nil
}

func (r *PgSecurityRepository) GetSecurityScanRun(ctx context.Context, id uuid.UUID) (*domain.SecurityScanRun, error) {
	return scanSecurityRun(r.pool.QueryRow(ctx, `SELECT `+securityRunColumns+` FROM security_scan_runs WHERE id = $1`, id))
}

func (r *PgSecurityRepository) ListSecurityScanRuns(ctx context.Context, targetKeyHash string, limit int) ([]domain.SecurityScanRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+securityRunColumns+` FROM security_scan_runs WHERE target_key_hash = $1 ORDER BY created_at DESC LIMIT $2`, strings.TrimSpace(targetKeyHash), limit)
	if err != nil {
		return nil, fmt.Errorf("listing security scan runs: %w", err)
	}
	defer rows.Close()
	return scanSecurityRunRows(rows)
}

func (r *PgSecurityRepository) UpdateSecurityScanRunStatus(ctx context.Context, id uuid.UUID, status domain.SecurityScanStatus, errorMessage string, finishedAt *time.Time) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE security_scan_runs
		SET status = $2, error = $3, finished_at = COALESCE($4, finished_at), updated_at = $5
		WHERE id = $1 AND status NOT IN ('completed', 'failed', 'cancelled')
	`, id, status, nilIfEmpty(errorMessage), finishedAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating security scan run status: %w", err)
	}
	if cmd.RowsAffected() > 0 {
		return nil
	}
	existing, getErr := r.GetSecurityScanRun(ctx, id)
	if getErr != nil {
		return getErr
	}
	if existing.Status.IsTerminal() {
		return fmt.Errorf("security scan run %s is terminal with status %s", id, existing.Status)
	}
	return fmt.Errorf("updating security scan run %s: %w", id, ErrNotFound)
}

func (r *PgSecurityRepository) UpsertSecurityTargetLatest(ctx context.Context, latest *domain.SecurityTargetLatest) error {
	if latest == nil {
		return fmt.Errorf("security target latest is required")
	}
	if latest.UpdatedAt.IsZero() {
		latest.UpdatedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO security_target_latest (`+securityLatestColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (target_key_hash) DO UPDATE SET
			target_id = EXCLUDED.target_id,
			run_id = EXCLUDED.run_id,
			status = EXCLUDED.status,
			finding_count = EXCLUDED.finding_count,
			critical_count = EXCLUDED.critical_count,
			high_count = EXCLUDED.high_count,
			moderate_count = EXCLUDED.moderate_count,
			low_count = EXCLUDED.low_count,
			unknown_count = EXCLUDED.unknown_count,
			scanned_at = EXCLUDED.scanned_at,
			updated_at = EXCLUDED.updated_at
	`, latest.TargetKeyHash, latest.TargetID, latest.RunID, latest.Status, latest.FindingCount, latest.SeverityCounts.Critical,
		latest.SeverityCounts.High, latest.SeverityCounts.Moderate, latest.SeverityCounts.Low, latest.SeverityCounts.Unknown,
		latest.ScannedAt, latest.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting security target latest: %w", err)
	}
	return nil
}

func (r *PgSecurityRepository) UpsertSecurityFindings(ctx context.Context, findings []domain.SecurityOSVFinding) error {
	for i := range findings {
		finding := &findings[i]
		if finding.ID == uuid.Nil {
			finding.ID = uuid.New()
		}
		now := time.Now().UTC()
		if finding.CreatedAt.IsZero() {
			finding.CreatedAt = now
		}
		finding.UpdatedAt = now
		pkgJSON, err := marshalJSON(finding.Package, "security finding package")
		if err != nil {
			return err
		}
		aliasesJSON, err := marshalJSON(finding.Aliases, "security finding aliases")
		if err != nil {
			return err
		}
		refsJSON, err := marshalJSON(finding.References, "security finding references")
		if err != nil {
			return err
		}
		metadataJSON, err := marshalJSON(finding.Metadata, "security finding metadata")
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO security_findings (`+securityFindingColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11, '{}'::jsonb),COALESCE($12, '[]'::jsonb),COALESCE($13, '[]'::jsonb),$14,$15,COALESCE($16, '{}'::jsonb),$17,$18)
			ON CONFLICT (run_id, finding_key_hash) DO UPDATE SET
				osv_id = EXCLUDED.osv_id,
				cve = EXCLUDED.cve,
				summary = EXCLUDED.summary,
				details = EXCLUDED.details,
				severity = EXCLUDED.severity,
				package = EXCLUDED.package,
				aliases = EXCLUDED.aliases,
				references = EXCLUDED.references,
				withdrawn_at = EXCLUDED.withdrawn_at,
				raw_modified = EXCLUDED.raw_modified,
				metadata = EXCLUDED.metadata,
				updated_at = EXCLUDED.updated_at
		`, finding.ID, finding.RunID, finding.TargetKeyHash, finding.FindingKey, finding.FindingKeyHash, finding.OSVID, nilIfEmpty(finding.CVE),
			nilIfEmpty(finding.Summary), nilIfEmpty(finding.Details), finding.Severity, pkgJSON, aliasesJSON, refsJSON, finding.WithdrawnAt,
			nilIfEmpty(finding.RawModified), metadataJSON, finding.CreatedAt, finding.UpdatedAt)
		if err != nil {
			return fmt.Errorf("upserting security finding %s: %w", finding.FindingKeyHash, err)
		}
	}
	return nil
}

func (r *PgSecurityRepository) ListSecurityFindings(ctx context.Context, runID uuid.UUID) ([]domain.SecurityOSVFinding, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+securityFindingColumns+` FROM security_findings WHERE run_id = $1 ORDER BY severity, osv_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("listing security findings: %w", err)
	}
	defer rows.Close()
	return scanSecurityFindingRows(rows)
}

func (r *PgSecurityRepository) UpsertSecurityScanSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error {
	if schedule == nil {
		return fmt.Errorf("security scan schedule is required")
	}
	if schedule.ID == uuid.Nil {
		schedule.ID = uuid.New()
	}
	now := time.Now().UTC()
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	schedule.UpdatedAt = now
	metadataJSON, err := marshalJSON(schedule.Metadata, "security scan schedule metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO security_scan_schedules (`+securityScheduleColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,COALESCE($12, '{}'::jsonb),$13,$14)
		ON CONFLICT (policy_id, target_key_hash) DO UPDATE SET
			target_id = EXCLUDED.target_id,
			enabled = EXCLUDED.enabled,
			interval_seconds = EXCLUDED.interval_seconds,
			next_due_at = EXCLUDED.next_due_at,
			lease_until = EXCLUDED.lease_until,
			leased_by = EXCLUDED.leased_by,
			last_dispatched_at = EXCLUDED.last_dispatched_at,
			last_run_id = EXCLUDED.last_run_id,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, schedule.ID, schedule.PolicyID, schedule.TargetID, schedule.TargetKeyHash, schedule.Enabled, schedule.IntervalSeconds,
		schedule.NextDueAt, schedule.LeaseUntil, nilIfEmpty(schedule.LeasedBy), schedule.LastDispatchedAt, schedule.LastRunID,
		metadataJSON, schedule.CreatedAt, schedule.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting security scan schedule: %w", err)
	}
	return nil
}

func (r *PgSecurityRepository) ClaimDueSecurityScanSchedules(ctx context.Context, now time.Time, limit int, leasedBy string, leaseUntil time.Time) ([]domain.SecurityScanSchedule, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE security_scan_schedules
		SET lease_until = $3, leased_by = $4, updated_at = $1
		WHERE id IN (
			SELECT id FROM security_scan_schedules
			WHERE enabled = true
			  AND next_due_at <= $1
			  AND (lease_until IS NULL OR lease_until <= $1)
			ORDER BY next_due_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+securityScheduleColumns, now, limit, leaseUntil, strings.TrimSpace(leasedBy))
	if err != nil {
		return nil, fmt.Errorf("claiming due security scan schedules: %w", err)
	}
	defer rows.Close()
	return scanSecurityScheduleRows(rows)
}

func (r *PgSecurityRepository) MarkSecurityScheduleDispatched(ctx context.Context, id uuid.UUID, runID uuid.UUID, dispatchedAt time.Time, nextDueAt time.Time) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE security_scan_schedules
		SET last_dispatched_at = $2, last_run_id = $3, next_due_at = $4, lease_until = NULL, leased_by = NULL, updated_at = $2
		WHERE id = $1
	`, id, dispatchedAt, runID, nextDueAt)
	if err != nil {
		return fmt.Errorf("marking security schedule dispatched: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("marking security schedule %s dispatched: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgSecurityRepository) RecordSecurityPolicyBreach(ctx context.Context, breach *domain.SecurityPolicyBreach) (domain.SecurityBreachRecordResult, error) {
	if breach == nil {
		return "", fmt.Errorf("security policy breach is required")
	}
	existing, err := r.GetActiveSecurityPolicyBreach(ctx, breach.PolicyID, breach.TargetKeyHash)
	if err != nil && err != ErrNotFound {
		return "", err
	}
	now := time.Now().UTC()
	if breach.FirstSeenAt.IsZero() {
		breach.FirstSeenAt = now
	}
	if breach.LastSeenAt.IsZero() {
		breach.LastSeenAt = now
	}
	if breach.NotificationStatus == "" {
		breach.NotificationStatus = domain.SecurityBreachNotificationPending
	}
	if existing != nil {
		result := domain.SecurityBreachRecordUnchanged
		previous := existing.PreviousFingerprint
		if existing.Fingerprint != breach.Fingerprint {
			result = domain.SecurityBreachRecordChanged
			previous = existing.Fingerprint
			breach.NotificationStatus = domain.SecurityBreachNotificationPending
		} else {
			breach.NotificationStatus = existing.NotificationStatus
		}
		breach.ID = existing.ID
		breach.FirstSeenAt = existing.FirstSeenAt
		breach.PreviousFingerprint = previous
		if err := r.updateBreach(ctx, breach); err != nil {
			return "", err
		}
		return result, nil
	}
	if breach.ID == uuid.Nil {
		breach.ID = uuid.New()
	}
	metadataJSON, violatedJSON, osvIDsJSON, err := marshalBreachJSON(breach)
	if err != nil {
		return "", err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO security_policy_breaches (`+securityBreachColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7, '[]'::jsonb),$8,$9,$10,$11,$12,COALESCE($13, '[]'::jsonb),$14,$15,$16,$17,COALESCE($18, '{}'::jsonb),$19,$20)
	`, breach.ID, breach.PolicyID, breach.TargetKeyHash, breach.Fingerprint, nilIfEmpty(breach.PreviousFingerprint), breach.Enforcement,
		violatedJSON, breach.SeverityCounts.Critical, breach.SeverityCounts.High, breach.SeverityCounts.Moderate,
		breach.SeverityCounts.Low, breach.SeverityCounts.Unknown, osvIDsJSON, breach.NotificationStatus, breach.FirstSeenAt,
		breach.LastSeenAt, breach.ResolvedAt, metadataJSON, now, now)
	if err != nil {
		return "", fmt.Errorf("recording security policy breach: %w", err)
	}
	return domain.SecurityBreachRecordNew, nil
}

func (r *PgSecurityRepository) ResolveSecurityPolicyBreach(ctx context.Context, policyID uuid.UUID, targetKeyHash string, resolvedAt time.Time) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE security_policy_breaches
		SET resolved_at = $3, updated_at = $3
		WHERE policy_id = $1 AND target_key_hash = $2 AND resolved_at IS NULL
	`, policyID, targetKeyHash, resolvedAt)
	if err != nil {
		return fmt.Errorf("resolving security policy breach: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("resolving security policy breach %s/%s: %w", policyID, targetKeyHash, ErrNotFound)
	}
	return nil
}

func (r *PgSecurityRepository) GetActiveSecurityPolicyBreach(ctx context.Context, policyID uuid.UUID, targetKeyHash string) (*domain.SecurityPolicyBreach, error) {
	return scanSecurityBreach(r.pool.QueryRow(ctx, `SELECT `+securityBreachColumns+` FROM security_policy_breaches WHERE policy_id = $1 AND target_key_hash = $2 AND resolved_at IS NULL`, policyID, targetKeyHash))
}

func (r *PgSecurityRepository) UpsertOSVVulnerabilityCache(ctx context.Context, cache *domain.OSVVulnerabilityCache) error {
	if cache == nil {
		return fmt.Errorf("OSV vulnerability cache is required")
	}
	aliasesJSON, err := marshalJSON(cache.Aliases, "OSV cache aliases")
	if err != nil {
		return err
	}
	rawJSON, err := marshalJSON(cache.Raw, "OSV cache raw")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO security_osv_vulnerability_cache (`+securityCacheColumns+`)
		VALUES ($1,$2,$3,COALESCE($4, '[]'::jsonb),COALESCE($5, '{}'::jsonb),$6,$7,$8)
		ON CONFLICT (osv_id) DO UPDATE SET
			summary = EXCLUDED.summary,
			severity = EXCLUDED.severity,
			aliases = EXCLUDED.aliases,
			raw = EXCLUDED.raw,
			cached_at = EXCLUDED.cached_at,
			expires_at = EXCLUDED.expires_at,
			withdrawn_at = EXCLUDED.withdrawn_at
	`, cache.OSVID, nilIfEmpty(cache.Summary), cache.Severity, aliasesJSON, rawJSON, cache.CachedAt, cache.ExpiresAt, cache.WithdrawnAt)
	if err != nil {
		return fmt.Errorf("upserting OSV vulnerability cache: %w", err)
	}
	return nil
}

func (r *PgSecurityRepository) GetOSVVulnerabilityCache(ctx context.Context, osvID string, now time.Time) (*domain.OSVVulnerabilityCache, error) {
	return scanOSVCache(r.pool.QueryRow(ctx, `SELECT `+securityCacheColumns+` FROM security_osv_vulnerability_cache WHERE osv_id = $1 AND expires_at > $2`, strings.TrimSpace(osvID), now))
}

func (r *PgSecurityRepository) PruneExpiredOSVVulnerabilityCache(ctx context.Context, now time.Time) (int64, error) {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM security_osv_vulnerability_cache WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("pruning expired OSV vulnerability cache: %w", err)
	}
	return cmd.RowsAffected(), nil
}

func (r *PgSecurityRepository) UpsertSecurityPublication(ctx context.Context, publication *domain.SecurityObservablePublication) error {
	if publication == nil {
		return fmt.Errorf("security observable publication is required")
	}
	if publication.ID == uuid.Nil {
		publication.ID = uuid.New()
	}
	if publication.PublishState == "" {
		publication.PublishState = domain.SecurityPublicationPending
	}
	now := time.Now().UTC()
	if publication.CreatedAt.IsZero() {
		publication.CreatedAt = now
	}
	publication.UpdatedAt = now
	_, err := r.pool.Exec(ctx, `
		INSERT INTO security_observable_publications (`+securityPublicationColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (observable_type, d_tag, event_kind) DO UPDATE SET
			run_id = EXCLUDED.run_id,
			target_key_hash = EXCLUDED.target_key_hash,
			finding_id = EXCLUDED.finding_id,
			breach_id = EXCLUDED.breach_id,
			schema = EXCLUDED.schema,
			publish_state = EXCLUDED.publish_state,
			event_id = EXCLUDED.event_id,
			attempt_count = EXCLUDED.attempt_count,
			last_error = EXCLUDED.last_error,
			next_retry_at = EXCLUDED.next_retry_at,
			published_at = EXCLUDED.published_at,
			updated_at = EXCLUDED.updated_at
	`, publication.ID, publication.ObservableType, publication.RunID, nilIfEmpty(publication.TargetKeyHash), publication.FindingID,
		publication.BreachID, publication.EventKind, publication.DTag, publication.Schema, publication.PublishState,
		nilIfEmpty(publication.EventID), publication.AttemptCount, nilIfEmpty(publication.LastError), publication.NextRetryAt,
		publication.PublishedAt, publication.CreatedAt, publication.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting security observable publication: %w", err)
	}
	return nil
}

func (r *PgSecurityRepository) UpdateSecurityPublicationState(ctx context.Context, id uuid.UUID, state domain.SecurityPublicationState, eventID, lastError string, nextRetryAt *time.Time, publishedAt *time.Time) error {
	cmd, err := r.pool.Exec(ctx, `
		UPDATE security_observable_publications
		SET publish_state = $2, event_id = $3, last_error = $4, next_retry_at = $5, published_at = COALESCE($6, published_at), attempt_count = attempt_count + 1, updated_at = $7
		WHERE id = $1
	`, id, state, nilIfEmpty(eventID), nilIfEmpty(lastError), nextRetryAt, publishedAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating security publication state: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating security publication %s: %w", id, ErrNotFound)
	}
	return nil
}

func (r *PgSecurityRepository) ListRetryableSecurityPublications(ctx context.Context, now time.Time, limit int) ([]domain.SecurityObservablePublication, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+securityPublicationColumns+`
		FROM security_observable_publications
		WHERE publish_state = 'failed_retryable'
		  AND (next_retry_at IS NULL OR next_retry_at <= $1)
		ORDER BY COALESCE(next_retry_at, created_at) ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("listing retryable security publications: %w", err)
	}
	defer rows.Close()
	return scanSecurityPublicationRows(rows)
}

func (r *PgSecurityRepository) updateBreach(ctx context.Context, breach *domain.SecurityPolicyBreach) error {
	metadataJSON, violatedJSON, osvIDsJSON, err := marshalBreachJSON(breach)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE security_policy_breaches
		SET fingerprint = $2, previous_fingerprint = $3, enforcement = $4, violated_rules = COALESCE($5, '[]'::jsonb),
			critical_count = $6, high_count = $7, moderate_count = $8, low_count = $9, unknown_count = $10,
			osv_ids = COALESCE($11, '[]'::jsonb), notification_status = $12, last_seen_at = $13,
			resolved_at = $14, metadata = COALESCE($15, '{}'::jsonb), updated_at = $16
		WHERE id = $1
	`, breach.ID, breach.Fingerprint, nilIfEmpty(breach.PreviousFingerprint), breach.Enforcement, violatedJSON,
		breach.SeverityCounts.Critical, breach.SeverityCounts.High, breach.SeverityCounts.Moderate, breach.SeverityCounts.Low,
		breach.SeverityCounts.Unknown, osvIDsJSON, breach.NotificationStatus, breach.LastSeenAt, breach.ResolvedAt,
		metadataJSON, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating security policy breach: %w", err)
	}
	return nil
}

func marshalBreachJSON(breach *domain.SecurityPolicyBreach) (metadataJSON, violatedJSON, osvIDsJSON []byte, err error) {
	metadataJSON, err = marshalJSON(breach.Metadata, "security policy breach metadata")
	if err != nil {
		return nil, nil, nil, err
	}
	violatedJSON, err = marshalJSON(breach.ViolatedRules, "security policy breach violated rules")
	if err != nil {
		return nil, nil, nil, err
	}
	osvIDsJSON, err = marshalJSON(breach.OSVIDs, "security policy breach osv ids")
	if err != nil {
		return nil, nil, nil, err
	}
	return metadataJSON, violatedJSON, osvIDsJSON, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanSecurityTarget(row scanner) (*domain.SecurityTarget, error) {
	var target domain.SecurityTarget
	var subjectJSON, packageJSON, metadataJSON []byte
	var purl, repoURL, commitHash pgtype.Text
	if err := row.Scan(&target.ID, &target.Type, &target.TargetKey, &target.TargetKeyHash, &target.Display, &subjectJSON, &packageJSON,
		&purl, &repoURL, &commitHash, &metadataJSON, &target.CreatedAt, &target.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	if err := unmarshalOptionalJSON(subjectJSON, &target.Subject, "security target subject"); err != nil {
		return nil, err
	}
	if err := unmarshalOptionalJSON(packageJSON, &target.Package, "security target package"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &target.Metadata, "security target metadata"); err != nil {
		return nil, err
	}
	target.PURL = textValue(purl)
	target.RepositoryURL = textValue(repoURL)
	target.CommitHash = textValue(commitHash)
	return &target, nil
}

func scanSecurityTargetRows(rows pgx.Rows) ([]domain.SecurityTarget, error) {
	out := make([]domain.SecurityTarget, 0)
	for rows.Next() {
		target, err := scanSecurityTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *target)
	}
	return out, rows.Err()
}

func scanSecurityRun(row scanner) (*domain.SecurityScanRun, error) {
	var run domain.SecurityScanRun
	var unsupportedJSON, metadataJSON []byte
	var requestedBy, requestEventID, requestDTag, errText pgtype.Text
	var startedAt, finishedAt pgtype.Timestamptz
	if err := row.Scan(&run.ID, &run.TargetID, &run.TargetKeyHash, &run.Status, &run.Trigger, &requestedBy, &requestEventID, &requestDTag,
		&run.OSVQueryCount, &run.FindingCount, &run.SeverityCounts.Critical, &run.SeverityCounts.High, &run.SeverityCounts.Moderate,
		&run.SeverityCounts.Low, &run.SeverityCounts.Unknown, &run.UnsupportedCount, &unsupportedJSON, &run.PublishState, &errText,
		&metadataJSON, &startedAt, &finishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	run.RequestedBy = textValue(requestedBy)
	run.RequestEventID = textValue(requestEventID)
	run.RequestDTag = textValue(requestDTag)
	run.Error = textValue(errText)
	run.StartedAt = timePtrFromPG(startedAt)
	run.FinishedAt = timePtrFromPG(finishedAt)
	if err := unmarshalJSON(unsupportedJSON, &run.UnsupportedReasons, "security scan run unsupported reasons"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &run.Metadata, "security scan run metadata"); err != nil {
		return nil, err
	}
	return &run, nil
}

func scanSecurityRunRows(rows pgx.Rows) ([]domain.SecurityScanRun, error) {
	out := make([]domain.SecurityScanRun, 0)
	for rows.Next() {
		run, err := scanSecurityRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func scanSecurityFinding(row scanner) (*domain.SecurityOSVFinding, error) {
	var finding domain.SecurityOSVFinding
	var packageJSON, aliasesJSON, refsJSON, metadataJSON []byte
	var cve, summary, details, rawModified pgtype.Text
	var withdrawnAt pgtype.Timestamptz
	if err := row.Scan(&finding.ID, &finding.RunID, &finding.TargetKeyHash, &finding.FindingKey, &finding.FindingKeyHash, &finding.OSVID,
		&cve, &summary, &details, &finding.Severity, &packageJSON, &aliasesJSON, &refsJSON, &withdrawnAt,
		&rawModified, &metadataJSON, &finding.CreatedAt, &finding.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	finding.CVE = textValue(cve)
	finding.Summary = textValue(summary)
	finding.Details = textValue(details)
	finding.RawModified = textValue(rawModified)
	finding.WithdrawnAt = timePtrFromPG(withdrawnAt)
	if err := unmarshalJSON(packageJSON, &finding.Package, "security finding package"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(aliasesJSON, &finding.Aliases, "security finding aliases"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(refsJSON, &finding.References, "security finding references"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &finding.Metadata, "security finding metadata"); err != nil {
		return nil, err
	}
	return &finding, nil
}

func scanSecurityFindingRows(rows pgx.Rows) ([]domain.SecurityOSVFinding, error) {
	out := make([]domain.SecurityOSVFinding, 0)
	for rows.Next() {
		finding, err := scanSecurityFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *finding)
	}
	return out, rows.Err()
}

func scanSecuritySchedule(row scanner) (*domain.SecurityScanSchedule, error) {
	var schedule domain.SecurityScanSchedule
	var leasedBy pgtype.Text
	var leaseUntil, lastDispatchedAt pgtype.Timestamptz
	var lastRunID pgtype.UUID
	var metadataJSON []byte
	if err := row.Scan(&schedule.ID, &schedule.PolicyID, &schedule.TargetID, &schedule.TargetKeyHash, &schedule.Enabled,
		&schedule.IntervalSeconds, &schedule.NextDueAt, &leaseUntil, &leasedBy, &lastDispatchedAt,
		&lastRunID, &metadataJSON, &schedule.CreatedAt, &schedule.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	schedule.LeasedBy = textValue(leasedBy)
	schedule.LeaseUntil = timePtrFromPG(leaseUntil)
	schedule.LastDispatchedAt = timePtrFromPG(lastDispatchedAt)
	schedule.LastRunID = uuidPtrFromPG(lastRunID)
	if err := unmarshalJSON(metadataJSON, &schedule.Metadata, "security scan schedule metadata"); err != nil {
		return nil, err
	}
	return &schedule, nil
}

func scanSecurityScheduleRows(rows pgx.Rows) ([]domain.SecurityScanSchedule, error) {
	out := make([]domain.SecurityScanSchedule, 0)
	for rows.Next() {
		schedule, err := scanSecuritySchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *schedule)
	}
	return out, rows.Err()
}

func scanSecurityBreach(row scanner) (*domain.SecurityPolicyBreach, error) {
	var breach domain.SecurityPolicyBreach
	var previous pgtype.Text
	var resolvedAt pgtype.Timestamptz
	var violatedJSON, osvIDsJSON, metadataJSON []byte
	if err := row.Scan(&breach.ID, &breach.PolicyID, &breach.TargetKeyHash, &breach.Fingerprint, &previous, &breach.Enforcement,
		&violatedJSON, &breach.SeverityCounts.Critical, &breach.SeverityCounts.High, &breach.SeverityCounts.Moderate,
		&breach.SeverityCounts.Low, &breach.SeverityCounts.Unknown, &osvIDsJSON, &breach.NotificationStatus, &breach.FirstSeenAt,
		&breach.LastSeenAt, &resolvedAt, &metadataJSON, &breach.CreatedAt, &breach.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	breach.PreviousFingerprint = textValue(previous)
	breach.ResolvedAt = timePtrFromPG(resolvedAt)
	if err := unmarshalJSON(violatedJSON, &breach.ViolatedRules, "security policy breach violated rules"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(osvIDsJSON, &breach.OSVIDs, "security policy breach osv ids"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &breach.Metadata, "security policy breach metadata"); err != nil {
		return nil, err
	}
	return &breach, nil
}

func scanOSVCache(row scanner) (*domain.OSVVulnerabilityCache, error) {
	var cache domain.OSVVulnerabilityCache
	var summary pgtype.Text
	var withdrawnAt pgtype.Timestamptz
	var aliasesJSON, rawJSON []byte
	if err := row.Scan(&cache.OSVID, &summary, &cache.Severity, &aliasesJSON, &rawJSON, &cache.CachedAt, &cache.ExpiresAt, &withdrawnAt); err != nil {
		return nil, mapNotFound(err)
	}
	cache.Summary = textValue(summary)
	cache.WithdrawnAt = timePtrFromPG(withdrawnAt)
	if err := unmarshalJSON(aliasesJSON, &cache.Aliases, "OSV cache aliases"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(rawJSON, &cache.Raw, "OSV cache raw"); err != nil {
		return nil, err
	}
	return &cache, nil
}

func scanSecurityPublication(row scanner) (*domain.SecurityObservablePublication, error) {
	var pub domain.SecurityObservablePublication
	var targetKeyHash, eventID, lastError pgtype.Text
	var runID, findingID, breachID pgtype.UUID
	var nextRetryAt, publishedAt pgtype.Timestamptz
	if err := row.Scan(&pub.ID, &pub.ObservableType, &runID, &targetKeyHash, &findingID, &breachID,
		&pub.EventKind, &pub.DTag, &pub.Schema, &pub.PublishState, &eventID, &pub.AttemptCount, &lastError,
		&nextRetryAt, &publishedAt, &pub.CreatedAt, &pub.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	pub.RunID = uuidPtrFromPG(runID)
	pub.TargetKeyHash = textValue(targetKeyHash)
	pub.FindingID = uuidPtrFromPG(findingID)
	pub.BreachID = uuidPtrFromPG(breachID)
	pub.EventID = textValue(eventID)
	pub.LastError = textValue(lastError)
	pub.NextRetryAt = timePtrFromPG(nextRetryAt)
	pub.PublishedAt = timePtrFromPG(publishedAt)
	return &pub, nil
}

func scanSecurityPublicationRows(rows pgx.Rows) ([]domain.SecurityObservablePublication, error) {
	out := make([]domain.SecurityObservablePublication, 0)
	for rows.Next() {
		pub, err := scanSecurityPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pub)
	}
	return out, rows.Err()
}

func mapNotFound(err error) error {
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	return err
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func unmarshalOptionalJSON[T any](data []byte, out **T, fieldName string) error {
	if len(data) == 0 || string(data) == "null" {
		*out = nil
		return nil
	}
	var value T
	if err := unmarshalJSON(data, &value, fieldName); err != nil {
		return err
	}
	*out = &value
	return nil
}
