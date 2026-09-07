package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgRouteCanaryRepository is the PostgreSQL route canary repository.
type PgRouteCanaryRepository struct {
	pool pgQueryer
}

// NewPgRouteCanaryRepository builds a pool-backed route canary repository.
func NewPgRouteCanaryRepository(pool *pgxpool.Pool) *PgRouteCanaryRepository {
	return newPgRouteCanaryRepositoryWithDB(pool)
}

func newPgRouteCanaryRepositoryWithDB(db pgQueryer) *PgRouteCanaryRepository {
	return &PgRouteCanaryRepository{pool: db}
}

const routeCanaryStateColumns = `route_coordinate, service_id, environment_id, deployment_unit_id, hostname, open, classification, perspective, consecutive_failures, consecutive_successes, failure_reason, tls_not_after, last_observed_at, opened_at, last_recovered_at, updated_at`

const routeCanaryEventColumns = `id, route_coordinate, service_id, environment_id, deployment_unit_id, hostname, transition, previous_classification, classification, perspective, reason, evidence, observed_instance_status, observed_at`

// UpsertStateWithEvent persists route state and appends its lineage record in a
// single transaction, so an operator-visible outage can never exist without the
// event that explains it.
func (r *PgRouteCanaryRepository) UpsertStateWithEvent(ctx context.Context, state *domain.RouteCanaryState, event *domain.RouteCanaryEvent) error {
	if event == nil {
		return fmt.Errorf("route canary event is required")
	}
	return r.withinTx(ctx, func(txRepo *PgRouteCanaryRepository) error {
		if err := txRepo.UpsertState(ctx, state); err != nil {
			return err
		}
		return txRepo.AppendEvent(ctx, event)
	})
}

// UpsertState writes the current outage state for one managed route.
//
// The write is guarded on last_observed_at so a delayed observation from a
// slower perspective can never overwrite a newer verdict.
func (r *PgRouteCanaryRepository) UpsertState(ctx context.Context, state *domain.RouteCanaryState) error {
	if state == nil {
		return fmt.Errorf("route canary state is required")
	}
	if state.Hostname == "" {
		return fmt.Errorf("route canary state hostname is required")
	}
	if !state.Classification.Valid() {
		return fmt.Errorf("route canary state has unknown classification %q", state.Classification)
	}
	if state.LastObservedAt.IsZero() {
		state.LastObservedAt = time.Now().UTC()
	}
	state.UpdatedAt = time.Now().UTC()
	state.FailureReason = domain.SanitizeEvidence(state.FailureReason)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO route_canary_state (`+routeCanaryStateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (route_coordinate) DO UPDATE SET
			open = EXCLUDED.open,
			classification = EXCLUDED.classification,
			perspective = EXCLUDED.perspective,
			consecutive_failures = EXCLUDED.consecutive_failures,
			consecutive_successes = EXCLUDED.consecutive_successes,
			failure_reason = EXCLUDED.failure_reason,
			tls_not_after = EXCLUDED.tls_not_after,
			last_observed_at = EXCLUDED.last_observed_at,
			opened_at = EXCLUDED.opened_at,
			last_recovered_at = EXCLUDED.last_recovered_at,
			updated_at = EXCLUDED.updated_at
		WHERE EXCLUDED.last_observed_at >= route_canary_state.last_observed_at`,
		state.Coordinate(),
		state.ServiceID,
		state.EnvironmentID,
		state.DeploymentUnitID,
		state.Hostname,
		state.Open,
		string(state.Classification),
		string(state.Perspective),
		state.ConsecutiveFailures,
		state.ConsecutiveSuccesses,
		state.FailureReason,
		state.TLSNotAfter,
		state.LastObservedAt,
		state.OpenedAt,
		state.LastRecoveredAt,
		state.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert route canary state: %w", err)
	}
	return nil
}

// GetState returns the durable state for one managed route, or nil when the
// route has never been observed.
func (r *PgRouteCanaryRepository) GetState(ctx context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+routeCanaryStateColumns+`
		FROM route_canary_state
		WHERE route_coordinate = $1`, key.Coordinate())
	state, err := scanRouteCanaryState(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get route canary state: %w", err)
	}
	return state, nil
}

// ListState returns every observed route, newest observation first.
func (r *PgRouteCanaryRepository) ListState(ctx context.Context) ([]domain.RouteCanaryState, error) {
	return r.queryStates(ctx, `
		SELECT `+routeCanaryStateColumns+`
		FROM route_canary_state
		ORDER BY last_observed_at DESC`)
}

// ListOpenState returns only routes with a currently declared outage.
func (r *PgRouteCanaryRepository) ListOpenState(ctx context.Context) ([]domain.RouteCanaryState, error) {
	return r.queryStates(ctx, `
		SELECT `+routeCanaryStateColumns+`
		FROM route_canary_state
		WHERE open
		ORDER BY opened_at DESC`)
}

// ListStateByEnvironment returns observed routes for one environment.
func (r *PgRouteCanaryRepository) ListStateByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.RouteCanaryState, error) {
	return r.queryStates(ctx, `
		SELECT `+routeCanaryStateColumns+`
		FROM route_canary_state
		WHERE environment_id = $1
		ORDER BY last_observed_at DESC`, environmentID)
}

func (r *PgRouteCanaryRepository) queryStates(ctx context.Context, sql string, args ...any) ([]domain.RouteCanaryState, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list route canary state: %w", err)
	}
	defer rows.Close()

	states := make([]domain.RouteCanaryState, 0)
	for rows.Next() {
		state, err := scanRouteCanaryState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan route canary state: %w", err)
		}
		states = append(states, *state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route canary state: %w", err)
	}
	return states, nil
}

// AppendEvent writes one append-only lineage record.
func (r *PgRouteCanaryRepository) AppendEvent(ctx context.Context, event *domain.RouteCanaryEvent) error {
	if event == nil {
		return fmt.Errorf("route canary event is required")
	}
	if event.Hostname == "" {
		return fmt.Errorf("route canary event hostname is required")
	}
	if !event.Classification.Valid() {
		return fmt.Errorf("route canary event has unknown classification %q", event.Classification)
	}
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = time.Now().UTC()
	}
	event.Reason = domain.SanitizeEvidence(event.Reason)
	event.Evidence = domain.SanitizeEvidence(event.Evidence)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO route_canary_events (`+routeCanaryEventColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		event.ID,
		event.Coordinate(),
		event.ServiceID,
		event.EnvironmentID,
		event.DeploymentUnitID,
		event.Hostname,
		string(event.Transition),
		string(event.PreviousClassification),
		string(event.Classification),
		string(event.Perspective),
		event.Reason,
		event.Evidence,
		string(event.ObservedInstanceStatus),
		event.ObservedAt,
	)
	if err != nil {
		return fmt.Errorf("append route canary event: %w", err)
	}
	return nil
}

// ListRecentEvents returns the newest lineage records for one route.
func (r *PgRouteCanaryRepository) ListRecentEvents(ctx context.Context, key domain.RouteCanaryKey, limit int) ([]domain.RouteCanaryEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+routeCanaryEventColumns+`
		FROM route_canary_events
		WHERE route_coordinate = $1
		ORDER BY observed_at DESC
		LIMIT $2`, key.Coordinate(), limit)
	if err != nil {
		return nil, fmt.Errorf("list route canary events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.RouteCanaryEvent, 0)
	for rows.Next() {
		var (
			event                  domain.RouteCanaryEvent
			coordinate             string
			transition             string
			previousClassification string
			classification         string
			perspective            string
			instanceStatus         string
		)
		if err := rows.Scan(
			&event.ID,
			&coordinate,
			&event.ServiceID,
			&event.EnvironmentID,
			&event.DeploymentUnitID,
			&event.Hostname,
			&transition,
			&previousClassification,
			&classification,
			&perspective,
			&event.Reason,
			&event.Evidence,
			&instanceStatus,
			&event.ObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scan route canary event: %w", err)
		}
		event.Transition = domain.RouteCanaryTransition(transition)
		event.PreviousClassification = domain.RouteCanaryClassification(previousClassification)
		event.Classification = domain.RouteCanaryClassification(classification)
		event.Perspective = domain.RouteCanaryPerspective(perspective)
		event.ObservedInstanceStatus = domain.InstanceHealthStatus(instanceStatus)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route canary events: %w", err)
	}
	return events, nil
}

// DeleteState removes route state and its lineage, used when a managed route is
// withdrawn from desired state so a removed route does not alarm forever.
func (r *PgRouteCanaryRepository) DeleteState(ctx context.Context, key domain.RouteCanaryKey) error {
	return r.withinTx(ctx, func(txRepo *PgRouteCanaryRepository) error {
		if _, err := txRepo.pool.Exec(ctx, `DELETE FROM route_canary_events WHERE route_coordinate = $1`, key.Coordinate()); err != nil {
			return fmt.Errorf("delete route canary events: %w", err)
		}
		if _, err := txRepo.pool.Exec(ctx, `DELETE FROM route_canary_state WHERE route_coordinate = $1`, key.Coordinate()); err != nil {
			return fmt.Errorf("delete route canary state: %w", err)
		}
		return nil
	})
}

type routeCanaryScanner interface {
	Scan(dest ...any) error
}

func scanRouteCanaryState(scanner routeCanaryScanner) (*domain.RouteCanaryState, error) {
	var (
		state          domain.RouteCanaryState
		coordinate     string
		classification string
		perspective    string
	)
	if err := scanner.Scan(
		&coordinate,
		&state.ServiceID,
		&state.EnvironmentID,
		&state.DeploymentUnitID,
		&state.Hostname,
		&state.Open,
		&classification,
		&perspective,
		&state.ConsecutiveFailures,
		&state.ConsecutiveSuccesses,
		&state.FailureReason,
		&state.TLSNotAfter,
		&state.LastObservedAt,
		&state.OpenedAt,
		&state.LastRecoveredAt,
		&state.UpdatedAt,
	); err != nil {
		return nil, err
	}
	state.Classification = domain.RouteCanaryClassification(classification)
	state.Perspective = domain.RouteCanaryPerspective(perspective)
	return &state, nil
}

func (r *PgRouteCanaryRepository) withinTx(ctx context.Context, fn func(*PgRouteCanaryRepository) error) error {
	beginner, ok := r.pool.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return fmt.Errorf("route canary repository does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin route canary transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(newPgRouteCanaryRepositoryWithDB(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit route canary transaction: %w", err)
	}
	committed = true
	return nil
}
