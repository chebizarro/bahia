package controlplane

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr"
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

func TestReactorBuildRequestSubscriptionFiltersScopesAuthorsByKind(t *testing.T) {
	since := gonostr.Timestamp(12345)
	r := NewReactor(Config{
		AuthorizedPubkeys:              []string{"global", "global", ""},
		AdoptionAuthorizedPubkeys:      []string{"adoption", "global", "adoption", ""},
		DirectRuntimeAuthorizedPubkeys: []string{"runtime", "global", "runtime", ""},
	}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	filters := r.buildRequestSubscriptionFilters(since)
	if len(filters) != 4 {
		t.Fatalf("expected default, service-action, adoption, and heartbeat filters, got %d", len(filters))
	}

	defaultFilter := filterWithoutKinds(t, filters, KindServiceAction, KindAdoptionScanRequest, KindAdoptionImportRequest)
	assertAuthors(t, defaultFilter.Authors, []string{"global"})
	assertFilterHasKinds(t, defaultFilter, KindDeployRequest, KindRollbackRequest, KindPackageDriftDetect, nostr.KindContinuityProfile, nostr.KindFailoverRequest)
	assertFilterMissingKinds(t, defaultFilter, KindServiceAction, KindAdoptionScanRequest, KindAdoptionImportRequest)

	serviceActionFilter := filterWithKinds(t, filters, KindServiceAction)
	assertAuthors(t, serviceActionFilter.Authors, []string{"global", "runtime"})
	assertFilterHasKinds(t, serviceActionFilter, KindServiceAction)
	assertFilterMissingKinds(t, serviceActionFilter, KindAdoptionScanRequest, KindAdoptionImportRequest, KindDeployRequest)

	adoptionFilter := filterWithKinds(t, filters, KindAdoptionScanRequest, KindAdoptionImportRequest)
	assertAuthors(t, adoptionFilter.Authors, []string{"global", "adoption"})
	assertFilterHasKinds(t, adoptionFilter, KindAdoptionScanRequest, KindAdoptionImportRequest)
	assertFilterMissingKinds(t, adoptionFilter, KindServiceAction, KindDeployRequest)

	heartbeatFilter := filterWithKinds(t, filters, nostr.KindHeartbeatObservation)
	if len(heartbeatFilter.Authors) != 0 {
		t.Fatalf("heartbeat filter should not be author-scoped, got %v", heartbeatFilter.Authors)
	}
	assertFilterHasKinds(t, heartbeatFilter, nostr.KindHeartbeatObservation)
	assertFilterMissingKinds(t, heartbeatFilter, KindDeployRequest, KindServiceAction)

	for i, filter := range filters {
		if filter.Since == nil || *filter.Since != since {
			t.Fatalf("filter %d should preserve shared since cursor %v, got %v", i, since, filter.Since)
		}
	}
}

func TestReactorBuildRequestSubscriptionFiltersPreservesGlobalOnlyBehavior(t *testing.T) {
	r := NewReactor(Config{AuthorizedPubkeys: []string{"global"}}, nil, nostr.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop())

	filters := r.buildRequestSubscriptionFilters(gonostr.Timestamp(67890))
	if len(filters) != 4 {
		t.Fatalf("expected split subscription filters, got %d", len(filters))
	}
	for _, filter := range filters {
		if slices.Contains(filter.Kinds, nostr.KindHeartbeatObservation) {
			assertAuthors(t, filter.Authors, nil)
			continue
		}
		assertAuthors(t, filter.Authors, []string{"global"})
	}
	assertFilterMissingKinds(t, filterWithoutKinds(t, filters, KindServiceAction, KindAdoptionScanRequest, KindAdoptionImportRequest, nostr.KindHeartbeatObservation), KindServiceAction, KindAdoptionScanRequest, KindAdoptionImportRequest, nostr.KindHeartbeatObservation)
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
