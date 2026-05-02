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

// PgLLMRouteRepository is a PostgreSQL implementation of LLMRouteRepository.
type PgLLMRouteRepository struct {
	pool pgQueryer
}

func NewPgLLMRouteRepository(pool *pgxpool.Pool) *PgLLMRouteRepository {
	return newPgLLMRouteRepositoryWithDB(pool)
}

func newPgLLMRouteRepositoryWithDB(db pgQueryer) *PgLLMRouteRepository {
	return &PgLLMRouteRepository{pool: db}
}

const llmRouteColumns = `id, name, description, gateway_config, default_placement_policy, default_promotion_gate, metadata, created_at, updated_at`

func (r *PgLLMRouteRepository) Create(ctx context.Context, route *domain.LLMRoute) error {
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	now := time.Now().UTC()
	route.CreatedAt = now
	route.UpdatedAt = now

	gatewayJSON, err := marshalJSON(route.GatewayConfig, "LLM route gateway config")
	if err != nil {
		return err
	}
	placementJSON, err := marshalJSON(route.DefaultPlacementPolicy, "LLM route placement policy")
	if err != nil {
		return err
	}
	gateJSON, err := marshalJSON(route.DefaultPromotionGate, "LLM route promotion gate")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(route.Metadata, "LLM route metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_routes (`+llmRouteColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, route.ID, route.Name, route.Description, gatewayJSON, placementJSON, gateJSON, metaJSON, route.CreatedAt, route.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting LLM route: %w", err)
	}
	return nil
}

func (r *PgLLMRouteRepository) scanRoute(row pgx.Row) (*domain.LLMRoute, error) {
	route := &domain.LLMRoute{}
	var gatewayJSON, placementJSON, gateJSON, metaJSON []byte
	if err := row.Scan(&route.ID, &route.Name, &route.Description, &gatewayJSON, &placementJSON, &gateJSON, &metaJSON, &route.CreatedAt, &route.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(gatewayJSON, &route.GatewayConfig, "LLM route gateway config"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(placementJSON, &route.DefaultPlacementPolicy, "LLM route placement policy"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(gateJSON, &route.DefaultPromotionGate, "LLM route promotion gate"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &route.Metadata, "LLM route metadata"); err != nil {
		return nil, err
	}
	return route, nil
}

func (r *PgLLMRouteRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, err := r.scanRoute(r.pool.QueryRow(ctx, `SELECT `+llmRouteColumns+` FROM llm_routes WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM route by id: %w", err)
	}
	return route, nil
}

func (r *PgLLMRouteRepository) GetByName(ctx context.Context, name string) (*domain.LLMRoute, error) {
	route, err := r.scanRoute(r.pool.QueryRow(ctx, `SELECT `+llmRouteColumns+` FROM llm_routes WHERE name = $1`, name))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM route by name: %w", err)
	}
	return route, nil
}

func (r *PgLLMRouteRepository) List(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+llmRouteColumns+` FROM llm_routes ORDER BY name ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing LLM routes: %w", err)
	}
	defer rows.Close()

	var routes []domain.LLMRoute
	for rows.Next() {
		route, err := r.scanRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM route: %w", err)
		}
		routes = append(routes, *route)
	}
	return routes, rows.Err()
}

func (r *PgLLMRouteRepository) Update(ctx context.Context, route *domain.LLMRoute) error {
	route.UpdatedAt = time.Now().UTC()
	gatewayJSON, err := marshalJSON(route.GatewayConfig, "LLM route gateway config")
	if err != nil {
		return err
	}
	placementJSON, err := marshalJSON(route.DefaultPlacementPolicy, "LLM route placement policy")
	if err != nil {
		return err
	}
	gateJSON, err := marshalJSON(route.DefaultPromotionGate, "LLM route promotion gate")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(route.Metadata, "LLM route metadata")
	if err != nil {
		return err
	}
	cmd, err := r.pool.Exec(ctx, `
		UPDATE llm_routes
		SET description = $2, gateway_config = $3, default_placement_policy = $4,
			default_promotion_gate = $5, metadata = $6, updated_at = $7
		WHERE id = $1
	`, route.ID, route.Description, gatewayJSON, placementJSON, gateJSON, metaJSON, route.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating LLM route: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating LLM route %s: %w", route.ID, ErrNotFound)
	}
	return nil
}

func (r *PgLLMRouteRepository) Delete(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM llm_routes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting LLM route: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting LLM route %s: %w", id, ErrNotFound)
	}
	return nil
}

// PgLLMReleaseRepository is a PostgreSQL implementation of LLMReleaseRepository.
type PgLLMReleaseRepository struct {
	pool pgQueryer
}

func NewPgLLMReleaseRepository(pool *pgxpool.Pool) *PgLLMReleaseRepository {
	return newPgLLMReleaseRepositoryWithDB(pool)
}

func newPgLLMReleaseRepositoryWithDB(db pgQueryer) *PgLLMReleaseRepository {
	return &PgLLMReleaseRepository{pool: db}
}

const llmReleaseColumns = `id, route_id, version, model_ref, model_source, model_revision, estimated_vram_gb, backend_preferences, runtime_backend, external_backend, placement_policy, promotion_gate, metadata, created_at`

func (r *PgLLMReleaseRepository) Create(ctx context.Context, release *domain.LLMRelease) error {
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	release.CreatedAt = time.Now().UTC()

	preferencesJSON, err := marshalJSON(release.BackendPreferences, "LLM backend preferences")
	if err != nil {
		return err
	}
	runtimeJSON, err := marshalJSON(release.RuntimeBackend, "LLM runtime backend")
	if err != nil {
		return err
	}
	externalJSON, err := marshalJSON(release.ExternalBackend, "LLM external backend")
	if err != nil {
		return err
	}
	placementJSON, err := marshalJSON(release.PlacementPolicy, "LLM release placement policy")
	if err != nil {
		return err
	}
	gateJSON, err := marshalJSON(release.PromotionGate, "LLM release promotion gate")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(release.Metadata, "LLM release metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_releases (`+llmReleaseColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, release.ID, release.RouteID, release.Version, release.ModelRef, release.ModelSource, release.ModelRevision,
		release.EstimatedVRAMGB, preferencesJSON, runtimeJSON, externalJSON, placementJSON, gateJSON, metaJSON, release.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting LLM release: %w", err)
	}
	return nil
}

func (r *PgLLMReleaseRepository) scanRelease(row pgx.Row) (*domain.LLMRelease, error) {
	release := &domain.LLMRelease{}
	var preferencesJSON, runtimeJSON, externalJSON, placementJSON, gateJSON, metaJSON []byte
	if err := row.Scan(&release.ID, &release.RouteID, &release.Version, &release.ModelRef, &release.ModelSource,
		&release.ModelRevision, &release.EstimatedVRAMGB, &preferencesJSON, &runtimeJSON, &externalJSON,
		&placementJSON, &gateJSON, &metaJSON, &release.CreatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(preferencesJSON, &release.BackendPreferences, "LLM backend preferences"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(runtimeJSON, &release.RuntimeBackend, "LLM runtime backend"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(externalJSON, &release.ExternalBackend, "LLM external backend"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(placementJSON, &release.PlacementPolicy, "LLM release placement policy"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(gateJSON, &release.PromotionGate, "LLM release promotion gate"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &release.Metadata, "LLM release metadata"); err != nil {
		return nil, err
	}
	return release, nil
}

func (r *PgLLMReleaseRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	release, err := r.scanRelease(r.pool.QueryRow(ctx, `SELECT `+llmReleaseColumns+` FROM llm_releases WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM release by id: %w", err)
	}
	return release, nil
}

func (r *PgLLMReleaseRepository) GetByRouteVersion(ctx context.Context, routeID uuid.UUID, version string) (*domain.LLMRelease, error) {
	release, err := r.scanRelease(r.pool.QueryRow(ctx, `SELECT `+llmReleaseColumns+` FROM llm_releases WHERE route_id = $1 AND version = $2`, routeID, version))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM release by route/version: %w", err)
	}
	return release, nil
}

func (r *PgLLMReleaseRepository) ListByRoute(ctx context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+llmReleaseColumns+` FROM llm_releases
		WHERE route_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, routeID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing LLM releases: %w", err)
	}
	defer rows.Close()

	var releases []domain.LLMRelease
	for rows.Next() {
		release, err := r.scanRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM release: %w", err)
		}
		releases = append(releases, *release)
	}
	return releases, rows.Err()
}
