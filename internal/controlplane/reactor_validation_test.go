package controlplane

import (
	"context"
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
