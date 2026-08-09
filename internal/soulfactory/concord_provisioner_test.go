package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type recordingConcordAssigner struct {
	pubkey   string
	assigned []string
	err      error
}

func (a *recordingConcordAssigner) Assign(_ context.Context, pubkey string) ([]string, error) {
	a.pubkey = pubkey
	return append([]string(nil), a.assigned...), a.err
}

func TestFullProvisionerAssignsAndRecordsConcordAfterSignet(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, scriptedGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	assigner := &recordingConcordAssigner{assigned: []string{strings.Repeat("a", 64)}}
	full.concordMembership = assigner

	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("concord-success").Hex(),
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{
		AgentID: "concord-agent",
		Name:    "Concord Agent",
		Brief:   "private coordination",
		Tier:    domain.SoulTierStandard,
	}, run)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if assigner.pubkey == "" || assigner.pubkey != soul.NostrPubkey {
		t.Fatalf("Concord Assign() pubkey = %q, soul pubkey = %q", assigner.pubkey, soul.NostrPubkey)
	}
	if len(run.Steps) < 2 || run.Steps[1].Name != domain.StepSignet || run.Steps[1].Status != domain.StepStatusComplete {
		t.Fatalf("Signet step = %+v", run.Steps)
	}
	recorded, ok := run.Steps[1].Output["concord_communities"].([]string)
	if !ok || len(recorded) != 1 || recorded[0] != assigner.assigned[0] {
		t.Fatalf("recorded Concord communities = %#v", run.Steps[1].Output["concord_communities"])
	}
}

func TestFullProvisionerFailsClosedWhenConcordAssignmentFails(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, scriptedGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	full.concordMembership = &recordingConcordAssigner{err: errors.New("relay rejected direct invite")}

	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("concord-failure").Hex(),
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{
		AgentID: "concord-agent",
		Name:    "Concord Agent",
		Brief:   "private coordination",
		Tier:    domain.SoulTierStandard,
	}, run)
	if soul != nil {
		t.Fatalf("Provision() soul = %+v, want nil", soul)
	}
	if err == nil || !strings.Contains(err.Error(), "assign Concord communities") || !strings.Contains(err.Error(), "relay rejected direct invite") {
		t.Fatalf("Provision() error = %v", err)
	}
	if len(run.Steps) != 2 || run.Steps[1].Name != domain.StepSignet || run.Steps[1].Status != domain.StepStatusFailed {
		t.Fatalf("Signet step = %+v", run.Steps)
	}
}
