package controlplane

import (
	"strings"
	"testing"
)

func TestValidateLoomSubmitPayloadRejectsSecretBearingFieldsWithoutEcho(t *testing.T) {
	const secret = "super-secret-value"
	tests := []struct {
		name    string
		payload loomSubmitContextVMPayload
	}{
		{name: "raw secrets", payload: loomSubmitContextVMPayload{Secrets: map[string]string{"TOKEN": secret}}},
		{name: "payment token", payload: loomSubmitContextVMPayload{PaymentToken: secret}},
		{name: "bunker argv", payload: loomSubmitContextVMPayload{Args: []string{"--signer=bunker://" + secret}}},
		{name: "nostr connect environment", payload: loomSubmitContextVMPayload{Env: map[string]string{"SIGNER": "nostrconnect://" + secret}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoomSubmitPayload(test.payload)
			if err == nil {
				t.Fatal("expected secret-bearing payload to be rejected")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret value: %v", err)
			}
		})
	}
}

func TestValidateLoomSubmitPayloadAllowsNonSecretExecutionMetadata(t *testing.T) {
	err := validateLoomSubmitPayload(loomSubmitContextVMPayload{
		Cmd:    "npm",
		Args:   []string{"run", "build"},
		Env:    map[string]string{"NODE_ENV": "production"},
		Params: map[string]string{"ref": "main"},
	})
	if err != nil {
		t.Fatalf("non-secret payload rejected: %v", err)
	}
}
