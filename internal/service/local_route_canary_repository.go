package service

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// LocalRouteCanaryRepository uses canonical route state for restart and an
// in-process overlay for observations not yet projected by the event bus.
// PostgreSQL is only a best-effort REST/query index.
type LocalRouteCanaryRepository struct {
	State   LocalSupervisionState
	Index   RouteCanaryRepository
	Logger  *zap.Logger
	mu      sync.Mutex
	current map[string]domain.RouteCanaryState
}

func NewLocalRouteCanaryRepository(state LocalSupervisionState, index RouteCanaryRepository, logger *zap.Logger) *LocalRouteCanaryRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LocalRouteCanaryRepository{State: state, Index: index, Logger: logger, current: make(map[string]domain.RouteCanaryState)}
}

func (r *LocalRouteCanaryRepository) UpsertStateWithEvent(ctx context.Context, state *domain.RouteCanaryState, event *domain.RouteCanaryEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	r.mu.Lock()
	r.current[state.Coordinate()] = *state
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.UpsertStateWithEvent(ctx, state, event); err != nil {
			r.Logger.Warn("route canary SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalRouteCanaryRepository) UpsertState(ctx context.Context, state *domain.RouteCanaryState) error {
	r.mu.Lock()
	r.current[state.Coordinate()] = *state
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.UpsertState(ctx, state); err != nil {
			r.Logger.Warn("route canary SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

func (r *LocalRouteCanaryRepository) GetState(ctx context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	states, err := r.ListState(ctx)
	if err != nil {
		return nil, err
	}
	for i := range states {
		if states[i].Coordinate() == key.Coordinate() {
			return &states[i], nil
		}
	}
	return nil, nil
}

func (r *LocalRouteCanaryRepository) ListState(context.Context) ([]domain.RouteCanaryState, error) {
	records, err := r.State.records(kinds.CPStateTopicRouteCanary)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]domain.RouteCanaryState)
	for _, event := range records {
		if localTag(event, "schema") != routeCanaryStateSchema {
			continue
		}
		var projection struct {
			RouteCanary *domain.RouteCanaryState `json:"route_canary"`
		}
		if !localStateContent(event, &projection) || projection.RouteCanary == nil {
			continue
		}
		state := *projection.RouteCanary
		byKey[state.Coordinate()] = state
	}
	r.mu.Lock()
	for key, state := range r.current {
		if prior, ok := byKey[key]; !ok || !prior.UpdatedAt.After(state.UpdatedAt) {
			byKey[key] = state
		}
	}
	r.mu.Unlock()
	result := make([]domain.RouteCanaryState, 0, len(byKey))
	for _, state := range byKey {
		result = append(result, state)
	}
	return result, nil
}

func (r *LocalRouteCanaryRepository) DeleteState(ctx context.Context, key domain.RouteCanaryKey) error {
	r.mu.Lock()
	delete(r.current, key.Coordinate())
	r.mu.Unlock()
	if r.Index != nil {
		if err := r.Index.DeleteState(ctx, key); err != nil {
			r.Logger.Warn("route canary SQL index unavailable", zap.Error(err))
		}
	}
	return nil
}

var _ RouteCanaryRepository = (*LocalRouteCanaryRepository)(nil)
