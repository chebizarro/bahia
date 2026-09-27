package soulfactory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

func acceptingEndpoint(url string) *fakeRelayEndpoint {
	endpoint := newFakeRelayEndpoint(url)
	endpoint.publishFn = func(_ context.Context, event nostr.Event) RelayPublishResult {
		endpoint.published = append(endpoint.published, event)
		return RelayPublishResult{RelayURL: url, Accepted: true}
	}
	return endpoint
}

func admissionTestEvent(t *testing.T, kind nostr.Kind) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: t.Name()}
	if err := event.Sign(nostr.Generate()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return event
}

func busWithAdmission(t *testing.T, admission *nostrout.Admission, endpoints ...relayBusEndpoint) *SoulFactoryRelayBus {
	t.Helper()
	bus, err := newSoulFactoryRelayBusFromEndpoints(endpoints, WithRelayBusAdmission(admission), WithRelayBusSigner(newFakeSigner(t)))
	if err != nil {
		t.Fatal(err)
	}
	return bus
}

func TestRelayBusPublishRejectedByKillSwitchSendsNoFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stop")
	if err := os.WriteFile(path, []byte("stop"), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint := acceptingEndpoint("wss://relay.example")
	bus := busWithAdmission(t, nostrout.New(nostrout.Config{KillSwitchFile: path}), endpoint)
	if _, err := bus.Publish(t.Context(), admissionTestEvent(t, 1)); !errors.Is(err, nostrout.ErrKillSwitch) {
		t.Fatalf("Publish() error = %v, want kill switch", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("kill switch must prevent every EVENT frame, sent %d", len(endpoint.published))
	}
}

func TestRelayBusesShareInjectedBudget(t *testing.T) {
	admission := nostrout.New(nostrout.Config{RatePerMinute: 1, Burst: 1})
	one := acceptingEndpoint("wss://one.example")
	two := acceptingEndpoint("wss://two.example")
	if _, err := busWithAdmission(t, admission, one).Publish(t.Context(), admissionTestEvent(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := busWithAdmission(t, admission, two).Publish(t.Context(), admissionTestEvent(t, 1)); !errors.Is(err, nostrout.ErrBudgetExceeded) {
		t.Fatalf("second bus must share the exhausted budget, got %v", err)
	}
	if len(two.published) != 0 {
		t.Fatal("rejected publication reached the relay")
	}
}

func TestConcordPublicationsArePacedByOperationInsteadOfRejected(t *testing.T) {
	admission := nostrout.New(nostrout.Config{PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
		nostrout.PurposePriority: {RatePerMinute: 1, Burst: 1},
		nostrout.PurposeBulk:     {RatePerMinute: 1_200, Burst: 1},
	}})
	endpoint := acceptingEndpoint("wss://community.example")
	bus := busWithAdmission(t, admission, endpoint)
	endpoints := []relayBusEndpoint{endpoint}

	// Without an operation, a burst of gift wraps exhausts the priority lane:
	// this is exactly how a naive hook broke multi-event rotations.
	if err := publishConcordInvite(t.Context(), bus, endpoints, admissionTestEvent(t, 1059)); err != nil {
		t.Fatal(err)
	}
	if err := publishConcordInvite(t.Context(), bus, endpoints, admissionTestEvent(t, 1059)); !errors.Is(err, nostrout.ErrBudgetExceeded) {
		t.Fatalf("second ungoverned gift wrap error = %v, want budget exhaustion", err)
	}

	op, err := admission.BeginOperation(t.Context(), nostrout.OperationSpec{MaxEvents: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	ctx := nostrout.WithOperation(t.Context(), op)
	for i := 0; i < 5; i++ {
		if err := publishConcordInvite(ctx, bus, endpoints, admissionTestEvent(t, 1059)); err != nil {
			t.Fatalf("operation publication %d failed instead of being paced: %v", i, err)
		}
	}
	if op.Remaining() != 0 {
		t.Fatalf("operation remaining = %d", op.Remaining())
	}
	if err := publishConcordInvite(ctx, bus, endpoints, admissionTestEvent(t, 1059)); !errors.Is(err, nostrout.ErrOperation) {
		t.Fatalf("publication beyond the declared bound error = %v", err)
	}
	if got := len(endpoint.published); got != 6 {
		t.Fatalf("relay frames = %d, want 6", got)
	}
	if got := admission.Metrics().Admitted; got != 6 {
		t.Fatalf("admitted = %d, want 6", got)
	}
}

func TestConcordAuthRetryIsAdmittedAsSecondFrame(t *testing.T) {
	admission := nostrout.New(nostrout.Config{RelayWire: nostrout.PurposeBudget{RatePerMinute: 1, Burst: 1}})
	endpoint := newFakeRelayEndpoint("wss://auth.example")
	endpoint.publishResults = []RelayPublishResult{{Reason: "auth-required: sign in"}}
	bus := busWithAdmission(t, admission, endpoint)
	err := publishConcordInvite(t.Context(), bus, []relayBusEndpoint{endpoint}, admissionTestEvent(t, 1059))
	if !errors.Is(err, nostrout.ErrBudgetExceeded) {
		t.Fatalf("AUTH retry over the wire budget error = %v", err)
	}
	if endpoint.publishCalls != 1 {
		t.Fatalf("frames sent = %d, want only the first", endpoint.publishCalls)
	}
	if len(endpoint.authCalls) != 1 {
		t.Fatal("expected the relay challenge to be answered before the admitted retry")
	}
}

func TestConcordReplayOfAcceptedWrapSendsNoFrame(t *testing.T) {
	admission := nostrout.New(nostrout.Config{})
	endpoint := acceptingEndpoint("wss://community.example")
	bus := busWithAdmission(t, admission, endpoint)
	wrap := admissionTestEvent(t, 1059)
	for i := 0; i < 2; i++ {
		if err := publishConcordInvite(t.Context(), bus, []relayBusEndpoint{endpoint}, wrap); err != nil {
			t.Fatal(err)
		}
	}
	if len(endpoint.published) != 1 {
		t.Fatalf("replayed wrap reached the relay %d times", len(endpoint.published))
	}
}

func TestConcordRotationPublicationBound(t *testing.T) {
	cases := []struct {
		name                                              string
		scopes, recipients, heads, members, invites, want int
		refound                                           bool
	}{
		{name: "empty rotation still admits one", want: 1},
		{name: "one scope one recipient", scopes: 1, recipients: 1, want: 1},
		{name: "chunked scopes and invites", scopes: 2, recipients: 200, invites: 3, want: 2*3 + 6},
		{name: "refounding adds compaction and snapshot", scopes: 1, recipients: 10, refound: true, heads: 7, members: 401, want: 1 + 7 + 2},
	}
	for _, tc := range cases {
		if got := concordRotationPublicationBound(tc.scopes, tc.recipients, tc.refound, tc.heads, tc.members, tc.invites); got != tc.want {
			t.Errorf("%s: bound = %d, want %d", tc.name, got, tc.want)
		}
	}
}
