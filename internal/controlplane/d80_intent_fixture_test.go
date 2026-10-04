package controlplane

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestD80IntentContentFixtures(t *testing.T) {
	const (
		orgID      = "018f6a60-0000-7000-8000-000000000001"
		envID      = "018f6a60-0000-7000-8000-000000000003"
		artifactID = "018f6a60-0000-7000-8000-000000000004"
		endpointID = "018f6a60-0000-7000-8000-000000000005"
		channelID  = "018f6a60-0000-7000-8000-000000000006"
		intentID   = "018f6a60-0000-7000-8000-000000000007"
		workerPK   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	type item struct {
		Domain     string         `json:"domain"`
		Op         string         `json:"op"`
		Coordinate string         `json:"coordinate"`
		Permission string         `json:"permission"`
		Modelling  string         `json:"modelling"`
		Outcome    string         `json:"outcome"`
		Content    map[string]any `json:"content"`
	}
	fixture := struct {
		Schema  string `json:"schema"`
		Intents []item `json:"intents"`
	}{Schema: "bahia.intent-fixtures.d80.v1", Intents: []item{
		{"security", "scan-run", "security-scan:" + intentID, "fleet-operator", "request", "security scan status/findings; bounded 30315 acknowledgement",
			map[string]any{"target": map[string]any{"type": "package", "package": map[string]any{"ecosystem": "npm", "name": "left-pad", "version": "1.3.0"}}}},
		{"sbom", "generate", "sbom-generate:" + intentID, "fleet-operator", "request", "SBOM 30078/30004 and 32017/32018; bounded 30315 acknowledgement",
			map[string]any{"idempotencyKey": intentID, "subject": map[string]any{"type": "artifact", "id": artifactID, "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "source": map[string]any{"kind": "oci-image", "locator": "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "formats": []any{"spdx"}, "generator": "syft", "storage": "blossom"}},
		{"sbom", "import", "sbom-import:" + intentID, "fleet-operator", "request", "SBOM 30078/30004 and 32017/32018; bounded 30315 acknowledgement",
			map[string]any{"idempotencyKey": intentID, "subject": map[string]any{"type": "artifact", "id": artifactID, "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "format": "spdx", "location": map[string]any{"type": "blossom", "uri": "https://blossom.example/sha256"}, "storage": "blossom", "generator": map[string]any{"id": "import"}}},
		{"artifact", "signature-verify", "artifact:" + artifactID, "services:write", "request", "artifact-signature 32016; bounded 30315 counts",
			map[string]any{"artifact_id": artifactID}},
		{"artifact", "register-build-result", "build-result:" + intentID, "services:write", "request", "build/artifact cp-state 30900; bounded 30315 acknowledgement",
			map[string]any{"build_id": intentID}},
		{"relay", "policy-set", RelaySettingsDTag, "fleet-operator", "desired-state-sensitive", "relay-settings protected cp-state 30900",
			map[string]any{"schema": RelaySettingsSchema, "browser_relays": []any{"wss://relay.example"}, "contextvm_relays": []any{"wss://relay.example"}, "service_relays": []any{"wss://relay.example"}, "relay_administration": map[string]any{"enabled": false}, "expected_projection": map[string]any{"availability": "never-configured"}}},
		{"notification", "channel-test", channelID, "settings:manage", "request-sensitive", "bounded 30315 delivery result",
			map[string]any{"id": channelID}},
		{"environment", "worker-policy-apply", envID, "fleet-operator", "desired-state", "environment cp-state 30900",
			map[string]any{"environment_id": envID, "policy": map[string]any{"pinned_worker": workerPK}, "expected_updated_at": "2026-10-03T12:00:00Z"}},
		{"ml", "pin", "endpoint:" + endpointID, "fleet-operator", "desired-state", "ML inference endpoint cp-state 30900",
			map[string]any{"workload_id": endpointID, "workload_kind": "ml_inference", "environment_id": envID, "worker_pubkey": workerPK, "expected_updated_at": "2026-10-03T12:00:00Z"}},
	}}
	for i := range fixture.Intents {
		fixture.Intents[i].Content["intent_id"] = intentID
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "web", "tests", "fixtures", "d80-intent-content.json")
	if os.Getenv("BAHIA_REGEN_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	committed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, committed), "regenerate d80-intent-content.json with BAHIA_REGEN_FIXTURE=1")
	for _, shape := range fixture.Intents {
		content, err := json.Marshal(shape.Content)
		require.NoError(t, err)
		tags := nostr.Tags{{"d", shape.Coordinate}, {"domain", shape.Domain}, {"op", shape.Op}, {"schema", "bahia.intent." + shape.Domain + ".v1"}, {"t", "bahia-intent"}, {"intent_id", intentID}}
		if shape.Domain != "security" && shape.Domain != "sbom" && shape.Domain != "relay" && shape.Domain != "ml" {
			tags = append(tags, nostr.Tag{"org", orgID})
		}
		parsed, err := ParseIntent(&nostr.Event{Kind: 30900, Tags: tags, Content: string(content)})
		require.NoError(t, err, shape.Domain+"/"+shape.Op)
		require.Equal(t, shape.Coordinate, parsed.Coordinate)
		require.Equal(t, shape.Op, parsed.Op)
	}
}
