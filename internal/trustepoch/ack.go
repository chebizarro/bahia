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

type Acknowledgment struct {
	ServiceID       string `json:"service_id"`
	ConsumerID      string `json:"consumer_id"`
	Epoch           uint64 `json:"epoch"`
	IncidentID      string `json:"incident_id"`
	EnforcingPubkey string `json:"enforcing_pubkey"`
	EpochEventID    string `json:"epoch_event_id"`
	AttestedBy      string `json:"attested_by,omitempty"`
}

// NewAcknowledgment builds the daemon's unsigned acknowledgment envelope.
func NewAcknowledgment(resolved Resolution, consumerID string, createdAt nostr.Timestamp) (nostr.Event, error) {
	return NewAcknowledgmentForAuthority(resolved, consumerID, createdAt, "")
}

// NewAcknowledgmentForAuthority builds an unsigned receipt. For operator-
// verified consumers, attestedBy is the operator pubkey; all other consumers
// leave it empty. The caller signs with the consumer's pinned authority.
func NewAcknowledgmentForAuthority(resolved Resolution, consumerID string, createdAt nostr.Timestamp, attestedBy string) (nostr.Event, error) {
	if resolved.Record == nil || !resolved.Record.Activated || !Registered(consumerID) ||
		!validHex64(resolved.EventID) || !validHex64(resolved.Pubkey) ||
		(attestedBy != "" && !validHex64(attestedBy)) {
		return nostr.Event{}, fmt.Errorf("cannot acknowledge an unactivated trust epoch")
	}
	content, err := json.Marshal(Acknowledgment{
		ServiceID: resolved.ServiceID, ConsumerID: consumerID,
		Epoch: resolved.Epoch, IncidentID: resolved.Record.IncidentID,
		EnforcingPubkey: resolved.Pubkey, EpochEventID: resolved.EventID,
		AttestedBy: attestedBy,
	})
	if err != nil {
		return nostr.Event{}, err
	}
	return nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"d", "trust-epoch-ack:" + resolved.ServiceID + ":" + consumerID},
			{"t", AckTopic}, {"domain", Domain}, {"schema", AckSchema},
			{"legacy_kind", kinds.CPStateFamilyIdentityRecoveryReceipt.TagValue()},
			{"epoch", strconv.FormatUint(resolved.Epoch, 10)}, {"deleted", "false"},
		}, Content: string(content)}, nil
}

func VerifyAcknowledgment(ev nostr.Event, resolved Resolution, consumerID string) error {
	return VerifyAcknowledgmentAuthority(ev, resolved, consumerID, resolved.Pubkey, false)
}

// VerifyAcknowledgmentAuthority binds a receipt to the configured consumer
// signer. Operator-verification receipts must name that signer in attested_by.
func VerifyAcknowledgmentAuthority(ev nostr.Event, resolved Resolution, consumerID, authority string, operatorAttested bool) error {
	if resolved.Record == nil || !resolved.Record.Activated || !Registered(consumerID) ||
		!validHex64(authority) || !validHex64(resolved.Pubkey) ||
		ev.Kind != nostr.Kind(kinds.CASControlState) || ev.PubKey.Hex() != authority ||
		!ev.CheckID() || !ev.VerifySignature() ||
		!exactly(ev.Tags, "d", "trust-epoch-ack:"+resolved.ServiceID+":"+consumerID) ||
		!exactly(ev.Tags, "t", AckTopic) || !exactly(ev.Tags, "domain", Domain) ||
		!exactly(ev.Tags, "schema", AckSchema) ||
		!exactly(ev.Tags, "legacy_kind", kinds.CPStateFamilyIdentityRecoveryReceipt.TagValue()) ||
		!exactly(ev.Tags, "epoch", strconv.FormatUint(resolved.Epoch, 10)) ||
		!exactly(ev.Tags, "deleted", "false") {
		return fmt.Errorf("invalid trust epoch acknowledgment envelope")
	}
	var body Acknowledgment
	decoder := json.NewDecoder(strings.NewReader(ev.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing acknowledgment content")
	}
	if body.ServiceID != resolved.ServiceID || body.ConsumerID != consumerID ||
		body.Epoch != resolved.Epoch || body.EpochEventID != resolved.EventID ||
		body.IncidentID != resolved.Record.IncidentID || body.EnforcingPubkey != resolved.Pubkey ||
		(operatorAttested && body.AttestedBy != authority) ||
		(!operatorAttested && body.AttestedBy != "") {
		return fmt.Errorf("invalid trust epoch acknowledgment content")
	}
	return nil
}
