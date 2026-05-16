package service

import (
	"embed"
	"encoding/json"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/encoding/yaml"
	"github.com/openagentsinc/bahia/internal/domain"
)

//go:embed ml_recipe_schema.cue
var mlRecipeSchemaFS embed.FS

// ValidateMLRecipeYAML validates a human-authored ML recipe YAML document against
// the CUE recipe contract and returns the normalized JSON representation used by
// read models/persistence.
func ValidateMLRecipeYAML(src []byte) (map[string]any, error) {
	ctx := cuecontext.New()

	schemaBytes, err := mlRecipeSchemaFS.ReadFile("ml_recipe_schema.cue")
	if err != nil {
		return nil, fmt.Errorf("reading ML recipe schema: %w", err)
	}
	schema := ctx.CompileBytes(schemaBytes, cue.Filename("ml_recipe_schema.cue"))
	if err := schema.Err(); err != nil {
		return nil, fmt.Errorf("compiling ML recipe schema: %w", err)
	}

	file, err := yaml.Extract("recipe.yaml", src)
	if err != nil {
		return nil, fmt.Errorf("parsing ML recipe YAML: %w", err)
	}
	data := ctx.BuildFile(file)
	if err := data.Err(); err != nil {
		return nil, fmt.Errorf("building ML recipe YAML: %w", err)
	}

	validated := schema.LookupPath(cue.ParsePath("#Recipe")).Unify(data)
	if err := validated.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("validating ML recipe YAML: %w", err)
	}

	jsonBytes, err := validated.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("normalizing ML recipe YAML: %w", err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(jsonBytes, &normalized); err != nil {
		return nil, fmt.Errorf("decoding normalized ML recipe: %w", err)
	}
	return normalized, nil
}

// ApplyValidatedRecipeYAML validates recipe.YAML and stores its normalized JSON.
func ApplyValidatedRecipeYAML(recipe *domain.MLRecipe) error {
	if recipe == nil {
		return fmt.Errorf("ML recipe is required")
	}
	normalized, err := ValidateMLRecipeYAML([]byte(recipe.YAML))
	if err != nil {
		return err
	}
	recipe.NormalizedJSON = normalized
	if name, ok := normalized["name"].(string); ok {
		recipe.Name = name
	}
	switch version := normalized["version"].(type) {
	case string:
		recipe.Version = version
	case float64:
		recipe.Version = fmt.Sprintf("%.0f", version)
	}
	return nil
}
