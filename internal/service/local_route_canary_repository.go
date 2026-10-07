package service

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// LocalRouteCanaryRepository is the route canary state the supervisor and the
// post-deploy gate decide from.
//
// A route's durable state is its canonical route-canary record (kind 30900,
// d = route coordinate) in the local event store, so a restarted or
// PostgreSQL-less daemon resumes the failure streak and the outage start it
// had published. The values written in this process are kept in memory as
// well: they are what the canonical record was built from, unredacted, and
// they keep the decision state current when a record's relay delivery was
// abandoned. PostgreSQL is a write-behind query index: a failed index write is
// logged and never fails the caller.
type LocalRouteCanaryRepository struct {
	state  LocalSupervisionState
	index  RouteCanaryRepository
	logger *zap.Logger
	// canonical, when set, withdraws a deleted route's canonical record with
	// a tombstone on the route coordinate.
	canonical *RouteCanaryProjector
	now       func() time.Time
	// resumeFromIndex makes a route that has neither a canonical record nor a
	// value written in this process read its state from the index. It is set
	// only when the daemon publishes no canonical route-canary records (relay
	// publishing disabled), where the index is the only durable copy.
	resumeFromIndex bool

	mu      sync.Mutex
	written map[string]domain.RouteCanaryState
}

// LocalRouteCanaryOption configures a LocalRouteCanaryRepository.
type LocalRouteCanaryOption func(*LocalRouteCanaryRepository)

// WithRouteCanaryIndexResume makes the index the durable copy of route state.
// Use it only when no canonical route-canary records are published.
func WithRouteCanaryIndexResume() LocalRouteCanaryOption {
	return func(r *LocalRouteCanaryRepository) { r.resumeFromIndex = true }
}

// WithRouteCanaryCanonicalProjector makes DeleteState publish the route's
// tombstone through projector before forgetting it, so the withdrawal is
// canonical and survives a restart.
func WithRouteCanaryCanonicalProjector(projector *RouteCanaryProjector) LocalRouteCanaryOption {
	return func(r *LocalRouteCanaryRepository) { r.canonical = projector }
}

// NewLocalRouteCanaryRepository builds the repository. index may be nil.
func NewLocalRouteCanaryRepository(state LocalSupervisionState, index RouteCanaryRepository, logger *zap.Logger, opts ...LocalRouteCanaryOption) *LocalRouteCanaryRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	r := &LocalRouteCanaryRepository{state: state, index: index, logger: logger.Named("route-canary-state"),
		now: func() time.Time { return time.Now().UTC() }, written: map[string]domain.RouteCanaryState{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// UpsertStateWithEvent records a transition and indexes it with its lineage.
func (r *LocalRouteCanaryRepository) UpsertStateWithEvent(ctx context.Context, state *domain.RouteCanaryState, event *domain.RouteCanaryEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	r.remember(*state)
	if r.index != nil {
		if err := r.index.UpsertStateWithEvent(ctx, state, event); err != nil {
			r.logger.Warn("route canary SQL index write failed", zap.String("route", state.Coordinate()), zap.Error(err))
		}
	}
	return nil
}

// UpsertState records an observation that changed no outage state.
func (r *LocalRouteCanaryRepository) UpsertState(ctx context.Context, state *domain.RouteCanaryState) error {
	r.remember(*state)
	if r.index != nil {
		if err := r.index.UpsertState(ctx, state); err != nil {
			r.logger.Warn("route canary SQL index write failed", zap.String("route", state.Coordinate()), zap.Error(err))
		}
	}
	return nil
}

func (r *LocalRouteCanaryRepository) remember(state domain.RouteCanaryState) {
	r.mu.Lock()
	r.written[state.Coordinate()] = state
	r.mu.Unlock()
}

// GetState returns the route's latest state, or nil when it has none.
func (r *LocalRouteCanaryRepository) GetState(ctx context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	record, err := r.state.coordinate(ctx, key.Coordinate())
	if err != nil {
		return nil, err
	}
	var latest *domain.RouteCanaryState
	if record != nil {
		if state, ok := decodeRouteCanaryStateRecord(*record); ok {
			latest = &state
		}
	}
	r.mu.Lock()
	written, ok := r.written[key.Coordinate()]
	r.mu.Unlock()
	if ok && (latest == nil || !latest.UpdatedAt.After(written.UpdatedAt)) {
		latest = &written
	}
	if latest == nil && r.resumeFromIndex && r.index != nil {
		indexed, err := r.index.GetState(ctx, key)
		if err != nil {
			r.logger.Warn("route canary SQL index read failed", zap.String("route", key.Coordinate()), zap.Error(err))
			return nil, nil
		}
		return indexed, nil
	}
	return latest, nil
}

// ListState returns the latest state of every route that has one.
func (r *LocalRouteCanaryRepository) ListState(ctx context.Context) ([]domain.RouteCanaryState, error) {
	records, err := r.state.family(ctx, kinds.CPStateTopicRouteCanary)
	if err != nil {
		return nil, err
	}
	byCoordinate := make(map[string]domain.RouteCanaryState, len(records))
	for _, record := range records {
		if state, ok := decodeRouteCanaryStateRecord(record); ok {
			byCoordinate[state.Coordinate()] = state
		}
	}
	r.mu.Lock()
	for coordinate, written := range r.written {
		if canonical, ok := byCoordinate[coordinate]; !ok || !canonical.UpdatedAt.After(written.UpdatedAt) {
			byCoordinate[coordinate] = written
		}
	}
	r.mu.Unlock()
	out := make([]domain.RouteCanaryState, 0, len(byCoordinate))
	for _, state := range byCoordinate {
		out = append(out, state)
	}
	return out, nil
}

// DeleteState withdraws the route: its canonical state record is replaced by
// a tombstone on the route coordinate first (bahia-as2bo), then the state
// written in this process and the index row are forgotten. A failed tombstone
// publish fails the call and changes nothing, so the caller retries.
func (r *LocalRouteCanaryRepository) DeleteState(ctx context.Context, key domain.RouteCanaryKey) error {
	if r.canonical != nil {
		if err := r.canonical.Withdraw(ctx, key, r.now()); err != nil {
			return err
		}
	}
	r.mu.Lock()
	delete(r.written, key.Coordinate())
	r.mu.Unlock()
	if r.index != nil {
		if err := r.index.DeleteState(ctx, key); err != nil {
			r.logger.Warn("route canary SQL index delete failed", zap.String("route", key.Coordinate()), zap.Error(err))
		}
	}
	return nil
}

// decodeRouteCanaryStateRecord decodes the canonical route-canary state record
// the RouteCanaryProjector publishes.
func decodeRouteCanaryStateRecord(ev gonostr.Event) (domain.RouteCanaryState, bool) {
	if supervisionTag(ev, kinds.CASControlStateTagSchema) != routeCanaryStateSchema || supervisionTag(ev, kinds.CASControlStateTagDeleted) == "true" {
		return domain.RouteCanaryState{}, false
	}
	var projection routeCanaryProjection
	if json.Unmarshal([]byte(ev.Content), &projection) != nil || projection.RouteCanary == nil {
		return domain.RouteCanaryState{}, false
	}
	state := *projection.RouteCanary
	if state.ServiceID == uuid.Nil || state.EnvironmentID == uuid.Nil || state.Coordinate() != supervisionTag(ev, kinds.CASControlStateTagD) {
		return domain.RouteCanaryState{}, false
	}
	return state, true
}

var _ RouteCanaryRepository = (*LocalRouteCanaryRepository)(nil)
