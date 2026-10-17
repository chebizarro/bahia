package trustepoch

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const ReadinessSchema = "bahia.trust-epoch-readiness.v1"

const (
	WaiverConsumerDisabled       = "consumer_disabled"
	WaiverConsumerIsolated       = "consumer_isolated"
	WaiverConsumerDecommissioned = "consumer_decommissioned"
)

func ValidWaiverReason(reason string) bool {
	switch reason {
	case WaiverConsumerDisabled, WaiverConsumerIsolated, WaiverConsumerDecommissioned:
		return true
	default:
		return false
	}
}

// Readiness is an operator-signed, pre-activation verification or explicit
// per-consumer waiver. It cannot substitute for the post-activation ack.
type Readiness struct {
	ServiceID       string   `json:"service_id"`
	ConsumerID      string   `json:"consumer_id"`
	Epoch           uint64   `json:"epoch"`
	IncidentID      string   `json:"incident_id"`
	EnforcingPubkey string   `json:"enforcing_pubkey"`
	Waived          bool     `json:"waived"`
	ReasonCode      string   `json:"reason_code,omitempty"`
	EvidenceIDs     []string `json:"evidence_ids,omitempty"`
}

func validReadiness(value Readiness) bool {
	if value.ServiceID == "" || strings.ContainsAny(value.ServiceID, " \t\r\n:") ||
		!Registered(value.ConsumerID) || value.Epoch == 0 || value.IncidentID == "" ||
		!validHex64(value.EnforcingPubkey) {
		return false
	}
	if value.Waived {
		if !ValidWaiverReason(value.ReasonCode) || len(value.EvidenceIDs) == 0 {
			return false
		}
	} else if value.ReasonCode != "" || len(value.EvidenceIDs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range value.EvidenceIDs {
		if !validHex64(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// NewReadiness returns an unsigned byte-stable envelope for the operator root.
func NewReadiness(value Readiness, createdAt nostr.Timestamp) (nostr.Event, error) {
	if !validReadiness(value) {
		return nostr.Event{}, fmt.Errorf("invalid trust epoch readiness fields")
	}
	content, err := json.Marshal(value)
	if err != nil {
		return nostr.Event{}, err
	}
	return nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: createdAt,
		Tags: nostr.Tags{{"d", "trust-epoch-readiness:" + value.ServiceID + ":" + value.ConsumerID},
			{"t", AckTopic}, {"domain", Domain}, {"schema", ReadinessSchema},
			{"legacy_kind", kinds.CPStateFamilyIdentityRecoveryReceipt.TagValue()},
			{"epoch", strconv.FormatUint(value.Epoch, 10)}, {"deleted", "false"}},
		Content: string(content)}, nil
}

// VerifyReadiness validates the complete signed envelope and its exact target.
func VerifyReadiness(ev nostr.Event, want Readiness, operatorRoot string) error {
	if want.ServiceID == "" || !Registered(want.ConsumerID) || want.Epoch == 0 ||
		want.IncidentID == "" || !validHex64(want.EnforcingPubkey) ||
		!validHex64(operatorRoot) || operatorRoot == want.EnforcingPubkey ||
		ev.Kind != nostr.Kind(kinds.CASControlState) || ev.PubKey.Hex() != operatorRoot ||
		!ev.CheckID() || !ev.VerifySignature() ||
		!exactly(ev.Tags, "d", "trust-epoch-readiness:"+want.ServiceID+":"+want.ConsumerID) ||
		!exactly(ev.Tags, "t", AckTopic) || !exactly(ev.Tags, "domain", Domain) ||
		!exactly(ev.Tags, "schema", ReadinessSchema) ||
		!exactly(ev.Tags, "legacy_kind", kinds.CPStateFamilyIdentityRecoveryReceipt.TagValue()) ||
		!exactly(ev.Tags, "epoch", strconv.FormatUint(want.Epoch, 10)) ||
		!exactly(ev.Tags, "deleted", "false") {
		return fmt.Errorf("invalid trust epoch readiness envelope")
	}
	var actual Readiness
	decoder := json.NewDecoder(strings.NewReader(ev.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("invalid trust epoch readiness content")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing trust epoch readiness content")
	}
	if !validReadiness(actual) || actual.ServiceID != want.ServiceID ||
		actual.ConsumerID != want.ConsumerID || actual.Epoch != want.Epoch ||
		actual.IncidentID != want.IncidentID || actual.EnforcingPubkey != want.EnforcingPubkey {
		return fmt.Errorf("trust epoch readiness target mismatch")
	}
	return nil
}
