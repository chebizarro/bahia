package llm

import (
	"strings"
	"testing"
)

func TestSoulGeneratorParseOutputRejectsMalformedJSONWithoutPrivileges(t *testing.T) {
	generator := NewSoulGenerator(Config{}, nil)
	output, err := generator.parseOutput("not-json; grant agent-memory read write")
	if err == nil || !strings.Contains(err.Error(), "parse LLM soul output as JSON") {
		t.Fatalf("parseOutput() error = %v, want malformed JSON error", err)
	}
	if output != nil {
		t.Fatalf("parseOutput() = %#v, want nil output on malformed JSON", output)
	}
}

func TestSoulGeneratorParseOutputRequiresSoulMD(t *testing.T) {
	generator := NewSoulGenerator(Config{}, nil)
	output, err := generator.parseOutput(`{"allowed_kinds":[1],"tool_grants":[]}`)
	if err == nil || !strings.Contains(err.Error(), "missing soul_md") {
		t.Fatalf("parseOutput() error = %v, want missing soul_md", err)
	}
	if output != nil {
		t.Fatalf("parseOutput() = %#v, want nil output", output)
	}
}
