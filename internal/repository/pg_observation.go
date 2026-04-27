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

// PgRuntimeObservationRepository is a PostgreSQL implementation.
type PgRuntimeObservationRepository struct {
	pool *pgxpool.Pool
}

func NewPgRuntimeObservationRepository(pool *pgxpool.Pool) *PgRuntimeObservationRepository {
	return &PgRuntimeObservationRepository{pool: pool}
}

const obsColumns = `id, service_id, environment_id, observed_image_digest, observed_image_repo, observed_container_id, observed_host, observed_version, health_status, source, metadata, observed_at`

func (r *PgRuntimeObservationRepository) Create(ctx context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}

	metaJSON, err := marshalJSON(obs.Metadata, "observation metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO runtime_observations (`+obsColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, obs.ID, obs.ServiceID, obs.EnvironmentID, obs.ObservedImageDigest, obs.ObservedImageRepo,
		obs.ObservedContainerID, obs.ObservedHost, obs.ObservedVersion, obs.HealthStatus, obs.Source, metaJSON, obs.ObservedAt)
	if err != nil {
		return fmt.Errorf("inserting runtime observation: %w", err)
	}
	return nil
}

func (r *PgRuntimeObservationRepository) scanObs(row pgx.Row) (*domain.RuntimeObservation, error) {
	obs := &domain.RuntimeObservation{}
	var metaJSON []byte
	err := row.Scan(&obs.ID, &obs.ServiceID, &obs.EnvironmentID, &obs.ObservedImageDigest, &obs.ObservedImageRepo,
		&obs.ObservedContainerID, &obs.ObservedHost, &obs.ObservedVersion, &obs.HealthStatus, &obs.Source, &metaJSON, &obs.ObservedAt)
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &obs.Metadata, "observation metadata"); err != nil {
		return nil, err
	}
	return obs, nil
}

func (r *PgRuntimeObservationRepository) GetLatest(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+obsColumns+` FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC LIMIT 1
	`, serviceID, envID)
	obs, err := r.scanObs(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying latest observation: %w", err)
	}
	return obs, nil
}

func (r *PgRuntimeObservationRepository) ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+obsColumns+` FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC LIMIT $3
	`, serviceID, envID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing observations: %w", err)
	}
	defer rows.Close()

	var observations []domain.RuntimeObservation
	for rows.Next() {
		var obs domain.RuntimeObservation
		var metaJSON []byte
		if err := rows.Scan(&obs.ID, &obs.ServiceID, &obs.EnvironmentID, &obs.ObservedImageDigest, &obs.ObservedImageRepo,
			&obs.ObservedContainerID, &obs.ObservedHost, &obs.ObservedVersion, &obs.HealthStatus, &obs.Source, &metaJSON, &obs.ObservedAt); err != nil {
			return nil, fmt.Errorf("scanning observation: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &obs.Metadata, "observation metadata"); err != nil {
			return nil, fmt.Errorf("reading observation %s: %w", obs.ID, err)
		}
		observations = append(observations, obs)
	}
	return observations, rows.Err()
}
