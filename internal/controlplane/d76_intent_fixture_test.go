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
	"github.com/stretchr/testify/require"
)

// The Go-generated content fixtures are the wire contract for the six D76
// actions. Regenerate deliberately with BAHIA_REGEN_FIXTURE=1.
func TestD76IntentContentFixtures(t *testing.T) {
	const (
		orgID       = "018f6a60-0000-7000-8000-000000000001"
		serviceID   = "018f6a60-0000-7000-8000-000000000002"
		envID       = "018f6a60-0000-7000-8000-000000000003"
		buildID     = "018f6a60-0000-7000-8000-000000000004"
		artifactID  = "018f6a60-0000-7000-8000-000000000005"
		intentID    = "018f6a60-0000-7000-8000-000000000006"
		imageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	type item struct {
		Domain     string         `json:"domain"`
		Op         string         `json:"op"`
		Coordinate string         `json:"coordinate"`
		Permission string         `json:"permission"`
		Modelling  string         `json:"modelling"`
		Content    map[string]any `json:"content"`
	}
	fixture := struct {
		Schema  string `json:"schema"`
		Intents []item `json:"intents"`
	}{Schema: "bahia.intent-fixtures.d76.v1", Intents: []item{
		{"artifact", "register", "artifact:" + artifactID, "services:write", "desired-state",
			map[string]any{"id": artifactID, "build_id": buildID, "service_id": serviceID, "image_repo": "registry.example/api", "image_tag": "v1", "image_digest": imageDigest, "intent_id": intentID}},
		{"artifact", "import-observed", "artifact-import:" + serviceID + ":" + envID + ":" + imageDigest, "services:write", "desired-state",
			map[string]any{"service_id": serviceID, "environment_id": envID, "image_repo": "registry.example/api", "image_tag": "v1", "image_digest": imageDigest, "intent_id": intentID}},
		{"adoption", "import", "adoption:" + orgID, "adoption-operator", "desired-state",
			map[string]any{"org_id": orgID, "targets": []any{map[string]any{"name": "production", "endpoint_ref": "docker-prod"}}, "selections": []any{map[string]any{"target_name": "production", "container_id": "container-123"}}, "intent_id": intentID}},
		{"dns", "drift-remediate", "dns-remediate:example.com", "fleet-operator", "request",
			map[string]any{"zone": "example.com", "intent_id": intentID}},
		{"deployment", "preview", "deployment-preview:" + serviceID + ":" + envID, "deployments:write", "request",
			map[string]any{"service_id": serviceID, "environment_id": envID, "artifact_id": artifactID, "managed_runtime_config": map[string]any{"replicas": 2}, "compact": true, "intent_id": intentID}},
		{"deployment", "route-attach", "deployment-route:" + serviceID + ":" + envID, "deployments:write", "desired-state",
			map[string]any{"service_id": serviceID, "environment_id": envID, "public_route": map[string]any{"hostname": "api.example.com"}, "expected_updated_at": "2026-10-03T12:00:00Z", "intent_id": intentID}},
	}}
	for i := range fixture.Intents {
		fixture.Intents[i].Content["intent_id"] = fmt.Sprintf("018f6a60-0000-7000-8000-%012d", i+6)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "web", "tests", "fixtures", "d76-intent-content.json")
	if os.Getenv("BAHIA_REGEN_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	committed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, committed), "regenerate d76-intent-content.json with BAHIA_REGEN_FIXTURE=1")
	for _, shape := range fixture.Intents {
		content, err := json.Marshal(shape.Content)
		require.NoError(t, err)
		tags := nostr.Tags{{"d", shape.Coordinate}, {"domain", shape.Domain}, {"op", shape.Op}, {"schema", "bahia.intent." + shape.Domain + ".v1"}, {"t", "bahia-intent"}, {"intent_id", shape.Content["intent_id"].(string)}}
		if shape.Domain != "dns" {
			tags = append(tags, nostr.Tag{"org", orgID})
		}
		parsed, err := ParseIntent(&nostr.Event{Kind: 30900, Tags: tags, Content: string(content)})
		require.NoError(t, err, shape.Domain+"/"+shape.Op)
		require.Equal(t, shape.Coordinate, parsed.Coordinate)
		require.Equal(t, shape.Op, parsed.Op)
	}
}
