package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestD79IntentContentFixtures(t *testing.T) {
	const (
		org        = "018f6a60-0000-7000-8000-000000000001"
		serviceID  = "018f6a60-0000-7000-8000-000000000002"
		endpoint   = "018f6a60-0000-7000-8000-000000000003"
		version    = "018f6a60-0000-7000-8000-000000000004"
		deployment = "018f6a60-0000-7000-8000-000000000005"
		recipe     = "018f6a60-0000-7000-8000-000000000006"
		credential = "018f6a60-0000-7000-8000-000000000007"
	)
	type shape struct {
		Domain         string         `json:"domain"`
		Op             string         `json:"op"`
		Coordinate     string         `json:"coordinate"`
		Permission     string         `json:"permission"`
		Modelling      string         `json:"modelling"`
		IdempotencyKey string         `json:"idempotency_key"`
		Content        map[string]any `json:"content"`
	}
	fixture := struct {
		Schema  string  `json:"schema"`
		Intents []shape `json:"intents"`
	}{Schema: "bahia.intent-fixtures.d79.v1", Intents: []shape{
		{"ml", "model-import", "model:weather", "fleet-operator", "desired-state", "", map[string]any{"model": "model:weather", "source": "huggingface", "source_uri": "hf://weather/model", "revision": "v1"}},
		{"ml", "recipe-apply", "recipe:train:1", "fleet-operator", "desired-state", "", map[string]any{"name": "train", "version": "1", "yaml": "name: train\nversion: '1'\ninputs: {}\nsteps:\n  - action: fetch_source\n    outputs:\n      source: artifact_ref\noutputs: {}\n"}},
		{"ml", "recipe-run", "recipe-run:" + recipe, "fleet-operator", "request", "", map[string]any{"recipe_id": recipe, "inputs": map[string]any{"dataset": "weather"}}},
		{"ml", "inference-deploy", "inference-deploy:" + endpoint, "fleet-operator", "request", "", map[string]any{"endpoint_id": endpoint, "model_version_id": version}},
		{"ml", "inference-approval", "inference-approval:" + deployment, "fleet-operator", "request", "", map[string]any{"intent_id": deployment, "decision": "approve"}},
		{"ml", "inference-approval", "inference-approval:" + deployment, "fleet-operator", "request", "", map[string]any{"intent_id": deployment, "decision": "reject"}},
		{"ml", "inference-rollback", "inference-rollback:" + endpoint, "fleet-operator", "request", "", map[string]any{"endpoint_id": endpoint}},
		{"tool", "approval-response", "tool-approval:" + deployment, "fleet-operator", "request", "", map[string]any{"intent_id": deployment, "action": "approve", "reason": "operator reviewed"}},
		{"tool", "approval-response", "tool-approval:" + deployment, "fleet-operator", "request", "", map[string]any{"intent_id": deployment, "action": "reject", "reason": "operator reviewed"}},
		{"build", "request", "build-request:" + serviceID, "services:write", "request", "", map[string]any{"service_id": serviceID, "git_ref": "refs/heads/main", "repository_credential_ref": credential, "artifact_repo": "registry.example/api", "build_args": map[string]any{}}},
		{"adoption", "scan", "adoption:" + org, "adoption-operator", "request", "", map[string]any{"targets": []any{map[string]any{"name": "production", "endpoint_ref": "docker-prod"}}, "offset": 0, "limit": 20}},
	}}
	for i := range fixture.Intents {
		fixture.Intents[i].IdempotencyKey = fmt.Sprintf("018f6a60-0000-7000-8000-%012d", i+20)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(current), "..", "..", "web", "tests", "fixtures", "d79-intent-content.json")
	if os.Getenv("BAHIA_REGEN_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	committed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, committed), "regenerate d79-intent-content.json with BAHIA_REGEN_FIXTURE=1")
	for _, item := range fixture.Intents {
		if item.Op == "recipe-apply" {
			_, err := service.ValidateMLRecipeYAML([]byte(item.Content["yaml"].(string)))
			require.NoError(t, err)
		}
		content, err := json.Marshal(item.Content)
		require.NoError(t, err)
		tags := nostr.Tags{{"d", item.Coordinate}, {"domain", item.Domain}, {"op", item.Op}, {"schema", "bahia.intent." + item.Domain + ".v1"}, {"t", "bahia-intent"}, {"intent_id", item.IdempotencyKey}, {"org", org}}
		parsed, err := ParseIntent(&nostr.Event{Kind: 30900, Tags: tags, Content: string(content)})
		require.NoError(t, err, item.Domain+"/"+item.Op)
		require.Equal(t, item.Coordinate, parsed.Coordinate)
	}
}
