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

type recordingConcordRotator struct {
	recordingConcordAssigner
	rotation  ConcordRotation
	receipt   *ConcordRotationReceipt
	rotateErr error
}

func (r *recordingConcordRotator) Rotate(_ context.Context, rotation ConcordRotation) (*ConcordRotationReceipt, error) {
	r.rotation = rotation
	return r.receipt, r.rotateErr
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

func TestFullProvisionerRotatesConcordCommunity(t *testing.T) {
	full := newConcordRotationProvisioner(t)
	rotator := &recordingConcordRotator{receipt: &ConcordRotationReceipt{
		CommunityID: strings.Repeat("a", 64),
		Refounded:   true,
		RootEpoch:   9,
		Recipients:  []string{strings.Repeat("b", 64)},
	}}
	full.concordMembership = rotator

	receipt, err := full.RotateConcordCommunity(t.Context(), ConcordRotation{
		CommunityID: strings.Repeat("a", 64),
		Refound:     true,
		Recipients:  []string{strings.Repeat("b", 64)},
	})
	if err != nil {
		t.Fatalf("RotateConcordCommunity() error = %v", err)
	}
	if receipt == nil || receipt.RootEpoch != 9 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !rotator.rotation.Refound || rotator.rotation.CommunityID != strings.Repeat("a", 64) {
		t.Fatalf("forwarded rotation = %+v", rotator.rotation)
	}
}

func TestFullProvisionerRotationSurfacesPartialRedistribution(t *testing.T) {
	full := newConcordRotationProvisioner(t)
	full.concordMembership = &recordingConcordRotator{
		receipt:   &ConcordRotationReceipt{CommunityID: strings.Repeat("a", 64), RootEpoch: 2},
		rotateErr: errors.New("relay rejected direct invite"),
	}

	receipt, err := full.RotateConcordCommunity(t.Context(), ConcordRotation{CommunityID: strings.Repeat("a", 64), Refound: true})
	if err == nil || !strings.Contains(err.Error(), "relay rejected direct invite") {
		t.Fatalf("RotateConcordCommunity() error = %v", err)
	}
	if receipt == nil {
		t.Fatal("a partial redistribution must still return its receipt: custody already holds the rotated material")
	}
}

func TestFullProvisionerRotationFailsClosedWithoutConcord(t *testing.T) {
	full := newConcordRotationProvisioner(t)
	if _, err := full.RotateConcordCommunity(t.Context(), ConcordRotation{CommunityID: strings.Repeat("a", 64), Refound: true}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("RotateConcordCommunity() error = %v", err)
	}

	full.concordMembership = &recordingConcordAssigner{}
	if _, err := full.RotateConcordCommunity(t.Context(), ConcordRotation{CommunityID: strings.Repeat("a", 64), Refound: true}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("RotateConcordCommunity() with a non-rotating assigner error = %v", err)
	}

	full.concordMembership = &recordingConcordRotator{}
	full.concordMembershipErr = errors.New("invite bundle is expired")
	if _, err := full.RotateConcordCommunity(t.Context(), ConcordRotation{CommunityID: strings.Repeat("a", 64), Refound: true}); err == nil ||
		!strings.Contains(err.Error(), "invite bundle is expired") {
		t.Fatalf("RotateConcordCommunity() with a broken configuration error = %v", err)
	}
}

func newConcordRotationProvisioner(t *testing.T) *FullProvisioner {
	t.Helper()
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, scriptedGenerator{}, newFakeSigner(t), slog.Default())
	attachPublishCapture(reactor)
	return NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
}
