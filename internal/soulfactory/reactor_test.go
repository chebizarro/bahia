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
