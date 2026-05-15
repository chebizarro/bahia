package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type capturedPublish struct {
	events []*nostr.Event
	relays [][]string
}

func attachPublishCapture(reactor *Reactor) *capturedPublish {
	capture := &capturedPublish{}
	reactor.publishFn = func(_ context.Context, event *nostr.Event, relays []string) error {
		copied := *event
		copied.Tags = append(nostr.Tags{}, event.Tags...)
		capture.events = append(capture.events, &copied)
		capture.relays = append(capture.relays, append([]string(nil), relays...))
		return nil
	}
	return capture
}

func (c *capturedPublish) eventsByKind(kind int) []*nostr.Event {
	var out []*nostr.Event
	for _, event := range c.events {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

func buildProvisioningEvent(t *testing.T, pubkey, eventID string, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	if eventID == "" {
		eventID = fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	return &nostr.Event{
		ID:        eventID,
		Kind:      domain.KindProvisioningRequest,
		CreatedAt: nostr.Now(),
		PubKey:    pubkey,
		Tags:      tags,
		Content:   content,
	}
}

func findTag(event *nostr.Event, name string) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

type scriptedGenerator struct{}

func (scriptedGenerator) Generate(_ context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	brief := strings.TrimSpace(input.Brief)
	return &domain.SoulGeneratorOutput{
		SoulMD:       "# Soul\n" + brief,
		IdentityMD:   "# Identity\n" + brief,
		AllowedKinds: []int{1, domain.KindSoulAction},
		ToolGrants: []domain.ToolGrant{{
			MCPServer: "memory",
			Scopes:    []string{"read", "write"},
		}},
	}, nil
}

type failingWorkspaceManager struct{ err error }

func (m failingWorkspaceManager) InitWorkspace(context.Context, *domain.AgentSoul) (string, error) {
	return "", m.err
}

func TestReactorPublishesCorrelatedErrorsForUnauthorizedAndMalformedRequests(t *testing.T) {
	authorized := strings.Repeat("1", 64)
	reactor := NewReactor(
		Config{AuthorizedPubkeys: []string{authorized}, AdditionalRelays: []string{"wss://private.example"}},
		fakeGenerator{},
		newFakeSigner(t),
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)

	t.Run("unauthorized requester", func(t *testing.T) {
		capture.events = nil
		request := buildProvisioningEvent(
			t,
			strings.Repeat("2", 64),
			"unauthorized-request",
			nostr.Tags{{"agent-id", "unauthorized-agent"}},
			`{"brief":"not allowed"}`,
		)

		reactor.handleProvisioningRequest(t.Context(), request)

		results := capture.eventsByKind(domain.KindProvisioningResult)
		if len(results) != 1 {
			t.Fatalf("provisioning result count = %d, want 1", len(results))
		}
		result := results[0]
		if got := findTag(result, "e"); got != request.ID {
			t.Fatalf("reply event tag = %q, want %q", got, request.ID)
		}
		if got := findTag(result, "p"); got != request.PubKey {
			t.Fatalf("reply pubkey tag = %q, want %q", got, request.PubKey)
		}
		if got := findTag(result, "status"); got != "error" {
			t.Fatalf("status tag = %q, want error", got)
		}
		if got := findTag(result, "step"); got != "unauthorized" {
			t.Fatalf("step tag = %q, want unauthorized", got)
		}
		if result.Content != "requester not in authorized provisioners list" {
			t.Fatalf("result content = %q, want unauthorized reason", result.Content)
		}
		if len(capture.eventsByKind(domain.KindAgentSoul)) != 0 {
			t.Fatalf("unexpected soul publication for unauthorized request")
		}
	})

	cases := []struct {
		name        string
		tags        nostr.Tags
		content     string
		wantStep    string
		wantMessage string
	}{
		{
			name:        "missing agent id",
			tags:        nostr.Tags{},
			content:     `{"brief":"missing agent id"}`,
			wantStep:    "parse_error",
			wantMessage: "missing agent-id tag",
		},
		{
			name:        "missing brief and references",
			tags:        nostr.Tags{{"agent-id", "scout"}},
			content:     `{}`,
			wantStep:    "parse_error",
			wantMessage: "must provide brief, draft, or template",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture.events = nil
			request := buildProvisioningEvent(t, authorized, tc.name, tc.tags, tc.content)
			reactor.handleProvisioningRequest(t.Context(), request)

			results := capture.eventsByKind(domain.KindProvisioningResult)
			if len(results) != 1 {
				t.Fatalf("provisioning result count = %d, want 1", len(results))
			}
			result := results[0]
			if got := findTag(result, "e"); got != request.ID {
				t.Fatalf("reply event tag = %q, want %q", got, request.ID)
			}
			if got := findTag(result, "p"); got != request.PubKey {
				t.Fatalf("reply pubkey tag = %q, want %q", got, request.PubKey)
			}
			if got := findTag(result, "status"); got != "error" {
				t.Fatalf("status tag = %q, want error", got)
			}
			if got := findTag(result, "step"); got != tc.wantStep {
				t.Fatalf("step tag = %q, want %q", got, tc.wantStep)
			}
			if result.Content != tc.wantMessage {
				t.Fatalf("result content = %q, want %q", result.Content, tc.wantMessage)
			}
			if len(capture.eventsByKind(domain.KindAgentSoul)) != 0 {
				t.Fatalf("unexpected soul publication for malformed request")
			}
		})
	}
}

func TestFullProvisionerSuccessRecordsEightStagesAndCorrelatedProgress(t *testing.T) {
	signer := newFakeSigner(t)
	oldPubkey := SoulFactoryPubkey
	SoulFactoryPubkey = signer.pubkey
	defer func() { SoulFactoryPubkey = oldPubkey }()

	reactor := NewReactor(
		Config{
			Relays:            []string{"wss://public.example"},
			AdditionalRelays:  []string{"wss://private.example"},
			AuthorizedPubkeys: []string{signer.pubkey},
			SoulFactoryPubkey: signer.pubkey,
		},
		scriptedGenerator{},
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	reactor.provisioner = full

	request := buildProvisioningEvent(
		t,
		signer.pubkey,
		"successful-provision",
		nostr.Tags{{"agent-id", "scout"}, {"name", "Scout"}, {"tier", string(domain.SoulTierStandard)}},
		`{"brief":"Monitor deployments"}`,
	)

	reactor.handleProvisioningRequest(t.Context(), request)

	run := reactor.GetRun(request.ID)
	if run == nil {
		t.Fatal("expected provisioning run to be tracked")
	}
	if run.Status != domain.ProvisioningStatusCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	if len(run.Steps) != len(domain.ProvisioningSteps) {
		t.Fatalf("run steps = %d, want %d", len(run.Steps), len(domain.ProvisioningSteps))
	}
	for idx, wantStep := range domain.ProvisioningSteps {
		if run.Steps[idx].Name != wantStep {
			t.Fatalf("step[%d] name = %s, want %s", idx, run.Steps[idx].Name, wantStep)
		}
	}
	if run.Steps[0].Status != domain.StepStatusComplete || run.Steps[1].Status != domain.StepStatusComplete || run.Steps[3].Status != domain.StepStatusComplete || run.Steps[7].Status != domain.StepStatusComplete {
		t.Fatalf("unexpected required-step statuses: %+v", run.Steps)
	}
	for _, stepIndex := range []int{2, 4, 5, 6} {
		if run.Steps[stepIndex].Status != domain.StepStatusSkipped {
			t.Fatalf("step[%d] status = %s, want skipped", stepIndex, run.Steps[stepIndex].Status)
		}
	}

	statusEvents := capture.eventsByKind(domain.KindProvisioningStatus)
	if len(statusEvents) != len(domain.ProvisioningSteps) {
		t.Fatalf("status event count = %d, want %d", len(statusEvents), len(domain.ProvisioningSteps))
	}
	for idx, event := range statusEvents {
		if got := findTag(event, "e"); got != request.ID {
			t.Fatalf("status[%d] reply tag = %q, want %q", idx, got, request.ID)
		}
		if got := findTag(event, "p"); got != request.PubKey {
			t.Fatalf("status[%d] pubkey tag = %q, want %q", idx, got, request.PubKey)
		}
		if got := findTag(event, "status"); got != "processing" {
			t.Fatalf("status[%d] status tag = %q, want processing", idx, got)
		}
		if got := findTag(event, "step"); got != string(domain.ProvisioningSteps[idx]) {
			t.Fatalf("status[%d] step tag = %q, want %q", idx, got, domain.ProvisioningSteps[idx])
		}
		progressTag := slices.IndexFunc(event.Tags, func(tag nostr.Tag) bool { return len(tag) >= 3 && tag[0] == "progress" })
		if progressTag < 0 {
			t.Fatalf("status[%d] missing progress tag", idx)
		}
		if got := event.Tags[progressTag][1]; got != fmt.Sprintf("%d", idx+1) {
			t.Fatalf("status[%d] progress current = %q, want %d", idx, got, idx+1)
		}
		if got := event.Tags[progressTag][2]; got != "8" {
			t.Fatalf("status[%d] progress total = %q, want 8", idx, got)
		}
	}

	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("result event count = %d, want 1", len(results))
	}
	result := results[0]
	if got := findTag(result, "status"); got != "success" {
		t.Fatalf("result status tag = %q, want success", got)
	}
	if got := findTag(result, "e"); got != request.ID {
		t.Fatalf("result reply tag = %q, want %q", got, request.ID)
	}
	if got := findTag(result, "soul"); got != fmt.Sprintf("31951:%s:scout", signer.pubkey) {
		t.Fatalf("result soul tag = %q, want authoritative coordinate", got)
	}
}

func TestOptionalIntegrationFailureIsRecordedWithoutFabricatedSuccess(t *testing.T) {
	reactor := NewReactor(Config{}, scriptedGenerator{}, newFakeSigner(t), slog.Default())
	attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	full.workspaceManager = failingWorkspaceManager{err: errors.New("workspace remote down")}
	reactor.provisioner = full

	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       "request-workspace-failure",
		RequesterPubkey: newFakeSigner(t).pubkey,
		Steps:           []domain.ProvisioningStepResult{},
	}
	soul, err := full.Provision(t.Context(), &domain.ProvisioningRequest{AgentID: "scout", Name: "Scout", Brief: "Handle incidents", Tier: domain.SoulTierStandard}, run)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if soul.WorkspaceRepoURL != "" {
		t.Fatalf("workspace repo url = %q, want empty after failure", soul.WorkspaceRepoURL)
	}
	if len(run.Steps) != len(domain.ProvisioningSteps) {
		t.Fatalf("run steps = %d, want %d", len(run.Steps), len(domain.ProvisioningSteps))
	}
	workspaceStep := run.Steps[6]
	if workspaceStep.Name != domain.StepWorkspace {
		t.Fatalf("workspace step name = %s, want workspace", workspaceStep.Name)
	}
	if workspaceStep.Status != domain.StepStatusFailed {
		t.Fatalf("workspace step status = %s, want failed", workspaceStep.Status)
	}
	if workspaceStep.Error != "workspace remote down" {
		t.Fatalf("workspace step error = %q, want workspace remote down", workspaceStep.Error)
	}
	if workspaceStep.Output != nil {
		t.Fatalf("workspace step output = %+v, want nil on failure", workspaceStep.Output)
	}
	deployStep := run.Steps[7]
	if deployStep.Name != domain.StepDeploy || deployStep.Status != domain.StepStatusComplete {
		t.Fatalf("deploy step = %+v, want completed finalization", deployStep)
	}
}

type capturingGenerator struct {
	inputs []domain.SoulGeneratorInput
}

func (g *capturingGenerator) Generate(_ context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	g.inputs = append(g.inputs, input)
	return &domain.SoulGeneratorOutput{
		SoulMD:       "# Soul\n" + input.Brief,
		IdentityMD:   "# Identity\n" + input.Brief,
		AllowedKinds: []int{999},
		ToolGrants:   []domain.ToolGrant{{MCPServer: "generator", Scopes: []string{"fallback"}}},
	}, nil
}

type fakeProvisioningEngine struct {
	called bool
}

func (e *fakeProvisioningEngine) Provision(context.Context, *domain.ProvisioningRequest, *domain.ProvisioningRun) (*domain.AgentSoul, error) {
	e.called = true
	return &domain.AgentSoul{AgentID: "should-not-run"}, nil
}

type trackingRuntimeAdapter struct {
	requests []RuntimeAdapterRequest
}

func (a *trackingRuntimeAdapter) Runtime() domain.RuntimeTarget { return domain.RuntimeTargetOpenClaw }
func (a *trackingRuntimeAdapter) DiscoverCapabilities(context.Context, domain.SoulRelayPolicySpec) ([]RuntimeCapability, error) {
	return nil, nil
}
func (a *trackingRuntimeAdapter) Execute(_ context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error) {
	a.requests = append(a.requests, req)
	return &RuntimeControlResultEnvelope{
		Schema:               domain.SoulFactoryRuntimeControlSchema,
		Method:               req.Method,
		IdempotencyKey:       "sha256:test",
		OperatorRequestEvent: req.Operator.RequestEvent,
		Status:               "success",
		Result: map[string]interface{}{
			"agent_id":        req.Target.AgentID,
			"runtime":         req.Target.Runtime,
			"runtime_binding": "openclaw://agents/" + req.Target.AgentID,
			"state":           "running",
			"spec_hash":       req.Soul.SpecHash,
			"capability_ref":  "capability-event",
		},
		Event: &nostr.Event{PubKey: "runtime-pubkey"},
	}, nil
}

func TestProvisioningReplaySkipsExternalSideEffectsWhenTerminalResultExists(t *testing.T) {
	signer := newFakeSigner(t)
	engine := &fakeProvisioningEngine{}
	reactor := NewReactor(
		Config{AuthorizedPubkeys: []string{signer.pubkey}, SoulFactoryPubkey: signer.pubkey},
		fakeGenerator{},
		signer,
		slog.Default(),
		WithProvisioningEngine(engine),
	)
	capture := attachPublishCapture(reactor)
	reactor.findProvisioningResultFn = func(context.Context, string) (*nostr.Event, error) {
		return &nostr.Event{
			ID:     "terminal-7950",
			Kind:   domain.KindProvisioningResult,
			PubKey: signer.pubkey,
			Tags:   nostr.Tags{{"e", "already-terminal"}, {"p", signer.pubkey}, {"request-kind", fmt.Sprint(domain.KindProvisioningRequest)}, {"status", "success"}},
		}, nil
	}

	request := buildProvisioningEvent(t, signer.pubkey, "already-terminal", nostr.Tags{{"agent-id", "scout"}}, `{"brief":"Monitor deployments"}`)
	reactor.handleProvisioningRequest(t.Context(), request)

	if engine.called {
		t.Fatal("provisioning engine was called despite existing terminal 7950")
	}
	if len(capture.events) != 0 {
		t.Fatalf("published events = %d, want none for terminal replay", len(capture.events))
	}
	if run := reactor.GetRun(request.ID); run != nil {
		t.Fatalf("run tracked for terminal replay: %+v", run)
	}
}

func TestDraftBackedRuntimeProvisioningPublishesFinalSoulWithResolvedFields(t *testing.T) {
	signer := newFakeSigner(t)
	oldPubkey := SoulFactoryPubkey
	SoulFactoryPubkey = signer.pubkey
	defer func() { SoulFactoryPubkey = oldPubkey }()

	generator := &capturingGenerator{}
	reactor := NewReactor(
		Config{
			Relays:            []string{"wss://public.example"},
			AdditionalRelays:  []string{"wss://private.example"},
			AuthorizedPubkeys: []string{signer.pubkey},
			SoulFactoryPubkey: signer.pubkey,
		},
		generator,
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	draft := &domain.SoulDraft{
		EventID:     "exact-draft-event",
		AgentID:     "scout",
		Name:        "Draft Scout",
		Tier:        domain.SoulTierStandard,
		TemplateRef: "31950:template-author:agent-template",
		CreatedBy:   signer.pubkey,
		Content: domain.SoulDraftContent{
			Brief:    "Draft purpose",
			SpecHash: "sha256:draft",
			Identity: domain.SoulIdentitySpec{Name: "Draft Scout", Purpose: "Draft purpose", Tier: domain.SoulTierStandard},
			Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey", CapabilityRef: "draft-capability"},
			Permissions: domain.SoulPermissionSpec{
				AllowedKinds:   []int{domain.KindSoulAction},
				ToolGrants:     []domain.ToolGrant{{MCPServer: "draft-tool", Scopes: []string{"use"}}},
				ApprovalPolicy: "operator",
			},
			RelayPolicy: domain.SoulRelayPolicySpec{Control: []string{"wss://control.example"}},
			Workspace:   domain.SoulWorkspaceSpec{Repo: "https://git.example/scout.git", Branch: "main", Environment: "prod"},
			Assets:      domain.SoulAssetRefs{AvatarRef: "blob:avatar", VoiceRef: "blob:voice"},
		},
	}
	reactor.getDraftFn = func(_ context.Context, draftRef, draftEventID string) (*domain.SoulDraft, error) {
		if draftEventID != draft.EventID || draftRef != "31952:"+signer.pubkey+":scout" {
			t.Fatalf("draft lookup = (%q, %q), want exact event and coordinate", draftRef, draftEventID)
		}
		return draft, nil
	}
	reactor.getTemplateFn = func(_ context.Context, templateRef string) (*domain.SoulTemplate, error) {
		if templateRef != draft.TemplateRef {
			t.Fatalf("template ref = %q, want %q", templateRef, draft.TemplateRef)
		}
		return &domain.SoulTemplate{
			EventID:      "template-event",
			Identifier:   "agent-template",
			Name:         "Template Agent",
			Tier:         domain.SoulTierLightweight,
			BasePrompt:   "Template prompt",
			DefaultKinds: []int{1},
			DefaultTools: []domain.ToolGrant{{MCPServer: "template-tool", Scopes: []string{"read"}}},
		}, nil
	}
	runtime := &trackingRuntimeAdapter{}
	full := NewFullProvisioner(reactor, FullProvisionerConfig{RuntimeAdapters: map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime}}, nil)
	reactor.provisioner = full

	request := buildProvisioningEvent(
		t,
		signer.pubkey,
		"draft-runtime-provision",
		nostr.Tags{{"agent-id", "scout"}, {"name", "Inline Scout"}, {"tier", string(domain.SoulTierHeavy)}, {"draft", "31952:" + signer.pubkey + ":scout"}, {"draft-event", draft.EventID}, {"spec-hash", "sha256:draft"}},
		`{"brief":"Inline purpose"}`,
	)
	reactor.handleProvisioningRequest(t.Context(), request)

	if len(generator.inputs) != 1 {
		t.Fatalf("generator inputs = %d, want 1", len(generator.inputs))
	}
	input := generator.inputs[0]
	if input.Template == nil || input.Template.Identifier != "agent-template" || input.Name != "Inline Scout" || input.Brief != "Inline purpose" || input.Tier != domain.SoulTierHeavy {
		t.Fatalf("generator input not resolved from template+draft+inline: %+v", input)
	}
	if len(runtime.requests) != 1 {
		t.Fatalf("runtime requests = %d, want 1", len(runtime.requests))
	}
	runtimeReq := runtime.requests[0]
	if runtimeReq.Method != RuntimeMethodProvision || runtimeReq.Operator.RequestEvent != request.ID || runtimeReq.Soul.Draft != draft.EventID || runtimeReq.Soul.SpecHash != "sha256:draft" {
		t.Fatalf("runtime request missing correlated draft/spec context: %+v", runtimeReq)
	}
	if got := runtimeReq.Params["relay_policy"]; got == nil {
		t.Fatalf("runtime request missing relay policy params: %+v", runtimeReq.Params)
	}

	soulEvents := capture.eventsByKind(domain.KindAgentSoul)
	if len(soulEvents) != 1 {
		t.Fatalf("soul event count = %d, want exactly one final 31951", len(soulEvents))
	}
	soulEvent := soulEvents[0]
	for tagName, want := range map[string]string{
		"name":            "Inline Scout",
		"tier":            string(domain.SoulTierHeavy),
		"draft":           "31952:" + signer.pubkey + ":scout",
		"draft-event":     draft.EventID,
		"spec-hash":       "sha256:draft",
		"runtime":         string(domain.RuntimeTargetOpenClaw),
		"runtime-pubkey":  "runtime-pubkey",
		"runtime-binding": "openclaw://agents/scout",
		"runtime-state":   "running",
		"capability":      "capability-event",
		"approval-policy": "operator",
		"avatar-ref":      "blob:avatar",
		"voice-ref":       "blob:voice",
	} {
		if got := findTag(soulEvent, tagName); got != want {
			t.Fatalf("soul tag %s = %q, want %q; tags=%#v", tagName, got, want, soulEvent.Tags)
		}
	}
	if got := findTag(soulEvent, "allowed-kind"); got != fmt.Sprint(domain.KindSoulAction) {
		t.Fatalf("allowed-kind = %q, want draft permission", got)
	}

	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("result event count = %d, want 1", len(results))
	}
	if findTag(results[0], "runtime-binding") != "openclaw://agents/scout" || findTag(results[0], "draft-event") != draft.EventID {
		t.Fatalf("terminal result missing final runtime/draft context: %#v", results[0].Tags)
	}
	if !eventKindBefore(capture.events, domain.KindAgentSoul, domain.KindProvisioningResult) {
		t.Fatalf("final 31951 was not published before terminal 7950: %#v", capture.events)
	}
}

func TestDraftSpecHashMismatchFailsBeforeRuntimeProvisioning(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{signer.pubkey}}, &capturingGenerator{}, signer, slog.Default())
	capture := attachPublishCapture(reactor)
	draft := &domain.SoulDraft{
		EventID:   "mismatched-draft-event",
		AgentID:   "scout",
		CreatedBy: signer.pubkey,
		Content: domain.SoulDraftContent{
			Brief:    "Draft purpose",
			SpecHash: "sha256:draft",
			Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey"},
		},
	}
	reactor.getDraftFn = func(context.Context, string, string) (*domain.SoulDraft, error) { return draft, nil }
	runtime := &trackingRuntimeAdapter{}
	full := NewFullProvisioner(reactor, FullProvisionerConfig{RuntimeAdapters: map[domain.RuntimeTarget]RuntimeAdapter{domain.RuntimeTargetOpenClaw: runtime}}, nil)
	reactor.provisioner = full

	request := buildProvisioningEvent(t, signer.pubkey, "hash-mismatch", nostr.Tags{{"agent-id", "scout"}, {"draft-event", draft.EventID}, {"spec-hash", "sha256:request"}}, `{}`)
	reactor.handleProvisioningRequest(t.Context(), request)

	if len(runtime.requests) != 0 {
		t.Fatalf("runtime called despite spec hash mismatch: %+v", runtime.requests)
	}
	if len(capture.eventsByKind(domain.KindProvisioningStatus)) != 0 || len(capture.eventsByKind(domain.KindAgentSoul)) != 0 {
		t.Fatalf("published progress/soul before resolving spec mismatch: %#v", capture.events)
	}
	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 || !strings.Contains(results[0].Content, "does not match") {
		t.Fatalf("mismatch result = %#v", results)
	}
}

func eventKindBefore(events []*nostr.Event, firstKind, secondKind int) bool {
	firstIndex, secondIndex := -1, -1
	for i, event := range events {
		if event.Kind == firstKind && firstIndex == -1 {
			firstIndex = i
		}
		if event.Kind == secondKind && secondIndex == -1 {
			secondIndex = i
		}
	}
	return firstIndex >= 0 && secondIndex >= 0 && firstIndex < secondIndex
}

func TestSuccessfulProvisioningPublishesAuthoritativeSoulAndSuccessPayload(t *testing.T) {
	signer := newFakeSigner(t)
	oldPubkey := SoulFactoryPubkey
	SoulFactoryPubkey = signer.pubkey
	defer func() { SoulFactoryPubkey = oldPubkey }()

	reactor := NewReactor(
		Config{
			Relays:            []string{"wss://public.example"},
			AdditionalRelays:  []string{"wss://private.example"},
			AuthorizedPubkeys: []string{signer.pubkey},
			SoulFactoryPubkey: signer.pubkey,
		},
		scriptedGenerator{},
		signer,
		slog.Default(),
	)
	capture := attachPublishCapture(reactor)
	full := NewFullProvisioner(reactor, FullProvisionerConfig{}, nil)
	reactor.provisioner = full

	request := buildProvisioningEvent(
		t,
		signer.pubkey,
		"publish-contract",
		nostr.Tags{{"agent-id", "compass"}, {"name", "Compass"}},
		`{"brief":"Map operator state"}`,
	)
	reactor.handleProvisioningRequest(t.Context(), request)

	soulEvents := capture.eventsByKind(domain.KindAgentSoul)
	if len(soulEvents) != 1 {
		t.Fatalf("soul event count = %d, want 1", len(soulEvents))
	}
	soulEvent := soulEvents[0]
	if got := findTag(soulEvent, "d"); got != "compass" {
		t.Fatalf("soul d tag = %q, want compass", got)
	}
	if got := findTag(soulEvent, "name"); got != "Compass" {
		t.Fatalf("soul name tag = %q, want Compass", got)
	}
	if got := findTag(soulEvent, "status"); got != string(domain.SoulStatusActive) {
		t.Fatalf("soul status tag = %q, want active", got)
	}
	if got := findTag(soulEvent, "npub"); got == "" {
		t.Fatal("soul event missing npub tag")
	}
	if got := findTag(soulEvent, "p"); got != signer.pubkey {
		t.Fatalf("soul agent pubkey tag = %q, want signer pubkey", got)
	}
	if !strings.Contains(soulEvent.Content, "Map operator state") {
		t.Fatalf("soul content = %q, want generated brief", soulEvent.Content)
	}

	results := capture.eventsByKind(domain.KindProvisioningResult)
	if len(results) != 1 {
		t.Fatalf("result event count = %d, want 1", len(results))
	}
	result := results[0]
	if got := findTag(result, "soul"); got != fmt.Sprintf("31951:%s:compass", signer.pubkey) {
		t.Fatalf("result soul tag = %q, want 31951 coordinate", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("unmarshal result content: %v", err)
	}
	if got := payload["soul_id"]; got != "compass" {
		t.Fatalf("result soul_id = %#v, want compass", got)
	}
	if got := payload["pubkey"]; got != signer.pubkey {
		t.Fatalf("result pubkey = %#v, want signer pubkey", got)
	}
	if got := payload["npub"]; got == "" {
		t.Fatal("result payload missing npub")
	}
}
