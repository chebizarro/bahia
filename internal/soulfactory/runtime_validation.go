package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

const RuntimeValidationScenarioSchema = "bahia.runtime-validation-scenario.v1"
const RuntimeValidationReportSchema = "bahia.runtime-validation-report.v1"

type RuntimeValidationEventSource interface {
	LoadEvents(context.Context, []string) (map[string]*nostr.Event, error)
}

type RelayRuntimeValidationEventSource struct {
	bus *SoulFactoryRelayBus
}

func NewRelayRuntimeValidationEventSource(relays []string) (*RelayRuntimeValidationEventSource, error) {
	bus, err := NewSoulFactoryRelayBus(relays)
	if err != nil {
		return nil, err
	}
	return &RelayRuntimeValidationEventSource{bus: bus}, nil
}

func (s *RelayRuntimeValidationEventSource) Close() {
	if s != nil && s.bus != nil {
		s.bus.Close()
	}
}

func (s *RelayRuntimeValidationEventSource) LoadEvents(ctx context.Context, eventIDs []string) (map[string]*nostr.Event, error) {
	ids := make([]nostr.ID, 0, len(eventIDs))
	for _, raw := range uniqueStrings(eventIDs) {
		id, err := nostr.IDFromHex(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid event id %q: %w", raw, err)
		}
		ids = append(ids, id)
	}
	sub, err := s.bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{IDs: ids, Limit: len(ids)}})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	events := make(map[string]*nostr.Event, len(ids))
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-sub.EndOfStoredEvents:
			return events, nil
		case event, ok := <-sub.Events:
			if !ok {
				return events, nil
			}
			if event != nil {
				events[event.ID.Hex()] = event
			}
		}
	}
}

type RuntimeValidationEventIDs struct {
	Draft                    string `json:"draft_31952"`
	ProvisionRequest         string `json:"provision_request_5950"`
	CapabilityBefore         string `json:"capability_before_30317"`
	ProvisionControl         string `json:"provision_control_38384"`
	ProvisionRuntimeResult   string `json:"provision_runtime_result_38386"`
	Soul                     string `json:"soul_31951"`
	ProvisionResult          string `json:"provision_result_7950"`
	LifecycleControl         string `json:"lifecycle_control_38384"`
	LifecycleRuntimeResult   string `json:"lifecycle_runtime_result_38386"`
	ReplayRuntimeResult      string `json:"replay_runtime_result_38386"`
	ConflictControl          string `json:"conflict_control_38384"`
	ConflictRuntimeResult    string `json:"conflict_runtime_result_38386"`
	UnsupportedControl       string `json:"unsupported_control_38384"`
	UnsupportedRuntimeResult string `json:"unsupported_runtime_result_38386"`
	CapabilityAfterRestart   string `json:"capability_after_restart_30317"`
	ReconciledResult         string `json:"reconciled_result_7950"`
}

func (ids RuntimeValidationEventIDs) all() []string {
	return []string{
		ids.Draft, ids.ProvisionRequest, ids.CapabilityBefore, ids.ProvisionControl,
		ids.ProvisionRuntimeResult, ids.Soul, ids.ProvisionResult, ids.LifecycleControl,
		ids.LifecycleRuntimeResult, ids.ReplayRuntimeResult, ids.ConflictControl,
		ids.ConflictRuntimeResult, ids.UnsupportedControl, ids.UnsupportedRuntimeResult,
		ids.CapabilityAfterRestart, ids.ReconciledResult,
	}
}

type RuntimeLocalStateEvidence struct {
	ProvisionEffectsAfterFirst       int    `json:"provision_effects_after_first"`
	ProvisionEffectsAfterReplay      int    `json:"provision_effects_after_replay"`
	ProvisionEffectsAfterConflict    int    `json:"provision_effects_after_conflict"`
	ProvisionEffectsAfterRestart     int    `json:"provision_effects_after_restart"`
	LifecycleEffectsBefore           int    `json:"lifecycle_effects_before"`
	LifecycleEffectsAfterHonored     int    `json:"lifecycle_effects_after_honored"`
	LifecycleEffectsAfterUnsupported int    `json:"lifecycle_effects_after_unsupported"`
	BindingBeforeRestart             string `json:"binding_before_restart"`
	BindingAfterRestart              string `json:"binding_after_restart"`
	RuntimeInstanceBeforeRestart     string `json:"runtime_instance_before_restart"`
	RuntimeInstanceAfterRestart      string `json:"runtime_instance_after_restart"`
	BahiaInstanceBeforeRestart       string `json:"bahia_instance_before_restart"`
	BahiaInstanceAfterRestart        string `json:"bahia_instance_after_restart"`
	StateRecovered                   bool   `json:"state_recovered"`
	Reconciliation                   string `json:"reconciliation"`
}

type RuntimeIncumbentEvidence struct {
	Name              string `json:"name"`
	IdentityPubkey    string `json:"identity_pubkey"`
	RuntimePubkey     string `json:"runtime_pubkey"`
	BeforeEventID     string `json:"before_event_id"`
	AfterEventID      string `json:"after_event_id"`
	BeforeFingerprint string `json:"before_fingerprint"`
	AfterFingerprint  string `json:"after_fingerprint"`
}

type RuntimeRollbackEvidence struct {
	PriorConfigDigest    string `json:"prior_config_digest"`
	EnabledConfigDigest  string `json:"enabled_config_digest"`
	RestoredConfigDigest string `json:"restored_config_digest"`
	Rehearsed            bool   `json:"rehearsed"`
}

type RuntimeValidationScenario struct {
	Schema           string                     `json:"schema"`
	Runtime          domain.RuntimeTarget       `json:"runtime"`
	AgentID          string                     `json:"agent_id"`
	ControllerPubkey string                     `json:"controller_pubkey"`
	RuntimePubkey    string                     `json:"runtime_pubkey"`
	LifecycleMethod  string                     `json:"lifecycle_method"`
	Events           RuntimeValidationEventIDs  `json:"events"`
	LocalState       RuntimeLocalStateEvidence  `json:"local_state"`
	Incumbents       []RuntimeIncumbentEvidence `json:"incumbents"`
	Rollback         RuntimeRollbackEvidence    `json:"rollback"`
}

type RuntimeValidationReport struct {
	Schema   string   `json:"schema"`
	Passed   bool     `json:"passed"`
	Checks   []string `json:"checks"`
	EventIDs []string `json:"event_ids"`
	Failures []string `json:"failures,omitempty"`
}

func ValidateRuntimeScenario(ctx context.Context, source RuntimeValidationEventSource, scenario RuntimeValidationScenario) (RuntimeValidationReport, error) {
	report := RuntimeValidationReport{Schema: RuntimeValidationReportSchema}
	fail := func(format string, args ...interface{}) {
		report.Failures = append(report.Failures, fmt.Sprintf(format, args...))
	}
	if scenario.Schema != RuntimeValidationScenarioSchema {
		fail("scenario schema must be %s", RuntimeValidationScenarioSchema)
	}
	if scenario.Runtime != domain.RuntimeTargetMetiq {
		fail("runtime must be metiq")
	}
	if scenario.AgentID == "" || !isHexPubkey(scenario.ControllerPubkey) || !isHexPubkey(scenario.RuntimePubkey) || scenario.ControllerPubkey == scenario.RuntimePubkey {
		fail("agent id and exact controller/runtime pubkeys are required")
	}
	for _, id := range scenario.Events.all() {
		if _, err := nostr.IDFromHex(id); err != nil {
			fail("invalid or missing event id %q", id)
		}
	}
	if len(report.Failures) > 0 {
		return report, fmt.Errorf("runtime validation scenario is invalid")
	}
	events, err := source.LoadEvents(ctx, scenario.Events.all())
	if err != nil {
		return report, fmt.Errorf("load runtime validation events: %w", err)
	}
	get := func(id string, kind int, author string) *nostr.Event {
		event := events[id]
		if event == nil {
			fail("event %s was not found", id)
			return nil
		}
		if event.ID.Hex() != id || event.Kind != nostr.Kind(kind) || !validSignedEvent(event) {
			fail("event %s failed id/signature/kind validation", id)
			return nil
		}
		if author != "" && event.PubKey.Hex() != author {
			fail("event %s author %s does not match %s", id, event.PubKey.Hex(), author)
		}
		return event
	}

	draftEvent := get(scenario.Events.Draft, domain.KindSoulDraft, "")
	requestEvent := get(scenario.Events.ProvisionRequest, domain.KindProvisioningRequest, "")
	capabilityBefore := get(scenario.Events.CapabilityBefore, domain.KindRuntimeCapability, scenario.RuntimePubkey)
	provisionControl := get(scenario.Events.ProvisionControl, domain.KindRuntimeControlRequest, scenario.ControllerPubkey)
	provisionRuntimeResult := get(scenario.Events.ProvisionRuntimeResult, domain.KindRuntimeControlResult, scenario.RuntimePubkey)
	soulEvent := get(scenario.Events.Soul, domain.KindAgentSoul, scenario.ControllerPubkey)
	provisionResult := get(scenario.Events.ProvisionResult, domain.KindProvisioningResult, scenario.ControllerPubkey)
	lifecycleControl := get(scenario.Events.LifecycleControl, domain.KindRuntimeControlRequest, scenario.ControllerPubkey)
	lifecycleResult := get(scenario.Events.LifecycleRuntimeResult, domain.KindRuntimeControlResult, scenario.RuntimePubkey)
	replayResult := get(scenario.Events.ReplayRuntimeResult, domain.KindRuntimeControlResult, scenario.RuntimePubkey)
	conflictControl := get(scenario.Events.ConflictControl, domain.KindRuntimeControlRequest, scenario.ControllerPubkey)
	conflictResult := get(scenario.Events.ConflictRuntimeResult, domain.KindRuntimeControlResult, scenario.RuntimePubkey)
	unsupportedControl := get(scenario.Events.UnsupportedControl, domain.KindRuntimeControlRequest, scenario.ControllerPubkey)
	unsupportedResult := get(scenario.Events.UnsupportedRuntimeResult, domain.KindRuntimeControlResult, scenario.RuntimePubkey)
	capabilityAfter := get(scenario.Events.CapabilityAfterRestart, domain.KindRuntimeCapability, scenario.RuntimePubkey)
	reconciledResult := get(scenario.Events.ReconciledResult, domain.KindProvisioningResult, scenario.ControllerPubkey)

	if draftEvent != nil {
		draft, parseErr := ParseSoulDraftEvent(draftEvent)
		if parseErr != nil || draft.AgentID != scenario.AgentID || draft.Content.Runtime.Target != scenario.Runtime {
			fail("31952 does not select Metiq for agent %s", scenario.AgentID)
		}
	}
	if requestEvent != nil {
		req, parseErr := ParseProvisioningRequestEvent(requestEvent)
		if parseErr != nil || req.AgentID != scenario.AgentID || req.Runtime.Target != scenario.Runtime || req.DraftEventID != scenario.Events.Draft {
			fail("5950 does not preserve the Metiq 31952 selection")
		}
	}
	capBefore, capBeforeOK := ParseRuntimeCapabilityEvent(capabilityBefore)
	capAfter, capAfterOK := ParseRuntimeCapabilityEvent(capabilityAfter)
	if !capBeforeOK || !capBefore.Supports(scenario.Runtime, RuntimeMethodProvision, scenario.ControllerPubkey) || capBefore.Pubkey != scenario.RuntimePubkey {
		fail("pre-restart 30317 is not a compatible trusted Metiq capability")
	}
	lifecycleMethods := []string{RuntimeMethodUpdate, RuntimeMethodSuspend, RuntimeMethodResume, RuntimeMethodRedeploy, RuntimeMethodRevoke}
	advertisedLifecycle := 0
	for _, method := range lifecycleMethods {
		if stringInSlice(method, capBefore.Methods) {
			advertisedLifecycle++
		}
	}
	if advertisedLifecycle != 1 || !stringInSlice(scenario.LifecycleMethod, capBefore.Methods) {
		fail("30317 must advertise exactly the selected lifecycle method")
	}
	if !capAfterOK || capAfter.Pubkey != scenario.RuntimePubkey || capAfter.Runtime != scenario.Runtime || capAfter.ID == capBefore.ID || !capAfter.CreatedAt.After(capBefore.CreatedAt) {
		fail("restart did not republish a newer signed 30317 for the same Metiq identity")
	}

	provisionEnvelope := validateControlLineage(provisionControl, provisionRuntimeResult, scenario, RuntimeMethodProvision, fail)
	validateControlLineage(lifecycleControl, lifecycleResult, scenario, scenario.LifecycleMethod, fail)
	if parsed, ok := parseRuntimeControlResultEvent(provisionRuntimeResult); !ok || parsed.Status != "success" {
		fail("Metiq provisioning 38386 is not successful")
	}
	if parsed, ok := parseRuntimeControlResultEvent(lifecycleResult); !ok || parsed.Status != "success" {
		fail("advertised lifecycle method was not honored")
	}
	if parsed, ok := parseRuntimeControlResultEvent(replayResult); !ok || provisionEnvelope == nil || parsed.IdempotencyKey != provisionEnvelope.IdempotencyKey || parsed.Status != "success" {
		fail("exact replay did not return the successful cached logical outcome")
	}
	conflictEnvelope := validateControlLineage(conflictControl, conflictResult, scenario, "", fail)
	if parsed, ok := parseRuntimeControlResultEvent(conflictResult); !ok || parsed.Status != "rejected" || parsed.Error == nil || parsed.Error.Code != "duplicate_conflict" || parsed.Error.Retryable {
		fail("conflicting replay did not fail closed with non-retryable duplicate_conflict")
	}
	if provisionEnvelope != nil && conflictEnvelope != nil {
		if provisionEnvelope.IdempotencyKey != conflictEnvelope.IdempotencyKey {
			fail("duplicate_conflict request did not reuse the original idempotency key")
		}
		if provisionControl.ID == conflictControl.ID || (provisionEnvelope.Method == conflictEnvelope.Method && provisionEnvelope.Soul.SpecHash == conflictEnvelope.Soul.SpecHash && provisionEnvelope.Operator.RequestEvent == conflictEnvelope.Operator.RequestEvent) {
			fail("duplicate_conflict request did not change bound input")
		}
	}
	unsupportedEnvelope := validateControlLineage(unsupportedControl, unsupportedResult, scenario, "", fail)
	if unsupportedEnvelope != nil && stringInSlice(unsupportedEnvelope.Method, capBefore.Methods) {
		fail("unsupported-method probe used an advertised method")
	}
	if parsed, ok := parseRuntimeControlResultEvent(unsupportedResult); !ok || parsed.Status != "rejected" || parsed.Error == nil || parsed.Error.Code != "unsupported_method" || parsed.Error.Retryable {
		fail("unsupported method did not fail closed")
	}

	if provisionResult != nil && tagValue(provisionResult.Tags, tagEvent) != scenario.Events.ProvisionRequest {
		fail("7950 is not correlated to the 5950")
	}
	if reconciledResult != nil && tagValue(reconciledResult.Tags, tagEvent) != scenario.Events.ProvisionRequest {
		fail("reconciled 7950 is not correlated to the original 5950")
	}
	if soulEvent != nil && (tagValue(soulEvent.Tags, tagParameterizedD) != scenario.AgentID || tagValue(soulEvent.Tags, tagRuntime) != string(scenario.Runtime) || tagValue(soulEvent.Tags, tagRuntimePubkey) != scenario.RuntimePubkey) {
		fail("31951 does not preserve Metiq runtime identity and agent lineage")
	}
	for _, event := range []*nostr.Event{soulEvent, provisionResult, reconciledResult} {
		if event != nil && containsPublicSecret(event) {
			fail("public event %s contains secret-shaped material", event.ID.Hex())
		}
	}

	state := scenario.LocalState
	if state.ProvisionEffectsAfterFirst != 1 || state.ProvisionEffectsAfterReplay != 1 || state.ProvisionEffectsAfterConflict != 1 || state.ProvisionEffectsAfterRestart != 1 {
		fail("local Metiq provisioning was not exactly once across replay/conflict/restart")
	}
	if state.LifecycleEffectsAfterHonored != state.LifecycleEffectsBefore+1 || state.LifecycleEffectsAfterUnsupported != state.LifecycleEffectsAfterHonored {
		fail("lifecycle method effects do not prove one honored and unsupported no-op")
	}
	if !validSHA256Digest(state.BindingBeforeRestart) || state.BindingBeforeRestart != state.BindingAfterRestart || !state.StateRecovered {
		fail("runtime binding/state was not recovered across restart")
	}
	if !validSHA256Digest(state.RuntimeInstanceBeforeRestart) || !validSHA256Digest(state.RuntimeInstanceAfterRestart) || state.RuntimeInstanceBeforeRestart == state.RuntimeInstanceAfterRestart {
		fail("Metiq runtime restart was not proven by distinct instance fingerprints")
	}
	if !validSHA256Digest(state.BahiaInstanceBeforeRestart) || !validSHA256Digest(state.BahiaInstanceAfterRestart) || state.BahiaInstanceBeforeRestart == state.BahiaInstanceAfterRestart {
		fail("Bahia restart was not proven by distinct instance fingerprints")
	}
	if state.Reconciliation != "backfill" && state.Reconciliation != "late_result" {
		fail("reconciliation must prove either backfill or late_result")
	}

	incumbents := map[string]bool{"Marjam": false, "SNR": false}
	for _, incumbent := range scenario.Incumbents {
		if _, required := incumbents[incumbent.Name]; required {
			incumbents[incumbent.Name] = true
		}
		if !isHexPubkey(incumbent.IdentityPubkey) || !isHexPubkey(incumbent.RuntimePubkey) || incumbent.IdentityPubkey == scenario.RuntimePubkey || incumbent.RuntimePubkey == scenario.RuntimePubkey {
			fail("incumbent %s is not identity-isolated from Metiq", incumbent.Name)
		}
		_, eventIDErr := nostr.IDFromHex(incumbent.BeforeEventID)
		if eventIDErr != nil || incumbent.BeforeEventID != incumbent.AfterEventID || !validSHA256Digest(incumbent.BeforeFingerprint) || incumbent.BeforeFingerprint != incumbent.AfterFingerprint {
			fail("incumbent %s changed during Metiq validation", incumbent.Name)
		}
	}
	for name, present := range incumbents {
		if !present {
			fail("missing %s regression evidence", name)
		}
	}
	rollback := scenario.Rollback
	if !rollback.Rehearsed || !validSHA256Digest(rollback.PriorConfigDigest) || rollback.PriorConfigDigest != rollback.RestoredConfigDigest || rollback.EnabledConfigDigest == rollback.PriorConfigDigest || !validSHA256Digest(rollback.EnabledConfigDigest) {
		fail("rollback evidence does not restore the captured prior config digest")
	}

	report.EventIDs = uniqueStrings(scenario.Events.all())
	sort.Strings(report.EventIDs)
	report.Checks = []string{
		"metiq_selection_and_lineage", "signed_addressed_runtime_control", "exactly_once_and_conflict",
		"lifecycle_and_unsupported_fail_closed", "secret_free_public_projection", "restart_and_reconciliation",
		"openclaw_incumbent_regression", "captured_digest_rollback",
	}
	report.Passed = len(report.Failures) == 0
	if !report.Passed {
		return report, fmt.Errorf("runtime validation failed: %s", strings.Join(report.Failures, "; "))
	}
	return report, nil
}

func validateControlLineage(request *nostr.Event, result *nostr.Event, scenario RuntimeValidationScenario, expectedMethod string, fail func(string, ...interface{})) *RuntimeControlEnvelope {
	if request == nil || result == nil {
		return nil
	}
	envelope, err := ParseRuntimeControlRequestEvent(request)
	if err != nil {
		fail("parse control request %s: %v", request.ID.Hex(), err)
		return nil
	}
	if expectedMethod != "" && envelope.Method != expectedMethod {
		fail("control request %s method %s does not match %s", request.ID.Hex(), envelope.Method, expectedMethod)
	}
	if envelope.Target.Runtime != scenario.Runtime || envelope.Target.RuntimePubkey != scenario.RuntimePubkey || envelope.Target.AgentID != scenario.AgentID || envelope.Controller.Pubkey != scenario.ControllerPubkey || tagValue(request.Tags, tagPubkey) != scenario.RuntimePubkey {
		fail("control request %s is not addressed and bound to the exact Metiq identity", request.ID.Hex())
	}
	parsedResult, ok := parseRuntimeControlResultEvent(result)
	if !ok || parsedResult.RequestEvent != request.ID.Hex() || parsedResult.IdempotencyKey != envelope.IdempotencyKey || tagValue(result.Tags, tagPubkey) != scenario.ControllerPubkey {
		fail("38386 %s is not signed and correlated to 38384 %s", result.ID.Hex(), request.ID.Hex())
	}
	return envelope
}

func containsPublicSecret(event *nostr.Event) bool {
	encodedTags, _ := json.Marshal(event.Tags)
	text := strings.ToLower(event.Content + " " + string(encodedTags))
	for _, marker := range []string{"nsec1", "bunker://", "secret=", "private_key", "private-key", "client_secret", "client-secret"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func validSHA256Digest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := nostr.IDFromHex(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
