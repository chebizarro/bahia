package soulfactory

import (
	"context"
	"fmt"
)

// SoulFactory runtime control methods before delegating unrelated methods to an
// underlying runtime driver.
type PersonaRuntimeControlDriver struct {
	Backend OpenClawControlDriver
}

func (d *PersonaRuntimeControlDriver) Methods() []string {
	methods := []string{RuntimeMethodPersonaConfigure, RuntimeMethodPersonaPreview, RuntimeMethodPersonaUpdate}
	if d != nil && d.Backend != nil {
		methods = append(methods, d.Backend.Methods()...)
	}
	return uniqueStrings(methods)
}

func (d *PersonaRuntimeControlDriver) Execute(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	switch invocation.Method {
	case RuntimeMethodPersonaConfigure, RuntimeMethodPersonaUpdate:
		return d.configure(invocation)
	case RuntimeMethodPersonaPreview:
		return d.preview(invocation)
	default:
		if d != nil && d.Backend != nil {
			return d.Backend.Execute(ctx, invocation)
		}
		return nil, fmt.Errorf("unsupported persona runtime method %s", invocation.Method)
	}
}

func (d *PersonaRuntimeControlDriver) configure(invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	mapping, err := ParsePersonaRuntimeParams(invocation.Params)
	if err != nil {
		return nil, err
	}
	return &OpenClawControlOutcome{Status: "success", Result: personaRuntimeResult(invocation, mapping, true)}, nil
}

func (d *PersonaRuntimeControlDriver) preview(invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	mapping, err := ParsePersonaRuntimeParams(invocation.Params)
	if err != nil {
		return nil, err
	}
	return &OpenClawControlOutcome{Status: "success", Result: personaRuntimeResult(invocation, mapping, false)}, nil
}

func personaRuntimeResult(invocation OpenClawControlInvocation, mapping *PersonalityMapping, applied bool) map[string]interface{} {
	sections := map[string]interface{}{
		"role":       mapping.Sections.Role,
		"guidelines": mapping.Sections.Guidelines,
		"red_lines":  mapping.Sections.RedLines,
	}
	return map[string]interface{}{
		"agent_id":               invocation.AgentID,
		"soul_id":                invocation.SoulID,
		"spec_hash":              invocation.SpecHash,
		"applied":                applied,
		"hot_reload":             applied,
		"schema":                 PersonalityRuntimeParamsSchema,
		"persona":                mapping.Persona,
		"system_prompt_sections": sections,
		"system_prompt":          mapping.SystemPrompt,
		"openclaw":               mapping.RuntimeParams.OpenClaw,
	}
}
