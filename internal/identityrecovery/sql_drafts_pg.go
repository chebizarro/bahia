package identityrecovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// PGReviewRows reads only identifiers in a repeatable-read, read-only
// transaction. No JSONB config, credentials, findings detail or other row
// payload enters the unsigned report. PostgreSQL is never a signing source.
type PGReviewRows struct {
	DB        config.DBConfig
	OldPubkey string
}

type sqlReviewQuery struct {
	family, table, query, legacy string
	kind                         int
}

var sqlReviewQueries = []sqlReviewQuery{
	{"service-registry", "services", `SELECT id::text, id::text, '' FROM services`, fmt.Sprint(adapter.KindServiceRegistry), kinds.CASControlState},
	{"environment-registry", "environments", `SELECT id::text, id::text, '' FROM environments`, fmt.Sprint(adapter.KindEnvironmentRegistry), kinds.CASControlState},
	{"deployment-unit", "deployment_units", `SELECT id::text, environment_id::text, id::text FROM deployment_units`, fmt.Sprint(adapter.KindEnvironmentRegistry), kinds.CASControlState},
	{"adoption-binding", "adopted_runtime_identity", `SELECT id::text, 'adoption:binding:' || service_id::text || ':' || environment_id::text, '' FROM adopted_runtime_identity`, fmt.Sprint(adapter.KindAdoptionBindingRecord), kinds.CASControlState},
	{"relay-settings", "relay_policy_projections", `SELECT author_pubkey, 'relay-settings:operator', '' FROM relay_policy_projections WHERE author_pubkey = $1`, "", kinds.CASControlState},
	{"backup-recipe", "backup_recipes", `SELECT id::text, 'backup-recipe:' || id::text, '' FROM backup_recipes`, fmt.Sprint(adapter.KindBackupRecipeRegistry), kinds.CASControlState},
	{"backup-policy", "backup_policies", `SELECT id::text, 'backup-policy:' || id::text, '' FROM backup_policies`, fmt.Sprint(adapter.KindBackupPolicyRegistry), kinds.CASControlState},
	{"backup-repository", "backup_repositories", `SELECT id::text, 'backup-repository:' || id::text, '' FROM backup_repositories`, fmt.Sprint(adapter.KindBackupRepositoryRegistry), kinds.CASControlState},
	{"backup-definition", "backup_definitions", `SELECT id::text, 'backup-definition:' || id::text, '' FROM backup_definitions`, fmt.Sprint(adapter.KindBackupDefinitionRegistry), kinds.CASControlState},
	{"backup-run", "backup_runs", `SELECT id::text, 'backup-run:' || id::text, '' FROM backup_runs`, fmt.Sprint(adapter.KindBackupRunState), kinds.CASControlState},
	{"backup-restore", "backup_restores", `SELECT id::text, 'backup-restore:' || id::text, '' FROM backup_restores`, fmt.Sprint(adapter.KindBackupRestoreState), kinds.CASControlState},
	{"backup-verification", "backup_verifications", `SELECT id::text, 'backup-verification:' || backup_run_id::text, '' FROM backup_verifications`, fmt.Sprint(adapter.KindBackupVerificationState), kinds.CASControlState},
	{"backup-retention", "backup_retention_runs", `SELECT id::text, 'backup-retention:' || id::text, '' FROM backup_retention_runs`, fmt.Sprint(adapter.KindBackupRetentionRegistry), kinds.CASControlState},
	{"security-target", "security_scan_targets", `SELECT id::text, 'security:target:' || target_key_hash, '' FROM security_scan_targets`, fmt.Sprint(adapter.KindSecurityTargetRecord), kinds.CASControlState},
	{"security-run", "security_scan_runs", `SELECT id::text, 'security:run:' || id::text, '' FROM security_scan_runs`, fmt.Sprint(adapter.KindSecurityRunRecord), kinds.CASControlState},
	{"security-finding", "security_findings", `SELECT id::text, 'security:finding:' || finding_key_hash, '' FROM security_findings`, fmt.Sprint(adapter.KindSecurityFindingRecord), kinds.CASControlState},
	{"security-finding-detail", "security_findings", `SELECT id::text, 'security:finding-detail:' || finding_key_hash, '' FROM security_findings WHERE COALESCE(details, '') <> ''`, fmt.Sprint(adapter.KindSecurityFindingDetailRecord), kinds.CASControlState},
	{"security-schedule", "security_scan_schedules", `SELECT id::text, 'security:schedule:' || id::text, '' FROM security_scan_schedules`, fmt.Sprint(adapter.KindSecurityScheduleRecord), kinds.CASControlState},
	{"security-attribution", "security_attributions", `SELECT org_id::text || ':' || subject_type || ':' || subject_digest, 'security:attribution:' || org_id::text || ':' || subject_type || ':' || subject_digest, '' FROM security_attributions`, fmt.Sprint(adapter.KindSecurityAttributionRecord), kinds.CASControlState},
	{"sbom-reference", "sbom_manifests", `SELECT id::text, COALESCE(reference_d_tag, ''), '' FROM sbom_manifests`, "", kinds.SBOMReference},
	{"sbom-availability", "sbom_manifests", `SELECT id::text, COALESCE(availability_d_tag, ''), '' FROM sbom_manifests`, "", kinds.SBOMAvailabilityList},
	{"notification-channel", "notification_channels", `SELECT id::text, id::text, '' FROM notification_channels`, fmt.Sprint(adapter.KindNotificationChannelRegistry), kinds.CASControlState},
}

func (p PGReviewRows) ListReviewRows(ctx context.Context) ([]SQLReviewRow, error) {
	// An absent derived PostgreSQL projection contributes no candidate rows;
	// the canonical relay/local inventory remains authoritative.
	if p.DB.Host == "" && p.DB.Name == "" {
		return []SQLReviewRow{}, nil
	}
	cfg, err := pgxpool.ParseConfig(p.DB.DSN())
	if err != nil {
		return nil, errors.New("SQL draft database configuration invalid")
	}
	cfg.MinConns, cfg.MaxConns = 0, 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("SQL draft database connection unavailable")
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, errors.New("SQL draft read-only snapshot unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rowsOut := []SQLReviewRow{}
	for _, spec := range sqlReviewQueries {
		var rows pgx.Rows
		if spec.table == "relay_policy_projections" {
			rows, err = tx.Query(ctx, spec.query, p.OldPubkey)
		} else {
			rows, err = tx.Query(ctx, spec.query)
		}
		if err != nil {
			return nil, fmt.Errorf("SQL draft family %s could not be enumerated", spec.family)
		}
		for rows.Next() {
			var id, coordinate, embedded string
			if err := rows.Scan(&id, &coordinate, &embedded); err != nil {
				rows.Close()
				return nil, fmt.Errorf("SQL draft family %s row unreadable", spec.family)
			}
			rowsOut = append(rowsOut, SQLReviewRow{Family: spec.family, Table: spec.table, RowID: id, Kind: spec.kind, Coordinate: coordinate, LegacyKind: spec.legacy, EmbeddedID: embedded})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("SQL draft family %s scan incomplete", spec.family)
		}
		rows.Close()
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errors.New("SQL draft read-only snapshot could not complete")
	}
	return rowsOut, nil
}
