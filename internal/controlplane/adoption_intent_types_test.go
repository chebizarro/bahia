package controlplane

import (
	"strings"
	"testing"
)

func TestValidateNonSecretEnvKeys(t *testing.T) {
	if err := validateNonSecretEnvKeys([]string{"SORT_KEY", "PASSWORD_MIN_LENGTH"}); err != nil {
		t.Fatalf("valid keys: %v", err)
	}
	for name, keys := range map[string][]string{
		"blank":      {""},
		"padded":     {" SORT_KEY"},
		"assignment": {"SORT_KEY=value"},
		"newline":    {"A\nB"},
		"duplicate":  {"A", "A"},
		"too many":   make([]string, maxNonSecretEnvKeys+1),
	} {
		if err := validateNonSecretEnvKeys(keys); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "value") && name == "assignment" {
			t.Errorf("assignment refusal echoes the value: %v", err)
		}
	}
}
