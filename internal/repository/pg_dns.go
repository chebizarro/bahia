package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgDNSPolicyRepository persists DNS policies in PostgreSQL.
type PgDNSPolicyRepository struct {
	pool pgQueryer
}

func NewPgDNSPolicyRepository(pool *pgxpool.Pool) *PgDNSPolicyRepository {
	return newPgDNSPolicyRepositoryWithDB(pool)
}

func newPgDNSPolicyRepositoryWithDB(db pgQueryer) *PgDNSPolicyRepository {
	return &PgDNSPolicyRepository{pool: db}
}

const dnsPolicyColumns = `id, name, zone_id, environment_id, rules, enabled, metadata, created_at, updated_at`

func (r *PgDNSPolicyRepository) Create(ctx context.Context, policy *domain.DNSPolicy) error {
	if policy.ID == uuid.Nil {
		policy.ID = uuid.New()
	}
	now := time.Now().UTC()
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	policy.UpdatedAt = now
	if policy.Metadata == nil {
		policy.Metadata = map[string]any{}
	}
	rules, err := marshalJSON(policy.Rules, "DNS policy rules")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(policy.Metadata, "DNS policy metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO dns_policies (`+dnsPolicyColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (name) DO UPDATE SET
			id = EXCLUDED.id, zone_id = EXCLUDED.zone_id, environment_id = EXCLUDED.environment_id,
			rules = EXCLUDED.rules, enabled = EXCLUDED.enabled, metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, policy.ID, policy.Name, policy.ZoneID, policy.EnvironmentID, rules, policy.Enabled, metadata, policy.CreatedAt, policy.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting DNS policy: %w", err)
	}
	return nil
}

func (r *PgDNSPolicyRepository) scan(row pgx.Row) (*domain.DNSPolicy, error) {
	policy := &domain.DNSPolicy{}
	var rules, metadata []byte
	if err := row.Scan(&policy.ID, &policy.Name, &policy.ZoneID, &policy.EnvironmentID, &rules, &policy.Enabled, &metadata, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(rules, &policy.Rules, "DNS policy rules"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadata, &policy.Metadata, "DNS policy metadata"); err != nil {
		return nil, err
	}
	return policy, nil
}

func (r *PgDNSPolicyRepository) Get(ctx context.Context, id uuid.UUID) (*domain.DNSPolicy, error) {
	policy, err := r.scan(r.pool.QueryRow(ctx, `SELECT `+dnsPolicyColumns+` FROM dns_policies WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying DNS policy: %w", err)
	}
	return policy, nil
}

func (r *PgDNSPolicyRepository) List(ctx context.Context) ([]domain.DNSPolicy, error) {
	return r.list(ctx, `SELECT `+dnsPolicyColumns+` FROM dns_policies ORDER BY name`)
}

func (r *PgDNSPolicyRepository) ListEnabled(ctx context.Context) ([]domain.DNSPolicy, error) {
	return r.list(ctx, `SELECT `+dnsPolicyColumns+` FROM dns_policies WHERE enabled = true ORDER BY name`)
}

// ListEnabledPolicies adapts the repository to reconcile.DNSPolicySource.
func (r *PgDNSPolicyRepository) ListEnabledPolicies(ctx context.Context) ([]domain.DNSPolicy, error) {
	return r.ListEnabled(ctx)
}

func (r *PgDNSPolicyRepository) list(ctx context.Context, query string) ([]domain.DNSPolicy, error) {
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing DNS policies: %w", err)
	}
	defer rows.Close()
	policies := []domain.DNSPolicy{}
	for rows.Next() {
		policy, err := r.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning DNS policy: %w", err)
		}
		policies = append(policies, *policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing DNS policies: %w", err)
	}
	return policies, nil
}

func (r *PgDNSPolicyRepository) Update(ctx context.Context, policy *domain.DNSPolicy) error {
	policy.UpdatedAt = time.Now().UTC()
	if policy.Metadata == nil {
		policy.Metadata = map[string]any{}
	}
	rules, err := marshalJSON(policy.Rules, "DNS policy rules")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(policy.Metadata, "DNS policy metadata")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `UPDATE dns_policies SET name = $2, zone_id = $3, environment_id = $4, rules = $5, enabled = $6, metadata = $7, updated_at = $8 WHERE id = $1`, policy.ID, policy.Name, policy.ZoneID, policy.EnvironmentID, rules, policy.Enabled, metadata, policy.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating DNS policy: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating DNS policy %s: %w", policy.ID, ErrNotFound)
	}
	return nil
}

func (r *PgDNSPolicyRepository) Delete(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM dns_policies WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting DNS policy: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting DNS policy %s: %w", id, ErrNotFound)
	}
	return nil
}

// PgDNSZoneRepository persists DNS zones in PostgreSQL.
type PgDNSZoneRepository struct {
	pool pgQueryer
}

func NewPgDNSZoneRepository(pool *pgxpool.Pool) *PgDNSZoneRepository {
	return newPgDNSZoneRepositoryWithDB(pool)
}

func newPgDNSZoneRepositoryWithDB(db pgQueryer) *PgDNSZoneRepository {
	return &PgDNSZoneRepository{pool: db}
}

func (r *PgDNSZoneRepository) Create(ctx context.Context, zone *domain.DNSZone) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO dns_zones (name, visibility, backend_ref, ttl, authoritative, allow_empty_authoritative)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET visibility = EXCLUDED.visibility, backend_ref = EXCLUDED.backend_ref, ttl = EXCLUDED.ttl, authoritative = EXCLUDED.authoritative, allow_empty_authoritative = EXCLUDED.allow_empty_authoritative, updated_at = now()
	`, zone.Name, zone.Visibility, zone.BackendRef, zone.TTL, zone.Authoritative, zone.AllowEmptyAuthoritative)
	if err != nil {
		return fmt.Errorf("inserting DNS zone: %w", err)
	}
	return nil
}

func (r *PgDNSZoneRepository) Get(ctx context.Context, name string) (*domain.DNSZone, error) {
	zone := &domain.DNSZone{}
	err := r.pool.QueryRow(ctx, `SELECT name, visibility, backend_ref, ttl, authoritative, allow_empty_authoritative FROM dns_zones WHERE name = $1`, name).Scan(&zone.Name, &zone.Visibility, &zone.BackendRef, &zone.TTL, &zone.Authoritative, &zone.AllowEmptyAuthoritative)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying DNS zone: %w", err)
	}
	return zone, nil
}

func (r *PgDNSZoneRepository) List(ctx context.Context) ([]domain.DNSZone, error) {
	rows, err := r.pool.Query(ctx, `SELECT name, visibility, backend_ref, ttl, authoritative, allow_empty_authoritative FROM dns_zones ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing DNS zones: %w", err)
	}
	defer rows.Close()
	zones := []domain.DNSZone{}
	for rows.Next() {
		var zone domain.DNSZone
		if err := rows.Scan(&zone.Name, &zone.Visibility, &zone.BackendRef, &zone.TTL, &zone.Authoritative, &zone.AllowEmptyAuthoritative); err != nil {
			return nil, fmt.Errorf("scanning DNS zone: %w", err)
		}
		zones = append(zones, zone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing DNS zones: %w", err)
	}
	return zones, nil
}

func (r *PgDNSZoneRepository) Delete(ctx context.Context, name string) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM dns_zones WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting DNS zone: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting DNS zone %q: %w", name, ErrNotFound)
	}
	return nil
}

// PgDNSRecordOverrideRepository persists manual DNS record pins in PostgreSQL.
type PgDNSRecordOverrideRepository struct {
	pool pgQueryer
}

func NewPgDNSRecordOverrideRepository(pool *pgxpool.Pool) *PgDNSRecordOverrideRepository {
	return newPgDNSRecordOverrideRepositoryWithDB(pool)
}

func newPgDNSRecordOverrideRepositoryWithDB(db pgQueryer) *PgDNSRecordOverrideRepository {
	return &PgDNSRecordOverrideRepository{pool: db}
}

const dnsOverrideColumns = `id, zone_name, record_name, record_type, value, ttl, reason, operator_pubkey, created_at, expires_at`

func (r *PgDNSRecordOverrideRepository) Create(ctx context.Context, override *domain.DNSRecordOverride) error {
	if override.ID == uuid.Nil {
		override.ID = uuid.New()
	}
	if override.CreatedAt.IsZero() {
		override.CreatedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO dns_record_overrides (`+dnsOverrideColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET zone_name = EXCLUDED.zone_name, record_name = EXCLUDED.record_name,
			record_type = EXCLUDED.record_type, value = EXCLUDED.value, ttl = EXCLUDED.ttl,
			reason = EXCLUDED.reason, operator_pubkey = EXCLUDED.operator_pubkey, expires_at = EXCLUDED.expires_at
	`, override.ID, override.ZoneName, override.RecordName, override.RecordType, override.Value, override.TTL, override.Reason, override.OperatorPubkey, override.CreatedAt, override.ExpiresAt)
	if err != nil {
		return fmt.Errorf("inserting DNS record override: %w", err)
	}
	return nil
}

func (r *PgDNSRecordOverrideRepository) Get(ctx context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	override := &domain.DNSRecordOverride{}
	err := r.pool.QueryRow(ctx, `SELECT `+dnsOverrideColumns+` FROM dns_record_overrides WHERE id = $1`, id).Scan(&override.ID, &override.ZoneName, &override.RecordName, &override.RecordType, &override.Value, &override.TTL, &override.Reason, &override.OperatorPubkey, &override.CreatedAt, &override.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying DNS record override: %w", err)
	}
	return override, nil
}

func (r *PgDNSRecordOverrideRepository) ListByZone(ctx context.Context, zoneName string) ([]domain.DNSRecordOverride, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+dnsOverrideColumns+` FROM dns_record_overrides WHERE zone_name = $1 AND (expires_at IS NULL OR expires_at > now()) ORDER BY record_name, record_type, created_at`, zoneName)
	if err != nil {
		return nil, fmt.Errorf("listing DNS record overrides: %w", err)
	}
	defer rows.Close()
	overrides := []domain.DNSRecordOverride{}
	for rows.Next() {
		var override domain.DNSRecordOverride
		if err := rows.Scan(&override.ID, &override.ZoneName, &override.RecordName, &override.RecordType, &override.Value, &override.TTL, &override.Reason, &override.OperatorPubkey, &override.CreatedAt, &override.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scanning DNS record override: %w", err)
		}
		overrides = append(overrides, override)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing DNS record overrides: %w", err)
	}
	return overrides, nil
}

func (r *PgDNSRecordOverrideRepository) Delete(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM dns_record_overrides WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting DNS record override: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting DNS record override %s: %w", id, ErrNotFound)
	}
	return nil
}
