package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestIntegrationCreateSoulWithFullCustomization(t *testing.T) {
	signer := newFakeSigner(t)
	draft := &domain.SoulDraft{EventID: "full-customization-draft", AgentID: "scout", Name: "Scout", Tier: domain.SoulTierHeavy, TemplateRef: "31950:template:scout", CreatedBy: signer.pubkey, Content: completeCustomizationDraftContent()}
	draft.Content.SpecHash = "sha256:full-customization"
	draft.Content.Runtime.RuntimePubkey = "runtime-pubkey"
	draft.Content.Runtime.CapabilityRef = "capability-full"

	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}, SoulFactoryPubkey: signer.pubkey}, scriptedGenerator{}, signer, slog.Default())
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	close(subscription.eose)
	relayBus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	reactor.relayBus = relayBus
	capture := attachPublishCapture(reactor)
	reactor.getDraftFn = func(context.Context, string, string) (*domain.SoulDraft, error) { return draft, nil }
	reactor.getTemplateFn = func(context.Context, string) (*domain.SoulTemplate, error) {
		return &domain.SoulTemplate{EventID: "template-event", Identifier: "scout", Name: "Scout Template", Tier: domain.SoulTierHeavy, BasePrompt: "Scout with full customization"}, nil
	}
	runtime := &integrationRuntimeAdapter{runtime: domain.RuntimeTargetOpenClaw, bindingPrefix: "openclaw"}
	reactor.provisioner = NewFullProvisioner(reactor, FullProvisionerConfig{RuntimeAdapters: map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime}, OpenClawReadiness: acceptingOpenClawReadiness{}}, nil)

	request := buildProvisioningEvent(t, signer.pubkey, "create-full-customization", nostr.Tags{{"agent-id", draft.AgentID}, {"draft-event", draft.EventID}, {"spec-hash", draft.Content.SpecHash}}, `{"brief":"Create full customization"}`)
	reactor.handleProvisioningRequest(t.Context(), request)

	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests = %d, want provision", len(runtime.requests))
	}
	params := runtime.requests[0].Params
	for _, section := range []string{"persona", "voice", "memory", "avatar"} {
		if params[section] == nil {
			t.Fatalf("runtime provision params missing %s: %#v", section, params)
		}
	}
	if voice := params["voice"].(domain.SoulVoiceSpec); voice.Provider != "openai-tts" || voice.PersonaID != "alloy" {
		t.Fatalf("voice params = %+v", voice)
	}
	if memory := params["memory"].(domain.SoulMemorySpec); memory.Search == nil || memory.Search.TopK != 8 {
		t.Fatalf("memory params = %+v", memory)
	}

	souls := capture.eventsByKind(domain.KindAgentSoul)
	if len(souls) != 1 {
		t.Fatalf("soul events = %d, want 1", len(souls))
	}
	for tag, want := range map[string]string{"draft-event": draft.EventID, "spec-hash": draft.Content.SpecHash, "template": draft.TemplateRef, "avatar-ref": "blossom://avatar", "voice-ref": "voice://alloy", "runtime": "openclaw", "capability": "capability-openclaw"} {
		if got := findTag(souls[0], tag); got != want {
			t.Fatalf("soul tag %s = %q, want %q", tag, got, want)
		}
	}
}

func TestIntegrationUpdateExistingSoulCustomization(t *testing.T) {
	signer := newFakeSigner(t)
	current := completeCustomizationDraftContent()
	current.SpecHash = "sha256:current"
	current.Identity.Purpose = "Research"
	current.Voice.PersonaID = "alloy"
	current.Assets.VoiceRef = "voice://alloy"
	proposed := current
	proposed.SpecHash = "sha256:updated"
	proposed.PreviousSpecHash = current.SpecHash
	proposed.Identity.Purpose = "Research deeply"
	proposed.Persona.Tone = "analytical"
	proposed.Voice.PersonaID = "verse"
	proposed.Assets.VoiceRef = "voice://verse"

	soul, runtime := runIntegrationHotReload(t, signer, current, proposed, &integrationRuntimeAdapter{runtime: domain.RuntimeTargetOpenClaw, bindingPrefix: "openclaw"}, nil)
	if soul.Purpose != "Research deeply" || soul.Assets.VoiceRef != "voice://verse" || soul.SpecHash != "sha256:updated" || soul.PreviousSpecHash != "sha256:current" {
		t.Fatalf("updated soul = purpose %q voice %q spec %q previous %q", soul.Purpose, soul.Assets.VoiceRef, soul.SpecHash, soul.PreviousSpecHash)
	}
	if got := integrationMethods(runtime.requests); !reflect.DeepEqual(got, []string{RuntimeMethodVoiceConfigure, RuntimeMethodPersonaUpdate}) {
		t.Fatalf("runtime methods = %#v", got)
	}
}

func TestIntegrationApplyTemplatePresetToNewSoul(t *testing.T) {
	signer := newFakeSigner(t)
	templateRef := "31950:template-author:analyst"
	draft := &domain.SoulDraft{EventID: "template-preset-draft", AgentID: "analyst", Name: "Analyst", Tier: domain.SoulTierStandard, TemplateRef: templateRef, CreatedBy: signer.pubkey, Content: completeCustomizationDraftContent()}
	draft.Content.Identity.Name = "Analyst"
	draft.Content.Identity.Purpose = "Analyze signals"
	draft.Content.Runtime.RuntimePubkey = "runtime-pubkey"
	draft.Content.SpecHash = "sha256:template-preset"

	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}, SoulFactoryPubkey: signer.pubkey}, &capturingGenerator{}, signer, slog.Default())
	capture := attachPublishCapture(reactor)
	reactor.getDraftFn = func(context.Context, string, string) (*domain.SoulDraft, error) { return draft, nil }
	reactor.getTemplateFn = func(_ context.Context, got string) (*domain.SoulTemplate, error) {
		if got != templateRef {
			t.Fatalf("template ref = %q, want %q", got, templateRef)
		}
		return &domain.SoulTemplate{EventID: "template-event", Identifier: "analyst", Name: "Analyst Preset", Tier: domain.SoulTierStandard, BasePrompt: "Analyze with rigor", DefaultKinds: []int{1, domain.KindSoulAction}, DefaultTools: []domain.ToolGrant{{MCPServer: "search", Scopes: []string{"read"}}}}, nil
	}
	runtime := &integrationRuntimeAdapter{runtime: domain.RuntimeTargetOpenClaw, bindingPrefix: "openclaw"}
	reactor.provisioner = NewFullProvisioner(reactor, FullProvisionerConfig{RuntimeAdapters: map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime}, OpenClawReadiness: acceptingOpenClawReadiness{}}, nil)

	reactor.handleProvisioningRequest(t.Context(), buildProvisioningEvent(t, signer.pubkey, "template-preset", nostr.Tags{{"agent-id", "analyst"}, {"template", templateRef}, {"draft-event", draft.EventID}, {"spec-hash", draft.Content.SpecHash}}, `{}`))
	if len(runtime.requests) != 1 || runtime.requests[0].Params["persona"] == nil || runtime.requests[0].Params["voice"] == nil {
		t.Fatalf("runtime request did not include preset customization: %+v", runtime.requests)
	}
	if souls := capture.eventsByKind(domain.KindAgentSoul); len(souls) != 1 || findTag(souls[0], "template") != templateRef {
		t.Fatalf("published soul template tags = %#v", souls)
	}
}

func TestIntegrationHotReloadVoicePersonaAndMemory(t *testing.T) {
	signer := newFakeSigner(t)
	base := completeCustomizationDraftContent()
	base.SpecHash = "sha256:base"
	next := base
	next.SpecHash = "sha256:next"
	next.Voice.Provider = "azure"
	next.Voice.PersonaID = "en-US-AvaMultilingualNeural"
	next.Persona.SystemPromptSections = map[string]string{"role": "You are Scout.", "style": "Use crisp evidence."}
	next.Memory.Search = &domain.SoulMemorySearchSpec{TopK: 3, ScoreThreshold: 0.82, Rerank: false}

	_, runtime := runIntegrationHotReload(t, signer, base, next, &integrationRuntimeAdapter{runtime: domain.RuntimeTargetOpenClaw, bindingPrefix: "openclaw"}, nil)
	byMethod := map[string]RuntimeAdapterRequest{}
	for _, req := range runtime.requests {
		byMethod[req.Method] = req
	}
	if voice := byMethod[RuntimeMethodVoiceConfigure].Params["proposed"].(map[string]interface{})["voice"].(domain.SoulVoiceSpec); voice.Provider != "azure" || voice.PersonaID != "en-US-AvaMultilingualNeural" {
		t.Fatalf("voice hot-reload proposed = %+v", voice)
	}
	if persona := byMethod[RuntimeMethodPersonaUpdate].Params["proposed"].(map[string]interface{})["persona"].(domain.SoulPersonaSpec); persona.SystemPromptSections["style"] != "Use crisp evidence." {
		t.Fatalf("persona hot-reload proposed = %+v", persona)
	}
	if memory := byMethod[RuntimeMethodMemoryConfigure].Params["proposed"].(domain.SoulMemorySpec); memory.Search == nil || memory.Search.TopK != 3 || memory.Search.ScoreThreshold != 0.82 {
		t.Fatalf("memory hot-reload proposed = %+v", memory)
	}
}

func TestIntegrationRollbackAfterFailedConfigChangeRestoresPreviousConfig(t *testing.T) {
	signer := newFakeSigner(t)
	current := completeCustomizationDraftContent()
	current.SpecHash = "sha256:old"
	current.Identity.Purpose = "Research"
	current.Voice.PersonaID = "alloy"
	proposed := current
	proposed.SpecHash = "sha256:new"
	proposed.Identity.Purpose = "Research deeply"
	proposed.Voice.PersonaID = "verse"

	soul, runtime := runIntegrationHotReload(t, signer, current, proposed, &integrationRuntimeAdapter{runtime: domain.RuntimeTargetOpenClaw, bindingPrefix: "openclaw", failOn: 2}, func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "runtime error")
	})
	if soul.SpecHash != "sha256:old" || soul.DraftEventID != "draft-current" || soul.Purpose != "Research" {
		t.Fatalf("soul after failed reload = draft %q spec %q purpose %q", soul.DraftEventID, soul.SpecHash, soul.Purpose)
	}
	if got := integrationActions(runtime.requests); !reflect.DeepEqual(got, []domain.SoulActionType{domain.SoulActionHotReload, domain.SoulActionHotReload, domain.SoulActionRollback, domain.SoulActionRollback}) {
		t.Fatalf("runtime actions = %#v", got)
	}
}

func TestIntegrationMultiRuntimeCustomizationParity(t *testing.T) {
	signer := newFakeSigner(t)
	content := completeCustomizationDraftContent()
	methods := []string{RuntimeMethodProvision, RuntimeMethodVoiceConfigure, RuntimeMethodMemoryConfigure, RuntimeMethodPersonaUpdate, RuntimeMethodAvatarGenerate}
	openclaw := runIntegrationProvisionForRuntime(t, signer, domain.RuntimeTargetOpenClaw, "openclaw", content)
	metiq := runIntegrationProvisionForRuntime(t, signer, domain.RuntimeTargetMetiq, "metiq", content)

	for _, runtime := range []*integrationRuntimeAdapter{openclaw, metiq} {
		caps, err := runtime.DiscoverCapabilities(t.Context(), content.RelayPolicy)
		if err != nil {
			t.Fatalf("DiscoverCapabilities(%s): %v", runtime.runtime, err)
		}
		if len(caps) != 1 || !reflect.DeepEqual(caps[0].Methods, methods) || caps[0].Runtime != runtime.runtime {
			t.Fatalf("capabilities for %s = %+v", runtime.runtime, caps)
		}
		if len(runtime.requests) != 1 || runtime.requests[0].Params["voice"] == nil || runtime.requests[0].Params["memory"] == nil || runtime.requests[0].Params["persona"] == nil {
			t.Fatalf("customization params for %s = %+v", runtime.runtime, runtime.requests)
		}
	}
}

func runIntegrationProvisionForRuntime(t *testing.T, signer fakeSigner, target domain.RuntimeTarget, binding string, content domain.SoulDraftContent) *integrationRuntimeAdapter {
	t.Helper()
	content.Runtime.Target = target
	content.Runtime.RuntimePubkey = string(target) + "-runtime-pubkey"
	content.Runtime.CapabilityRef = string(target) + "-capability"
	content.SpecHash = "sha256:" + string(target)
	draft := &domain.SoulDraft{EventID: string(target) + "-draft", AgentID: "parity-" + string(target), Name: "Parity", Tier: domain.SoulTierHeavy, CreatedBy: signer.pubkey, Content: content}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}, SoulFactoryPubkey: signer.pubkey}, scriptedGenerator{}, signer, slog.Default())
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	close(subscription.eose)
	relayBus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	reactor.relayBus = relayBus
	attachPublishCapture(reactor)
	reactor.getDraftFn = func(context.Context, string, string) (*domain.SoulDraft, error) { return draft, nil }
	runtime := &integrationRuntimeAdapter{runtime: target, bindingPrefix: binding, methods: []string{RuntimeMethodProvision, RuntimeMethodVoiceConfigure, RuntimeMethodMemoryConfigure, RuntimeMethodPersonaUpdate, RuntimeMethodAvatarGenerate}}
	config := FullProvisionerConfig{RuntimeAdapters: map[domain.RuntimeTarget]RuntimeAdapter{target: runtime}}
	if target == domain.RuntimeTargetOpenClaw {
		config.OpenClawReadiness = acceptingOpenClawReadiness{}
	}
	reactor.provisioner = NewFullProvisioner(reactor, config, nil)
	reactor.handleProvisioningRequest(t.Context(), buildProvisioningEvent(t, signer.pubkey, string(target)+"-provision", nostr.Tags{{"agent-id", draft.AgentID}, {"draft-event", draft.EventID}, {"spec-hash", content.SpecHash}}, `{}`))
	return runtime
}

func runIntegrationHotReload(t *testing.T, signer fakeSigner, current, proposed domain.SoulDraftContent, runtime *integrationRuntimeAdapter, wantErr func(error) bool) (*domain.AgentSoul, *integrationRuntimeAdapter) {
	t.Helper()
	current.Runtime.RuntimePubkey = "runtime-pubkey"
	current.Runtime.Target = runtime.runtime
	proposed.Runtime.RuntimePubkey = "runtime-pubkey"
	proposed.Runtime.Target = runtime.runtime
	currentDraft := &domain.SoulDraft{EventID: "draft-current", AgentID: "scout", CreatedBy: signer.pubkey, Content: current}
	proposedDraft := &domain.SoulDraft{EventID: "draft-proposed", AgentID: "scout", CreatedBy: signer.pubkey, Content: proposed}
	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: current.Identity.Name, Purpose: current.Identity.Purpose, Tier: current.Identity.Tier, Status: domain.SoulStatusActive, DraftRef: "31952:" + signer.pubkey + ":scout", DraftEventID: currentDraft.EventID, SpecHash: current.SpecHash, Runtime: current.Runtime, Assets: current.Assets, CreatedAt: time.Now().UTC()}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}}, scriptedGenerator{}, signer, slog.Default())
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	close(subscription.eose)
	relayBus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	reactor.relayBus = relayBus
	attachPublishCapture(reactor)
	reactor.getSoulFn = func(context.Context, string) (*domain.AgentSoul, error) { return soul, nil }
	reactor.getDraftFn = func(_ context.Context, _ string, eventID string) (*domain.SoulDraft, error) {
		switch eventID {
		case currentDraft.EventID:
			return currentDraft, nil
		case proposedDraft.EventID:
			return proposedDraft, nil
		default:
			return nil, nil
		}
	}
	handler := NewLifecycleHandler(reactor, nil, nil, slog.Default())
	handler.SetRuntimeAdapters(map[domain.RuntimeTarget]RuntimeAdapter{runtime.runtime: runtime})
	err = handler.HandleAction(t.Context(), buildActionEvent(t, signer, "hot-reload", nostr.Tags{{"soul", buildSoulRefForTest(soul)}, {"action", string(domain.SoulActionHotReload)}, {"draft-event", proposedDraft.EventID}, {"spec-hash", proposed.SpecHash}, {"previous-spec-hash", current.SpecHash}}, ""))
	if wantErr == nil && err != nil {
		t.Fatalf("HandleAction(hot-reload) error = %v", err)
	}
	if wantErr != nil && !wantErr(err) {
		t.Fatalf("HandleAction(hot-reload) error = %v", err)
	}
	return soul, runtime
}

func integrationMethods(requests []RuntimeAdapterRequest) []string {
	out := make([]string, 0, len(requests))
	for _, req := range requests {
		out = append(out, req.Method)
	}
	return out
}

func integrationActions(requests []RuntimeAdapterRequest) []domain.SoulActionType {
	out := make([]domain.SoulActionType, 0, len(requests))
	for _, req := range requests {
		out = append(out, req.Action)
	}
	return out
}

type integrationRuntimeAdapter struct {
	runtime       domain.RuntimeTarget
	bindingPrefix string
	methods       []string
	requests      []RuntimeAdapterRequest
	failOn        int
}

func (a *integrationRuntimeAdapter) Runtime() domain.RuntimeTarget { return a.runtime }

func (a *integrationRuntimeAdapter) DiscoverCapabilities(context.Context, domain.SoulRelayPolicySpec) ([]RuntimeCapability, error) {
	methods := a.methods
	if len(methods) == 0 {
		methods = []string{RuntimeMethodProvision, RuntimeMethodVoiceConfigure, RuntimeMethodMemoryConfigure, RuntimeMethodPersonaUpdate, RuntimeMethodAvatarGenerate}
	}
	runtimePubkey := soulTestPubKeyHex(string(a.runtime) + "-runtime-pubkey")
	return []RuntimeCapability{{Runtime: a.runtime, Pubkey: runtimePubkey, Methods: methods, ControlSchema: domain.SoulFactoryRuntimeControlSchema, ControllerPubkeys: []string{"controller"}, Coordinate: "30317:" + string(a.runtime) + ":capability"}}, nil
}

func (a *integrationRuntimeAdapter) Execute(_ context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error) {
	a.requests = append(a.requests, req)
	if a.failOn > 0 && len(a.requests) == a.failOn {
		return &RuntimeControlResultEnvelope{Schema: domain.SoulFactoryRuntimeControlSchema, Method: req.Method, Status: "failed", Error: &RuntimeControlError{Code: "runtime_error", Message: "runtime error"}}, fmt.Errorf("runtime error")
	}
	runtimePubkey := firstNonEmpty(req.Target.RuntimePubkey, soulTestPubKeyHex(string(req.Target.Runtime)+"-runtime-pubkey"))
	return &RuntimeControlResultEnvelope{Schema: domain.SoulFactoryRuntimeControlSchema, Method: req.Method, IdempotencyKey: "sha256:test", OperatorRequestEvent: req.Operator.RequestEvent, RequestEvent: "runtime-request", Status: "success", Result: map[string]interface{}{"agent_id": req.Target.AgentID, "runtime": req.Target.Runtime, "runtime_pubkey": runtimePubkey, "runtime_binding": a.bindingPrefix + "://agents/" + req.Target.AgentID, "state": "running", "spec_hash": req.Soul.SpecHash, "capability_ref": "capability-" + string(req.Target.Runtime), "account_id": req.Target.AgentID + "-account", "provider": "routstr", "model": "routstr/model-a"}, Event: &nostr.Event{PubKey: soulTestPubKey(runtimePubkey)}}, nil
}
