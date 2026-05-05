package soulfactory

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

func TestStatusSyncHandlerUsesBahiaEventsToUpdateDeployStatus(t *testing.T) {
	reactor := NewReactor(Config{}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	handler := NewStatusSyncHandler(reactor, nil, slog.Default())
	serviceID := uuid.New()
	soul := &domain.AgentSoul{
		AgentID:        "scout",
		Name:           "Scout",
		Status:         domain.SoulStatusActive,
		Tier:           domain.SoulTierStandard,
		NostrPubkey:    "agent-pubkey",
		NostrNpub:      "npub1test",
		BahiaServiceID: &serviceID,
		CreatedAt:      time.Now().UTC(),
	}
	handler.RegisterSoul(soul)

	handler.HandleEvent(t.Context(), events.Event{Type: events.EventDeploymentIntentCreated, EntityID: serviceID.String(), Data: map[string]interface{}{"service_id": serviceID.String()}})
	if soul.DeployStatus != "deploying" {
		t.Fatalf("deploy status after intent = %q, want deploying", soul.DeployStatus)
	}

	handler.HandleEvent(t.Context(), events.Event{Type: events.EventDeploymentRunCompleted, EntityID: serviceID.String(), Data: map[string]string{"status": string(domain.RunStatusSucceeded)}})
	if soul.DeployStatus != "deployed" {
		t.Fatalf("deploy status after completion = %q, want deployed", soul.DeployStatus)
	}
}

func TestStatusSyncHandlerIgnoresUnknownServices(t *testing.T) {
	reactor := NewReactor(Config{}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	handler := NewStatusSyncHandler(reactor, nil, slog.Default())
	soul := &domain.AgentSoul{AgentID: "scout", DeployStatus: "pending"}

	handler.HandleEvent(t.Context(), events.Event{Type: events.EventDeploymentRunCompleted, EntityID: uuid.NewString(), Data: map[string]string{"status": string(domain.RunStatusFailed)}})
	if soul.DeployStatus != "pending" {
		t.Fatalf("deploy status changed unexpectedly: %q", soul.DeployStatus)
	}
}
