package controlplane

import (
	"context"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
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

	r.handleEvent(context.Background(), signedControlPlaneTestEventAt(t, nostradapter.KindControlPlaneDeploymentStatus, eventTime))

	got := r.lastSeenByGroup["core_control_plane_live"]
	if got != eventTime {
		t.Fatalf("lastSeen mismatch: got %v want %v", got, eventTime)
	}
	if since := r.requestSubscriptionSince(context.Background()); since != eventTime-1 {
		t.Fatalf("expected reconnect cursor to use lastSeen overlap, got %v want %v", since, eventTime-1)
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

func signedControlPlaneTestEventAt(t *testing.T, kind int, createdAt gonostr.Timestamp) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: kind, CreatedAt: createdAt, Content: "{}", Tags: gonostr.Tags{}}
	if err := ev.Sign("1111111111111111111111111111111111111111111111111111111111111111"); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}
