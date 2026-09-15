package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgAgentRuntimeReleaseRepository persists shared runtime releases and append-only bindings.
type PgAgentRuntimeReleaseRepository struct{ db pgQueryer }

func NewPgAgentRuntimeReleaseRepository(pool *pgxpool.Pool) *PgAgentRuntimeReleaseRepository {
	return &PgAgentRuntimeReleaseRepository{db: pool}
}

func (r *PgAgentRuntimeReleaseRepository) CreateSource(ctx context.Context, source *domain.AgentRuntimeSource) error {
	if source.ID == uuid.Nil {
		source.ID = uuid.New()
	}
	return r.db.QueryRow(ctx, `
		INSERT INTO agent_runtime_sources (id, org_id, repository, branch, release_channel)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (org_id, repository, branch, release_channel) DO UPDATE SET repository=EXCLUDED.repository
		RETURNING id, created_at`, source.ID, source.OrgID, source.Repository, source.Branch, source.ReleaseChannel).
		Scan(&source.ID, &source.CreatedAt)
}

func (r *PgAgentRuntimeReleaseRepository) GetSource(ctx context.Context, orgID, sourceID uuid.UUID) (*domain.AgentRuntimeSource, error) {
	var source domain.AgentRuntimeSource
	err := r.db.QueryRow(ctx, `SELECT id, org_id, repository, branch, release_channel, created_at FROM agent_runtime_sources WHERE org_id=$1 AND id=$2`, orgID, sourceID).Scan(&source.ID, &source.OrgID, &source.Repository, &source.Branch, &source.ReleaseChannel, &source.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &source, nil
}

func (r *PgAgentRuntimeReleaseRepository) CreateRelease(ctx context.Context, release *domain.AgentRuntimeRelease) error {
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	provenance, err := json.Marshal(release.Provenance)
	if err != nil {
		return fmt.Errorf("encode runtime release provenance: %w", err)
	}
	return r.db.QueryRow(ctx, `
		INSERT INTO agent_runtime_releases (id, org_id, source_id, image_repo, image_digest, provenance, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at`, release.ID, release.OrgID, release.SourceID, release.ImageRepo, release.ImageDigest, provenance, release.VerifiedAt).
		Scan(&release.CreatedAt)
}

func scanAgentRuntimeRelease(row pgx.Row) (*domain.AgentRuntimeRelease, error) {
	var release domain.AgentRuntimeRelease
	var provenance []byte
	if err := row.Scan(&release.ID, &release.OrgID, &release.SourceID, &release.ImageRepo, &release.ImageDigest, &provenance, &release.VerifiedAt, &release.CreatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(provenance, &release.Provenance); err != nil {
		return nil, fmt.Errorf("decode runtime release provenance: %w", err)
	}
	return &release, nil
}

const agentRuntimeReleaseColumns = `id, org_id, source_id, image_repo, image_digest, provenance, verified_at, created_at`

func (r *PgAgentRuntimeReleaseRepository) GetRelease(ctx context.Context, orgID, releaseID uuid.UUID) (*domain.AgentRuntimeRelease, error) {
	return scanAgentRuntimeRelease(r.db.QueryRow(ctx, `SELECT `+agentRuntimeReleaseColumns+` FROM agent_runtime_releases WHERE org_id=$1 AND id=$2`, orgID, releaseID))
}

func (r *PgAgentRuntimeReleaseRepository) GetReleaseByDigest(ctx context.Context, orgID uuid.UUID, imageRepo, imageDigest string) (*domain.AgentRuntimeRelease, error) {
	return scanAgentRuntimeRelease(r.db.QueryRow(ctx, `SELECT `+agentRuntimeReleaseColumns+` FROM agent_runtime_releases WHERE org_id=$1 AND image_repo=$2 AND image_digest=$3`, orgID, imageRepo, imageDigest))
}

// bindReleaseChainRetryLimit bounds the retries triggered by concurrent producers
// racing to append to the same (org, agent, service, channel) linear history.
// The per-chain UNIQUE indexes serialize appenders at the database level; the
// loop only exists to reload the current head and try again if we lost the
// race. Real-world concurrency is bounded by producers-per-chain.
const bindReleaseChainRetryLimit = 32

// BindRelease durably records a promotion event. It is idempotent on
// source_event_id (replays return the stored binding) and appends a new
// binding whenever a fresh source_event_id refers to any release, including a
// previously bound one. Concurrent appenders on the same chain are serialized
// by the chain_head_idx / chain_next_idx unique indexes; a loser retries.
func (r *PgAgentRuntimeReleaseRepository) BindRelease(ctx context.Context, binding *domain.AgentServiceReleaseBinding) error {
	if binding.ID == uuid.Nil {
		binding.ID = uuid.New()
	}
	originalID := binding.ID
	for attempt := 0; attempt < bindReleaseChainRetryLimit; attempt++ {
		err := r.db.QueryRow(ctx, `
			WITH previous AS (
				SELECT id FROM agent_service_runtime_release_bindings
				WHERE org_id=$2 AND agent_id=$3 AND service_id=$4 AND release_channel=$6
				ORDER BY created_at DESC, id DESC LIMIT 1
			), inserted AS (
				INSERT INTO agent_service_runtime_release_bindings
					(id, org_id, agent_id, service_id, release_id, release_channel, source_event_id, previous_binding_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,(SELECT id FROM previous))
				ON CONFLICT (source_event_id) DO NOTHING
				RETURNING id, release_id, previous_binding_id, created_at
			)
			SELECT id, release_id, previous_binding_id, created_at FROM inserted
			UNION ALL
			SELECT id, release_id, previous_binding_id, created_at FROM agent_service_runtime_release_bindings
			WHERE org_id=$2 AND source_event_id=$7
			LIMIT 1`, binding.ID, binding.OrgID, binding.AgentID, binding.ServiceID, binding.ReleaseID, binding.ReleaseChannel, binding.SourceEventID).
			Scan(&binding.ID, &binding.ReleaseID, &binding.PreviousBindingID, &binding.CreatedAt)
		if err == nil {
			return nil
		}
		if isBindReleaseChainRace(err) {
			// Another producer appended to the chain between our SELECT of the
			// current head and our INSERT. Reset the ID we tried to allocate so
			// the caller does not accidentally observe our failed candidate, and
			// retry with a fresh view of the head.
			binding.ID = originalID
			continue
		}
		return err
	}
	return fmt.Errorf("bind release: contention on (%s,%s,%s,%s) did not resolve after %d retries", binding.OrgID, binding.AgentID, binding.ServiceID, binding.ReleaseChannel, bindReleaseChainRetryLimit)
}

// isBindReleaseChainRace matches the two partial UNIQUE indexes that serialize
// concurrent appenders on the same chain. Any other error is not retryable.
func isBindReleaseChainRace(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "23505" { // unique_violation
		return false
	}
	switch pgErr.ConstraintName {
	case "agent_service_runtime_release_bindings_chain_head_idx",
		"agent_service_runtime_release_bindings_chain_next_idx":
		return true
	}
	return false
}

func scanAgentServiceRuntimeRelease(row pgx.Row) (*domain.AgentServiceRuntimeRelease, error) {
	var out domain.AgentServiceRuntimeRelease
	var provenance []byte
	err := row.Scan(
		&out.Binding.ID, &out.Binding.OrgID, &out.Binding.AgentID, &out.Binding.ServiceID,
		&out.Binding.ReleaseID, &out.Binding.ReleaseChannel, &out.Binding.SourceEventID,
		&out.Binding.PreviousBindingID, &out.Binding.CreatedAt,
		&out.Release.ID, &out.Release.OrgID, &out.Release.SourceID, &out.Release.ImageRepo,
		&out.Release.ImageDigest, &provenance, &out.Release.VerifiedAt, &out.Release.CreatedAt,
		&out.Source.ID, &out.Source.OrgID, &out.Source.Repository, &out.Source.Branch,
		&out.Source.ReleaseChannel, &out.Source.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(provenance, &out.Release.Provenance); err != nil {
		return nil, fmt.Errorf("decode runtime release provenance: %w", err)
	}
	return &out, nil
}

const agentServiceReleaseJoin = `
	SELECT b.id,b.org_id,b.agent_id,b.service_id,b.release_id,b.release_channel,b.source_event_id,b.previous_binding_id,b.created_at,
	       r.id,r.org_id,r.source_id,r.image_repo,r.image_digest,r.provenance,r.verified_at,r.created_at,
	       s.id,s.org_id,s.repository,s.branch,s.release_channel,s.created_at
	FROM agent_service_runtime_release_bindings b
	JOIN agent_runtime_releases r ON r.id=b.release_id AND r.org_id=b.org_id
	JOIN agent_runtime_sources s ON s.id=r.source_id AND s.org_id=b.org_id`

func (r *PgAgentRuntimeReleaseRepository) ListServiceReleases(ctx context.Context, orgID, serviceID uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error) {
	rows, err := r.db.Query(ctx, agentServiceReleaseJoin+` WHERE b.org_id=$1 AND b.service_id=$2 ORDER BY b.created_at DESC,b.id DESC`, orgID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.AgentServiceRuntimeRelease
	for rows.Next() {
		item, err := scanAgentServiceRuntimeRelease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, rows.Err()
}

// GetServiceRelease resolves a shared release only through a durable binding
// to the requested service. The release remains the canonical digest and
// provenance source; the binding contributes no duplicated artifact evidence.
func (r *PgAgentRuntimeReleaseRepository) GetServiceRelease(ctx context.Context, orgID, serviceID, releaseID uuid.UUID) (*domain.AgentServiceRuntimeRelease, error) {
	return scanAgentServiceRuntimeRelease(r.db.QueryRow(ctx, agentServiceReleaseJoin+`
		WHERE b.org_id=$1 AND b.service_id=$2 AND b.release_id=$3
		ORDER BY b.created_at DESC,b.id DESC LIMIT 1`, orgID, serviceID, releaseID))
}

func (r *PgAgentRuntimeReleaseRepository) GetRollbackRelease(ctx context.Context, orgID uuid.UUID, agentID string, serviceID uuid.UUID, channel string) (*domain.AgentServiceRuntimeRelease, error) {
	return scanAgentServiceRuntimeRelease(r.db.QueryRow(ctx, agentServiceReleaseJoin+`
		WHERE b.id=(
			SELECT previous_binding_id FROM agent_service_runtime_release_bindings
			WHERE org_id=$1 AND agent_id=$2 AND service_id=$3 AND release_channel=$4
			ORDER BY created_at DESC,id DESC LIMIT 1
		)`, orgID, agentID, serviceID, channel))
}

var _ AgentRuntimeReleaseRepository = (*PgAgentRuntimeReleaseRepository)(nil)
