package controlplane

import (
	"context"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func TestReactorKindCatalogOptionIsApplied(t *testing.T) {
	catalog := nostradapter.NewKindCatalog()

	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		WithKindCatalog(catalog),
	)

	if r.kindCatalog != catalog {
		t.Fatal("expected WithKindCatalog to set catalog")
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

func TestReactorFallsBackToNowWithoutLastSeen(t *testing.T) {
	r := NewReactor(Config{}, nil, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	before := gonostr.Now()
	got := r.requestSubscriptionSince(context.Background())
	after := gonostr.Now()
	if got < before || got > after {
		t.Fatalf("expected fallback cursor between %v and %v, got %v", before, after, got)
	}
}

func TestSubscriberAndReactorDefaultSubscriptionsDoNotOverlapOrIncludeLegacy(t *testing.T) {
	reactorKinds := requestSubscriptionKinds()
	for _, kind := range append(append([]int{}, reactorKinds...), nostradapter.DefaultInboundKinds...) {
		if isLegacyProductionRuntimeKind(kind) && kind != KindArtifactRegister {
			t.Fatalf("production default subscription still includes legacy runtime kind %d", kind)
		}
	}
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
	event := signedControlPlaneTestEvent(t, nostradapter.KindCASControlState)

	r.handleEvent(ctx, event)

	rec, err := repo.GetByID(ctx, event.ID.Hex())
	if err != nil {
		t.Fatalf("get audit record: %v", err)
	}
	if rec == nil {
		t.Fatal("expected inbound control-plane event to be audited")
	}
	if rec.Kind != int(event.Kind) || rec.PubKey != event.PubKey.Hex() || rec.Content != event.Content || rec.Sig != gonostr.HexEncodeToString(event.Sig[:]) {
		t.Fatalf("audit record mismatch: got %#v for event %#v", rec, event)
	}
}

func signedControlPlaneTestEventAt(t *testing.T, kind int, createdAt gonostr.Timestamp) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: createdAt, Content: "{}", Tags: gonostr.Tags{}}
	secret := testNostrSecretKey(t, "1111111111111111111111111111111111111111111111111111111111111111")
	if err := ev.Sign(secret); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}
