package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
)

// The checked-in events are the wire contract consumed by web and CLI clients.
func TestDeploymentIntentContentFixtures(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "web", "tests", "fixtures", "deployment-intents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var events []nostr.Event
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"deployment": {"create", "approve", "reject", "rollback"},
		"runtime":    {"deploy", "restart", "stop"},
		"llm":        {"deploy", "rollback", "approve", "reject"},
		"backup":     {"restore-approval"},
	}
	seen := map[string]map[string]bool{}
	for i := range events {
		intent, err := ParseIntent(&events[i])
		if err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		if intent.Schema != "bahia.intent."+intent.Domain+".v1" {
			t.Fatalf("fixture %d schema: %s", i, intent.Schema)
		}
		if intent.Content["intent_id"] != intent.IntentID {
			t.Fatalf("fixture %d intent_id mismatch", i)
		}
		if _, ok := seen[intent.Domain]; !ok {
			seen[intent.Domain] = map[string]bool{}
		}
		if seen[intent.Domain][intent.Op] {
			t.Fatalf("duplicate fixture %s/%s", intent.Domain, intent.Op)
		}
		seen[intent.Domain][intent.Op] = true
		if (intent.Op == "approve" || intent.Op == "reject") && intent.Domain != "backup" && intent.ExpectedUpdatedAt == nil {
			t.Fatalf("fixture %d missing revision", i)
		}
	}
	for domain, ops := range expected {
		for _, op := range ops {
			if !seen[domain][op] {
				t.Errorf("missing fixture %s/%s", domain, op)
			}
		}
	}
}
