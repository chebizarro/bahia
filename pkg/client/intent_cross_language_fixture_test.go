package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// TestIntentCrossLanguageFixture pins the Go event builder to a committed file
// and parses the separately generated web fixture with the daemon parser.
func TestIntentCrossLanguageFixture(t *testing.T) {
	const orgID = "3b45458b-2724-4dda-9fc6-66f12249660d"
	const intentID = "018f1fae-7b91-7bea-81d6-0669758de945"
	revision := int64(42)
	publisher := &IntentPublisher{}
	event, err := publisher.BuildIntentEvent(PublishIntentRequest{
		Domain: "service", Op: "update", Coordinate: "service:record-1",
		OrgID: orgID, IntentID: intentID, ExpectedUpdatedAt: &revision,
		Content: map[string]interface{}{
			"id": "record-1", "name": "api", "org_id": orgID,
			"config": map[string]interface{}{"a": 2, "z": 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event.CreatedAt = nostr.Timestamp(1727740800)
	unsigned := map[string]interface{}{
		"kind": event.Kind, "created_at": event.CreatedAt,
		"tags": event.Tags, "content": event.Content,
	}
	generated, err := json.MarshalIndent(unsigned, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	generated = append(generated, '\n')
	_, thisFile, _, _ := runtime.Caller(0)
	fixtureDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "web", "tests", "fixtures")
	goPath := filepath.Join(fixtureDir, "intent-go.json")
	if os.Getenv("BAHIA_REGEN_FIXTURE") == "1" {
		if err := os.WriteFile(goPath, generated, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(goPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(committed) != string(generated) {
		t.Fatal("Go intent fixture drifted; regenerate with BAHIA_REGEN_FIXTURE=1")
	}

	webBytes, err := os.ReadFile(filepath.Join(fixtureDir, "intent-web.json"))
	if err != nil {
		t.Fatal(err)
	}
	var webEvent nostr.Event
	if err := json.Unmarshal(webBytes, &webEvent); err != nil {
		t.Fatal(err)
	}
	parsed, err := controlplane.ParseIntent(&webEvent)
	if err != nil {
		t.Fatalf("daemon rejected web-built intent: %v", err)
	}
	if parsed.IntentID != intentID || parsed.Domain != "service" || parsed.ExpectedUpdatedAt == nil || *parsed.ExpectedUpdatedAt != revision {
		t.Fatalf("daemon parsed wrong web intent: %#v", parsed)
	}
}
