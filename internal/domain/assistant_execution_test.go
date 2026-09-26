package domain

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAssistantBatchApprovalGoldenVectors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "assistant", "batch_approval_hash_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema  string `json:"schema"`
		Vectors []struct {
			Name      string                          `json:"name"`
			Input     AssistantBatchApprovalHashInput `json:"input"`
			Canonical string                          `json:"canonical"`
			SHA256    string                          `json:"sha256"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != "bahia.assistant-batch-approval-hash.v1" || len(fixture.Vectors) != 5 {
		t.Fatalf("unexpected fixture: %s %d", fixture.Schema, len(fixture.Vectors))
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			canonical, err := vector.Input.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != vector.Canonical {
				t.Fatalf("canonical mismatch\ngot:  %s\nwant: %s", canonical, vector.Canonical)
			}
			hash, err := ComputeAssistantBatchApprovalHash(vector.Input)
			if err != nil || hash != vector.SHA256 {
				t.Fatalf("hash=%q, err=%v, want %q", hash, err, vector.SHA256)
			}
		})
	}
	if fixture.Vectors[0].SHA256 == fixture.Vectors[1].SHA256 {
		t.Fatal("reordering must change approval hash")
	}
}

func TestAssistantAllowedToolsRoundTripAndDeepClone(t *testing.T) {
	for _, allowed := range [][]string{nil, {}} {
		original := AssistantExecution{Workflow: AssistantWorkflowBatch, Phase: AssistantExecutionAwaitingApproval, Scope: AssistantCommandScope{AllowedTools: allowed, Arguments: map[string]any{"nested": map[string]any{"list": []any{map[string]any{"value": "before"}}}}}, Work: []AssistantWorkItem{{State: AssistantWorkPending, Arguments: map[string]any{"nested": []any{map[string]any{"value": "before"}}}}}}
		copy, err := original.Clone()
		if err != nil {
			t.Fatal(err)
		}
		serialized, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		if allowed == nil && !strings.Contains(string(serialized), `"allowed_tools":null`) {
			t.Fatalf("nil scope lost: %s", serialized)
		}
		if allowed != nil && !strings.Contains(string(serialized), `"allowed_tools":[]`) {
			t.Fatalf("empty scope lost: %s", serialized)
		}
		if (copy.Scope.AllowedTools == nil) != (allowed == nil) {
			t.Fatalf("scope changed nilness: %#v", copy.Scope.AllowedTools)
		}
		original.Scope.Arguments["nested"].(map[string]any)["list"].([]any)[0].(map[string]any)["value"] = "after"
		original.Work[0].Arguments["nested"].([]any)[0].(map[string]any)["value"] = "after"
		if reflect.DeepEqual(original.Scope.Arguments, copy.Scope.Arguments) || reflect.DeepEqual(original.Work, copy.Work) {
			t.Fatal("clone aliases nested values")
		}
	}
}

func TestAssistantExecutionEnumsAndPlanValidation(t *testing.T) {
	if AssistantWorkflow("anything").Valid() || AssistantExecutionPhase("anything").Valid() || AssistantWorkState("anything").Valid() {
		t.Fatal("open enum")
	}
	if !AssistantWorkflowBatch.Valid() || !AssistantExecutionWaitingAsync.Valid() || !AssistantWorkUncertain.Valid() {
		t.Fatal("valid enum rejected")
	}
	plan := AssistantPlan{Steps: []AssistantPlanStep{{StepID: "one", ToolName: "tool", ToolArgs: map[string]any{}}, {StepID: "one", ToolName: "tool", ToolArgs: map[string]any{}}}}
	if _, err := NormalizeAssistantExecutablePlan(plan); err == nil {
		t.Fatal("duplicate step accepted")
	}
	plan.Steps[1].StepID = "two"
	plan.Steps[1].ToolArgs = nil
	if _, err := NormalizeAssistantExecutablePlan(plan); err == nil {
		t.Fatal("non-object arguments accepted")
	}
	plan.Steps[1].ToolArgs = map[string]any{}
	plan.Steps[0].ArgsPreview = map[string]any{"secret": "preview"}
	plan.Steps[0].IdempotencyKey = "model-key"
	out, err := NormalizeAssistantExecutablePlan(plan)
	if err != nil || out.Steps[0].ArgsPreview != nil || out.Steps[0].IdempotencyKey != "" {
		t.Fatalf("derived fields retained: %#v %v", out.Steps[0], err)
	}
}

func TestDecodeAssistantExecutablePlanStrict(t *testing.T) {
	for _, raw := range []string{
		`{"summary":"x","needs_clarification":false,"risk_level":"low","steps":[{"step_id":"s","tool_name":"tool","tool_args":{},"unknown":true}]}`,
		`{"summary":"x","needs_clarification":false,"risk_level":"low","steps":[{"step_id":"s","tool_name":"tool","tool_args":[]}]}`,
		`{"summary":"x","needs_clarification":false,"risk_level":"low","steps":[]} {}`,
	} {
		if _, err := DecodeAssistantExecutablePlan([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed plan: %s", raw)
		}
	}
}

func TestAssistantExecutionEnumJSONRejectsUnknown(t *testing.T) {
	for _, target := range []any{new(AssistantWorkflow), new(AssistantExecutionPhase), new(AssistantWorkState)} {
		if err := json.Unmarshal([]byte(`"unknown"`), target); err == nil {
			t.Fatalf("unknown enum accepted by %T", target)
		}
		if err := json.Unmarshal([]byte(`null`), target); err == nil {
			t.Fatalf("null enum accepted by %T", target)
		}
	}
}

func TestAssistantJCSRFC8785NumberSamples(t *testing.T) {
	cases := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, `{"n":0}`},
		{0x8000000000000000, `{"n":0}`},
		{0x0000000000000001, `{"n":5e-324}`},
		{0x8000000000000001, `{"n":-5e-324}`},
		{0x7fefffffffffffff, `{"n":1.7976931348623157e+308}`},
		{0x4340000000000000, `{"n":9007199254740992}`},
		{0x4430000000000000, `{"n":295147905179352830000}`},
		{0x44b52d02c7e14af5, `{"n":9.999999999999997e+22}`},
		{0x44b52d02c7e14af6, `{"n":1e+23}`},
		{0x44b52d02c7e14af7, `{"n":1.0000000000000001e+23}`},
		{0x444b1ae4d6e2ef4e, `{"n":999999999999999700000}`},
		{0x444b1ae4d6e2ef4f, `{"n":999999999999999900000}`},
		{0x444b1ae4d6e2ef50, `{"n":1e+21}`},
		{0x3eb0c6f7a0b5ed8c, `{"n":9.999999999999997e-7}`},
		{0x3eb0c6f7a0b5ed8d, `{"n":0.000001}`},
		{0x41b3de4355555553, `{"n":333333333.3333332}`},
		{0x41b3de4355555554, `{"n":333333333.33333325}`},
		{0x41b3de4355555555, `{"n":333333333.3333333}`},
		{0x41b3de4355555556, `{"n":333333333.3333334}`},
		{0x41b3de4355555557, `{"n":333333333.33333343}`},
		{0xbecbf647612f3696, `{"n":-0.0000033333333333333333}`},
		{0x43143ff3c1cb0959, `{"n":1424953923781206.2}`},
	}
	for _, tc := range cases {
		actual, err := assistantCanonicalJSON(map[string]any{"n": math.Float64frombits(tc.bits)})
		if err != nil || string(actual) != tc.want {
			t.Errorf("bits %016x: got %s err %v, want %s", tc.bits, actual, err, tc.want)
		}
	}
}

func TestAssistantIJSONRejectsDuplicateKeysAndLoneSurrogates(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":{"x":1,"x":2}}`, `{"a":"\ud800"}`, `{"a":"\udc00"}`, `{"a":"\ud800\u0041"}`} {
		if err := ValidateAssistantIJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid I-JSON: %s", raw)
		}
	}
	if err := ValidateAssistantIJSON([]byte(`{"a":"\ud83d\ude00"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := assistantCanonicalJSON(map[string]any{"bad": string([]byte{0xff})}); err == nil {
		t.Fatal("invalid Go UTF-8 accepted")
	}
}

func TestAssistantExecutionCheckpointWireContract(t *testing.T) {
	if AssistantExecutionCheckpointKind != 4903 || AssistantCheckpointTagDomain != "domain" || AssistantCheckpointTagType != "type" || AssistantCheckpointTagSchema != "schema" || AssistantCheckpointTagPrevious != "prev" {
		t.Fatal("checkpoint kind/tag contract drift")
	}
	envelope := AssistantExecutionCheckpointAEADEnvelope{Schema: AssistantExecutionCheckpointSchema, Envelope: AssistantTranscriptEnvelopeServiceHeldAEAD, Algorithm: AssistantTranscriptAEADAlgorithmXChaCha20, KeyRef: "assistant/checkpoint", Nonce: "nonce", Ciphertext: "ciphertext", AssociatedData: map[string]string{AssistantCheckpointTagSession: "session", AssistantCheckpointTagRun: "run", AssistantCheckpointTagRevision: "1"}}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "tool_args") || strings.Contains(string(encoded), "plaintext") {
		t.Fatalf("checkpoint envelope leaks work: %s", encoded)
	}
	var decoded AssistantExecutionCheckpointAEADEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Schema != AssistantExecutionCheckpointSchema || decoded.AssociatedData[AssistantCheckpointTagRun] != "run" {
		t.Fatalf("checkpoint envelope lost identity: %#v %v", decoded, err)
	}
}

func TestAssistantApprovalScopeArgumentCommitmentVector(t *testing.T) {
	private := AssistantCommandScope{AllowedTools: []string{}, Arguments: map[string]any{"nested": map[string]any{"😀": "smile", "\ue000": "private", "é": []any{"界", "<tag>", "line\nnext"}}}}
	public, err := private.ApprovalScope()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "assistant", "batch_approval_hash_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []struct {
			Name  string                          `json:"name"`
			Input AssistantBatchApprovalHashInput `json:"input"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, vector := range fixture.Vectors {
		if vector.Name == "unicode_nested_empty_scope" {
			if public.ArgumentsDigest != vector.Input.Scope.ArgumentsDigest || public.AllowedTools == nil {
				t.Fatalf("scope commitment mismatch: %#v vs %#v", public, vector.Input.Scope)
			}
			return
		}
	}
	t.Fatal("Unicode scope vector missing")
}
