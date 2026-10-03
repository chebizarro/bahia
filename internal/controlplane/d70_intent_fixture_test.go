package controlplane

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestD70CrossLanguageIntentContentFixtures(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "web", "tests", "fixtures", "d70-intent-content.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture struct {
		Schema  string `json:"schema"`
		Intents []struct {
			Domain     string                 `json:"domain"`
			Op         string                 `json:"op"`
			Coordinate string                 `json:"coordinate"`
			Content    map[string]interface{} `json:"content"`
		} `json:"intents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&fixture))
	require.Equal(t, "bahia.intent-fixtures.d70.v1", fixture.Schema)
	require.Len(t, fixture.Intents, 18)
	seen := map[string]bool{}
	for _, item := range fixture.Intents {
		key := item.Domain + "/" + item.Op
		require.False(t, seen[key], "duplicate fixture %s", key)
		seen[key] = true
		content, err := json.Marshal(item.Content)
		require.NoError(t, err)
		event := &nostr.Event{Kind: 30900, Tags: nostr.Tags{{"d", item.Coordinate}, {"domain", item.Domain}, {"op", item.Op}, {"schema", "bahia.intent." + item.Domain + ".v1"}, {"t", "bahia-intent"}, {"intent_id", "018f6a60-0000-7000-8000-000000000001"}}, Content: string(content)}
		parsed, err := ParseIntent(event)
		require.NoError(t, err, key)
		require.Equal(t, item.Coordinate, parsed.Coordinate)
		require.Equal(t, item.Op, parsed.Op)
		require.Equal(t, item.Content, parsed.Content)
		if _, hasRevision := item.Content["expected_updated_at"]; hasRevision {
			require.NotNil(t, parsed.ExpectedUpdatedAt)
		}
		if item.Domain == "worker" {
			require.Equal(t, "worker:"+item.Content["worker_pubkey"].(string), parsed.Coordinate)
			require.True(t, validWorkerIntentState(domain.WorkerSchedulingState(item.Content["scheduling_state"].(string))))
			_, err := workerIntentLabels(parsed.Content)
			require.NoError(t, err)
		}
	}
}
