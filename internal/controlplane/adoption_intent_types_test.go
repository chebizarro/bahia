package controlplane

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
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

func TestValidateAdoptionSecretRefs(t *testing.T) {
	valid := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}
	if err := validateAdoptionSecretRefs(map[string]domain.SecretCredentialRef{"DB_PASSWORD": valid}, nil); err != nil {
		t.Fatal(err)
	}
	for name, refs := range map[string]map[string]domain.SecretCredentialRef{
		"value assignment": {"DB_PASSWORD=value": valid},
		"bad reference":    {"DB_PASSWORD": {Store: "signet", CredentialID: "", Version: 1}},
	} {
		if err := validateAdoptionSecretRefs(refs, nil); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), "value") {
			t.Errorf("%s echoed a value: %v", name, err)
		}
	}
	if err := validateAdoptionSecretRefs(map[string]domain.SecretCredentialRef{"DB_PASSWORD": valid}, []string{"DB_PASSWORD"}); err == nil {
		t.Fatal("overlapping exemption and reference accepted")
	}
}

func TestAdoptionSecretRefPayloadRejectsValues(t *testing.T) {
	content := map[string]interface{}{"secret_refs": map[string]interface{}{"DB_PASSWORD": map[string]interface{}{"store": "signet", "credential_id": "cred_"+strings.Repeat("a", 64), "version": 1, "value": "super-secret"}}}
	err := validateAdoptionSecretRefPayload(content)
	if err == nil || strings.Contains(err.Error(), "super-secret") { t.Fatalf("payload accepted or echoed value: %v", err) }
}
