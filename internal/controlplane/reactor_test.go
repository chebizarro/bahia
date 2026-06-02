package controlplane

import (
	"context"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type reactorCursorSource struct {
	latest time.Time
	kinds  []int
}

func (s *reactorCursorSource) LatestEventTimestamp(_ context.Context, kinds []int) (time.Time, error) {
	s.kinds = append([]int(nil), kinds...)
	return s.latest, nil
}

func TestReactorReplayCursorOptionsAreApplied(t *testing.T) {
	planner := nostradapter.NewReplayCursorPlanner(time.Second)
	catalog := nostradapter.NewKindCatalog()

	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		WithReplayCursorPlanner(planner),
		WithKindCatalog(catalog),
	)

	if r.replayCursorPlanner != planner {
		t.Fatal("expected WithReplayCursorPlanner to set planner")
	}
	if r.kindCatalog != catalog {
		t.Fatal("expected WithKindCatalog to set catalog")
	}
}

func TestReactorUsesReplayCursorPlannerForRequestSubscriptionSince(t *testing.T) {
	latest := time.Unix(1_700_000_000, 0).UTC()
	source := &reactorCursorSource{latest: latest}
	planner := nostradapter.NewReplayCursorPlanner(time.Second, source)
	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithReplayCursorPlanner(planner))

	filters := r.buildRequestSubscriptionFiltersForCurrentCursor(context.Background())
	want := gonostr.Timestamp(latest.Add(-time.Second).Unix())
	for i, filter := range filters {
		if filter.Since == nil || *filter.Since != want {
			t.Fatalf("filter %d since mismatch: got %v want %v", i, filter.Since, want)
		}
	}
	if len(source.kinds) == 0 {
		t.Fatal("expected planner to receive request subscription kinds")
	}
}

func TestReactorLastSeenTrackingUpdatesCursor(t *testing.T) {
	catalog := nostradapter.NewKindCatalog()
	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithKindCatalog(catalog))
	eventTime := gonostr.Timestamp(time.Now().Unix() - 60) // 1 minute ago, within validation window

	r.handleEvent(context.Background(), signedControlPlaneTestEventAt(t, nostradapter.KindCASControlState, eventTime))

	got := r.lastSeenByGroup["state_snapshot"]
	if got != eventTime {
		t.Fatalf("lastSeen mismatch: got %v want %v", got, eventTime)
	}
}

func TestReactorFallsBackToNowWhenPlannerReturnsNil(t *testing.T) {
	planner := nostradapter.NewReplayCursorPlanner(time.Second)
	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithReplayCursorPlanner(planner))

	before := gonostr.Now()
	got := r.requestSubscriptionSince(context.Background())
	after := gonostr.Now()
	if got < before || got > after {
		t.Fatalf("expected fallback cursor between %v and %v, got %v", before, after, got)
	}
}

func TestSubscriberAndReactorDefaultSubscriptionsDoNotOverlap(t *testing.T) {
	reactorKinds := requestSubscriptionKinds()
	for _, kind := range nostradapter.DefaultInboundKinds {
		for _, reactorKind := range reactorKinds {
			if kind == reactorKind {
				t.Fatalf("subscriber default kind %d duplicates reactor subscription kinds", kind)
			}
		}
	}
}

func TestReactorAuditsAcceptedInboundEventToRepository(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithNostrEventRepository(repo))
	event := signedControlPlaneTestEvent(t, KindDNSZoneCreateRequest)

	r.handleEvent(ctx, event)

	rec, err := repo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get audit record: %v", err)
	}
	if rec == nil {
		t.Fatal("expected inbound control-plane event to be audited")
	}
	if rec.Kind != event.Kind || rec.PubKey != event.PubKey || rec.Content != event.Content || rec.Sig != event.Sig {
		t.Fatalf("audit record mismatch: got %#v for event %#v", rec, event)
	}
}

func signedControlPlaneTestEventAt(t *testing.T, kind int, createdAt gonostr.Timestamp) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: kind, CreatedAt: createdAt, Content: "{}", Tags: gonostr.Tags{}}
	if err := ev.Sign("1111111111111111111111111111111111111111111111111111111111111111"); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}
