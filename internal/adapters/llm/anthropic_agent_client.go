package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// AnthropicAgentClientConfig configures the Anthropic /v1/messages adapter.
// The implementation is intentionally deferred to bahia-6hic.12 while
// the item 3 seam is proven with OpenAI-compatible native tool-calling.
type AnthropicAgentClientConfig struct {
	BaseURL    string
	Model      string
	APIKey     string
	MaxTokens  int
	Timeout    time.Duration
	HTTPClient *http.Client
}

// AnthropicAgentClient satisfies AgentModelClient so provider selection can be
// compiled against one seam. Anthropic tool_use/tool_result transport remains
// owned by bahia-6hic.12. It is not wired into production configuration by this
// item.
type AnthropicAgentClient struct {
	baseURL    string
	model      string
	apiKey     string
	maxTokens  int
	httpClient *http.Client
	logger     *slog.Logger
}

// NewAnthropicAgentClient creates the deferred Anthropic agent client seam.
func NewAnthropicAgentClient(config AnthropicAgentClientConfig, logger *slog.Logger) *AnthropicAgentClient {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 4096
	}
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.Timeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicAgentClient{
		baseURL:    strings.TrimRight(config.BaseURL, "/"),
		model:      config.Model,
		apiKey:     config.APIKey,
		maxTokens:  config.MaxTokens,
		httpClient: config.HTTPClient,
		logger:     logger.With("component", "llm_anthropic_agent_client"),
	}
}

// Next returns a typed deferred-provider error until bahia-6hic.12 implements
// Anthropic native tool_use/tool_result serialization.
func (c *AnthropicAgentClient) Next(context.Context, AgentModelRequest, AgentModelEventHandler) (*AgentModelResponse, error) {
	model := strings.TrimSpace(c.model)
	if model == "" {
		model = "unspecified"
	}
	return nil, fmt.Errorf("anthropic agent model client for model %q: %w", model, ErrAgentModelClientNotImplemented)
}

var _ AgentModelClient = (*AnthropicAgentClient)(nil)
