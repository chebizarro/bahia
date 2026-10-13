package repository

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgSecurityAttributionIndex is a disposable mirror of verified canonical edges.
// It must never be used to authorize a tenant read.
type PgSecurityAttributionIndex struct{ pool pgQueryer }

func NewPgSecurityAttributionIndex(pool *pgxpool.Pool) *PgSecurityAttributionIndex {
	return &PgSecurityAttributionIndex{pool: pool}
}

// ResetAttributionIndex removes stale SQL-only edges before signed replay.
func (r *PgSecurityAttributionIndex) ResetAttributionIndex(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("security attribution SQL index unavailable")
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM security_attributions`)
	return err
}

func (r *PgSecurityAttributionIndex) UpsertAttribution(ctx context.Context, edge domain.SecurityAttribution, eventID string, createdAt int64) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("security attribution SQL index unavailable")
	}
	if err := domain.ValidateSecurityAttribution(&edge); err != nil {
		return err
	}
	if len(eventID) != 64 || createdAt <= 0 {
		return fmt.Errorf("security attribution index requires a canonical event id")
	}
	if _, err := hex.DecodeString(eventID); err != nil || eventID != strings.ToLower(eventID) {
		return fmt.Errorf("security attribution index requires a lowercase canonical event id")
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO security_attributions (org_id, subject_type, subject_digest, visible, source_event_id, source_created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, subject_type, subject_digest) DO UPDATE SET
		visible = EXCLUDED.visible, source_event_id = EXCLUDED.source_event_id, source_created_at = EXCLUDED.source_created_at
		WHERE EXCLUDED.source_created_at > security_attributions.source_created_at OR
		  (EXCLUDED.source_created_at = security_attributions.source_created_at AND EXCLUDED.source_event_id < security_attributions.source_event_id)`,
		edge.OrgID, edge.SubjectType, edge.SubjectDigest, edge.Visible, eventID, createdAt)
	return err
}
