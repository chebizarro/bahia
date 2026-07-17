// Package llm provides LLM integration for soul generation.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// SoulGenerator generates agent souls using an LLM.
type SoulGenerator struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client
	logger     *slog.Logger
}

// Config holds LLM configuration.
type Config struct {
	APIKey  string // Anthropic API key
	APIURL  string // API endpoint (default: https://api.anthropic.com)
	Model   string // Model name (default: claude-sonnet-4-20250514)
	Timeout time.Duration
}

// NewSoulGenerator creates a new soul generator.
func NewSoulGenerator(config Config, logger *slog.Logger) *SoulGenerator {
	if config.APIURL == "" {
		config.APIURL = "https://api.anthropic.com"
	}
	if config.Model == "" {
		config.Model = "claude-sonnet-4-20250514"
	}
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &SoulGenerator{
		apiKey: config.APIKey,
		apiURL: config.APIURL,
		model:  config.Model,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger.With("component", "llm"),
	}
}

// Generate creates soul content from a brief.
func (g *SoulGenerator) Generate(ctx context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error) {
	g.logger.Info("generating soul",
		"agent_id", input.AgentID,
		"name", input.Name,
		"tier", input.Tier,
	)

	// Build the prompt
	prompt, err := g.buildPrompt(input)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	// Call LLM
	response, err := g.callLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("call LLM: %w", err)
	}

	// Parse structured output
	output, err := g.parseOutput(response)
	if err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}

	g.logger.Info("soul generated",
		"agent_id", input.AgentID,
		"allowed_kinds", len(output.AllowedKinds),
		"tools", len(output.ToolGrants),
	)

	return output, nil
}

// buildPrompt constructs the LLM prompt from input.
func (g *SoulGenerator) buildPrompt(input domain.SoulGeneratorInput) (string, error) {
	// Use template if provided, otherwise use default
	basePrompt := defaultSoulPrompt
	if input.Template != nil && input.Template.BasePrompt != "" {
		basePrompt = input.Template.BasePrompt
	}

	// Parse and execute template
	tmpl, err := template.New("soul").Parse(basePrompt)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	data := map[string]interface{}{
		"agent_id": input.AgentID,
		"name":     input.Name,
		"brief":    input.Brief,
		"tier":     string(input.Tier),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// callLLM sends a request to the Anthropic API.
func (g *SoulGenerator) callLLM(ctx context.Context, prompt string) (string, error) {
	// Build request body
	reqBody := map[string]interface{}{
		"model":      g.model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"system": systemPrompt,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", g.apiURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	// Send request
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from LLM")
	}

	return apiResp.Content[0].Text, nil
}

// parseOutput extracts structured output from LLM response.
func (g *SoulGenerator) parseOutput(response string) (*domain.SoulGeneratorOutput, error) {
	// Try to find JSON in the response
	// LLM might wrap it in markdown code blocks
	jsonStr := response

	// Strip markdown code blocks if present
	if strings.Contains(response, "```json") {
		start := strings.Index(response, "```json") + 7
		end := strings.LastIndex(response, "```")
		if end > start {
			jsonStr = strings.TrimSpace(response[start:end])
		}
	} else if strings.Contains(response, "```") {
		start := strings.Index(response, "```") + 3
		end := strings.LastIndex(response, "```")
		if end > start {
			jsonStr = strings.TrimSpace(response[start:end])
		}
	}

	// Parse JSON
	var output domain.SoulGeneratorOutput
	if err := json.Unmarshal([]byte(jsonStr), &output); err != nil {
		return nil, fmt.Errorf("parse LLM soul output as JSON: %w", err)
	}

	// Validate required fields
	if output.SoulMD == "" {
		return nil, fmt.Errorf("missing soul_md in output")
	}

	return &output, nil
}

// System prompt for soul generation
const systemPrompt = `You are an expert agent designer for a Nostr-native AI agent fleet. Your task is to design complete agent personalities based on briefs provided by users.

You must respond with a JSON object containing the agent's soul. The JSON must be valid and complete.

Key principles:
- Agents should have distinct personalities that serve their purpose
- Tool grants should be minimal (principle of least privilege)
- Allowed kinds should match the agent's communication needs
- SOUL.md should be written in first person, as the agent describing itself`

// Default prompt template for soul generation
const defaultSoulPrompt = `# Agent Design Request

Design an agent with the following specifications:

**Agent ID:** {{.agent_id}}
**Display Name:** {{.name}}
**Tier:** {{.tier}}

## Mission Brief

{{.brief}}

## Your Task

Generate a complete agent soul. Return a JSON object with these fields:

{
  "soul_md": "# Agent Name\n\nFirst-person description of who I am, how I work, my communication style, and my boundaries...",
  "identity_md": "# Agent Name — Role\n\nPublic-facing identity document...",
  "allowed_kinds": [0, 1, 4],
  "tool_grants": [
    {"server": "agent-memory", "scopes": ["read", "write"]}
  ],
  "avatar_prompt": "Description for generating a visual avatar...",
  "personality_tags": ["helpful", "precise", "reliable"]
}

Guidelines:
- soul_md: Write in first person. Include: identity, methodology, communication style, boundaries
- identity_md: Public-facing. Include: name, role, capabilities, how to interact
- allowed_kinds: 0 (profile), 1 (notes), 4 (DMs) are typical. Add others based on role.
- tool_grants: Minimum necessary. Common servers: agent-memory, git, web-fetch, nostr
- avatar_prompt: Visual description for FLUX/Stable Diffusion
- personality_tags: 3-5 adjectives describing the agent

Return ONLY the JSON object, no additional text.`

// Ensure SoulGenerator implements the interface
var _ interface {
	Generate(ctx context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error)
} = (*SoulGenerator)(nil)
