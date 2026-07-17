package controlplane

import (
	"context"
	"testing"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

func TestReactorIsAuthorized(t *testing.T) {
	t.Run("empty allowlist denies all", func(t *testing.T) {
		reactor := NewReactor(Config{}, nil, nil, nil, nil)
		if reactor.isAuthorized("pubkey-1") {
			t.Fatal("expected authorization to fail when allowlist is empty")
		}
	})

	t.Run("configured allowlist permits matching pubkey", func(t *testing.T) {
		reactor := NewReactor(Config{AuthorizedPubkeys: []string{"pubkey-1", "pubkey-2"}}, nil, nil, nil, nil)
		if !reactor.isAuthorized("pubkey-2") {
			t.Fatal("expected configured pubkey to be authorized")
		}
		if reactor.isAuthorized("pubkey-3") {
			t.Fatal("expected non-configured pubkey to be rejected")
		}
	})
}

type continuityCapturePublisher struct {
	events []events.Event
}

func (p *continuityCapturePublisher) Publish(_ context.Context, event events.Event) {
	p.events = append(p.events, event)
}
func (*continuityCapturePublisher) Subscribe(events.EventType, events.Handler) {}

func TestContinuityDefinitionsRejectUnauthorizedAuthorsBeforePublication(t *testing.T) {
	publisher := &continuityCapturePublisher{}
	reactor := NewReactor(
		Config{AuthorizedPubkeys: []string{"operator"}},
		nil,
		nil,
		nil,
		nil,
		WithEventPublisher(publisher),
	)
	event, err := nostradapter.EncodeContinuityProfileEvent(domain.ServiceContinuityProfile{
		ServiceKey: "svc.api",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull: {},
		},
	})
	if err != nil {
		t.Fatalf("encode continuity profile: %v", err)
	}
	event.ID = gonostr.ID{1}
	event.PubKey = gonostr.PubKey{2}

	reactor.handleContinuityProfileDefinition(context.Background(), &event)
	if len(publisher.events) != 0 {
		t.Fatalf("published %d events from unauthorized definition", len(publisher.events))
	}
}

func TestReactorIsAuthorizedForScopedOperatorPaths(t *testing.T) {
	reactor := NewReactor(Config{
		AuthorizedPubkeys:              []string{"global-operator"},
		AdoptionAuthorizedPubkeys:      []string{"adoption-operator"},
		DirectRuntimeAuthorizedPubkeys: []string{"runtime-operator"},
	}, nil, nil, nil, nil)

	if !reactor.isAuthorizedFor("global-operator", operatorScopeAdoption) {
		t.Fatal("expected global operator to be authorized for adoption")
	}
	if !reactor.isAuthorizedFor("adoption-operator", operatorScopeAdoption) {
		t.Fatal("expected adoption operator to be authorized for adoption")
	}
	if reactor.isAuthorizedFor("adoption-operator", operatorScopeDefault) {
		t.Fatal("expected adoption-scoped operator to be rejected for default scope")
	}
	if reactor.isAuthorizedFor("adoption-operator", operatorScopeDirectRuntime) {
		t.Fatal("expected adoption-scoped operator to be rejected for direct runtime")
	}
	if !reactor.isAuthorizedFor("global-operator", operatorScopeDirectRuntime) {
		t.Fatal("expected global operator to be authorized for direct runtime")
	}
	if !reactor.isAuthorizedFor("runtime-operator", operatorScopeDirectRuntime) {
		t.Fatal("expected runtime operator to be authorized for direct runtime")
	}
	if reactor.isAuthorizedFor("runtime-operator", operatorScopeAdoption) {
		t.Fatal("expected runtime-scoped operator to be rejected for adoption")
	}
	if reactor.isAuthorizedFor("unknown", operatorScopeAdoption) || reactor.isAuthorizedFor("unknown", operatorScopeDirectRuntime) {
		t.Fatal("expected unknown operator to be rejected for scoped paths")
	}
}
