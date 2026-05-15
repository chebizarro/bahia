package soulfactory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	// SoulFactoryMemoryConfigSchema versions the portable memory config payload
	// embedded in kind:38384 soulfactory.memory.configure params.
	SoulFactoryMemoryConfigSchema  = "soulfactory-memory-config/v1"
	SoulFactoryMemoryStatusSchema  = "soulfactory-memory-status/v1"
	SoulFactoryMemoryReindexSchema = "soulfactory-memory-reindex/v1"

	MemoryEmbeddingProviderAuto   = "auto"
	MemoryEmbeddingProviderVoyage = "voyage"
	MemoryEmbeddingProviderOpenAI = "openai"
	MemoryEmbeddingProviderCohere = "cohere"
	MemoryEmbeddingProviderLocal  = "local"

	MemoryStrategySessionAware = "session-aware"
	MemoryStrategyLongTerm     = "long-term"
	MemoryStrategyEphemeral    = "ephemeral"

	MemorySearchTopKMin = 1
	MemorySearchTopKMax = 100

	MemoryReindexModeIncremental = "incremental"
	MemoryReindexModeFull        = "full"
)

var supportedMemoryEmbeddingProviders = map[string]string{
	"":                 MemoryEmbeddingProviderAuto,
	"auto":             MemoryEmbeddingProviderAuto,
	"voyage":           MemoryEmbeddingProviderVoyage,
	"voyageai":         MemoryEmbeddingProviderVoyage,
	"voyage-ai":        MemoryEmbeddingProviderVoyage,
	"openai":           MemoryEmbeddingProviderOpenAI,
	"open-ai":          MemoryEmbeddingProviderOpenAI,
	"cohere":           MemoryEmbeddingProviderCohere,
	"cohereai":         MemoryEmbeddingProviderCohere,
	"cohere-ai":        MemoryEmbeddingProviderCohere,
	"local":            MemoryEmbeddingProviderLocal,
	"llama":            MemoryEmbeddingProviderLocal,
	"llama-cpp":        MemoryEmbeddingProviderLocal,
	"node-llama-cpp":   MemoryEmbeddingProviderLocal,
	"node_llama_cpp":   MemoryEmbeddingProviderLocal,
	"nomic-embed-text": MemoryEmbeddingProviderLocal,
}

var supportedMemoryStrategies = map[string]string{
	"":              MemoryStrategySessionAware,
	"session-aware": MemoryStrategySessionAware,
	"session_aware": MemoryStrategySessionAware,
	"sessionaware":  MemoryStrategySessionAware,
	"long-term":     MemoryStrategyLongTerm,
	"long_term":     MemoryStrategyLongTerm,
	"longterm":      MemoryStrategyLongTerm,
	"ephemeral":     MemoryStrategyEphemeral,
	"off":           MemoryStrategyEphemeral,
	"disabled":      MemoryStrategyEphemeral,
}

// MemorySpecValidationError reports all invalid memory spec fields at once so
// draft editors can surface complete feedback instead of failing one field at a time.
type MemorySpecValidationError struct {
	Violations []string
}

func (e *MemorySpecValidationError) Error() string {
	if e == nil || len(e.Violations) == 0 {
		return "invalid memory spec"
	}
	return "invalid memory spec: " + strings.Join(e.Violations, "; ")
}

// MemoryConfigService maps Bahia memory specs into runtime-control params.
// It is pure and deterministic; runtimes report applied config and memory stats
// through kind:38386 result events.
type MemoryConfigService struct{}

func NewMemoryConfigService() MemoryConfigService { return MemoryConfigService{} }

// MemoryConfigMapping is the normalized service result produced from a
// domain.SoulMemorySpec before it is serialized into runtime control params.
type MemoryConfigMapping struct {
	Schema        string                     `json:"schema"`
	Provider      string                     `json:"embedding_provider"`
	Model         string                     `json:"embedding_model,omitempty"`
	Strategy      string                     `json:"strategy"`
	AutoIndex     bool                       `json:"auto_index"`
	RetentionDays int                        `json:"retention_days,omitempty"`
	Search        MemorySearchRuntimeConfig  `json:"search"`
	OpenClaw      OpenClawMemorySearchConfig `json:"-"`
}

// MemorySearchRuntimeConfig is the portable search contract retained in
// soulfactory.memory.configure params, even when an individual runtime maps only
// part of it to native config fields.
type MemorySearchRuntimeConfig struct {
	TopK           int     `json:"top_k,omitempty"`
	ScoreThreshold float64 `json:"score_threshold,omitempty"`
	Rerank         bool    `json:"rerank"`
	RerankModel    string  `json:"rerank_model,omitempty"`
}

// OpenClawMemorySearchConfig mirrors the OpenClaw agents.defaults.memorySearch
// fields used by Bahia today. It intentionally avoids transport side effects;
// runtimes apply this config through Nostr 38384 control requests.
type OpenClawMemorySearchConfig struct {
	Enabled      bool                              `json:"enabled"`
	Sources      []string                          `json:"sources,omitempty"`
	Provider     string                            `json:"provider,omitempty"`
	Model        string                            `json:"model,omitempty"`
	Experimental *OpenClawMemoryExperimentalConfig `json:"experimental,omitempty"`
	Sync         *OpenClawMemorySyncConfig         `json:"sync,omitempty"`
	Query        *OpenClawMemoryQueryConfig        `json:"query,omitempty"`
}

type OpenClawMemoryExperimentalConfig struct {
	SessionMemory bool `json:"sessionMemory,omitempty"`
}

type OpenClawMemorySyncConfig struct {
	OnSessionStart bool `json:"onSessionStart,omitempty"`
	OnSearch       bool `json:"onSearch,omitempty"`
	Watch          bool `json:"watch,omitempty"`
}

type OpenClawMemoryQueryConfig struct {
	MaxResults int                         `json:"maxResults,omitempty"`
	MinScore   float64                     `json:"minScore,omitempty"`
	Hybrid     *OpenClawMemoryHybridConfig `json:"hybrid,omitempty"`
}

type OpenClawMemoryHybridConfig struct {
	MMR *OpenClawMemoryMMRConfig `json:"mmr,omitempty"`
}

type OpenClawMemoryMMRConfig struct {
	Enabled bool `json:"enabled"`
}

// MemoryReindexRequest is the portable params contract for kind:38384
// soulfactory.memory.reindex requests. The runtime reports progress and terminal
// stats through its kind:38386 result event; lifecycle hot-reload also emits
// kind:6950 progress around the control request.
type MemoryReindexRequest struct {
	Schema            string              `json:"schema"`
	Mode              string              `json:"mode"`
	Reason            string              `json:"reason,omitempty"`
	MemoryConfig      MemoryConfigMapping `json:"memory_config"`
	RetentionDays     int                 `json:"retention_days,omitempty"`
	EnforceRetention  bool                `json:"enforce_retention"`
	PreviousSpecHash  string              `json:"previous_spec_hash,omitempty"`
	NewSpecHash       string              `json:"new_spec_hash,omitempty"`
	DraftRef          string              `json:"draft_ref,omitempty"`
	DraftEventID      string              `json:"draft_event_id,omitempty"`
	ProgressEventKind int                 `json:"progress_event_kind"`
	ResultEventKind   int                 `json:"result_event_kind"`
	OpenClaw          map[string]any      `json:"openclaw,omitempty"`
}

// ValidateSoulMemorySpec validates Bahia's portable SoulMemorySpec before it is
// mapped to runtime-native config. Empty provider/strategy are accepted and
// normalized by MapSoulMemorySpec.
func ValidateSoulMemorySpec(spec domain.SoulMemorySpec) error {
	var violations []string
	if _, ok := normalizeMemoryProvider(spec.EmbeddingProvider); !ok {
		violations = append(violations, fmt.Sprintf("embedding_provider %q is unsupported; supported providers are auto, voyage, openai, cohere, local", spec.EmbeddingProvider))
	}
	if _, ok := normalizeMemoryStrategy(spec.Strategy); !ok {
		violations = append(violations, fmt.Sprintf("strategy %q is unsupported; supported strategies are session-aware, long-term, ephemeral", spec.Strategy))
	}
	if spec.RetentionDays < 0 {
		violations = append(violations, "retention_days must be >= 0")
	}
	if spec.Search != nil {
		if spec.Search.TopK != 0 && (spec.Search.TopK < MemorySearchTopKMin || spec.Search.TopK > MemorySearchTopKMax) {
			violations = append(violations, fmt.Sprintf("search.top_k must be between %d and %d", MemorySearchTopKMin, MemorySearchTopKMax))
		}
		if spec.Search.ScoreThreshold < 0 || spec.Search.ScoreThreshold > 1 {
			violations = append(violations, "search.score_threshold must be between 0 and 1")
		}
		if strings.TrimSpace(spec.Search.RerankModel) != "" && !spec.Search.Rerank {
			violations = append(violations, "search.rerank_model requires search.rerank=true")
		}
	}
	if len(violations) > 0 {
		return &MemorySpecValidationError{Violations: violations}
	}
	return nil
}

// MapSoulMemorySpec normalizes SoulMemorySpec and builds both the portable
// runtime contract and OpenClaw-native memorySearch config.
func MapSoulMemorySpec(spec domain.SoulMemorySpec) (MemoryConfigMapping, error) {
	if err := ValidateSoulMemorySpec(spec); err != nil {
		return MemoryConfigMapping{}, err
	}
	provider, _ := normalizeMemoryProvider(spec.EmbeddingProvider)
	strategy, _ := normalizeMemoryStrategy(spec.Strategy)
	search := MemorySearchRuntimeConfig{}
	if spec.Search != nil {
		search = MemorySearchRuntimeConfig{
			TopK:           spec.Search.TopK,
			ScoreThreshold: spec.Search.ScoreThreshold,
			Rerank:         spec.Search.Rerank,
			RerankModel:    strings.TrimSpace(spec.Search.RerankModel),
		}
	}
	openclaw := buildOpenClawMemorySearchConfig(provider, strings.TrimSpace(spec.EmbeddingModel), strategy, spec.AutoIndex, search)
	return MemoryConfigMapping{
		Schema:        SoulFactoryMemoryConfigSchema,
		Provider:      provider,
		Model:         strings.TrimSpace(spec.EmbeddingModel),
		Strategy:      strategy,
		AutoIndex:     spec.AutoIndex,
		RetentionDays: spec.RetentionDays,
		Search:        search,
		OpenClaw:      openclaw,
	}, nil
}

func (s MemoryConfigService) Map(spec domain.SoulMemorySpec) (MemoryConfigMapping, error) {
	return MapSoulMemorySpec(spec)
}

func (s MemoryConfigService) BuildConfigureRuntimeParams(spec domain.SoulMemorySpec) (map[string]interface{}, error) {
	mapping, err := s.Map(spec)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"schema":        SoulFactoryMemoryConfigSchema,
		"config_mode":   "replace",
		"memory_config": mapping,
		"openclaw": map[string]interface{}{
			"config_mode": "replace_memorySearch",
			"agents": map[string]interface{}{
				"defaults": map[string]interface{}{
					"memorySearch": mapping.OpenClaw,
				},
			},
		},
	}, nil
}

func (s MemoryConfigService) BuildStatusRuntimeParams() map[string]interface{} {
	return map[string]interface{}{
		"schema":  SoulFactoryMemoryStatusSchema,
		"include": []string{"config", "stats"},
	}
}

// BuildMemoryConfigureRuntimeParams serializes the memory configuration contract
// to the params object embedded in a kind:38384 soulfactory.memory.configure
// RuntimeControlEnvelope.
func BuildMemoryConfigureRuntimeParams(spec domain.SoulMemorySpec) (map[string]interface{}, error) {
	return NewMemoryConfigService().BuildConfigureRuntimeParams(spec)
}

func BuildMemoryStatusRuntimeParams() map[string]interface{} {
	return NewMemoryConfigService().BuildStatusRuntimeParams()
}

// MarshalMemoryConfigureRuntimeParams returns stable JSON for callers that need
// to persist or diff the exact 38384 params payload before signing the event.
func MarshalMemoryConfigureRuntimeParams(spec domain.SoulMemorySpec) ([]byte, error) {
	params, err := BuildMemoryConfigureRuntimeParams(spec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(params)
}

// BuildMemoryReindexRuntimeParams builds the params object embedded in a
// kind:38384 soulfactory.memory.reindex RuntimeControlEnvelope.
func BuildMemoryReindexRuntimeParams(spec domain.SoulMemorySpec, mode, reason, previousSpecHash, newSpecHash, draftRef, draftEventID string) (map[string]interface{}, error) {
	mapping, err := MapSoulMemorySpec(spec)
	if err != nil {
		return nil, err
	}
	mode = normalizeMemoryReindexMode(mode)
	if mode == "" {
		return nil, fmt.Errorf("memory reindex mode must be incremental or full")
	}
	req := MemoryReindexRequest{
		Schema:            SoulFactoryMemoryReindexSchema,
		Mode:              mode,
		Reason:            strings.TrimSpace(reason),
		MemoryConfig:      mapping,
		RetentionDays:     mapping.RetentionDays,
		EnforceRetention:  mapping.RetentionDays > 0,
		PreviousSpecHash:  strings.TrimSpace(previousSpecHash),
		NewSpecHash:       strings.TrimSpace(newSpecHash),
		DraftRef:          strings.TrimSpace(draftRef),
		DraftEventID:      strings.TrimSpace(draftEventID),
		ProgressEventKind: domain.KindProvisioningStatus,
		ResultEventKind:   domain.KindRuntimeControlResult,
		OpenClaw: map[string]any{
			"operation":      "memory.reindex",
			"mode":           mode,
			"memorySearch":   mapping.OpenClaw,
			"retention_days": mapping.RetentionDays,
			"progress_kind":  domain.KindProvisioningStatus,
			"result_kind":    domain.KindRuntimeControlResult,
		},
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var params map[string]interface{}
	if err := json.Unmarshal(encoded, &params); err != nil {
		return nil, err
	}
	return params, nil
}

func normalizeMemoryReindexMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", MemoryReindexModeIncremental, "increment", "partial":
		return MemoryReindexModeIncremental
	case MemoryReindexModeFull, "complete", "rebuild":
		return MemoryReindexModeFull
	default:
		return ""
	}
}

func buildOpenClawMemorySearchConfig(provider, model, strategy string, autoIndex bool, search MemorySearchRuntimeConfig) OpenClawMemorySearchConfig {
	cfg := OpenClawMemorySearchConfig{
		Enabled:  strategy != MemoryStrategyEphemeral,
		Provider: provider,
		Model:    model,
	}
	if strategy == MemoryStrategySessionAware {
		cfg.Sources = []string{"memory", "sessions"}
		cfg.Experimental = &OpenClawMemoryExperimentalConfig{SessionMemory: true}
	} else if strategy == MemoryStrategyLongTerm {
		cfg.Sources = []string{"memory"}
	}
	if autoIndex {
		cfg.Sync = &OpenClawMemorySyncConfig{OnSessionStart: true, OnSearch: true, Watch: true}
	}
	if search.TopK != 0 || search.ScoreThreshold != 0 || search.Rerank {
		cfg.Query = &OpenClawMemoryQueryConfig{MaxResults: search.TopK, MinScore: search.ScoreThreshold}
		if search.Rerank {
			cfg.Query.Hybrid = &OpenClawMemoryHybridConfig{MMR: &OpenClawMemoryMMRConfig{Enabled: true}}
		}
	}
	return cfg
}

func normalizeMemoryProvider(provider string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(provider))
	value, ok := supportedMemoryEmbeddingProviders[key]
	return value, ok
}

func normalizeMemoryStrategy(strategy string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(strategy))
	value, ok := supportedMemoryStrategies[key]
	return value, ok
}
