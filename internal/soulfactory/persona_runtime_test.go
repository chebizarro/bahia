package soulfactory

import (
	"strings"
	"testing"
)

func TestPersonaRuntimeControlDriverConfigureAndPreview(t *testing.T) {
	driver := &PersonaRuntimeControlDriver{}
	personaParams := map[string]interface{}{
		"persona": map[string]interface{}{
			"traits":      []interface{}{"curious", "thorough"},
			"style":       "conversational",
			"tone":        "friendly professional",
			"constraints": []interface{}{"Never fabricate citations"},
			"system_prompt_sections": map[string]interface{}{
				"role":       "You are Scout, a research assistant.",
				"guidelines": "Answer with concise findings.",
			},
		},
	}

	configure, err := driver.Execute(t.Context(), OpenClawControlInvocation{
		Method:   RuntimeMethodPersonaConfigure,
		AgentID:  "scout",
		SoulID:   "scout",
		SpecHash: "sha256:spec",
		Params:   personaParams,
	})
	if err != nil {
		t.Fatalf("persona.configure Execute error = %v", err)
	}
	if configure.Status != "success" || configure.Result["applied"] != true || configure.Result["hot_reload"] != true {
		t.Fatalf("unexpected configure outcome = %+v", configure)
	}
	prompt, _ := configure.Result["system_prompt"].(string)
	if !strings.Contains(prompt, "### Role\nYou are Scout") || !strings.Contains(prompt, "Never fabricate citations") {
		t.Fatalf("configure prompt = %q", prompt)
	}
	if configure.Result["system_prompt_sections"] == nil || configure.Result["openclaw"] == nil {
		t.Fatalf("configure result missing prompt sections/openclaw = %+v", configure.Result)
	}

	preview, err := driver.Execute(t.Context(), OpenClawControlInvocation{
		Method:   RuntimeMethodPersonaPreview,
		AgentID:  "scout",
		SoulID:   "scout",
		SpecHash: "sha256:spec",
		Params:   personaParams,
	})
	if err != nil {
		t.Fatalf("persona.preview Execute error = %v", err)
	}
	if preview.Status != "success" || preview.Result["applied"] != false || preview.Result["hot_reload"] != false || preview.Result["system_prompt"] == "" {
		t.Fatalf("unexpected preview outcome = %+v", preview)
	}
}
