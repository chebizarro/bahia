package soulfactory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestMapSoulMemorySpecToOpenClawMemorySearch(t *testing.T) {
	mapping, err := MapSoulMemorySpec(domain.SoulMemorySpec{
		EmbeddingProvider: "VoyageAI",
		EmbeddingModel:    "voyage-3",
		Strategy:          "session-aware",
		AutoIndex:         true,
		RetentionDays:     90,
		Search: &domain.SoulMemorySearchSpec{
			TopK:           10,
			ScoreThreshold: 0.7,
			Rerank:         true,
			RerankModel:    "cohere-rerank-v3",
		},
	})
	if err != nil {
		t.Fatalf("MapSoulMemorySpec error = %v", err)
	}
	if mapping.Schema != SoulFactoryMemoryConfigSchema || mapping.Provider != MemoryEmbeddingProviderVoyage || mapping.Model != "voyage-3" {
		t.Fatalf("normalized mapping = %+v", mapping)
	}
	if mapping.Strategy != MemoryStrategySessionAware || !mapping.AutoIndex || mapping.RetentionDays != 90 {
		t.Fatalf("strategy/index/retention mapping = %+v", mapping)
	}
	if mapping.Search.TopK != 10 || mapping.Search.ScoreThreshold != 0.7 || !mapping.Search.Rerank || mapping.Search.RerankModel != "cohere-rerank-v3" {
		t.Fatalf("portable search mapping = %+v", mapping.Search)
	}
	openclaw := mapping.OpenClaw
	if !openclaw.Enabled || openclaw.Provider != "voyage" || openclaw.Model != "voyage-3" {
		t.Fatalf("OpenClaw provider mapping = %+v", openclaw)
	}
	if len(openclaw.Sources) != 2 || openclaw.Sources[0] != "memory" || openclaw.Sources[1] != "sessions" {
		t.Fatalf("OpenClaw sources = %#v", openclaw.Sources)
	}
	if openclaw.Experimental == nil || !openclaw.Experimental.SessionMemory {
		t.Fatalf("OpenClaw experimental mapping = %+v", openclaw.Experimental)
	}
	if openclaw.Sync == nil || !openclaw.Sync.OnSessionStart || !openclaw.Sync.OnSearch || !openclaw.Sync.Watch {
		t.Fatalf("OpenClaw sync mapping = %+v", openclaw.Sync)
	}
	if openclaw.Query == nil || openclaw.Query.MaxResults != 10 || openclaw.Query.MinScore != 0.7 {
		t.Fatalf("OpenClaw query mapping = %+v", openclaw.Query)
	}
	if openclaw.Query.Hybrid == nil || openclaw.Query.Hybrid.MMR == nil || !openclaw.Query.Hybrid.MMR.Enabled {
		t.Fatalf("OpenClaw rerank/MMR mapping = %+v", openclaw.Query.Hybrid)
	}
}

func TestMapSoulMemorySpecSupportsLocalEphemeralStrategy(t *testing.T) {
	mapping, err := MapSoulMemorySpec(domain.SoulMemorySpec{
		EmbeddingProvider: "llama-cpp",
		EmbeddingModel:    "nomic-embed-text",
		Strategy:          "off",
	})
	if err != nil {
		t.Fatalf("MapSoulMemorySpec local error = %v", err)
	}
	if mapping.Provider != MemoryEmbeddingProviderLocal || mapping.Strategy != MemoryStrategyEphemeral {
		t.Fatalf("local/ephemeral normalization = %+v", mapping)
	}
	if mapping.OpenClaw.Enabled || mapping.OpenClaw.Provider != "local" || mapping.OpenClaw.Model != "nomic-embed-text" {
		t.Fatalf("OpenClaw local ephemeral config = %+v", mapping.OpenClaw)
	}
}

func TestValidateSoulMemorySpecRejectsInvalidSearchConfig(t *testing.T) {
	err := ValidateSoulMemorySpec(domain.SoulMemorySpec{
		EmbeddingProvider: "redis",
		Strategy:          "forever",
		RetentionDays:     -1,
		Search: &domain.SoulMemorySearchSpec{
			TopK:           101,
			ScoreThreshold: 1.1,
			Rerank:         false,
			RerankModel:    "cohere-rerank-v3",
		},
	})
	if err == nil {
		t.Fatal("ValidateSoulMemorySpec error = nil, want validation error")
	}
	message := err.Error()
	for _, want := range []string{
		"embedding_provider",
		"strategy",
		"retention_days",
		"search.top_k",
		"search.score_threshold",
		"search.rerank_model",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("validation error %q missing %q", message, want)
		}
	}
}

func TestBuildMemoryConfigureRuntimeParamsSerializesInto38384Envelope(t *testing.T) {
	params, err := BuildMemoryConfigureRuntimeParams(domain.SoulMemorySpec{
		EmbeddingProvider: "openai",
		EmbeddingModel:    "text-embedding-3-small",
		Strategy:          "long-term",
		Search:            &domain.SoulMemorySearchSpec{TopK: 5, ScoreThreshold: 0.25},
	})
	if err != nil {
		t.Fatalf("BuildMemoryConfigureRuntimeParams error = %v", err)
	}
	envelope := RuntimeControlEnvelope{
		Method:         RuntimeMethodMemoryConfigure,
		IdempotencyKey: "sha256:memory-config",
		RequestedAt:    1715700000,
		Operator:       RuntimeOperatorRef{Pubkey: "operator", RequestEvent: "1950-event"},
		Controller:     RuntimeControllerRef{Pubkey: "controller"},
		Target:         RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey", AgentID: "scout"},
		Soul:           RuntimeSoulRef{ID: "scout", Draft: "draft-event", SpecHash: "sha256:spec"},
		Params:         params,
	}
	event, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	parsed, err := ParseRuntimeControlRequestEvent(event)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
	}
	if parsed.Method != RuntimeMethodMemoryConfigure || parsed.Params["schema"] != SoulFactoryMemoryConfigSchema || parsed.Params["config_mode"] != "replace" {
		t.Fatalf("parsed envelope params = %+v", parsed)
	}
	encoded, err := json.Marshal(parsed.Params)
	if err != nil {
		t.Fatalf("marshal parsed params error = %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal parsed params error = %v", err)
	}
	memoryConfig, ok := decoded["memory_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("memory_config missing or wrong type: %#v", decoded["memory_config"])
	}
	if memoryConfig["embedding_provider"] != "openai" || memoryConfig["embedding_model"] != "text-embedding-3-small" {
		t.Fatalf("memory_config provider/model = %#v", memoryConfig)
	}
	if _, hasRuntimeSpecificConfig := memoryConfig["openclaw_memory_search"]; hasRuntimeSpecificConfig {
		t.Fatalf("portable memory_config must not embed OpenClaw-native config: %#v", memoryConfig)
	}
	openclaw := decoded["openclaw"].(map[string]interface{})
	if openclaw["config_mode"] != "replace_memorySearch" {
		t.Fatalf("OpenClaw config mode = %#v", openclaw["config_mode"])
	}
	agents := openclaw["agents"].(map[string]interface{})
	defaults := agents["defaults"].(map[string]interface{})
	memorySearch := defaults["memorySearch"].(map[string]interface{})
	if memorySearch["provider"] != "openai" || memorySearch["model"] != "text-embedding-3-small" {
		t.Fatalf("OpenClaw memorySearch provider/model = %#v", memorySearch)
	}
	query := memorySearch["query"].(map[string]interface{})
	if query["maxResults"] != float64(5) || query["minScore"] != 0.25 {
		t.Fatalf("OpenClaw memorySearch query = %#v", query)
	}
}
