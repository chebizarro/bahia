package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateMLRecipeYAMLFixtures(t *testing.T) {
	fixtures := []string{
		"hf-vllm-import-deploy.yaml",
		"onnx-rknn-edge-deploy.yaml",
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			src := readRecipeFixture(t, fixture)
			normalized, err := ValidateMLRecipeYAML(src)
			require.NoError(t, err)
			require.NotEmpty(t, normalized["name"])
			require.NotEmpty(t, normalized["steps"])
		})
	}
}

func TestValidateMLRecipeYAMLRejectsMissingRequiredFields(t *testing.T) {
	_, err := ValidateMLRecipeYAML([]byte(`
name: broken
version: 1
steps:
  - action: fetch_source
outputs: {}
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "inputs")
}

func TestValidateMLRecipeYAMLRejectsInvalidStepContract(t *testing.T) {
	_, err := ValidateMLRecipeYAML([]byte(`
name: broken-deploy
version: 1
inputs:
  model: {}
steps:
  - action: deploy_endpoint
    runtime: vllm
    inputs:
      artifact: artifact_ref
    outputs:
      endpoint: endpoint
outputs:
  endpoint: {}
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "target")
}

func TestValidateMLRecipeYAMLRejectsInvalidRetryPolicy(t *testing.T) {
	_, err := ValidateMLRecipeYAML([]byte(`
name: broken-retry
version: 1
inputs:
  model: {}
steps:
  - action: fetch_source
    outputs:
      source: artifact_ref
    retry_policy:
      max_attempts: -1
outputs:
  source: {}
`))
	require.Error(t, err)
}

func TestApplyValidatedRecipeYAMLStoresNormalizedJSON(t *testing.T) {
	recipe := &domain.MLRecipe{YAML: string(readRecipeFixture(t, "hf-vllm-import-deploy.yaml"))}
	require.NoError(t, ApplyValidatedRecipeYAML(recipe))
	require.Equal(t, "hf-vllm-import-deploy", recipe.NormalizedJSON["name"])
	require.Equal(t, "hf-vllm-import-deploy", recipe.Name)
	require.Equal(t, "1", recipe.Version)
}

func readRecipeFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "test", "fixtures", "ml_recipes", name)
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	return src
}
