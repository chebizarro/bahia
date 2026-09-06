package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgDNSPolicyRepositoryCreateAndListEnabled(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgDNSPolicyRepositoryWithDB(mock)
	ttl := 42
	policy := &domain.DNSPolicy{Name: "short-prod-ttl", Enabled: true, Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{TTLOverride: &ttl}}}, Metadata: map[string]any{"owner": "ops"}}

	mock.ExpectExec("INSERT INTO dns_policies").WithArgs(pgxmock.AnyArg(), policy.Name, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), true, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, repo.Create(ctx, policy))
	require.NotEqual(t, uuid.Nil, policy.ID)

	mock.ExpectQuery("FROM dns_policies WHERE enabled = true").WillReturnRows(
		pgxmock.NewRows([]string{"id", "name", "zone_id", "environment_id", "rules", "enabled", "metadata", "created_at", "updated_at"}).
			AddRow(policy.ID, policy.Name, nil, nil, []byte(`[{"match":{"environment":"prod"},"action":{"ttl_override":42}}]`), true, []byte(`{"owner":"ops"}`), policy.CreatedAt, policy.UpdatedAt),
	)
	policies, err := repo.ListEnabledPolicies(ctx)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, 42, *policies[0].Rules[0].Action.TTLOverride)
	require.Equal(t, "ops", policies[0].Metadata["owner"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDNSZoneRepositoryPersistsMeshVisibility(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgDNSZoneRepositoryWithDB(mock)
	zone := &domain.DNSZone{Name: "mesh.example", Visibility: domain.ZoneVisibilityMesh, BackendRef: "mesh-dns", TTL: 60, Authoritative: true, AllowEmptyAuthoritative: true}

	mock.ExpectExec("INSERT INTO dns_zones").WithArgs(zone.Name, domain.ZoneVisibilityMesh, zone.BackendRef, zone.TTL, zone.Authoritative, zone.AllowEmptyAuthoritative).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, repo.Create(ctx, zone))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDNSZoneRepositoryReadsAuthoritativeAndAllowEmpty(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgDNSZoneRepositoryWithDB(mock)
	columns := []string{"name", "visibility", "backend_ref", "ttl", "authoritative", "allow_empty_authoritative"}

	mock.ExpectQuery("SELECT name, visibility, backend_ref, ttl, authoritative, allow_empty_authoritative FROM dns_zones WHERE name").WithArgs("prod.example").WillReturnRows(
		pgxmock.NewRows(columns).AddRow("prod.example", domain.ZoneVisibilityInternal, "primary", 120, true, true),
	)
	zone, err := repo.Get(ctx, "prod.example")
	require.NoError(t, err)
	require.True(t, zone.Authoritative)
	require.True(t, zone.AllowEmptyAuthoritative)

	mock.ExpectQuery("SELECT name, visibility, backend_ref, ttl, authoritative, allow_empty_authoritative FROM dns_zones ORDER BY name").WillReturnRows(
		pgxmock.NewRows(columns).AddRow("prod.example", domain.ZoneVisibilityInternal, "primary", 120, true, true),
	)
	zones, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, zones, 1)
	require.True(t, zones[0].Authoritative)
	require.True(t, zones[0].AllowEmptyAuthoritative)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDNSZoneAndOverrideRepositoriesPersistAndListActive(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	zones := newPgDNSZoneRepositoryWithDB(mock)
	overrides := newPgDNSRecordOverrideRepositoryWithDB(mock)
	zone := &domain.DNSZone{Name: "prod.example", Visibility: domain.ZoneVisibilityInternal, BackendRef: "primary", TTL: 120}

	mock.ExpectExec("INSERT INTO dns_zones").WithArgs(zone.Name, zone.Visibility, zone.BackendRef, zone.TTL, zone.Authoritative, zone.AllowEmptyAuthoritative).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, zones.Create(ctx, zone))

	expiresAt := time.Now().UTC().Add(time.Hour)
	override := &domain.DNSRecordOverride{ZoneName: zone.Name, RecordName: "api", RecordType: domain.DNSRecordTypeA, Value: "10.0.0.99", TTL: 30, Reason: "incident", OperatorPubkey: "operator", ExpiresAt: &expiresAt}
	mock.ExpectExec("INSERT INTO dns_record_overrides").WithArgs(pgxmock.AnyArg(), override.ZoneName, override.RecordName, override.RecordType, override.Value, override.TTL, override.Reason, override.OperatorPubkey, pgxmock.AnyArg(), override.ExpiresAt).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, overrides.Create(ctx, override))

	mock.ExpectQuery("FROM dns_record_overrides WHERE zone_name = \\$1 AND \\(expires_at IS NULL OR expires_at > now\\(\\)\\)").WithArgs(zone.Name).WillReturnRows(
		pgxmock.NewRows([]string{"id", "zone_name", "record_name", "record_type", "value", "ttl", "reason", "operator_pubkey", "created_at", "expires_at"}).
			AddRow(override.ID, override.ZoneName, override.RecordName, override.RecordType, override.Value, override.TTL, override.Reason, override.OperatorPubkey, override.CreatedAt, override.ExpiresAt),
	)
	got, err := overrides.ListByZone(ctx, zone.Name)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "10.0.0.99", got[0].Value)
	require.NoError(t, mock.ExpectationsWereMet())
}
