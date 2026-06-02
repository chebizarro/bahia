package controlplane

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func signedControlPlaneTestEvent(t *testing.T, kind int) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: kind, CreatedAt: gonostr.Now(), Content: "{}", Tags: gonostr.Tags{}}
	if err := ev.Sign("1111111111111111111111111111111111111111111111111111111111111111"); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return ev
}

func TestReactorHandleEventDropsInvalidBeforeDedupAndDispatch(t *testing.T) {
	r := NewReactor(Config{}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())
	valid := signedControlPlaneTestEvent(t, KindDeployRequest)
	invalid := *valid
	invalid.ID = strings.Repeat("0", 64)

	r.handleEvent(context.Background(), &invalid)

	if r.dedup.IsDuplicate(valid.ID) {
		t.Fatal("invalid control-plane event must not be marked seen")
	}
}

func TestReactorHandleEventDropsLegacyRuntimeKindBeforeAudit(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewInMemoryNostrEventRepository()
	r := NewReactor(Config{}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), WithNostrEventRepository(repo))
	event := signedControlPlaneTestEvent(t, KindDeployRequest)

	r.handleEvent(ctx, event)

	rec, err := repo.GetByID(ctx, event.ID)
	if err != nil {
		t.Fatalf("get audit record: %v", err)
	}
	if rec != nil {
		t.Fatalf("legacy runtime event should be dropped before audit, got %#v", rec)
	}
	if r.dedup.IsDuplicate(event.ID) {
		t.Fatal("legacy runtime event must not be marked seen")
	}
}

func TestReactorHandleEventRecordsEOSEState(t *testing.T) {
	r := NewReactor(Config{}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	if r.caughtUp.Load() {
		t.Fatal("reactor should start before EOSE catch-up")
	}
	r.handleEOSE()
	if !r.caughtUp.Load() {
		t.Fatal("reactor should record aggregate EOSE catch-up")
	}
	r.handleEOSE()
	if !r.caughtUp.Load() {
		t.Fatal("duplicate EOSE should be harmless")
	}
}

func TestReactorBuildRequestSubscriptionFiltersUsesCanonicalKindsOnly(t *testing.T) {
	since := gonostr.Timestamp(12345)
	r := NewReactor(Config{
		AuthorizedPubkeys:              []string{"global", "global", ""},
		AdoptionAuthorizedPubkeys:      []string{"adoption", "global", "adoption", ""},
		DirectRuntimeAuthorizedPubkeys: []string{"runtime", "global", "runtime", ""},
	}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	filters := r.buildRequestSubscriptionFilters(since)
	if len(filters) != 1 {
		t.Fatalf("expected one canonical runtime replay filter, got %d", len(filters))
	}
	filter := filters[0]
	assertAuthors(t, filter.Authors, nil)
	assertFilterHasKinds(t, filter, KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap, nostr.KindHeartbeatObservation)
	assertFilterMissingKinds(t, filter,
		nostr.KindCASControlState,
		nostr.KindCASAudit,
		nostr.KindNIP38Status,
		KindDeployRequest,
		KindRollbackRequest,
		KindServiceAction,
		KindAdoptionScanRequest,
		KindAdoptionImportRequest,
		KindPackageDriftDetect,
		nostr.KindFailoverRequest,
	)
	if filter.Since == nil || *filter.Since != since {
		t.Fatalf("filter should preserve shared since cursor %v, got %v", since, filter.Since)
	}
}

func TestReactorBuildRequestSubscriptionFiltersIgnoresLegacyAuthorScopes(t *testing.T) {
	r := NewReactor(Config{AuthorizedPubkeys: []string{"global"}}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	filters := r.buildRequestSubscriptionFilters(gonostr.Timestamp(67890))
	if len(filters) != 1 {
		t.Fatalf("expected one canonical runtime replay filter, got %d", len(filters))
	}
	assertAuthors(t, filters[0].Authors, nil)
	assertFilterMissingKinds(t, filters[0], KindServiceAction, KindAdoptionScanRequest, KindAdoptionImportRequest, KindWorkerCleanupRequest)
}

func assertAuthors(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authors mismatch: got %v want %v", got, want)
	}
}

func assertFilterHasKinds(t *testing.T, filter gonostr.Filter, kinds ...int) {
	t.Helper()
	for _, kind := range kinds {
		if !slices.Contains(filter.Kinds, kind) {
			t.Fatalf("expected filter kinds %v to include %d", filter.Kinds, kind)
		}
	}
}

func assertFilterMissingKinds(t *testing.T, filter gonostr.Filter, kinds ...int) {
	t.Helper()
	for _, kind := range kinds {
		if slices.Contains(filter.Kinds, kind) {
			t.Fatalf("expected filter kinds %v not to include %d", filter.Kinds, kind)
		}
	}
}

func filterWithKinds(t *testing.T, filters []gonostr.Filter, kinds ...int) gonostr.Filter {
	t.Helper()
	for _, filter := range filters {
		matched := true
		for _, kind := range kinds {
			if !slices.Contains(filter.Kinds, kind) {
				matched = false
				break
			}
		}
		if matched {
			return filter
		}
	}
	t.Fatalf("no filter contained all kinds %v", kinds)
	return gonostr.Filter{}
}

func filterWithoutKinds(t *testing.T, filters []gonostr.Filter, kinds ...int) gonostr.Filter {
	t.Helper()
	for _, filter := range filters {
		matched := true
		for _, kind := range kinds {
			if slices.Contains(filter.Kinds, kind) {
				matched = false
				break
			}
		}
		if matched {
			return filter
		}
	}
	t.Fatalf("no filter excluded all kinds %v", kinds)
	return gonostr.Filter{}
}
