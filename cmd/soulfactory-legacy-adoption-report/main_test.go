package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFixtureProducesReadOnlyReport(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "soulfactory", "testdata", "legacy_adoption_input.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-input", fixture}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, expected := range []string{
		`"read_only": true`,
		`"mutation_allowed": false`,
		`"reason_code": "single_authoritative_match_requires_operator_approval"`,
		`"reason_code": "no_authoritative_match"`,
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output missing %s:\n%s", expected, stdout.String())
		}
	}
}

func TestRunRefusesAmbiguousClassification(t *testing.T) {
	pubkey := strings.Repeat("a", 64)
	input := fmt.Sprintf(`{
  "schema":"soulfactory-legacy-adoption-input/v1",
  "running_agents":[{"inventory_id":"runtime","running":true,"managed_pubkey":%q,"runtime_binding":"shared","source_ref":"runtime.json"}],
  "identity_records":[
    {"agent_id":"agent-a","managed_pubkey":%q,"runtime_binding":"shared","source_refs":["a.json"]},
    {"agent_id":"agent-b","managed_pubkey":%q,"runtime_binding":"shared","source_refs":["b.json"]}
  ],
  "trusted_souls":[]
}`, pubkey, pubkey, pubkey)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-input", "-"}, strings.NewReader(input), &stdout, &stderr); code != exitAmbiguous {
		t.Fatalf("exit = %d, want %d; stderr = %s", code, exitAmbiguous, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"reason_code": "multiple_authoritative_matches"`) || !strings.Contains(stderr.String(), "refused") {
		t.Fatalf("missing refusal evidence: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}
