package controlplane

import (
	"errors"
	"testing"

	"fiatjaf.com/nostr"
)

func TestAuthorizeIntentDelegationPermittedClaimKeepsSignerAuthoritative(t *testing.T) {
	privateKey := nostr.Generate().Hex()
	signerPubkey := testNostrPubKeyHexFromPrivateKey(t, privateKey)
	event := signedLLMRequest(t, privateKey, KindMLInferenceRollbackRequest, `{}`, nostr.Tags{{"d", "delegation-permitted"}})

	record, err := authorizeIntentDelegation(event, "alice", intentDelegationVersionML, intentDelegationCapabilityMLRollback, true)
	if err != nil {
		t.Fatalf("permitted claim returned error: %v", err)
	}
	if record == nil {
		t.Fatal("permitted claim did not produce a delegation record")
	}
	if record.ServicePubkey != signerPubkey {
		t.Fatalf("delegation ServicePubkey = %q, want signer %q", record.ServicePubkey, signerPubkey)
	}
	if record.RequesterPubkey != "alice" {
		t.Fatalf("delegation RequesterPubkey = %q, want claimed human %q", record.RequesterPubkey, "alice")
	}
	if record.Version != intentDelegationVersionML || record.Capability != intentDelegationCapabilityMLRollback {
		t.Fatalf("delegation version/capability = %q/%q", record.Version, record.Capability)
	}
	if record.RequestEventID != event.ID.Hex() || record.RequestEventKind != int(event.Kind) {
		t.Fatalf("delegation event correlation = %q/%d", record.RequestEventID, record.RequestEventKind)
	}
}

func TestAuthorizeIntentDelegationRejectsUntrustedSigner(t *testing.T) {
	privateKey := nostr.Generate().Hex()
	event := signedLLMRequest(t, privateKey, KindMLInferenceRollbackRequest, `{}`, nostr.Tags{{"d", "delegation-untrusted"}})

	record, err := authorizeIntentDelegation(event, "alice", intentDelegationVersionML, intentDelegationCapabilityMLRollback, false)
	if err == nil || !errors.Is(err, errIntentDelegationUntrusted) {
		t.Fatalf("untrusted signer error = %v, want %v", err, errIntentDelegationUntrusted)
	}
	if record != nil {
		t.Fatalf("untrusted signer produced a delegation record: %+v", record)
	}
}

func TestAuthorizeIntentDelegationRejectsSelfDelegation(t *testing.T) {
	privateKey := nostr.Generate().Hex()
	signerPubkey := testNostrPubKeyHexFromPrivateKey(t, privateKey)
	event := signedLLMRequest(t, privateKey, KindMLInferenceRollbackRequest, `{}`, nostr.Tags{{"d", "delegation-self"}})

	record, err := authorizeIntentDelegation(event, signerPubkey, intentDelegationVersionML, intentDelegationCapabilityMLRollback, true)
	if err == nil || !errors.Is(err, errIntentDelegationSelf) {
		t.Fatalf("self-delegation error = %v, want %v", err, errIntentDelegationSelf)
	}
	if record != nil {
		t.Fatalf("self-delegation produced a delegation record: %+v", record)
	}
}

func TestAuthorizeIntentDelegationNoClaimReturnsNil(t *testing.T) {
	privateKey := nostr.Generate().Hex()
	event := signedLLMRequest(t, privateKey, KindMLInferenceRollbackRequest, `{}`, nostr.Tags{{"d", "delegation-none"}})

	record, err := authorizeIntentDelegation(event, "  ", intentDelegationVersionML, intentDelegationCapabilityMLRollback, true)
	if err != nil {
		t.Fatalf("empty claim returned error: %v", err)
	}
	if record != nil {
		t.Fatalf("empty claim produced a delegation record: %+v", record)
	}
}
