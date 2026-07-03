package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseAssistantSkillValid(t *testing.T) {
	content := "---\nname: deploy-runbook\ndescription: Steps to safely deploy a service\n---\n# Deploy Runbook\n1. Check health\n2. Roll forward\n"
	spec, err := ParseAssistantSkill(content, "/skills/deploy-runbook", "SKILL.md")
	if err != nil {
		t.Fatalf("ParseAssistantSkill: %v", err)
	}
	if spec.Name != "deploy-runbook" || spec.Description != "Steps to safely deploy a service" {
		t.Fatalf("spec = %#v", spec)
	}
	if !strings.Contains(spec.Body, "Roll forward") {
		t.Fatalf("body = %q", spec.Body)
	}
}

func TestParseAssistantSkillInvalid(t *testing.T) {
	cases := map[string]string{
		"missing frontmatter": "# no frontmatter\n",
		"missing name":        "---\ndescription: d\n---\nbody\n",
		"missing description": "---\nname: n\n---\nbody\n",
		"malformed yaml":      "---\nname: {bad\n---\nbody\n",
	}
	for label, content := range cases {
		if _, err := ParseAssistantSkill(content, "/skills/x", "SKILL.md"); err == nil {
			t.Fatalf("%s: expected error", label)
		}
	}
}

func TestAssistantSkillCatalogProgressiveDisclosure(t *testing.T) {
	lib := mustSkillLibrary(
		AssistantSkillSpec{Name: "deploy", Description: "Deploy safely", Root: "/skills/deploy", Body: "full deploy body"},
		AssistantSkillSpec{Name: "rollback", Description: "Rollback safely", Root: "/skills/rollback", Body: "full rollback body"},
	)
	catalog := lib.Catalog()
	if !strings.Contains(catalog, "deploy: Deploy safely") || !strings.Contains(catalog, "rollback: Rollback safely") {
		t.Fatalf("catalog = %q", catalog)
	}
	if !strings.Contains(catalog, assistantSkillLoadToolName) {
		t.Fatalf("catalog should reference the skill load tool: %q", catalog)
	}
	if strings.Contains(catalog, "full deploy body") {
		t.Fatalf("catalog must not disclose skill bodies: %q", catalog)
	}
}

func TestAssistantResolveContainedPathRejectsTraversal(t *testing.T) {
	if _, err := assistantResolveContainedPath("/skills/deploy", "../secrets.txt"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := assistantResolveContainedPath("/skills/deploy", "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
	resolved, err := assistantResolveContainedPath("/skills/deploy", "references/notes.md")
	if err != nil || resolved != "/skills/deploy/references/notes.md" {
		t.Fatalf("resolved = %q err = %v", resolved, err)
	}
}

func TestAssistantAgentLoopSkillLoadReturnsBody(t *testing.T) {
	skills := mustSkillLibrary(AssistantSkillSpec{Name: "deploy", Description: "Deploy safely", Root: "/skills/deploy", Body: "load-me deploy instructions"})
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-skill", Name: assistantSkillLoadToolName, Arguments: map[string]any{"skill": "deploy"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("using the deploy skill"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, _ := newAssistantExtLoop(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Skills: skills})
	session := assistantRuntimeSession("session-skill")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-skill", Prompt: "use the deploy skill"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("result = %#v", res)
	}
	obs := findToolObservation(model.request(1), "call-skill")
	if obs == nil || obs.Status != domain.AssistantToolObservationSucceeded || !strings.Contains(obs.Summary, "load-me deploy instructions") {
		t.Fatalf("skill_load observation = %#v", obs)
	}
	// The turn-start catalog is injected as system context.
	if !requestHasText(model.request(0), "Deploy safely") {
		t.Fatalf("skill catalog not injected at turn start: %#v", model.request(0).Messages)
	}
}

func mustSkillLibrary(specs ...AssistantSkillSpec) *AssistantSkillLibrary {
	lib := &AssistantSkillLibrary{byName: map[string]AssistantSkillSpec{}}
	for _, spec := range specs {
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib
}

func findToolObservation(req llm.AgentModelRequest, toolCallID string) *domain.AssistantToolObservation {
	for _, msg := range req.Messages {
		if msg.Role == domain.AssistantAgentMessageRoleTool && msg.ToolCallID == toolCallID {
			return msg.Observation
		}
	}
	return nil
}
