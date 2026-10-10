package config

import (
	"strings"
	"testing"
)

func TestAssistantWrappedKeysConfigRejectsIncompleteReadOnlySelection(t *testing.T) {
	for _, mode := range []string{"wrapped_read_only", "unknown"} {
		cfg := &Config{Assistant: AssistantConfig{Enabled: true, WrappedKeys: AssistantWrappedKeysConfig{Mode: mode}}}
		err := cfg.validateAssistant()
		if err == nil || !strings.Contains(err.Error(), "assistant.wrapped_keys") {
			t.Fatalf("mode %q validation = %v", mode, err)
		}
	}
	cfg := &Config{Assistant: AssistantConfig{Enabled: false, WrappedKeys: AssistantWrappedKeysConfig{Mode: "wrapped_read_only"}}}
	if err := cfg.validateAssistant(); err == nil {
		t.Fatal("disabled assistant accepted a wrapped-key selection")
	}
}
