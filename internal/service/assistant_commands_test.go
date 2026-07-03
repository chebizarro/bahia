package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseAssistantCommandWithFrontmatter(t *testing.T) {
	content := "---\ndescription: Deploy a service\nallowed-tools: bahia_list_services, bahia_assistant_service_deploy\nmodel: gpt-4o\nargument-hint: \"[service] [env]\"\n---\nDeploy service $1 to environment $2.\n"
	spec, err := ParseAssistantCommand(content, "deploy", "deploy.md")
	if err != nil {
		t.Fatalf("ParseAssistantCommand: %v", err)
	}
	if spec.Name != "deploy" || spec.Description != "Deploy a service" || spec.Model != "gpt-4o" || spec.ArgumentHint != "[service] [env]" {
		t.Fatalf("spec = %#v", spec)
	}
	if strings.Join(spec.AllowedTools, ",") != "bahia_list_services,bahia_assistant_service_deploy" {
		t.Fatalf("allowed tools = %#v", spec.AllowedTools)
	}
}

func TestParseAssistantCommandNoFrontmatter(t *testing.T) {
	spec, err := ParseAssistantCommand("Summarize the current deployment status.", "status", "status.md")
	if err != nil {
		t.Fatalf("ParseAssistantCommand: %v", err)
	}
	if spec.Template != "Summarize the current deployment status." {
		t.Fatalf("template = %q", spec.Template)
	}
}

func TestParseAssistantCommandInvalid(t *testing.T) {
	cases := map[string]struct{ content, name string }{
		"empty body":     {"---\ndescription: d\n---\n\n", "empty"},
		"malformed yaml": {"---\nallowed-tools: [oops\n---\nbody\n", "bad"},
		"missing name":   {"body", ""},
	}
	for label, tc := range cases {
		if _, err := ParseAssistantCommand(tc.content, tc.name, label); err == nil {
			t.Fatalf("%s: expected error", label)
		}
	}
}

func TestAssistantCommandExpand(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{Name: "deploy", Template: "Deploy service $1 to environment $2.", AllowedTools: []string{"bahia_list_services"}})
	expansion, ok := lib.Expand("/deploy api prod")
	if !ok {
		t.Fatal("expected command to expand")
	}
	if expansion.Prompt != "Deploy service api to environment prod." {
		t.Fatalf("prompt = %q", expansion.Prompt)
	}
	if _, ok := lib.Expand("just a normal question"); ok {
		t.Fatal("non-command prompt should not expand")
	}
}

func TestAssistantCommandExpandArguments(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{Name: "ask", Template: "Answer this question: $ARGUMENTS"})
	expansion, ok := lib.Expand("/ask what is the status of api")
	if !ok || expansion.Prompt != "Answer this question: what is the status of api" {
		t.Fatalf("expansion = %#v ok=%v", expansion, ok)
	}
}

func TestAssistantAgentLoopExpandsCommandIntoPrompt(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{Name: "deploy", Template: "Deploy service $1 to $2 and report."})
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{Content: textBlocks("deployment plan ready"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	loop, _ := newAssistantExtLoop(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Commands: lib})
	session := assistantRuntimeSession("session-command")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-command", Prompt: "/deploy api prod"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("result = %#v", res)
	}
	if !requestHasText(model.request(0), "Deploy service api to prod and report.") {
		t.Fatalf("command was not expanded into the prompt: %#v", model.request(0).Messages)
	}
}

func mustCommandLibrary(specs ...AssistantCommandSpec) *AssistantCommandLibrary {
	lib := &AssistantCommandLibrary{byName: map[string]AssistantCommandSpec{}}
	for _, spec := range specs {
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib
}
