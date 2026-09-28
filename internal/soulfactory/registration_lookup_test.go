package soulfactory

import (
	"context"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestIsRegisteredAgentTracksCurrentSoulRegistration(t *testing.T) {
	secret := gonostr.Generate()
	pubkey := secret.Public().Hex()
	var souls []*domain.AgentSoul
	r := &Reactor{listSoulsFn: func(context.Context) ([]*domain.AgentSoul, error) { return souls, nil }}
	check := func(want bool) {
		t.Helper()
		got, err := r.IsRegisteredAgent(context.Background(), pubkey)
		if err != nil || got != want {
			t.Fatalf("registered = %v, err = %v, want %v", got, err, want)
		}
	}
	check(false)
	souls = []*domain.AgentSoul{{AgentID: "agent", NostrPubkey: pubkey, Status: domain.SoulStatusActive}}
	check(true)
	souls[0].Status = domain.SoulStatusRevoked
	check(false)
	souls = nil
	check(false)
	r.listSoulsFn = func(context.Context) ([]*domain.AgentSoul, error) { return nil, errors.New("relay unavailable") }
	if trusted, err := r.IsRegisteredAgent(context.Background(), pubkey); trusted || err == nil {
		t.Fatalf("failed registration read must not grant trust: trusted=%v err=%v", trusted, err)
	}
}
