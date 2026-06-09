package runtime

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Fragment directory layout constants
// ---------------------------------------------------------------------------

const (
	// bahiaFragmentsDir is the subdirectory under .bahia/ where per-service
	// fragment overlay files are written.
	bahiaFragmentsDir = "fragments"

	// fragmentStateFile is the fragment-tracking metadata file inside .bahia/fragments/.
	fragmentStateFile = "fragment-state.json"
)

// ---------------------------------------------------------------------------
// FragmentLayout — fragment file path derivation and rendered content
// ---------------------------------------------------------------------------

// FragmentLayout describes the file layout for a service-scoped Compose fragment.
//
// Fragment file layout under the compose directory:
//
//	<compose_dir>/
//	  docker-compose.yml                    # Full project (source of truth)
//	  .bahia/
//	    render-state.json                   # Full project metadata
//	    env/<service-key>.env               # Per-service env files
//	    fragments/
//	      <service-key>.yml                 # Per-service fragment (for eligible changes)
//	      fragment-state.json               # Fragment metadata (which services have fragments)
type FragmentLayout struct {
	// FragmentDir is the directory for fragment files: <compose_dir>/.bahia/fragments/
	FragmentDir string

	// FragmentFile is the per-service fragment path: <compose_dir>/.bahia/fragments/<service-key>.yml
	FragmentFile string

	// ServiceKey is the target service key.
	ServiceKey string

	// FragmentYAML is the rendered single-service Compose YAML.
	// Populated by RenderFragmentForPlan; empty when created via NewFragmentLayout.
	FragmentYAML []byte
}

// NewFragmentLayout derives the fragment layout paths for a named service
// under the given compose directory. FragmentYAML is left empty; call
// RenderFragmentForPlan to get a layout with rendered content.
func NewFragmentLayout(composeDir, serviceKey string) *FragmentLayout {
	fragmentDir := filepath.Join(composeDir, bahiaMarkerDir, bahiaFragmentsDir)
	return &FragmentLayout{
		FragmentDir:  fragmentDir,
		FragmentFile: filepath.Join(fragmentDir, serviceKey+".yml"),
		ServiceKey:   serviceKey,
	}
}

// ---------------------------------------------------------------------------
// ComposeFragmentRenderer — single-service fragment renderer
// ---------------------------------------------------------------------------

// ComposeFragmentRenderer renders a single-service Compose fragment file.
// The fragment contains only the target service definition and is merged
// with the full project by the applier at apply time.
//
// The fragment intentionally omits project-wide network and volume declarations;
// those remain authoritative only in the full docker-compose.yml.
type ComposeFragmentRenderer struct {
	renderer *ComposeRenderer // reuses the existing renderer's service-building logic
}

// NewComposeFragmentRenderer creates a new fragment renderer backed by a
// ComposeRenderer instance.
func NewComposeFragmentRenderer() *ComposeFragmentRenderer {
	return &ComposeFragmentRenderer{renderer: NewComposeRenderer()}
}

// RenderServiceFragment renders a single-service Compose fragment YAML.
//
// The fragment contains:
//   - The project name (must match the full project for correct merge semantics)
//   - A single service definition built by the same logic as the full renderer
//   - NO top-level network or volume declarations (those are project-wide)
//
// The caller is responsible for writing the returned YAML to the path given by
// FragmentLayout.FragmentFile.
func (r *ComposeFragmentRenderer) RenderServiceFragment(
	projectName string,
	svc domain.DesiredServiceSpec,
) (*FragmentLayout, error) {
	if projectName == "" {
		return nil, fmt.Errorf("compose fragment: project name must not be empty")
	}
	if svc.StableServiceKey == "" {
		return nil, fmt.Errorf("compose fragment: service key must not be empty")
	}

	cs := r.renderer.buildComposeService(svc)

	// Fragment: project name + single service only.
	// Networks and Volumes are left nil so marshalDeterministicYAML omits them,
	// keeping project-wide declarations in the full docker-compose.yml only.
	doc := composeDocument{
		Name: projectName,
		Services: map[string]composeService{
			svc.StableServiceKey: cs,
		},
	}

	yamlBytes, err := marshalDeterministicYAML(doc)
	if err != nil {
		return nil, fmt.Errorf("compose fragment renderer: marshal YAML: %w", err)
	}
	return &FragmentLayout{
		ServiceKey:   svc.StableServiceKey,
		FragmentYAML: yamlBytes,
	}, nil
}

// RenderFragmentForPlan derives the project name from plan, renders a
// single-service Compose fragment, and returns a *FragmentLayout with the
// rendered FragmentYAML populated. This is the entry point used by the
// applier's fragment-apply path via the fragmentRendererFn hook.
func (r *ComposeFragmentRenderer) RenderFragmentForPlan(
	_ context.Context,
	plan *domain.DesiredEnvironmentPlan,
	target *domain.DesiredServiceSpec,
) (*FragmentLayout, error) {
	if plan == nil {
		return nil, fmt.Errorf("compose fragment: plan is nil")
	}
	if target == nil {
		return nil, fmt.Errorf("compose fragment: target service is nil")
	}

	layout, err := r.RenderServiceFragment(deriveProjectName(plan), *target)
	if err != nil {
		return nil, err
	}
	return layout, nil
}
