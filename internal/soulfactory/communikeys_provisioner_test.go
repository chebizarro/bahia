package soulfactory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type recordingCommunikeysAssigner struct {
	pubkey   string
	assigned []string
	err      error
}

func (a *recordingCommunikeysAssigner) Assign(_ context.Context, pubkey string) ([]string, error) {
	a.pubkey = pubkey
	return append([]string(nil), a.assigned...), a.err
}

func TestFullProvisionerAssignsAndRecordsCommunikeysAfterSignet(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, scriptedGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	assigner := &recordingCommunikeysAssigner{assigned: []string{"30000:" + signer.pubkey + ":Apps"}}
	full.communikeysMembership = assigner

	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("communikeys-success").Hex(),
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{
		AgentID: "community-agent",
		Name:    "Community Agent",
		Brief:   "Publish community applications",
		Tier:    domain.SoulTierStandard,
	}, run)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if assigner.pubkey == "" || assigner.pubkey != soul.NostrPubkey {
		t.Fatalf("Communikeys Assign() pubkey = %q, soul pubkey = %q", assigner.pubkey, soul.NostrPubkey)
	}
	if len(run.Steps) < 2 || run.Steps[1].Name != domain.StepSignet || run.Steps[1].Status != domain.StepStatusComplete {
		t.Fatalf("Signet step = %+v", run.Steps)
	}
	recorded, ok := run.Steps[1].Output["communikeys_communities"].([]string)
	if !ok || len(recorded) != 1 || recorded[0] != assigner.assigned[0] {
		t.Fatalf("recorded Communikeys communities = %#v", run.Steps[1].Output["communikeys_communities"])
	}
}

func TestFullProvisionerFailsClosedWhenCommunikeysAssignmentFails(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, scriptedGenerator{}, signer, slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	full.communikeysMembership = &recordingCommunikeysAssigner{err: errors.New("relay rejected ACL replacement")}

	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       soulTestID("communikeys-failure").Hex(),
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{
		AgentID: "community-agent",
		Name:    "Community Agent",
		Brief:   "Publish community applications",
		Tier:    domain.SoulTierStandard,
	}, run)
	if soul != nil {
		t.Fatalf("Provision() soul = %+v, want nil", soul)
	}
	if err == nil || !strings.Contains(err.Error(), "assign Communikeys communities") || !strings.Contains(err.Error(), "relay rejected ACL replacement") {
		t.Fatalf("Provision() error = %v", err)
	}
	if len(run.Steps) != 2 || run.Steps[1].Name != domain.StepSignet || run.Steps[1].Status != domain.StepStatusFailed {
		t.Fatalf("failed Signet step = %+v", run.Steps)
	}
}
