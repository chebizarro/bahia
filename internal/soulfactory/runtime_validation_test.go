package soulfactory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type validationMemorySource map[string]*nostr.Event

func (s validationMemorySource) LoadEvents(_ context.Context, ids []string) (map[string]*nostr.Event, error) {
	out := make(map[string]*nostr.Event, len(ids))
	for _, id := range ids {
		if event := s[id]; event != nil {
			out[id] = event
		}
	}
	return out, nil
}

func TestValidateRuntimeScenarioWithCIDoubles(t *testing.T) {
	scenario, source := runtimeValidationFixture(t)
	report, err := ValidateRuntimeScenario(t.Context(), source, scenario)
	if err != nil {
		t.Fatalf("ValidateRuntimeScenario: %v; failures=%v", err, report.Failures)
	}
	if !report.Passed || len(report.EventIDs) == 0 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "content") || strings.Contains(string(encoded), "bunker://") {
		t.Fatalf("report is not sanitized: %s", encoded)
	}
}

func TestValidateRuntimeScenarioRejectsSecretAndNonExactlyOnceEvidence(t *testing.T) {
	scenario, source := runtimeValidationFixture(t)
	soul := *source[scenario.Events.Soul]
	soul.Content = "bunker://" + strings.Repeat("a", 64) + "?secret=must-not-appear"
	if err := (&fakeSigner{secret: validationControllerSecret, pubkey: scenario.ControllerPubkey}).Sign(t.Context(), &soul); err != nil {
		t.Fatal(err)
	}
	delete(source, scenario.Events.Soul)
	source[soul.ID.Hex()] = &soul
	scenario.Events.Soul = soul.ID.Hex()
	scenario.LocalState.ProvisionEffectsAfterReplay = 2
	report, err := ValidateRuntimeScenario(t.Context(), source, scenario)
	if err == nil || report.Passed || !containsFailure(report.Failures, "secret-shaped") || !containsFailure(report.Failures, "exactly once") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

var validationControllerSecret string

func runtimeValidationFixture(t *testing.T) (RuntimeValidationScenario, validationMemorySource) {
	t.Helper()
	controller := newFakeSigner(t)
	runtime := newFakeSigner(t)
	validationControllerSecret = controller.secret
	operator := newFakeSigner(t)
	agentID := "metiq-disposable"
	specHash := "sha256:" + strings.Repeat("1", 64)
	now := int64(nostr.Now())

	draft, err := BuildSoulDraftEvent(&domain.SoulDraft{AgentID: agentID, Name: "Metiq Disposable", Tier: domain.SoulTierStandard, Content: domain.SoulDraftContent{
		Schema: domain.SoulFactoryDraftSchemaV2, Brief: "Disposable Metiq validation soul",
		Identity: domain.SoulIdentitySpec{Name: "Metiq Disposable", Purpose: "Conformance", Tier: domain.SoulTierStandard},
		Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetMetiq, RuntimePubkey: runtime.pubkey}, SpecHash: specHash,
	}})
	if err != nil {
		t.Fatal(err)
	}
	signValidationEvent(t, operator, draft)

	request := &nostr.Event{Kind: nostr.Kind(domain.KindProvisioningRequest), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{tagAgentID, agentID}, {tagDraft, "31952:" + operator.pubkey + ":" + agentID}, {tagDraftEvent, draft.ID.Hex()},
		{tagRuntime, string(domain.RuntimeTargetMetiq)}, {tagRuntimePubkey, runtime.pubkey}, {tagSpecHash, specHash},
	}, Content: `{"brief":"Disposable Metiq validation soul"}`}
	signValidationEvent(t, operator, request)

	capabilityBefore := signedRuntimeCapabilityEventAt(t, runtime, now, map[string]interface{}{
		"schema": domain.SoulFactoryRuntimeCapabilitySchema, "runtime": string(domain.RuntimeTargetMetiq),
		"methods": []string{RuntimeMethodProvision, RuntimeMethodSuspend}, "control_schema": domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, "metiq-main"}, {tagRuntime, string(domain.RuntimeTargetMetiq)}})
	capabilityAfter := signedRuntimeCapabilityEventAt(t, runtime, now+1, map[string]interface{}{
		"schema": domain.SoulFactoryRuntimeCapabilitySchema, "runtime": string(domain.RuntimeTargetMetiq),
		"methods": []string{RuntimeMethodProvision, RuntimeMethodSuspend}, "control_schema": domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, "metiq-main"}, {tagRuntime, string(domain.RuntimeTargetMetiq)}})

	provisionControl := validationControl(t, controller, runtime.pubkey, agentID, request.ID.Hex(), RuntimeMethodProvision, "idem-provision", specHash)
	provisionRuntimeResult := validationResult(t, runtime, controller.pubkey, provisionControl, "success", nil)
	lifecycleControl := validationControl(t, controller, runtime.pubkey, agentID, strings.Repeat("2", 64), RuntimeMethodSuspend, "idem-suspend", specHash)
	lifecycleRuntimeResult := validationResult(t, runtime, controller.pubkey, lifecycleControl, "success", nil)
	conflictControl := validationControl(t, controller, runtime.pubkey, agentID, strings.Repeat("3", 64), RuntimeMethodProvision, "idem-provision", "sha256:"+strings.Repeat("4", 64))
	conflictRuntimeResult := validationResult(t, runtime, controller.pubkey, conflictControl, "rejected", &RuntimeControlError{Code: "duplicate_conflict", Message: "conflict", Retryable: false})
	unsupportedControl := validationControl(t, controller, runtime.pubkey, agentID, strings.Repeat("5", 64), RuntimeMethodResume, "idem-unsupported", specHash)
	unsupportedRuntimeResult := validationResult(t, runtime, controller.pubkey, unsupportedControl, "rejected", &RuntimeControlError{Code: "unsupported_method", Message: "unsupported", Retryable: false})

	soul := &domain.AgentSoul{AgentID: agentID, Name: "Metiq Disposable", Purpose: "Conformance", Tier: domain.SoulTierStandard,
		Status: domain.SoulStatusActive, NostrPubkey: operator.pubkey, DraftRef: "31952:" + operator.pubkey + ":" + agentID,
		DraftEventID: draft.ID.Hex(), SpecHash: specHash, Runtime: domain.SoulRuntimeSpec{Target: domain.RuntimeTargetMetiq, RuntimePubkey: runtime.pubkey, RuntimeBinding: "metiq://" + agentID, State: "running"},
	}
	soulEvent := BuildAgentSoulEvent(soul)
	signValidationEvent(t, controller, soulEvent)
	provisionResult, err := BuildProvisioningSuccessResultEvent(request, soul, controller.pubkey)
	if err != nil {
		t.Fatal(err)
	}
	signValidationEvent(t, controller, provisionResult)

	events := []*nostr.Event{draft, request, capabilityBefore, provisionControl, provisionRuntimeResult, soulEvent, provisionResult, lifecycleControl, lifecycleRuntimeResult, conflictControl, conflictRuntimeResult, unsupportedControl, unsupportedRuntimeResult, capabilityAfter}
	source := validationMemorySource{}
	for _, event := range events {
		source[event.ID.Hex()] = event
	}
	scenario := RuntimeValidationScenario{
		Schema: RuntimeValidationScenarioSchema, Runtime: domain.RuntimeTargetMetiq, AgentID: agentID,
		ControllerPubkey: controller.pubkey, RuntimePubkey: runtime.pubkey, LifecycleMethod: RuntimeMethodSuspend,
		Events: RuntimeValidationEventIDs{
			Draft: draft.ID.Hex(), ProvisionRequest: request.ID.Hex(), CapabilityBefore: capabilityBefore.ID.Hex(),
			ProvisionControl: provisionControl.ID.Hex(), ProvisionRuntimeResult: provisionRuntimeResult.ID.Hex(), Soul: soulEvent.ID.Hex(), ProvisionResult: provisionResult.ID.Hex(),
			LifecycleControl: lifecycleControl.ID.Hex(), LifecycleRuntimeResult: lifecycleRuntimeResult.ID.Hex(), ReplayRuntimeResult: provisionRuntimeResult.ID.Hex(),
			ConflictControl: conflictControl.ID.Hex(), ConflictRuntimeResult: conflictRuntimeResult.ID.Hex(), UnsupportedControl: unsupportedControl.ID.Hex(), UnsupportedRuntimeResult: unsupportedRuntimeResult.ID.Hex(),
			CapabilityAfterRestart: capabilityAfter.ID.Hex(), ReconciledResult: provisionResult.ID.Hex(),
		},
		LocalState: RuntimeLocalStateEvidence{
			ProvisionEffectsAfterFirst: 1, ProvisionEffectsAfterReplay: 1, ProvisionEffectsAfterConflict: 1, ProvisionEffectsAfterRestart: 1,
			LifecycleEffectsBefore: 0, LifecycleEffectsAfterHonored: 1, LifecycleEffectsAfterUnsupported: 1,
			BindingBeforeRestart: "sha256:" + strings.Repeat("6", 64), BindingAfterRestart: "sha256:" + strings.Repeat("6", 64), StateRecovered: true, Reconciliation: "backfill",
			RuntimeInstanceBeforeRestart: "sha256:" + strings.Repeat("d", 64), RuntimeInstanceAfterRestart: "sha256:" + strings.Repeat("e", 64),
			BahiaInstanceBeforeRestart: "sha256:" + strings.Repeat("f", 64), BahiaInstanceAfterRestart: "sha256:" + strings.Repeat("0", 64),
		},
		Incumbents: []RuntimeIncumbentEvidence{
			{Name: "Marjam", IdentityPubkey: operator.pubkey, RuntimePubkey: controller.pubkey, BeforeEventID: strings.Repeat("7", 64), AfterEventID: strings.Repeat("7", 64), BeforeFingerprint: "sha256:" + strings.Repeat("8", 64), AfterFingerprint: "sha256:" + strings.Repeat("8", 64)},
			{Name: "SNR", IdentityPubkey: controller.pubkey, RuntimePubkey: operator.pubkey, BeforeEventID: strings.Repeat("9", 64), AfterEventID: strings.Repeat("9", 64), BeforeFingerprint: "sha256:" + strings.Repeat("a", 64), AfterFingerprint: "sha256:" + strings.Repeat("a", 64)},
		},
		Rollback: RuntimeRollbackEvidence{PriorConfigDigest: "sha256:" + strings.Repeat("b", 64), EnabledConfigDigest: "sha256:" + strings.Repeat("c", 64), RestoredConfigDigest: "sha256:" + strings.Repeat("b", 64), Rehearsed: true},
	}
	return scenario, source
}

func validationControl(t *testing.T, controller fakeSigner, runtimePubkey, agentID, operatorRequest, method, idempotencyKey, specHash string) *nostr.Event {
	t.Helper()
	event, err := BuildRuntimeControlRequestEvent(RuntimeControlEnvelope{
		Schema: domain.SoulFactoryRuntimeControlSchema, Method: method, IdempotencyKey: idempotencyKey, RequestedAt: time.Now().Unix(),
		Operator: RuntimeOperatorRef{Pubkey: controller.pubkey, RequestEvent: operatorRequest}, Controller: RuntimeControllerRef{Pubkey: controller.pubkey},
		Target: RuntimeTargetRef{Runtime: domain.RuntimeTargetMetiq, RuntimePubkey: runtimePubkey, AgentID: agentID},
		Soul:   RuntimeSoulRef{ID: agentID, Draft: "draft", SpecHash: specHash}, Params: map[string]interface{}{"validation": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	signValidationEvent(t, controller, event)
	return event
}

func validationResult(t *testing.T, runtime fakeSigner, controllerPubkey string, request *nostr.Event, status string, resultErr *RuntimeControlError) *nostr.Event {
	t.Helper()
	envelope, err := ParseRuntimeControlRequestEvent(request)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(RuntimeControlResultEnvelope{
		Schema: domain.SoulFactoryRuntimeControlSchema, Method: envelope.Method, IdempotencyKey: envelope.IdempotencyKey,
		RequestEvent: request.ID.Hex(), OperatorRequestEvent: envelope.Operator.RequestEvent, Status: status,
		Result: map[string]interface{}{"state": "running"}, Error: resultErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeControlResult), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{tagPubkey, controllerPubkey}, {tagEvent, request.ID.Hex()}, {tagMethod, envelope.Method}, {tagIdempotencyKey, envelope.IdempotencyKey},
		{tagAgentID, envelope.Target.AgentID}, {tagSoul, envelope.Soul.ID}, {tagSpecHash, envelope.Soul.SpecHash}, {tagSchema, domain.SoulFactoryRuntimeControlSchema}, {tagStatus, status},
	}, Content: string(content)}
	signValidationEvent(t, runtime, event)
	return event
}

func signValidationEvent(t *testing.T, signer fakeSigner, event *nostr.Event) {
	t.Helper()
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatal(err)
	}
}

func containsFailure(failures []string, substring string) bool {
	for _, failure := range failures {
		if strings.Contains(failure, substring) {
			return true
		}
	}
	return false
}
