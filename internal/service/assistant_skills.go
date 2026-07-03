package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openagentsinc/bahia/internal/domain"
)

// assistantSkillLoadToolName is the internal, read-only tool the model calls to
// pull a skill's full body on demand (progressive disclosure). Only skill
// names/descriptions are injected at turn start; bodies load through this tool.
const assistantSkillLoadToolName = "bahia_assistant_skill_load"

// assistantSkillManifest is the conventional SKILL.md entrypoint filename.
const assistantSkillManifest = "SKILL.md"

// AssistantSkillSpec is one loaded SKILL.md package. Root is the skill directory;
// all on-demand file reads are confined to it.
type AssistantSkillSpec struct {
	Name        string
	Description string
	Root        string
	ManifestRel string
	Body        string
}

// AssistantSkillLibrary is the loaded, name-indexed set of skill packages.
type AssistantSkillLibrary struct {
	order  []string
	byName map[string]AssistantSkillSpec
}

// Len returns the number of loaded skills.
func (lib *AssistantSkillLibrary) Len() int {
	if lib == nil {
		return 0
	}
	return len(lib.byName)
}

// Get returns a skill spec by name.
func (lib *AssistantSkillLibrary) Get(name string) (AssistantSkillSpec, bool) {
	if lib == nil {
		return AssistantSkillSpec{}, false
	}
	spec, ok := lib.byName[strings.TrimSpace(name)]
	return spec, ok
}

// Specs returns loaded skills in deterministic order.
func (lib *AssistantSkillLibrary) Specs() []AssistantSkillSpec {
	if lib == nil {
		return nil
	}
	out := make([]AssistantSkillSpec, 0, len(lib.order))
	for _, name := range lib.order {
		out = append(out, lib.byName[name])
	}
	return out
}

// Catalog renders the progressive-disclosure summary injected at turn start:
// only skill names and descriptions, never bodies.
func (lib *AssistantSkillLibrary) Catalog() string {
	if lib.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available skills (call ")
	b.WriteString(assistantSkillLoadToolName)
	b.WriteString(" with the skill name to load full instructions before using one):\n")
	for _, name := range lib.order {
		spec := lib.byName[name]
		b.WriteString("- ")
		b.WriteString(spec.Name)
		b.WriteString(": ")
		b.WriteString(spec.Description)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// LoadAssistantSkills discovers SKILL.md packages under the configured roots. A
// malformed frontmatter block or a missing required field fails closed.
func LoadAssistantSkills(roots []string) (*AssistantSkillLibrary, error) {
	lib := &AssistantSkillLibrary{byName: map[string]AssistantSkillSpec{}}
	manifests, err := assistantSkillManifests(roots)
	if err != nil {
		return nil, err
	}
	for _, path := range manifests {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read assistant skill %q: %w", path, err)
		}
		spec, err := parseAssistantSkill(string(content), path)
		if err != nil {
			return nil, err
		}
		if _, dup := lib.byName[spec.Name]; dup {
			return nil, fmt.Errorf("duplicate assistant skill name %q (%s)", spec.Name, path)
		}
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib, nil
}

// ParseAssistantSkill parses one SKILL.md document rooted at root. Exposed for
// unit tests that do not touch the filesystem.
func ParseAssistantSkill(content, root, manifestRel string) (AssistantSkillSpec, error) {
	return parseAssistantSkillContent(content, root, manifestRel, filepath.Join(root, manifestRel))
}

func parseAssistantSkill(content, path string) (AssistantSkillSpec, error) {
	root := filepath.Dir(path)
	return parseAssistantSkillContent(content, root, filepath.Base(path), path)
}

func parseAssistantSkillContent(content, root, manifestRel, sourcePath string) (AssistantSkillSpec, error) {
	frontmatter, body, ok := assistantSplitFrontmatter(content)
	if !ok {
		return AssistantSkillSpec{}, fmt.Errorf("assistant skill %q is missing a yaml frontmatter block", assistantSourceLabel(sourcePath))
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return AssistantSkillSpec{}, fmt.Errorf("parse assistant skill frontmatter %q: %w", assistantSourceLabel(sourcePath), err)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return AssistantSkillSpec{}, fmt.Errorf("assistant skill %q frontmatter is missing required field name", assistantSourceLabel(sourcePath))
	}
	description := strings.TrimSpace(fm.Description)
	if description == "" {
		return AssistantSkillSpec{}, fmt.Errorf("assistant skill %q frontmatter is missing required field description", assistantSourceLabel(sourcePath))
	}
	return AssistantSkillSpec{
		Name:        name,
		Description: description,
		Root:        root,
		ManifestRel: manifestRel,
		Body:        strings.TrimSpace(body),
	}, nil
}

// buildSkillLoadTool constructs the internal, read-only skill-loading tool.
func (l *AssistantAgentLoop) buildSkillLoadTool() *assistantInternalTool {
	return &assistantInternalTool{
		name:        assistantSkillLoadToolName,
		description: "Load the full instructions for a named skill (progressive disclosure). Optionally read a supporting file within the skill directory via the path argument.",
		effect:      domain.AssistantToolEffectRead,
		risk:        domain.AssistantToolRiskLow,
		inputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill": map[string]any{"type": "string", "description": "Name of the skill to load."},
				"path":  map[string]any{"type": "string", "description": "Optional file path, relative to the skill directory, to read instead of SKILL.md."},
			},
			"required": []any{"skill"},
		},
		handler: l.runSkillLoad,
	}
}

func (l *AssistantAgentLoop) runSkillLoad(_ context.Context, _ assistantAgentLoopRun, call domain.AssistantAgentToolCall) (*domain.AssistantToolObservation, error) {
	name := strings.TrimSpace(stringFromAnyMapAny(call.Arguments, "skill"))
	if name == "" {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, "skill_load requires a skill argument", nil), nil
	}
	spec, ok := l.skills.Get(name)
	if !ok {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, fmt.Sprintf("unknown skill %q", name), nil), nil
	}
	rel := strings.TrimSpace(stringFromAnyMapAny(call.Arguments, "path"))
	if rel == "" {
		obs := l.internalToolObservation(call, domain.AssistantToolObservationSucceeded, spec.Body, map[string]any{"skill": spec.Name})
		obs.Result = map[string]any{"skill": spec.Name, "content": spec.Body}
		return obs, nil
	}
	resolved, err := assistantResolveContainedPath(spec.Root, rel)
	if err != nil {
		return l.internalToolObservation(call, domain.AssistantToolObservationDenied, err.Error(), map[string]any{"skill": spec.Name}), nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, fmt.Sprintf("read skill file %q: %v", rel, err), map[string]any{"skill": spec.Name}), nil
	}
	body := string(data)
	obs := l.internalToolObservation(call, domain.AssistantToolObservationSucceeded, body, map[string]any{"skill": spec.Name, "path": rel})
	obs.Result = map[string]any{"skill": spec.Name, "path": rel, "content": body}
	return obs, nil
}

func assistantSkillManifests(roots []string) ([]string, error) {
	var manifests []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat assistant skill root %q: %w", root, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Base(root), assistantSkillManifest) {
				manifests = append(manifests, root)
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Base(path), assistantSkillManifest) {
				manifests = append(manifests, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk assistant skill root %q: %w", root, err)
		}
	}
	sort.Strings(manifests)
	return manifests, nil
}

// assistantResolveContainedPath resolves rel against root and guarantees the
// result stays within root, rejecting absolute paths and `..` traversal.
func assistantResolveContainedPath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the skill directory", rel)
	}
	cleanedRoot := filepath.Clean(root)
	joined := filepath.Clean(filepath.Join(cleanedRoot, rel))
	if joined != cleanedRoot && !strings.HasPrefix(joined, cleanedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the skill directory", rel)
	}
	return joined, nil
}
