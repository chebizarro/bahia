package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMLClosedValueSets(t *testing.T) {
	require.True(t, MLTaskKindChatCompletions.IsValid())
	require.True(t, MLArtifactKindEvaluationReport.IsValid())
	require.True(t, MLArtifactFormatRKNN.IsValid())
	require.True(t, MLRuntimeKindTensorRTLLM.IsValid())

	require.False(t, MLTaskKind("queue_polling").IsValid())
	require.False(t, MLArtifactFormat("tarball_guess").IsValid())
	require.False(t, MLRuntimeKind("rpc_runtime").IsValid())
}

func TestValidateMLModelVersion(t *testing.T) {
	version := &MLModelVersion{
		ModelID: uuid.New(),
		Version: "v1",
		Source:  MLSourceRef{Kind: ModelSourceHuggingFace, URI: "hf://Qwen/Qwen2.5-Coder"},
		RuntimeRequirements: MLRuntimeRequirements{
			PreferredRuntimes: []MLRuntimeKind{MLRuntimeKindVLLM},
			RequiredFormats:   []MLArtifactFormat{MLArtifactFormatSafeTensors},
		},
	}
	require.NoError(t, ValidateMLModelVersion(version))

	version.RuntimeRequirements.PreferredRuntimes = []MLRuntimeKind{"poller"}
	require.Error(t, ValidateMLModelVersion(version))
}

func TestValidateMLArtifactRef(t *testing.T) {
	artifact := &MLArtifactRef{Kind: MLArtifactKindModel, Format: MLArtifactFormatONNX, URI: "oci://models/example@sha256:abc"}
	require.NoError(t, ValidateMLArtifactRef(artifact))
	artifact.Format = "zip"
	require.Error(t, ValidateMLArtifactRef(artifact))
}
