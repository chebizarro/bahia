package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateLLMRouteName(t *testing.T) {
	require.NoError(t, ValidateLLMRouteName("chat-prod"))
	require.Error(t, ValidateLLMRouteName(""))
	require.Error(t, ValidateLLMRouteName("Chat"))
	require.Error(t, ValidateLLMRouteName("chat_underscored"))
	require.Error(t, ValidateLLMRouteName("chat-"))
}

func TestValidateLLMReleaseConfig(t *testing.T) {
	base := &LLMRelease{
		RouteID:            uuid.New(),
		Version:            "v1",
		ModelRef:           "meta-llama/Llama-3",
		ModelSource:        ModelSourceHuggingFace,
		BackendPreferences: []LLMBackendKind{LLMBackendKindVLLM},
		RuntimeBackend: &LLMRuntimeManagedBackendConfig{
			Image:         "vllm/vllm-openai:latest",
			ContainerPort: 8000,
			HostPort:      18000,
			HealthPath:    "/health",
		},
		PromotionGate: &LLMPromotionGateConfig{
			IntervalSeconds:  1,
			TimeoutSeconds:   30,
			SuccessThreshold: 1,
			FailureThreshold: 2,
		},
	}
	require.NoError(t, ValidateLLMReleaseConfig(base))

	missingRuntime := *base
	missingRuntime.RuntimeBackend = nil
	require.Error(t, ValidateLLMReleaseConfig(&missingRuntime))

	external := *base
	external.BackendPreferences = []LLMBackendKind{LLMBackendKindExternalAPI}
	external.RuntimeBackend = nil
	external.ExternalBackend = &LLMExternalBackendConfig{BaseURL: "https://llm.example"}
	require.NoError(t, ValidateLLMReleaseConfig(&external))

	external.ExternalBackend = &LLMExternalBackendConfig{}
	require.Error(t, ValidateLLMReleaseConfig(&external))
}
