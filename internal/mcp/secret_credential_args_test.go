package mcp

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSecretCredentialArgs(t *testing.T) {
	id := "cred_" + strings.Repeat("ab", 32)
	got, err := secretCredentialArgs(map[string]interface{}{"credential_id": id, "credential_version": float64(3)})
	if err != nil || got == nil || *got != (domain.SecretCredentialRef{Store: "signet", CredentialID: id, Version: 3}) {
		t.Fatalf("reference = %+v, %v", got, err)
	}
	if got, err := secretCredentialArgs(map[string]interface{}{}); got != nil || err != nil {
		t.Fatalf("no reference = %+v, %v", got, err)
	}
	for name, args := range map[string]map[string]interface{}{
		"value only":          {"value": "v"},
		"value and reference": {"credential_id": id, "credential_version": float64(1), "value": "v"},
		"missing version":     {"credential_id": id},
		"fractional version":  {"credential_id": id, "credential_version": 1.5},
		"string version":      {"credential_id": id, "credential_version": "1"},
		"zero version":        {"credential_id": id, "credential_version": float64(0)},
		"version without id":  {"credential_version": float64(2)},
	} {
		if got, err := secretCredentialArgs(args); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}
