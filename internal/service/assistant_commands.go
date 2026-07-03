package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// AssistantCommandSpec is one markdown command prompt template using the
// claude-code command convention (description/allowed-tools/model/argument-hint
// frontmatter, markdown body as the expandable prompt template).
type AssistantCommandSpec struct {
	Name         string
	Description  string
	AllowedTools []string
	Model        string
	ArgumentHint string
	Template     string
	SourcePath   string
}

// AssistantCommandLibrary is the loaded, name-indexed set of command templates.
type AssistantCommandLibrary struct {
	order  []string
	byName map[string]AssistantCommandSpec
}

// Len returns the number of loaded commands.
func (lib *AssistantCommandLibrary) Len() int {
	if lib == nil {
		return 0
	}
	return len(lib.byName)
}

// Get returns a command spec by name.
func (lib *AssistantCommandLibrary) Get(name string) (AssistantCommandSpec, bool) {
	if lib == nil {
		return AssistantCommandSpec{}, false
	}
	spec, ok := lib.byName[strings.TrimSpace(name)]
	return spec, ok
}

// Specs returns loaded commands in deterministic order.
func (lib *AssistantCommandLibrary) Specs() []AssistantCommandSpec {
	if lib == nil {
		return nil
	}
	out := make([]AssistantCommandSpec, 0, len(lib.order))
	for _, name := range lib.order {
		out = append(out, lib.byName[name])
	}
	return out
}

// AssistantCommandExpansion is the result of expanding a slash-command prompt
// into its template plus the command's tool/model scoping.
type AssistantCommandExpansion struct {
	Command      AssistantCommandSpec
	Prompt       string
	AllowedTools []string
	Model        string
}

// Expand expands a leading slash-command in the operator prompt into the
// command template. It returns ok=false when the prompt is not a known command,
// leaving the original prompt untouched.
func (lib *AssistantCommandLibrary) Expand(prompt string) (AssistantCommandExpansion, bool) {
	trimmed := strings.TrimSpace(prompt)
	if lib.Len() == 0 || !strings.HasPrefix(trimmed, "/") {
		return AssistantCommandExpansion{}, false
	}
	name, args := splitAssistantCommandInvocation(trimmed)
	spec, ok := lib.Get(name)
	if !ok {
		return AssistantCommandExpansion{}, false
	}
	return AssistantCommandExpansion{
		Command:      spec,
		Prompt:       expandAssistantCommandTemplate(spec.Template, args),
		AllowedTools: append([]string(nil), spec.AllowedTools...),
		Model:        spec.Model,
	}, true
}

// LoadAssistantCommands parses every *.md command template under the configured
// roots. A malformed frontmatter block or duplicate command name fails closed.
func LoadAssistantCommands(roots []string) (*AssistantCommandLibrary, error) {
	lib := &AssistantCommandLibrary{byName: map[string]AssistantCommandSpec{}}
	files, err := assistantMarkdownFiles(roots)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read assistant command %q: %w", path, err)
		}
		name := assistantCommandNameFromPath(path)
		spec, err := ParseAssistantCommand(string(content), name, path)
		if err != nil {
			return nil, err
		}
		if _, dup := lib.byName[spec.Name]; dup {
			return nil, fmt.Errorf("duplicate assistant command name %q (%s)", spec.Name, path)
		}
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib, nil
}

// ParseAssistantCommand parses one command markdown document. Frontmatter is
// optional; when present it must parse as YAML. The command name is supplied by
// the caller (derived from the filename during directory loads).
func ParseAssistantCommand(content, name, sourcePath string) (AssistantCommandSpec, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AssistantCommandSpec{}, fmt.Errorf("assistant command %q requires a name", assistantSourceLabel(sourcePath))
	}
	frontmatter, body, hasFrontmatter := assistantSplitFrontmatter(content)
	spec := AssistantCommandSpec{Name: name, Template: strings.TrimSpace(body), SourcePath: sourcePath}
	if !hasFrontmatter {
		spec.Template = strings.TrimSpace(content)
	} else {
		var fm struct {
			Description  string `yaml:"description"`
			AllowedTools any    `yaml:"allowed-tools"`
			Model        string `yaml:"model"`
			ArgumentHint string `yaml:"argument-hint"`
		}
		if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
			return AssistantCommandSpec{}, fmt.Errorf("parse assistant command frontmatter %q: %w", assistantSourceLabel(sourcePath), err)
		}
		spec.Description = strings.TrimSpace(fm.Description)
		spec.AllowedTools = assistantNormalizeStringList(fm.AllowedTools)
		spec.Model = strings.TrimSpace(fm.Model)
		spec.ArgumentHint = strings.TrimSpace(fm.ArgumentHint)
	}
	if spec.Template == "" {
		return AssistantCommandSpec{}, fmt.Errorf("assistant command %q has an empty prompt body", assistantSourceLabel(sourcePath))
	}
	return spec, nil
}

func splitAssistantCommandInvocation(prompt string) (string, string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(prompt), "/")
	if trimmed == "" {
		return "", ""
	}
	if idx := strings.IndexAny(trimmed, " \t\n"); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx]), strings.TrimSpace(trimmed[idx+1:])
	}
	return trimmed, ""
}

// expandAssistantCommandTemplate substitutes $ARGUMENTS (all args) and $1..$N
// (positional args) into the command template.
func expandAssistantCommandTemplate(template, args string) string {
	expanded := strings.ReplaceAll(template, "$ARGUMENTS", args)
	fields := strings.Fields(args)
	for i := len(fields); i >= 1; i-- {
		expanded = strings.ReplaceAll(expanded, "$"+strconv.Itoa(i), fields[i-1])
	}
	if !strings.Contains(template, "$ARGUMENTS") && !strings.ContainsAny(template, "$") && strings.TrimSpace(args) != "" {
		expanded = expanded + "\n\n" + args
	}
	return strings.TrimSpace(expanded)
}

func assistantCommandNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
