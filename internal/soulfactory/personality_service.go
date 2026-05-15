package soulfactory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	// RuntimeMethodPersonaUpdate is the kind:38384 method used to hot-reload a
	// persona/system prompt into a running runtime. The method is defined here so
	// Bahia's mapper and runtime bridge work share one exact dispatch string.
	RuntimeMethodPersonaUpdate = "soulfactory.persona.update"

	// PersonalityRuntimeParamsSchema versions the persona-specific params payload
	// nested under RuntimeControlEnvelope.params for RuntimeMethodPersonaUpdate.
	PersonalityRuntimeParamsSchema = "soulfactory-persona/v1"

	OpenClawPromptSectionRole       = "role"
	OpenClawPromptSectionGuidelines = "guidelines"
	OpenClawPromptSectionRedLines   = "red_lines"

	personaPromptSectionExpertise = "expertise"
	personaPromptSectionGoals     = "goals"

	maxPersonaTraits            = 24
	maxPersonaTraitRunes        = 64
	maxPersonaStyleToneRunes    = 128
	maxPersonaConstraints       = 32
	maxPersonaConstraintRunes   = 500
	maxPersonaSectionRunes      = 8000
	maxPersonaSystemPromptRunes = 24000
)

var openClawPromptSectionOrder = []string{
	OpenClawPromptSectionRole,
	OpenClawPromptSectionGuidelines,
	OpenClawPromptSectionRedLines,
}

// PersonalityService maps SoulFactory persona specs into runtime-portable
// OpenClaw prompt payloads. It is intentionally pure/deterministic: no relay,
// timer, or LLM calls happen in this service.
type PersonalityService struct{}

func NewPersonalityService() PersonalityService { return PersonalityService{} }

// OpenClawPromptSections is the canonical section shape Bahia sends to OpenClaw
// runtimes. The final system prompt is assembled from these sections in a
// deterministic role -> guidelines -> red_lines order.
type OpenClawPromptSections struct {
	Role       string `json:"role,omitempty"`
	Guidelines string `json:"guidelines,omitempty"`
	RedLines   string `json:"red_lines,omitempty"`
}

func (s OpenClawPromptSections) Map() map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(s.Role) != "" {
		out[OpenClawPromptSectionRole] = strings.TrimSpace(s.Role)
	}
	if strings.TrimSpace(s.Guidelines) != "" {
		out[OpenClawPromptSectionGuidelines] = strings.TrimSpace(s.Guidelines)
	}
	if strings.TrimSpace(s.RedLines) != "" {
		out[OpenClawPromptSectionRedLines] = strings.TrimSpace(s.RedLines)
	}
	return out
}

// OpenClawPersonalityConfig is the runtime-facing OpenClaw contract nested
// inside PersonalityRuntimeParams. Portable contract fields stay snake_case;
// AgentDefaultsPatch intentionally uses OpenClaw-native config keys.
type OpenClawPersonalityConfig struct {
	SystemPromptSections OpenClawPromptSections `json:"system_prompt_sections"`
	SystemPromptOverride string                 `json:"system_prompt_override"`
	AgentDefaultsPatch   map[string]interface{} `json:"agent_defaults_patch,omitempty"`
}

// PersonalityRuntimeParams is the params contract for kind:38384
// RuntimeMethodPersonaUpdate requests.
type PersonalityRuntimeParams struct {
	Schema   string                    `json:"schema"`
	Persona  domain.SoulPersonaSpec    `json:"persona"`
	OpenClaw OpenClawPersonalityConfig `json:"openclaw"`
}

// PersonalityMapping is the normalized mapping result used by callers that need
// both the assembled prompt and the exact runtime params.
type PersonalityMapping struct {
	Persona       domain.SoulPersonaSpec   `json:"persona"`
	Sections      OpenClawPromptSections   `json:"system_prompt_sections"`
	SystemPrompt  string                   `json:"system_prompt"`
	RuntimeParams PersonalityRuntimeParams `json:"runtime_params"`
}

// Map validates and normalizes a SoulPersonaSpec, maps it to OpenClaw prompt
// sections, assembles the composite system prompt, and returns the runtime
// params contract for soulfactory.persona.update.
func (PersonalityService) Map(spec domain.SoulPersonaSpec) (*PersonalityMapping, error) {
	normalized, err := normalizePersonaSpec(spec)
	if err != nil {
		return nil, err
	}
	sections := buildOpenClawPromptSections(normalized)
	systemPrompt := AssembleOpenClawSystemPrompt(sections)
	if runeLen(systemPrompt) > maxPersonaSystemPromptRunes {
		return nil, fmt.Errorf("persona system prompt exceeds %d characters", maxPersonaSystemPromptRunes)
	}
	params := PersonalityRuntimeParams{
		Schema:  PersonalityRuntimeParamsSchema,
		Persona: normalized,
		OpenClaw: OpenClawPersonalityConfig{
			SystemPromptSections: sections,
			SystemPromptOverride: systemPrompt,
			AgentDefaultsPatch: map[string]interface{}{
				"systemPromptOverride": systemPrompt,
			},
		},
	}
	return &PersonalityMapping{
		Persona:       normalized,
		Sections:      sections,
		SystemPrompt:  systemPrompt,
		RuntimeParams: params,
	}, nil
}

func ValidateSoulPersonaSpec(spec domain.SoulPersonaSpec) error {
	_, err := normalizePersonaSpec(spec)
	return err
}

func MapSoulPersonaToOpenClaw(spec domain.SoulPersonaSpec) (*PersonalityMapping, error) {
	return NewPersonalityService().Map(spec)
}

// GenerateSystemPrompt validates the persona spec and returns the assembled
// system prompt used by runtime agent defaults.
func (s PersonalityService) GenerateSystemPrompt(spec domain.SoulPersonaSpec) (string, error) {
	mapping, err := s.Map(spec)
	if err != nil {
		return "", err
	}
	return mapping.SystemPrompt, nil
}

func GenerateSystemPrompt(spec domain.SoulPersonaSpec) (string, error) {
	return NewPersonalityService().GenerateSystemPrompt(spec)
}

func BuildPersonaRuntimeControlParams(spec domain.SoulPersonaSpec) (map[string]interface{}, error) {
	mapping, err := MapSoulPersonaToOpenClaw(spec)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(mapping.RuntimeParams)
	if err != nil {
		return nil, fmt.Errorf("marshal persona runtime params: %w", err)
	}
	var params map[string]interface{}
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, fmt.Errorf("decode persona runtime params: %w", err)
	}
	return params, nil
}

func AssembleOpenClawSystemPrompt(sections OpenClawPromptSections) string {
	sectionMap := sections.Map()
	labels := map[string]string{
		OpenClawPromptSectionRole:       "Role",
		OpenClawPromptSectionGuidelines: "Guidelines",
		OpenClawPromptSectionRedLines:   "Red Lines",
	}
	var parts []string
	for _, key := range openClawPromptSectionOrder {
		value := strings.TrimSpace(sectionMap[key])
		if value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", labels[key], value))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func normalizePersonaSpec(spec domain.SoulPersonaSpec) (domain.SoulPersonaSpec, error) {
	var errs []error
	normalized := domain.SoulPersonaSpec{
		Style:                strings.TrimSpace(spec.Style),
		Tone:                 strings.TrimSpace(spec.Tone),
		SystemPromptSections: map[string]string{},
	}
	if runeLen(normalized.Style) > maxPersonaStyleToneRunes {
		errs = append(errs, fmt.Errorf("persona style exceeds %d characters", maxPersonaStyleToneRunes))
	}
	if hasDisallowedControl(normalized.Style, false) {
		errs = append(errs, fmt.Errorf("persona style contains control characters"))
	}
	if runeLen(normalized.Tone) > maxPersonaStyleToneRunes {
		errs = append(errs, fmt.Errorf("persona tone exceeds %d characters", maxPersonaStyleToneRunes))
	}
	if hasDisallowedControl(normalized.Tone, false) {
		errs = append(errs, fmt.Errorf("persona tone contains control characters"))
	}

	traits, traitErrs := normalizePersonaList("trait", spec.Traits, maxPersonaTraits, maxPersonaTraitRunes, false)
	normalized.Traits = traits
	errs = append(errs, traitErrs...)
	constraints, constraintErrs := normalizePersonaList("constraint", spec.Constraints, maxPersonaConstraints, maxPersonaConstraintRunes, true)
	normalized.Constraints = constraints
	errs = append(errs, constraintErrs...)

	seenSections := map[string]string{}
	for rawKey, rawValue := range spec.SystemPromptSections {
		key, ok := canonicalPromptSection(rawKey)
		if !ok {
			errs = append(errs, fmt.Errorf("unsupported system prompt section %q", rawKey))
			continue
		}
		value := strings.TrimSpace(rawValue)
		if value == "" {
			continue
		}
		if previous, exists := seenSections[key]; exists && previous != value {
			errs = append(errs, fmt.Errorf("duplicate system prompt section %q", key))
			continue
		}
		if runeLen(value) > maxPersonaSectionRunes {
			errs = append(errs, fmt.Errorf("system prompt section %q exceeds %d characters", key, maxPersonaSectionRunes))
		}
		if hasDisallowedControl(value, true) {
			errs = append(errs, fmt.Errorf("system prompt section %q contains control characters", key))
		}
		seenSections[key] = value
	}
	for _, key := range openClawPromptSectionOrder {
		if value := seenSections[key]; value != "" {
			normalized.SystemPromptSections[key] = value
		}
	}
	for _, key := range []string{personaPromptSectionExpertise, personaPromptSectionGoals} {
		if value := seenSections[key]; value != "" {
			normalized.SystemPromptSections[key] = value
		}
	}
	if len(normalized.SystemPromptSections) == 0 {
		normalized.SystemPromptSections = nil
	}
	return normalized, errors.Join(errs...)
}

func normalizePersonaList(label string, values []string, maxItems, maxRunes int, allowNewlines bool) ([]string, []error) {
	var errs []error
	if len(values) > maxItems {
		errs = append(errs, fmt.Errorf("persona %ss exceed max count %d", label, maxItems))
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for i, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			errs = append(errs, fmt.Errorf("persona %s %d is empty", label, i))
			continue
		}
		if runeLen(value) > maxRunes {
			errs = append(errs, fmt.Errorf("persona %s %q exceeds %d characters", label, value, maxRunes))
		}
		if hasDisallowedControl(value, allowNewlines) {
			errs = append(errs, fmt.Errorf("persona %s %q contains control characters", label, value))
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate persona %s %q", label, value))
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out, errs
}

func canonicalPromptSection(key string) (string, bool) {
	canonical := strings.ToLower(strings.TrimSpace(key))
	canonical = strings.ReplaceAll(canonical, "-", "_")
	canonical = strings.ReplaceAll(canonical, " ", "_")
	switch canonical {
	case OpenClawPromptSectionRole:
		return OpenClawPromptSectionRole, true
	case OpenClawPromptSectionGuidelines:
		return OpenClawPromptSectionGuidelines, true
	case "redlines", OpenClawPromptSectionRedLines:
		return OpenClawPromptSectionRedLines, true
	case personaPromptSectionExpertise:
		return personaPromptSectionExpertise, true
	case "goal", personaPromptSectionGoals:
		return personaPromptSectionGoals, true
	default:
		return "", false
	}
}

func buildOpenClawPromptSections(spec domain.SoulPersonaSpec) OpenClawPromptSections {
	sections := OpenClawPromptSections{}
	if spec.SystemPromptSections != nil {
		sections.Role = spec.SystemPromptSections[OpenClawPromptSectionRole]
		sections.Guidelines = spec.SystemPromptSections[OpenClawPromptSectionGuidelines]
		sections.RedLines = spec.SystemPromptSections[OpenClawPromptSectionRedLines]
	}
	if strings.TrimSpace(sections.Role) == "" {
		sections.Role = "You are a SoulFactory-managed agent. Stay consistent with the configured persona and identity while helping users."
	}
	sections.Guidelines = appendPromptLines(sections.Guidelines, buildPersonaGuidelines(spec)...)
	sections.RedLines = appendPromptLines(sections.RedLines, buildPersonaConstraints(spec.Constraints)...)
	return sections
}

func buildPersonaGuidelines(spec domain.SoulPersonaSpec) []string {
	var lines []string
	if spec.Style != "" {
		lines = append(lines, "- Style: "+spec.Style)
	}
	if spec.Tone != "" {
		lines = append(lines, "- Tone: "+spec.Tone)
	}
	if len(spec.Traits) > 0 {
		lines = append(lines, "- Maintain these persona traits: "+strings.Join(spec.Traits, ", ")+".")
	}
	if spec.SystemPromptSections != nil {
		if expertise := strings.TrimSpace(spec.SystemPromptSections[personaPromptSectionExpertise]); expertise != "" {
			lines = append(lines, "Expertise:\n"+expertise)
		}
		if goals := strings.TrimSpace(spec.SystemPromptSections[personaPromptSectionGoals]); goals != "" {
			lines = append(lines, "Goals:\n"+goals)
		}
	}
	return lines
}

func buildPersonaConstraints(constraints []string) []string {
	if len(constraints) == 0 {
		return nil
	}
	lines := []string{"Constraints:"}
	for _, constraint := range constraints {
		lines = append(lines, "- "+constraint)
	}
	return lines
}

func appendPromptLines(base string, lines ...string) string {
	base = strings.TrimSpace(base)
	if len(lines) == 0 {
		return base
	}
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	if len(nonEmpty) == 0 {
		return base
	}
	if base == "" {
		return strings.Join(nonEmpty, "\n")
	}
	return base + "\n" + strings.Join(nonEmpty, "\n")
}

func hasDisallowedControl(value string, allowNewlines bool) bool {
	for _, r := range value {
		if !unicode.IsControl(r) {
			continue
		}
		if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		return true
	}
	return false
}

func runeLen(value string) int { return len([]rune(value)) }
