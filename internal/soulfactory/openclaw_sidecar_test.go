package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeOpenClawSidecarTransport struct {
	published []nostr.Event
	sub       *RelayBusSubscription
}

func (f *fakeOpenClawSidecarTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	f.published = append(f.published, event)
	return 1, nil
}

func (f *fakeOpenClawSidecarTransport) SubscribeAllWithEOSE(_ context.Context, _ []nostr.Filter) (*RelayBusSubscription, error) {
	if f.sub == nil {
		return nil, errors.New("no subscription configured")
	}
	return f.sub, nil
}

func (f *fakeOpenClawSidecarTransport) Close() {}

type fakeOpenClawDriver struct {
	methods []string
	calls   []OpenClawControlInvocation
	outcome *OpenClawControlOutcome
	err     error
}

type fakeAvatarRuntimeGenerator struct {
	spec domain.SoulAvatarGenerationSpec
}

func (f *fakeAvatarRuntimeGenerator) GenerateWithSpec(_ context.Context, spec domain.SoulAvatarGenerationSpec, progress llm.AvatarProgressFunc) (*llm.AvatarResult, error) {
	f.spec = spec
	progress(llm.AvatarProgressEvent{Provider: spec.Provider, Stage: llm.AvatarProgressQueued, Percent: 0, Message: "queued"})
	progress(llm.AvatarProgressEvent{Provider: spec.Provider, Stage: llm.AvatarProgressCompleted, Percent: 100, Message: "done"})
	return &llm.AvatarResult{ImageData: []byte("png"), ContentType: "image/png", Seed: spec.Seed, Provider: spec.Provider}, nil
}

func (f *fakeAvatarRuntimeGenerator) ProviderInfos() []llm.AvatarProviderInfo {
	return []llm.AvatarProviderInfo{{Name: "test-provider", Available: true}}
}

type fakeAvatarRuntimeStore struct{}

func (f fakeAvatarRuntimeStore) StoreAvatar(context.Context, []byte, string, string) (*blossom.AvatarStoreResult, error) {
	return &blossom.AvatarStoreResult{Ref: "blossom:avatar", Hash: "avatar", ContentType: "image/png", Size: 3}, nil
}

func (f *fakeOpenClawDriver) Methods() []string {
	if len(f.methods) > 0 {
		return f.methods
	}
	return append([]string{}, openClawSoulFactoryMethods...)
}

func (f *fakeOpenClawDriver) Execute(_ context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	f.calls = append(f.calls, invocation)
	if f.err != nil {
		return nil, f.err
	}
	if f.outcome != nil {
		return f.outcome, nil
	}
	return &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "running"}}, nil
}

func newTestOpenClawSidecar(t *testing.T, runtime, controller fakeSigner, transport *fakeOpenClawSidecarTransport, driver *fakeOpenClawDriver) *OpenClawSidecar {
	t.Helper()
	sidecar, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Identifier:               "openclaw-test",
		Relays:                   []string{"wss://relay.example"},
		RelayHints: domain.SoulRelayPolicySpec{
			Read:    []string{"wss://read.example"},
			Write:   []string{"wss://write.example"},
			Control: []string{"wss://control.example"},
		},
		Transport: transport,
		Driver:    driver,
		Now:       time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar error = %v", err)
	}
	return sidecar
}

func TestOpenClawSidecarPublishesCompatibleCapability(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{methods: []string{RuntimeMethodProvision, RuntimeMethodSuspend}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)

	if err := sidecar.PublishCapability(t.Context()); err != nil {
		t.Fatalf("PublishCapability error = %v", err)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(transport.published))
	}
	event := transport.published[0]
	capability, ok := ParseRuntimeCapabilityEvent(&event)
	if !ok {
		t.Fatal("published capability did not parse")
	}
	if capability.Pubkey != runtime.pubkey || capability.Runtime != domain.RuntimeTargetOpenClaw || !capability.Compatible {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	if !capability.Supports(domain.RuntimeTargetOpenClaw, RuntimeMethodProvision, controller.pubkey) || !capability.Supports(domain.RuntimeTargetOpenClaw, RuntimeMethodSuspend, controller.pubkey) {
		t.Fatalf("capability does not advertise OpenClaw SoulFactory support: %+v", capability)
	}
	if got := capability.RelayHints.Control; !reflect.DeepEqual(got, []string{"wss://control.example"}) {
		t.Fatalf("control relay hints = %#v", got)
	}
}

func TestOpenClawSidecarValidatesTrustAddressingAndRequiredParams(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	untrusted := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)

	valid := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(valid); err != nil {
		t.Fatalf("valid request rejected: %+v", err)
	}

	badController := signedOpenClawControlRequest(t, untrusted, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(badController); err == nil || err.Code != "unauthorized_controller" {
		t.Fatalf("untrusted controller error = %+v, want unauthorized_controller", err)
	}

	misaddressed := signedOpenClawControlRequest(t, controller, stringsRepeat("f", 64), RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(misaddressed); err == nil || err.Code != "misaddressed_request" {
		t.Fatalf("misaddressed error = %+v, want misaddressed_request", err)
	}

	missingReason := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodSuspend, map[string]interface{}{}, nil)
	if _, err := sidecar.ValidateControlEvent(missingReason); err == nil || err.Code != "missing_required_param" {
		t.Fatalf("missing reason error = %+v, want missing_required_param", err)
	}
}

func TestOpenClawSidecarExecutesProvisionAndPublishesCorrelatedResult(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{outcome: &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "running", "runtime_binding": "openclaw://agents/agent-alice"}}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)

	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent error = %v", err)
	}
	if result.Status != "success" || result.Method != RuntimeMethodProvision || result.RequestEvent != request.ID || result.OperatorRequestEvent != "operator-request" {
		t.Fatalf("unexpected result envelope: %+v", result)
	}
	if len(driver.calls) != 1 || driver.calls[0].Method != RuntimeMethodProvision || driver.calls[0].AgentID != "agent-alice" {
		t.Fatalf("driver calls = %+v", driver.calls)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want result", len(transport.published))
	}
	published := transport.published[0]
	if published.Kind != domain.KindRuntimeControlResult || published.PubKey != runtime.pubkey || !published.CheckID() {
		t.Fatalf("result event not signed by runtime: kind=%d pubkey=%s", published.Kind, published.PubKey)
	}
	if tagValue(published.Tags, tagEvent) != request.ID || tagValue(published.Tags, tagPubkey) != controller.pubkey || tagValue(published.Tags, "idempotency-key") != "idem-soulfactory.provision" {
		t.Fatalf("result tags are not correlated: %#v", published.Tags)
	}
	parsed, ok := parseRuntimeControlResultEvent(&published)
	if !ok || !runtimeResultCorrelates(parsed, request, RuntimeAdapterRequest{Method: RuntimeMethodProvision, IdempotencyKey: "idem-soulfactory.provision", Operator: RuntimeOperatorRef{RequestEvent: "operator-request"}, Target: RuntimeTargetRef{RuntimePubkey: runtime.pubkey, AgentID: "agent-alice"}, Soul: RuntimeSoulRef{ID: "soul-alice", SpecHash: "sha256:spec"}}, controller.pubkey) {
		t.Fatalf("published result does not parse/correlate: %+v", parsed)
	}
}

func TestOpenClawSidecarIdempotentReplayDoesNotRepeatSideEffects(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)

	if _, err := sidecar.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("first HandleControlEvent error = %v", err)
	}
	if _, err := sidecar.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("replay HandleControlEvent error = %v", err)
	}
	if len(driver.calls) != 1 {
		t.Fatalf("driver called %d times, want 1", len(driver.calls))
	}
	if len(transport.published) != 2 {
		t.Fatalf("published results = %d, want one per request event delivery", len(transport.published))
	}
}

func TestOpenClawSidecarPersistsIdempotencyAcrossRestart(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	storePath := t.TempDir() + "/idempotency.json"
	store1, err := NewFileOpenClawIdempotencyStore(storePath)
	if err != nil {
		t.Fatalf("NewFileOpenClawIdempotencyStore first error = %v", err)
	}
	transport1 := &fakeOpenClawSidecarTransport{}
	driver1 := &fakeOpenClawDriver{}
	sidecar1, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Relays:                   []string{"wss://relay.example"},
		Transport:                transport1,
		Driver:                   driver1,
		IdempotencyStore:         store1,
		Now:                      time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar first error = %v", err)
	}
	if _, err := sidecar1.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("first HandleControlEvent error = %v", err)
	}
	if len(driver1.calls) != 1 {
		t.Fatalf("first driver calls = %d, want 1", len(driver1.calls))
	}

	store2, err := NewFileOpenClawIdempotencyStore(storePath)
	if err != nil {
		t.Fatalf("NewFileOpenClawIdempotencyStore second error = %v", err)
	}
	transport2 := &fakeOpenClawSidecarTransport{}
	driver2 := &fakeOpenClawDriver{}
	sidecar2, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Relays:                   []string{"wss://relay.example"},
		Transport:                transport2,
		Driver:                   driver2,
		IdempotencyStore:         store2,
		Now:                      time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar second error = %v", err)
	}
	if _, err := sidecar2.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("replayed HandleControlEvent error = %v", err)
	}
	if len(driver2.calls) != 0 {
		t.Fatalf("restarted sidecar driver calls = %d, want 0", len(driver2.calls))
	}
	if len(transport2.published) != 1 || tagValue(transport2.published[0].Tags, tagEvent) != request.ID {
		t.Fatalf("restarted sidecar did not republish cached correlated result: %#v", transport2.published)
	}
}

func TestOpenClawSidecarExecutesLifecycleSuspend(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{outcome: &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "suspended"}}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodSuspend, map[string]interface{}{"reason": "operator request"}, nil)

	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent error = %v", err)
	}
	if result.Status != "success" || result.Result["state"] != "suspended" {
		t.Fatalf("unexpected lifecycle result: %+v", result)
	}
	if len(driver.calls) != 1 || driver.calls[0].Method != RuntimeMethodSuspend {
		t.Fatalf("driver calls = %+v", driver.calls)
	}
}

func TestOpenClawSidecarAvatarRuntimeMethodsPublish38386Results(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	generator := &fakeAvatarRuntimeGenerator{}
	driver := &AvatarRuntimeControlDriver{Generator: generator, Store: fakeAvatarRuntimeStore{}, Now: func() time.Time { return time.Unix(10, 0) }}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, &fakeOpenClawDriver{methods: driver.Methods()})
	sidecar.driver = driver

	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodAvatarGenerate, map[string]interface{}{
		"generation": map[string]interface{}{"prompt": "pixel owl", "style_preset": "pixel-art", "seed": "owl-1", "provider": "test-provider"},
	}, nil)
	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent avatar.generate error = %v", err)
	}
	if result.Status != "success" || result.Method != RuntimeMethodAvatarGenerate || result.Result["avatar_ref"] != "blossom:avatar" {
		t.Fatalf("unexpected avatar generate result: %+v", result)
	}
	if generator.spec.Prompt != "pixel owl" || generator.spec.StylePreset != "pixel-art" || generator.spec.Seed != "owl-1" {
		t.Fatalf("generation spec = %+v", generator.spec)
	}
	if patch, ok := result.Result["read_model_patch"].(map[string]interface{}); !ok || patch["assets"] == nil {
		t.Fatalf("missing read model patch: %+v", result.Result)
	}
	if progress, ok := result.Result["progress_events"].([]map[string]interface{}); !ok || len(progress) != 2 {
		t.Fatalf("progress events = %#v", result.Result["progress_events"])
	}
	if len(transport.published) != 1 || transport.published[0].Kind != domain.KindRuntimeControlResult {
		t.Fatalf("published avatar result events = %#v", transport.published)
	}

	statusReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodAvatarStatus, map[string]interface{}{}, nil)
	status, err := sidecar.HandleControlEvent(t.Context(), statusReq)
	if err != nil {
		t.Fatalf("HandleControlEvent avatar.status error = %v", err)
	}
	if status.Result["avatar_ref"] != "blossom:avatar" || status.Result["state"] != "completed" {
		t.Fatalf("unexpected avatar status: %+v", status.Result)
	}
}

func TestOpenClawSidecarVoiceRuntimeMethodsPublish38386Results(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &VoiceRuntimeControlDriver{Now: func() time.Time { return time.Unix(20, 0) }}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, &fakeOpenClawDriver{methods: driver.Methods()})
	sidecar.driver = driver

	voice := map[string]interface{}{
		"provider":    "openai-tts",
		"persona_id":  "nova",
		"auto_mode":   "tagged",
		"sample_text": "Hello from Scout.",
		"persona": map[string]interface{}{
			"label":   "Scout Voice",
			"profile": "Helpful researcher",
			"style":   "clear",
			"accent":  "neutral american",
			"pacing":  "measured",
		},
	}
	configureReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodVoiceConfigure, map[string]interface{}{"voice": voice}, nil)
	configure, err := sidecar.HandleControlEvent(t.Context(), configureReq)
	if err != nil {
		t.Fatalf("HandleControlEvent voice.configure error = %v", err)
	}
	if configure.Status != "success" || configure.Method != RuntimeMethodVoiceConfigure || configure.Result["provider"] != VoiceProviderOpenAITTS || configure.Result["persona_id"] != "nova" {
		t.Fatalf("unexpected voice configure result: %+v", configure)
	}
	if configure.Result["read_model_patch"] == nil || configure.Result["voice_config"] == nil || configure.Result["hot_reload"] != true {
		t.Fatalf("missing voice config result fields: %+v", configure.Result)
	}

	previewReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodVoicePreview, map[string]interface{}{"voice": voice}, nil)
	preview, err := sidecar.HandleControlEvent(t.Context(), previewReq)
	if err != nil {
		t.Fatalf("HandleControlEvent voice.preview error = %v", err)
	}
	if preview.Result["sample_audio_ref"] == "" || preview.Result["sample_text"] != "Hello from Scout." || preview.Result["provider"] != VoiceProviderOpenAITTS {
		t.Fatalf("unexpected voice preview result: %+v", preview.Result)
	}

	sampleReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodVoiceSample, map[string]interface{}{"voice": voice}, nil)
	sample, err := sidecar.HandleControlEvent(t.Context(), sampleReq)
	if err != nil {
		t.Fatalf("HandleControlEvent voice.sample error = %v", err)
	}
	if sample.Result["sample_audio_ref"] == "" || sample.Result["preview_ref"] != sample.Result["sample_audio_ref"] {
		t.Fatalf("unexpected voice sample result: %+v", sample.Result)
	}

	listReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodVoiceList, map[string]interface{}{}, nil)
	list, err := sidecar.HandleControlEvent(t.Context(), listReq)
	if err != nil {
		t.Fatalf("HandleControlEvent voice.list error = %v", err)
	}
	if list.Result["providers"] == nil || list.Result["voices"] == nil {
		t.Fatalf("unexpected voice list result: %+v", list.Result)
	}
	if len(transport.published) != 4 {
		t.Fatalf("published voice result events = %d, want 4", len(transport.published))
	}
}

func TestOpenClawSidecarPersonaConfigureAndPreview(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &PersonaRuntimeControlDriver{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, &fakeOpenClawDriver{methods: driver.Methods()})
	sidecar.driver = driver

	persona := map[string]interface{}{
		"traits":      []interface{}{"curious", "thorough"},
		"style":       "conversational",
		"tone":        "friendly professional",
		"constraints": []interface{}{"Never fabricate citations"},
		"system_prompt_sections": map[string]interface{}{
			"role":       "You are Scout, a research assistant.",
			"guidelines": "Answer with concise findings.",
		},
	}
	configureReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodPersonaConfigure, map[string]interface{}{"persona": persona}, nil)
	configure, err := sidecar.HandleControlEvent(t.Context(), configureReq)
	if err != nil {
		t.Fatalf("HandleControlEvent persona.configure error = %v", err)
	}
	if configure.Status != "success" || configure.Method != RuntimeMethodPersonaConfigure || configure.Result["hot_reload"] != true || configure.Result["applied"] != true {
		t.Fatalf("unexpected persona configure result: %+v", configure)
	}
	if configure.Result["system_prompt"] == "" || configure.Result["system_prompt_sections"] == nil || configure.Result["openclaw"] == nil {
		t.Fatalf("missing generated prompt fields: %+v", configure.Result)
	}

	previewReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodPersonaPreview, map[string]interface{}{"persona": persona}, nil)
	preview, err := sidecar.HandleControlEvent(t.Context(), previewReq)
	if err != nil {
		t.Fatalf("HandleControlEvent persona.preview error = %v", err)
	}
	if preview.Status != "success" || preview.Method != RuntimeMethodPersonaPreview || preview.Result["applied"] != false || preview.Result["hot_reload"] != false {
		t.Fatalf("unexpected persona preview result: %+v", preview)
	}
	if len(transport.published) != 2 || transport.published[0].Kind != domain.KindRuntimeControlResult || transport.published[1].Kind != domain.KindRuntimeControlResult {
		t.Fatalf("published persona result events = %+v, want two 38386 results", transport.published)
	}
}

func TestOpenClawSidecarAvatarListAndSet(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &AvatarRuntimeControlDriver{Generator: &fakeAvatarRuntimeGenerator{}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, &fakeOpenClawDriver{methods: driver.Methods()})
	sidecar.driver = driver

	listReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodAvatarList, map[string]interface{}{}, nil)
	listResult, err := sidecar.HandleControlEvent(t.Context(), listReq)
	if err != nil {
		t.Fatalf("HandleControlEvent avatar.list error = %v", err)
	}
	if listResult.Result["providers"] == nil || listResult.Result["style_presets"] == nil {
		t.Fatalf("unexpected avatar list result: %+v", listResult.Result)
	}

	setReq := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodAvatarSet, map[string]interface{}{"avatar_ref": "blossom:existing"}, nil)
	setResult, err := sidecar.HandleControlEvent(t.Context(), setReq)
	if err != nil {
		t.Fatalf("HandleControlEvent avatar.set error = %v", err)
	}
	if setResult.Result["avatar_ref"] != "blossom:existing" || setResult.Result["read_model_patch"] == nil {
		t.Fatalf("unexpected avatar set result: %+v", setResult.Result)
	}
}

func openClawProvisionParams() map[string]interface{} {
	return map[string]interface{}{
		"identity":     map[string]interface{}{"name": "Alice", "purpose": "help operators", "tier": "standard"},
		"runtime":      map[string]interface{}{"target": "openclaw", "capability_ref": "capability-event"},
		"permissions":  map[string]interface{}{"allowed_kinds": []int{1}, "tool_grants": []string{}, "approval_policy": "manual"},
		"relay_policy": map[string]interface{}{"read": []string{"wss://relay.example"}, "write": []string{"wss://relay.example"}, "control": []string{"wss://relay.example"}},
		"workspace":    map[string]interface{}{"repo": "/tmp/alice", "branch": "main"},
		"assets":       map[string]interface{}{"avatar_ref": "https://example.com/alice.png"},
	}
}

func signedOpenClawControlRequest(t *testing.T, signer fakeSigner, runtimePubkey, method string, params map[string]interface{}, mutate func(*nostr.Event, *RuntimeControlEnvelope)) *nostr.Event {
	t.Helper()
	specHash := "sha256:spec"
	if method == RuntimeMethodUpdate {
		specHash = "sha256:spec2"
	}
	envelope := RuntimeControlEnvelope{
		Schema:         domain.SoulFactoryRuntimeControlSchema,
		Method:         method,
		IdempotencyKey: "idem-" + method,
		RequestedAt:    int64(nostr.Now()),
		Operator:       RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: "operator-request"},
		Controller:     RuntimeControllerRef{Pubkey: signer.pubkey},
		Target:         RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: runtimePubkey, AgentID: "agent-alice"},
		Soul:           RuntimeSoulRef{ID: "soul-alice", Draft: "draft-event", SpecHash: specHash},
		Params:         params,
	}
	event, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	event.CreatedAt = nostr.Now()
	if mutate != nil {
		mutate(event, &envelope)
		content, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal mutated envelope: %v", err)
		}
		event.Content = string(content)
	}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	return event
}
