package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLM route creates are resolved by the client-minted id,
// then by content, like services and environments.
func TestLLMRouteCreateIsIdempotentByClientID(t *testing.T) {
	ctx := context.Background()
	routes := &identityRouteRepo{rows: map[uuid.UUID][]byte{}}
	bus := &capturePublisher{}
	llm := NewLLMRegistryService(routes, nil, nil, nil, nil, nil, nil, bus, zap.NewNop())

	id := domain.NewEntityID()
	request := func() *domain.LLMRoute {
		return &domain.LLMRoute{ID: id, Name: "chat-prod", Description: "chat completions", Metadata: map[string]any{"tier": "gold", "weight": 3}}
	}
	first := request()
	if err := llm.CreateRoute(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}
	if routes.creates != 1 || len(bus.events) != 1 || first.ID != id {
		t.Fatalf("create: writes=%d events=%d id=%s", routes.creates, len(bus.events), first.ID)
	}

	// The stored row comes back with database defaults ({} for unset JSON
	// objects, the stamped timestamps); the retry still matches it.
	retry := request()
	if err := llm.CreateRoute(ctx, retry); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if routes.creates != 1 || len(bus.events) != 1 || retry.CreatedAt.IsZero() {
		t.Fatalf("retry wrote or emitted again: writes=%d events=%d, route %+v", routes.creates, len(bus.events), retry)
	}

	conflicting := request()
	conflicting.Description = "embeddings"
	if err := llm.CreateRoute(ctx, conflicting); !errors.Is(err, domain.ErrEntityIDConflict) {
		t.Fatalf("different content: err = %v, want ErrEntityIDConflict", err)
	}

	minted := &domain.LLMRoute{Name: "embed"}
	if err := llm.CreateRoute(ctx, minted); err != nil || minted.ID.Version() != 7 {
		t.Fatalf("create without id: id %s err %v, want a UUIDv7", minted.ID, err)
	}
}

func TestCanonicalCreateContentIgnoresEmptyValuesAndServerFields(t *testing.T) {
	a := domain.LLMRoute{ID: domain.NewEntityID(), Name: "chat", Metadata: nil, CreatedAt: time.Now()}
	b := domain.LLMRoute{ID: domain.NewEntityID(), Name: "chat", Metadata: map[string]any{}, GatewayConfig: &domain.LLMGatewayRouteConfig{}}
	if string(canonicalCreateContent(a)) != string(canonicalCreateContent(b)) {
		t.Fatalf("empty values or server fields changed the content:\n%s\n%s", canonicalCreateContent(a), canonicalCreateContent(b))
	}
	b.Metadata = map[string]any{"tier": "gold"}
	if string(canonicalCreateContent(a)) == string(canonicalCreateContent(b)) {
		t.Fatal("a declared field did not change the content")
	}
}

// identityRouteRepo keeps routes as their JSON rows, so reads return what a
// database would (an unset object comes back as {}).
type identityRouteRepo struct {
	rows    map[uuid.UUID][]byte
	creates int
}

func (r *identityRouteRepo) Create(_ context.Context, route *domain.LLMRoute) error {
	if _, ok := r.rows[route.ID]; ok {
		return repository.ErrAlreadyExists
	}
	r.creates++
	route.CreatedAt, route.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	stored := *route
	if stored.Metadata == nil {
		stored.Metadata = map[string]any{}
	}
	if stored.DefaultPlacementPolicy == nil {
		stored.DefaultPlacementPolicy = &domain.LLMPlacementPolicy{}
	}
	raw, _ := json.Marshal(stored)
	r.rows[route.ID] = raw
	return nil
}

func (r *identityRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	raw, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	var route domain.LLMRoute
	if err := json.Unmarshal(raw, &route); err != nil {
		return nil, err
	}
	return &route, nil
}

func (r *identityRouteRepo) GetByName(context.Context, string) (*domain.LLMRoute, error) {
	return nil, nil
}
func (r *identityRouteRepo) List(context.Context, int, int) ([]domain.LLMRoute, error) {
	return nil, nil
}
func (r *identityRouteRepo) Update(context.Context, *domain.LLMRoute) error { return nil }
func (r *identityRouteRepo) Delete(context.Context, uuid.UUID) error        { return nil }
