package controlplane

import (
	"encoding/json"
	"testing"
)

func TestContextVMRequestFingerprintCanonicalizesEquivalentJSON(t *testing.T) {
	first, err := contextVMRequestFingerprint(json.RawMessage(`{"package":{"version":1.0,"name":"bahia"},"enabled":true}`))
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	second, err := contextVMRequestFingerprint(json.RawMessage(` { "enabled" : true, "package" : { "name" : "bahia", "version" : 10e-1 } } `))
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if first != second {
		t.Fatalf("equivalent JSON fingerprints differ: %q != %q", first, second)
	}
}

func TestContextVMRequestFingerprintDistinguishesLargeIntegers(t *testing.T) {
	first, err := contextVMRequestFingerprint(json.RawMessage(`{"serial":9007199254740992}`))
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	second, err := contextVMRequestFingerprint(json.RawMessage(`{"serial":9007199254740993}`))
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if first == second {
		t.Fatalf("distinct large integers produced the same fingerprint: %q", first)
	}
}
