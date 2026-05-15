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

func TestDiffHotReloadDraftsIdentifiesCustomizationSections(t *testing.T) {
	current := domain.SoulDraftContent{
		Schema:   domain.SoulFactoryDraftSchemaV2,
		Identity: domain.SoulIdentitySpec{Name: "Scout", Purpose: "Research", Tier: domain.SoulTierStandard},
		Persona:  domain.SoulPersonaSpec{Traits: []string{"careful"}},
		Avatar:   domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{Prompt: "old owl"}},
		Voice:    domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "old-voice"},
		Memory:   domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
	}
	proposed := current
	proposed.Identity.Theme = "warm"
	proposed.Persona.Traits = []string{"careful", "curious"}
	proposed.Avatar.Generation = &domain.SoulAvatarGenerationSpec{Prompt: "new owl"}
	proposed.Voice.PersonaID = "new-voice"
	proposed.Memory.Search = &domain.SoulMemorySearchSpec{TopK: 12, ScoreThreshold: 0.72}

	diff := DiffHotReloadDrafts(current, proposed)
	if !diff.Avatar || !diff.Voice || !diff.Memory || !diff.Persona {
		t.Fatalf("diff = %+v, want all hot-reload sections changed", diff)
	}
	want := []string{"avatar", "voice", "memory", "persona"}
	if fmt.Sprint(diff.ChangedSections) != fmt.Sprint(want) {
		t.Fatalf("changed sections = %#v, want %#v", diff.ChangedSections, want)
	}

	unchanged := DiffHotReloadDrafts(current, current)
	if len(unchanged.ChangedSections) != 0 || unchanged.Avatar || unchanged.Voice || unchanged.Memory || unchanged.Persona {
		t.Fatalf("unchanged diff = %+v, want no changes", unchanged)
	}
}

func TestBuildHotReloadRuntimeCallsAddsMemoryReindexWhenAutoIndexEnabled(t *testing.T) {
	current := domain.SoulDraftContent{Memory: domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"}}
	proposed := current
	proposed.Memory = domain.SoulMemorySpec{EmbeddingProvider: "voyage", EmbeddingModel: "voyage-3", Strategy: "session-aware", AutoIndex: true, RetentionDays: 45}
	draft := &domain.SoulDraft{EventID: "draft-proposed-event", AgentID: "scout", CreatedBy: "author"}
	action := &domain.SoulAction{DraftRef: "31952:author:scout"}

	calls := buildHotReloadRuntimeCalls(current, proposed, DiffHotReloadDrafts(current, proposed), draft, action, "sha256:new", "sha256:old")
	if len(calls) != 2 || calls[0].Method != RuntimeMethodMemoryConfigure || calls[1].Method != RuntimeMethodMemoryReindex {
		t.Fatalf("memory hot-reload calls = %+v, want configure then reindex", calls)
	}
	if calls[1].Params["schema"] != SoulFactoryMemoryReindexSchema || calls[1].Params["mode"] != MemoryReindexModeIncremental {
		t.Fatalf("reindex params = %#v", calls[1].Params)
	}
	if calls[1].Params["progress_event_kind"] != float64(domain.KindProvisioningStatus) || calls[1].Params["result_event_kind"] != float64(domain.KindRuntimeControlResult) {
		t.Fatalf("reindex event kinds = %#v", calls[1].Params)
	}
}

func TestLifecycleHandlerHotReloadDispatchesSelectiveRuntimeControlsAndPublishesProgress(t *testing.T) {
	signer := newFakeSigner(t)
	currentDraft := &domain.SoulDraft{
		EventID:   "draft-current-event",
		AgentID:   "scout",
		CreatedBy: signer.pubkey,
		Content: domain.SoulDraftContent{
			Schema:   domain.SoulFactoryDraftSchemaV2,
			Brief:    "old brief",
			Identity: domain.SoulIdentitySpec{Name: "Scout", Purpose: "Research", Tier: domain.SoulTierStandard},
			Persona:  domain.SoulPersonaSpec{Traits: []string{"careful"}},
			Avatar:   domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{Prompt: "old owl", Provider: "flux-comfyui"}},
			Voice:    domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "old-voice"},
			Memory:   domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
			Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey"},
			SpecHash: "sha256:old",
		},
	}
	proposedDraft := &domain.SoulDraft{
		EventID:   "draft-proposed-event",
		AgentID:   "scout",
		CreatedBy: signer.pubkey,
		Content: domain.SoulDraftContent{
			Schema:           domain.SoulFactoryDraftSchemaV2,
			Brief:            "old brief",
			Identity:         domain.SoulIdentitySpec{Name: "Scout", Purpose: "Research deeply", Tier: domain.SoulTierStandard, Theme: "warm"},
			Persona:          domain.SoulPersonaSpec{Traits: []string{"careful", "curious"}, Tone: "friendly"},
			Avatar:           domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{Prompt: "new owl", Provider: "flux-comfyui"}, GeneratedRef: "blossom:new-avatar", Current: "generated"},
			Voice:            domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "new-voice"},
			Memory:           domain.SoulMemorySpec{EmbeddingProvider: "voyage", EmbeddingModel: "voyage-3", Search: &domain.SoulMemorySearchSpec{TopK: 10}},
			Runtime:          domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey"},
			SpecHash:         "sha256:new",
			PreviousSpecHash: "sha256:old",
		},
	}
	soul := &domain.AgentSoul{
		ID:           uuid.New(),
		AgentID:      "scout",
		Name:         "Scout",
		Tier:         domain.SoulTierStandard,
		Status:       domain.SoulStatusActive,
		DraftRef:     "31952:" + signer.pubkey + ":scout",
		DraftEventID: currentDraft.EventID,
		SpecHash:     "sha256:old",
		Runtime:      domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey"},
		CreatedAt:    time.Now().UTC(),
	}

	reactor := NewReactor(
		Config{Relays: []string{"wss://public.example"}, AuthorizedPubkeys: []string{signer.pubkey}},
		scriptedGenerator{},
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }
	reactor.getDraftFn = func(_ context.Context, draftRef, draftEventID string) (*domain.SoulDraft, error) {
		switch draftEventID {
		case currentDraft.EventID:
			return currentDraft, nil
		case proposedDraft.EventID:
			return proposedDraft, nil
		default:
			if draftRef == "31952:"+signer.pubkey+":scout" {
				return proposedDraft, nil
			}
			return nil, nil
		}
	}
	runtime := &trackingRuntimeAdapter{}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())
	handler.SetRuntimeAdapters(map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime})

	event := buildActionEvent(t, signer, "hot-reload-action", nostr.Tags{
		{"soul", buildSoulRefForTest(soul)},
		{"action", string(domain.SoulActionHotReload)},
		{"draft-event", proposedDraft.EventID},
		{"spec-hash", "sha256:new"},
		{"previous-spec-hash", "sha256:old"},
	}, "")
	if err := handler.HandleAction(t.Context(), event); err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v", err)
	}

	methods := make([]string, 0, len(runtime.requests))
	for _, req := range runtime.requests {
		methods = append(methods, req.Method)
		if req.RequestKind != domain.KindSoulAction || req.Action != domain.SoulActionHotReload {
			t.Fatalf("runtime request context = kind %d action %s, want 1950 hot-reload", req.RequestKind, req.Action)
		}
		if req.Soul.SpecHash != "sha256:new" || req.Soul.Draft != proposedDraft.EventID {
			t.Fatalf("runtime soul ref = %+v, want proposed draft and new spec hash", req.Soul)
		}
	}
	wantMethods := []string{RuntimeMethodAvatarGenerate, RuntimeMethodVoiceConfigure, RuntimeMethodMemoryConfigure, RuntimeMethodPersonaUpdate}
	if fmt.Sprint(methods) != fmt.Sprint(wantMethods) {
		t.Fatalf("runtime methods = %#v, want %#v", methods, wantMethods)
	}

	statusEvents := capture.eventsByKind(domain.KindProvisioningStatus)
	if len(statusEvents) < 10 {
		t.Fatalf("status event count = %d, want initial + diff + per-section progress", len(statusEvents))
	}
	if !strings.Contains(statusEvents[1].Content, "avatar,voice,memory,persona") {
		t.Fatalf("diff progress content = %q, want changed sections", statusEvents[1].Content)
	}
	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(results[0].Content), &payload); err != nil {
		t.Fatalf("unmarshal hot-reload result: %v", err)
	}
	if payload["hot_reload"] != true || payload["applied_change_count"] != float64(4) {
		t.Fatalf("hot-reload result payload = %#v, want applied count 4", payload)
	}
	if soul.DraftEventID != proposedDraft.EventID || soul.SpecHash != "sha256:new" || soul.PreviousSpecHash != "sha256:old" {
		t.Fatalf("soul refs after hot-reload = draft %q spec %q previous %q", soul.DraftEventID, soul.SpecHash, soul.PreviousSpecHash)
	}
	if soul.Purpose != "Research deeply" {
		t.Fatalf("soul purpose after hot-reload = %q, want proposed identity purpose", soul.Purpose)
	}
	if soul.Assets.AvatarRef != "blossom:new-avatar" || soul.Assets.VoiceRef != "new-voice" {
		t.Fatalf("soul assets after hot-reload = %+v", soul.Assets)
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
