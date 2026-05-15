package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func buildActionEvent(t *testing.T, signer fakeSigner, eventID string, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		ID:        eventID,
		Kind:      domain.KindSoulAction,
		CreatedAt: nostr.Now(),
		PubKey:    signer.pubkey,
		Tags:      tags,
		Content:   content,
	}
	return event
}

func TestLifecycleHandlerRejectsMalformedUnauthorizedAndProcessesValidActions(t *testing.T) {
	authorized := newFakeSigner(t)
	unauthorized := newFakeSigner(t)
	soul := &domain.AgentSoul{
		ID:          uuid.New(),
		AgentID:     "scout",
		Name:        "Scout",
		Tier:        domain.SoulTierStandard,
		Status:      domain.SoulStatusActive,
		NostrPubkey: strings.Repeat("a", 64),
		NostrNpub:   "npub1scout",
		SoulMD:      "# Original soul",
		CreatedAt:   time.Now().UTC(),
	}

	reactor := NewReactor(
		Config{Relays: []string{"wss://public.example"}, AuthorizedPubkeys: []string{authorized.pubkey}},
		scriptedGenerator{},
		authorized,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	reactor.getSoulFn = func(_ context.Context, soulRef string) (*domain.AgentSoul, error) {
		if normalizeSoulLookupRef(soulRef) != soul.AgentID {
			return nil, nil
		}
		return soul, nil
	}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())

	t.Run("malformed action", func(t *testing.T) {
		capture.events = nil
		err := handler.HandleAction(t.Context(), buildActionEvent(t, authorized, "malformed", nostr.Tags{{"action", string(domain.SoulActionSuspend)}}, ""))
		if err == nil || !strings.Contains(err.Error(), "missing soul reference") {
			t.Fatalf("HandleAction() error = %v, want missing soul reference", err)
		}
		if got := len(capture.eventsByKind(domain.KindProvisioningResult)); got != 1 {
			t.Fatalf("malformed action result count = %d, want 1", got)
		}
		if got := findTag(capture.eventsByKind(domain.KindProvisioningResult)[0], "status"); got != "error" {
			t.Fatalf("malformed action result status = %q, want error", got)
		}
	})

	t.Run("unauthorized action", func(t *testing.T) {
		capture.events = nil
		soul.Status = domain.SoulStatusActive
		err := handler.HandleAction(t.Context(), buildActionEvent(t, unauthorized, "unauthorized", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionSuspend)}}, ""))
		if err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("HandleAction() error = %v, want unauthorized", err)
		}
		if soul.Status != domain.SoulStatusActive {
			t.Fatalf("soul status = %s, want active after unauthorized action", soul.Status)
		}
		if got := len(capture.eventsByKind(domain.KindProvisioningResult)); got != 1 {
			t.Fatalf("unauthorized action result count = %d, want 1", got)
		}
		if got := len(capture.eventsByKind(domain.KindAgentSoul)); got != 0 {
			t.Fatalf("soul publish count for unauthorized action = %d, want 0", got)
		}
	})

	t.Run("valid suspend action", func(t *testing.T) {
		capture.events = nil
		soul.Status = domain.SoulStatusActive
		soul.DeployStatus = "healthy"
		err := handler.HandleAction(t.Context(), buildActionEvent(t, authorized, "suspend", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionSuspend)}, {"reason", "maintenance"}}, ""))
		if err != nil {
			t.Fatalf("HandleAction() error = %v", err)
		}
		if soul.Status != domain.SoulStatusSuspended {
			t.Fatalf("soul status = %s, want suspended", soul.Status)
		}
		if soul.DeployStatus != "stopped" {
			t.Fatalf("deploy status = %s, want stopped", soul.DeployStatus)
		}
		if len(capture.eventsByKind(domain.KindAgentSoul)) != 1 {
			t.Fatalf("soul publish count = %d, want 1", len(capture.eventsByKind(domain.KindAgentSoul)))
		}
		statusEvents := capture.eventsByKind(domain.KindProvisioningStatus)
		if len(statusEvents) != 1 {
			t.Fatalf("action status count = %d, want 1", len(statusEvents))
		}
		if got := findTag(statusEvents[0], "request-kind"); got != fmt.Sprint(domain.KindSoulAction) {
			t.Fatalf("action status request-kind = %q, want 1950", got)
		}
		if got := findTag(statusEvents[0], "agent-id"); got != soul.AgentID {
			t.Fatalf("action status agent-id = %q, want %q", got, soul.AgentID)
		}
		actionResults := capture.eventsByKind(domain.KindProvisioningResult)
		if len(actionResults) != 1 {
			t.Fatalf("action result count = %d, want 1", len(actionResults))
		}
		if got := findTag(actionResults[0], "e"); got != "suspend" {
			t.Fatalf("action result reply tag = %q, want suspend", got)
		}
		if got := findTag(actionResults[0], "request-kind"); got != fmt.Sprint(domain.KindSoulAction) {
			t.Fatalf("action result request-kind = %q, want 1950", got)
		}
		if got := findTag(actionResults[0], "agent-id"); got != soul.AgentID {
			t.Fatalf("action result agent-id = %q, want %q", got, soul.AgentID)
		}
		if got := findTag(actionResults[0], "status"); got != "completed" {
			t.Fatalf("action result status = %q, want completed", got)
		}
		if legacy := capture.eventsByKind(domain.KindSoulActionLegacyResult); len(legacy) != 0 {
			t.Fatalf("legacy action result count = %d, want 0 by default", len(legacy))
		}
	})
}

func TestLifecycleHandlerRegenerateRequiresBriefAndRepublishesUpdatedIdentity(t *testing.T) {
	authorized := newFakeSigner(t)
	soul := &domain.AgentSoul{
		ID:            uuid.New(),
		AgentID:       "navigator",
		Name:          "Navigator",
		Tier:          domain.SoulTierStandard,
		Status:        domain.SoulStatusActive,
		NostrPubkey:   strings.Repeat("b", 64),
		NostrNpub:     "npub1navigator",
		SoulMD:        "# Soul\nold brief",
		IdentityMD:    "# Identity\nold brief",
		OriginalBrief: "old brief",
		CreatedAt:     time.Now().UTC(),
	}

	reactor := NewReactor(
		Config{Relays: []string{"wss://public.example"}, AuthorizedPubkeys: []string{authorized.pubkey}},
		scriptedGenerator{},
		authorized,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	reactor.getSoulFn = func(_ context.Context, soulRef string) (*domain.AgentSoul, error) {
		if normalizeSoulLookupRef(soulRef) != soul.AgentID {
			return nil, nil
		}
		return soul, nil
	}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())

	missingBrief := buildActionEvent(
		t,
		authorized,
		"regen-missing",
		nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionRegenerate)}},
		"{}",
	)
	if err := handler.HandleAction(t.Context(), missingBrief); err == nil || !strings.Contains(err.Error(), "requires a new brief") {
		t.Fatalf("HandleAction() error = %v, want missing brief rejection", err)
	}
	if got := len(capture.eventsByKind(domain.KindProvisioningStatus)); got != 1 {
		t.Fatalf("action status count for rejected regenerate = %d, want 1", got)
	}
	if got := len(capture.eventsByKind(domain.KindProvisioningResult)); got != 1 {
		t.Fatalf("action result count for rejected regenerate = %d, want 1", got)
	}
	if got := len(capture.eventsByKind(domain.KindAgentSoul)); got != 0 {
		t.Fatalf("soul publish count for rejected regenerate = %d, want 0", got)
	}

	capture.events = nil
	valid := buildActionEvent(
		t,
		authorized,
		"regen-valid",
		nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionRegenerate)}},
		`{"new_brief":"Guide release operators safely"}`,
	)
	if err := handler.HandleAction(t.Context(), valid); err != nil {
		t.Fatalf("HandleAction() error = %v", err)
	}
	if soul.OriginalBrief != "Guide release operators safely" {
		t.Fatalf("original brief = %q, want updated brief", soul.OriginalBrief)
	}
	if !strings.Contains(soul.SoulMD, "Guide release operators safely") {
		t.Fatalf("soul markdown = %q, want regenerated brief", soul.SoulMD)
	}
	if !strings.Contains(soul.IdentityMD, "Guide release operators safely") {
		t.Fatalf("identity markdown = %q, want regenerated brief", soul.IdentityMD)
	}
	if len(capture.eventsByKind(domain.KindAgentSoul)) != 1 {
		t.Fatalf("soul publish count = %d, want 1", len(capture.eventsByKind(domain.KindAgentSoul)))
	}
	if len(capture.eventsByKind(domain.KindProvisioningStatus)) != 1 {
		t.Fatalf("action status count = %d, want 1", len(capture.eventsByKind(domain.KindProvisioningStatus)))
	}
	actionResults := capture.eventsByKind(domain.KindProvisioningResult)
	if len(actionResults) != 1 {
		t.Fatalf("action result count = %d, want 1", len(actionResults))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(actionResults[0].Content), &payload); err != nil {
		t.Fatalf("unmarshal action result: %v", err)
	}
	if payload["regenerated"] != true {
		t.Fatalf("action result payload = %#v, want regenerated=true", payload)
	}
	if payload["new_brief"] != "Guide release operators safely" {
		t.Fatalf("action result new_brief = %#v, want regenerated brief", payload["new_brief"])
	}
}

func TestLifecycleHandlerReplayDoesNotDuplicateSideEffects(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	soul := &domain.AgentSoul{
		ID:          uuid.New(),
		AgentID:     "sentinel",
		Name:        "Sentinel",
		Tier:        domain.SoulTierStandard,
		Status:      domain.SoulStatusActive,
		NostrPubkey: signer.pubkey,
		NostrNpub:   "npub1sentinel",
		SoulMD:      "# Sentinel",
		CreatedAt:   time.Now().UTC(),
	}
	reactor := NewReactor(
		Config{Relays: []string{"wss://public.example"}, AuthorizedPubkeys: []string{signer.pubkey}},
		scriptedGenerator{},
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	reactor.getSoulFn = func(_ context.Context, soulRef string) (*domain.AgentSoul, error) {
		if normalizeSoulLookupRef(soulRef) != soul.AgentID {
			return nil, nil
		}
		return soul, nil
	}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())
	event := buildActionEvent(t, signer.fakeSigner, "replay-suspend", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionSuspend)}}, "")

	if err := handler.HandleAction(t.Context(), event); err != nil {
		t.Fatalf("first HandleAction() error = %v", err)
	}
	if err := handler.HandleAction(t.Context(), event); err != nil {
		t.Fatalf("replay HandleAction() error = %v", err)
	}
	if len(signer.suspended) != 1 || signer.suspended[0] != soul.NostrPubkey {
		t.Fatalf("suspend signer calls = %+v, want one call", signer.suspended)
	}
	if got := len(capture.eventsByKind(domain.KindProvisioningStatus)); got != 1 {
		t.Fatalf("action status publish count after replay = %d, want 1", got)
	}
	if got := len(capture.eventsByKind(domain.KindProvisioningResult)); got != 1 {
		t.Fatalf("action result publish count after replay = %d, want 1", got)
	}
	if got := len(capture.eventsByKind(domain.KindAgentSoul)); got != 1 {
		t.Fatalf("soul publish count after replay = %d, want 1", got)
	}
}

func TestLifecycleHandlerSkipsExecutionWhenTerminalResultAlreadyExists(t *testing.T) {
	signer := &trackingSigner{fakeSigner: newFakeSigner(t)}
	soul := &domain.AgentSoul{
		ID:          uuid.New(),
		AgentID:     "sentinel",
		Name:        "Sentinel",
		Tier:        domain.SoulTierStandard,
		Status:      domain.SoulStatusActive,
		NostrPubkey: signer.pubkey,
		CreatedAt:   time.Now().UTC(),
	}
	reactor := NewReactor(
		Config{Relays: []string{"wss://public.example"}, AuthorizedPubkeys: []string{signer.pubkey}},
		scriptedGenerator{},
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }
	reactor.findLifecycleResultFn = func(context.Context, string) (*nostr.Event, error) {
		return &nostr.Event{ID: "terminal-7950", Kind: domain.KindProvisioningResult, Tags: nostr.Tags{{"request-kind", fmt.Sprint(domain.KindSoulAction)}}}, nil
	}

	event := buildActionEvent(t, signer.fakeSigner, "already-terminal", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionSuspend)}}, "")
	if err := reactor.lifecycle().HandleAction(t.Context(), event); err != nil {
		t.Fatalf("HandleAction() error = %v", err)
	}
	if len(signer.suspended) != 0 {
		t.Fatalf("suspend signer calls = %+v, want none", signer.suspended)
	}
	if len(capture.events) != 0 {
		t.Fatalf("published events = %d, want none when terminal already exists", len(capture.events))
	}
	if soul.Status != domain.SoulStatusActive {
		t.Fatalf("soul status = %s, want active", soul.Status)
	}
}

func buildSoulRefForTest(soul *domain.AgentSoul) string {
	return fmt.Sprintf("%d:%s:%s", domain.KindAgentSoul, SoulFactoryPubkey, soul.AgentID)
}
