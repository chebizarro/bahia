package soulfactory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestPersonalityServiceMapsPersonaToOpenClawPromptSections(t *testing.T) {
	spec := domain.SoulPersonaSpec{
		Traits:      []string{" curious ", "thorough"},
		Style:       " conversational ",
		Tone:        "friendly professional",
		Constraints: []string{"Always cite sources", "Never fabricate citations"},
		SystemPromptSections: map[string]string{
			"role":       "You are Scout, a research assistant.",
			"guidelines": "Answer with concise, sourced findings.",
			"red-lines":  "Do not invent source titles.",
		},
	}

	mapping, err := NewPersonalityService().Map(spec)
	if err != nil {
		t.Fatalf("Map error = %v", err)
	}
	if got := mapping.Persona.Traits; len(got) != 2 || got[0] != "curious" || got[1] != "thorough" {
		t.Fatalf("normalized traits = %#v", got)
	}
	if mapping.Sections.Role != "You are Scout, a research assistant." {
		t.Fatalf("role section = %q", mapping.Sections.Role)
	}
	for _, want := range []string{
		"Answer with concise, sourced findings.",
		"- Style: conversational",
		"- Tone: friendly professional",
		"- Maintain these persona traits: curious, thorough.",
	} {
		if !strings.Contains(mapping.Sections.Guidelines, want) {
			t.Fatalf("guidelines missing %q:\n%s", want, mapping.Sections.Guidelines)
		}
	}
	for _, want := range []string{"Do not invent source titles.", "Constraints:", "- Always cite sources", "- Never fabricate citations"} {
		if !strings.Contains(mapping.Sections.RedLines, want) {
			t.Fatalf("red_lines missing %q:\n%s", want, mapping.Sections.RedLines)
		}
	}
	roleIdx := strings.Index(mapping.SystemPrompt, "### Role")
	guidelinesIdx := strings.Index(mapping.SystemPrompt, "### Guidelines")
	redLinesIdx := strings.Index(mapping.SystemPrompt, "### Red Lines")
	if roleIdx < 0 || guidelinesIdx < 0 || redLinesIdx < 0 || !(roleIdx < guidelinesIdx && guidelinesIdx < redLinesIdx) {
		t.Fatalf("system prompt sections not in deterministic order:\n%s", mapping.SystemPrompt)
	}
	if mapping.RuntimeParams.Schema != PersonalityRuntimeParamsSchema || mapping.RuntimeParams.OpenClaw.SystemPromptOverride != mapping.SystemPrompt {
		t.Fatalf("runtime params not wired to prompt: %+v", mapping.RuntimeParams)
	}
}

func TestPersonalityServiceGenerateSystemPromptIncludesPersonaInstructions(t *testing.T) {
	prompt, err := NewPersonalityService().GenerateSystemPrompt(domain.SoulPersonaSpec{
		Traits:      []string{"analytical", "patient"},
		Style:       "concise",
		Tone:        "warm professional",
		Constraints: []string{"Do not claim unsupported facts."},
		SystemPromptSections: map[string]string{
			"role":      "You are Scout, a research assistant.",
			"expertise": "Nostr protocol analysis and source-backed research.",
			"goals":     "Help operators make informed deployment decisions.",
		},
	})
	if err != nil {
		t.Fatalf("GenerateSystemPrompt error = %v", err)
	}
	for _, want := range []string{
		"### Role\nYou are Scout, a research assistant.",
		"- Style: concise",
		"- Tone: warm professional",
		"- Maintain these persona traits: analytical, patient.",
		"Expertise:\nNostr protocol analysis and source-backed research.",
		"Goals:\nHelp operators make informed deployment decisions.",
		"### Red Lines\nConstraints:\n- Do not claim unsupported facts.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestGenerateSystemPromptFunctionReturnsValidationErrors(t *testing.T) {
	_, err := GenerateSystemPrompt(domain.SoulPersonaSpec{Traits: []string{"curious", "Curious"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate persona trait") {
		t.Fatalf("GenerateSystemPrompt error = %v, want duplicate persona trait", err)
	}
}

func TestBuildPersonaRuntimeControlParamsDefines38384Contract(t *testing.T) {
	params, err := BuildPersonaRuntimeControlParams(domain.SoulPersonaSpec{
		Traits: []string{"patient"},
		SystemPromptSections: map[string]string{
			"role": "You are PatienceBot.",
		},
	})
	if err != nil {
		t.Fatalf("BuildPersonaRuntimeControlParams error = %v", err)
	}
	if RuntimeMethodPersonaUpdate != "soulfactory.persona.update" {
		t.Fatalf("persona update method drifted: %q", RuntimeMethodPersonaUpdate)
	}
	if params["schema"] != PersonalityRuntimeParamsSchema {
		t.Fatalf("params schema = %#v", params["schema"])
	}
	openclaw, ok := params["openclaw"].(map[string]interface{})
	if !ok {
		t.Fatalf("openclaw params type = %T", params["openclaw"])
	}
	prompt, ok := openclaw["system_prompt_override"].(string)
	if !ok || !strings.Contains(prompt, "### Role\nYou are PatienceBot.") || !strings.Contains(prompt, "patient") {
		t.Fatalf("system prompt override = %#v", openclaw["system_prompt_override"])
	}
	sections, ok := openclaw["system_prompt_sections"].(map[string]interface{})
	if !ok || sections["role"] != "You are PatienceBot." {
		t.Fatalf("system prompt sections = %#v", openclaw["system_prompt_sections"])
	}
	patch, ok := openclaw["agent_defaults_patch"].(map[string]interface{})
	if !ok || patch["systemPromptOverride"] != prompt {
		t.Fatalf("agent defaults patch = %#v", openclaw["agent_defaults_patch"])
	}

	envelope := RuntimeControlEnvelope{
		Schema:         domain.SoulFactoryRuntimeControlSchema,
		Method:         RuntimeMethodPersonaUpdate,
		IdempotencyKey: "sha256:persona",
		RequestedAt:    1715700000,
		Operator:       RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
		Controller:     RuntimeControllerRef{Pubkey: stringsRepeat("c", 64)},
		Target:         RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: stringsRepeat("d", 64), AgentID: "patience-bot"},
		Soul:           RuntimeSoulRef{ID: "patience-bot", Draft: "draft-event", SpecHash: "sha256:spec"},
		Params:         params,
	}
	event, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	if event.Kind != domain.KindRuntimeControlRequest || tagValue(event.Tags, "method") != RuntimeMethodPersonaUpdate || tagValue(event.Tags, tagSchema) != domain.SoulFactoryRuntimeControlSchema {
		t.Fatalf("38384 request contract tags drifted: kind=%d tags=%#v", event.Kind, event.Tags)
	}
	parsed, err := ParseRuntimeControlRequestEvent(event)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
	}
	if parsed.Method != RuntimeMethodPersonaUpdate || parsed.Params["schema"] != PersonalityRuntimeParamsSchema {
		body, _ := json.MarshalIndent(parsed, "", "  ")
		t.Fatalf("parsed runtime contract drifted:\n%s", body)
	}
}

func TestPersonalityValidationRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec domain.SoulPersonaSpec
		want string
	}{
		{
			name: "duplicate trait",
			spec: domain.SoulPersonaSpec{Traits: []string{"curious", "Curious"}},
			want: "duplicate persona trait",
		},
		{
			name: "unknown section",
			spec: domain.SoulPersonaSpec{SystemPromptSections: map[string]string{"mission": "do work"}},
			want: "unsupported system prompt section",
		},
		{
			name: "control character",
			spec: domain.SoulPersonaSpec{Constraints: []string{"never\x00do that"}},
			want: "contains control characters",
		},
		{
			name: "too many constraints",
			spec: domain.SoulPersonaSpec{Constraints: repeatStrings("constraint", maxPersonaConstraints+1)},
			want: "constraints exceed max count",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSoulPersonaSpec(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateSoulPersonaSpec error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAssembleOpenClawSystemPromptOmitsEmptySections(t *testing.T) {
	prompt := AssembleOpenClawSystemPrompt(OpenClawPromptSections{Guidelines: "Be helpful."})
	if prompt != "### Guidelines\nBe helpful." {
		t.Fatalf("prompt = %q", prompt)
	}
}

func repeatStrings(value string, count int) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = value + string(rune('a'+i%26))
	}
	return out
}
