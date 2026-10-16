package identityrecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

// AcknowledgmentReader verifies consumer receipts against the same controlled
// relays used by recovery and the daemon's durable local event store.
type AcknowledgmentReader struct {
	Source       EventSource
	Local        LocalInventory
	Ledger       *Ledger
	ServiceID    string
	OperatorRoot string
	OldPubkey    string
	Authorities  map[string]string
}

func (r *AcknowledgmentReader) authority(id, newPubkey string) (string, bool, error) {
	if !trustepoch.Registered(id) || len(r.Authorities) != len(trustepoch.ConsumerIDs()) {
		return "", false, fmt.Errorf("unknown or incomplete acknowledgment authority table")
	}
	for configured := range r.Authorities {
		if !trustepoch.Registered(configured) {
			return "", false, fmt.Errorf("unknown acknowledgment authority")
		}
	}
	seenComponents := map[string]string{}
	for _, component := range []string{"sidecar", "dns", "loom", "soulfactory", "hiveci"} {
		key := r.Authorities[component]
		if prior, ok := seenComponents[key]; ok {
			return "", false, fmt.Errorf("shared acknowledgment authority for %s and %s", prior, component)
		}
		seenComponents[key] = component
	}
	authority := r.Authorities[id]
	parsed, err := nostr.PubKeyFromHex(authority)
	if err != nil || parsed.Hex() != authority {
		return "", false, fmt.Errorf("invalid acknowledgment authority for %s", id)
	}
	operatorAttested := id == "web" || id == "pinned-runtimes"
	switch id {
	case "daemon":
		if authority != newPubkey {
			return "", false, fmt.Errorf("daemon acknowledgment authority differs from new identity")
		}
	case "cli", "web", "pinned-runtimes":
		if authority != r.OperatorRoot {
			return "", false, fmt.Errorf("operator acknowledgment authority mismatch for %s", id)
		}
	default:
		if authority == newPubkey || authority == r.OldPubkey || authority == r.OperatorRoot {
			return "", false, fmt.Errorf("component acknowledgment authority is not independent for %s", id)
		}
	}
	return authority, operatorAttested, nil
}

func (r *AcknowledgmentReader) Missing(ctx context.Context, epoch int64, roster []string) ([]string, error) {
	if r == nil || r.Source == nil || r.Local == nil || r.Ledger == nil ||
		r.ServiceID == "" || epoch <= 0 || !slices.Equal(roster, trustepoch.ConsumerIDs()) {
		return nil, fmt.Errorf("acknowledgment reader is not configured for the complete roster")
	}
	s, err := r.Ledger.Load()
	if err != nil {
		return nil, err
	}
	if s.IncidentID == "" || s.Epoch != epoch || s.OldPubkey != r.OldPubkey || s.OperatorPubkey != r.OperatorRoot {
		return nil, fmt.Errorf("acknowledgment incident or epoch mismatch")
	}
	root, err := nostr.PubKeyFromHex(r.OperatorRoot)
	if err != nil {
		return nil, err
	}
	events, err := r.Source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{root}, Tags: nostr.TagMap{"d": {"trust-epoch:" + r.ServiceID}, "t": {trustepoch.Topic}}})
	if err != nil {
		return nil, err
	}
	var head *nostr.Event
	for _, ev := range events {
		body, parseErr := trustepoch.Parse(ev, r.ServiceID, r.OperatorRoot)
		if parseErr != nil {
			return nil, parseErr
		}
		if body.Epoch > uint64(epoch) {
			return nil, fmt.Errorf("trust epoch advanced during acknowledgment verification")
		}
		if body.Epoch == uint64(epoch) {
			if head != nil && head.ID != ev.ID {
				return nil, fmt.Errorf("conflicting trust epoch heads")
			}
			copy := ev
			head = &copy
		}
	}
	if head == nil {
		return nil, fmt.Errorf("current trust epoch unavailable")
	}
	body, err := trustepoch.Parse(*head, r.ServiceID, r.OperatorRoot)
	if err != nil {
		return nil, err
	}
	if !body.Activated || body.IncidentID != s.IncidentID || body.NewPubkey != s.NewPubkey ||
		body.OldPubkey != s.OldPubkey {
		return nil, fmt.Errorf("activated trust epoch does not match recovery ledger")
	}
	if _, signed := s.Records[head.ID.Hex()]; !signed {
		return nil, fmt.Errorf("trust epoch is absent from the signed recovery ledger")
	}
	resolved := trustepoch.Resolution{ServiceID: r.ServiceID, Pubkey: body.NewPubkey,
		Epoch: body.Epoch, Record: &body, EventID: head.ID.Hex()}
	acknowledged := make(map[string]bool, len(roster))
	for _, id := range roster {
		authority, operatorAttested, authorityErr := r.authority(id, body.NewPubkey)
		if authorityErr != nil {
			return nil, authorityErr
		}
		pubkey, _ := nostr.PubKeyFromHex(authority)
		filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
			Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"d": {"trust-epoch-ack:" + r.ServiceID + ":" + id}, "t": {trustepoch.AckTopic}}}
		relayAcks, readErr := r.Source.Read(ctx, filter)
		if readErr != nil {
			return nil, readErr
		}
		localAcks, readErr := r.Local.Events(ctx, pubkey)
		if readErr != nil {
			return nil, readErr
		}
		for _, ev := range append(relayAcks, localAcks...) {
			if trustepoch.VerifyAcknowledgmentAuthority(ev, resolved, id, authority, operatorAttested) == nil {
				acknowledged[id] = true
			}
		}
	}
	return trustepoch.MissingAcknowledgments(acknowledged), nil
}

// Readiness requires an operator-signed enforcement verification or explicit
// waiver for every roster entry, including entries still marked pending.
func (r *AcknowledgmentReader) Readiness(ctx context.Context, epoch int64, incidentID, newPubkey string, roster []string) (CutoverReadiness, error) {
	if r == nil || r.Source == nil || r.Local == nil || r.Ledger == nil ||
		epoch <= 0 || incidentID == "" || !slices.Equal(roster, trustepoch.ConsumerIDs()) {
		return CutoverReadiness{}, fmt.Errorf("readiness reader is not configured for the complete roster")
	}
	s, err := r.Ledger.Load()
	if err != nil {
		return CutoverReadiness{}, err
	}
	if s.IncidentID != incidentID || s.NewPubkey != newPubkey || s.OldPubkey != r.OldPubkey || s.OperatorPubkey != r.OperatorRoot {
		return CutoverReadiness{}, fmt.Errorf("readiness incident or identity mismatch")
	}
	root, err := nostr.PubKeyFromHex(r.OperatorRoot)
	if err != nil {
		return CutoverReadiness{}, err
	}
	local, err := r.Local.Events(ctx, root)
	if err != nil {
		return CutoverReadiness{}, err
	}
	proof := CutoverReadiness{Waivers: map[string]string{}}
	ready := map[string]bool{}
	for _, id := range roster {
		if _, _, err := r.authority(id, newPubkey); err != nil {
			return CutoverReadiness{}, err
		}
		d := "trust-epoch-readiness:" + r.ServiceID + ":" + id
		filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
			Authors: []nostr.PubKey{root}, Tags: nostr.TagMap{"d": {d}, "t": {trustepoch.AckTopic}}}
		relay, readErr := r.Source.Read(ctx, filter)
		if readErr != nil {
			return CutoverReadiness{}, readErr
		}
		var latest *nostr.Event
		for _, ev := range append(relay, local...) {
			if ev.PubKey != root || ev.Kind != nostr.Kind(kinds.CASControlState) || tag(ev, "d") != d {
				continue
			}
			if latest == nil || ev.CreatedAt > latest.CreatedAt ||
				(ev.CreatedAt == latest.CreatedAt && ev.ID.Hex() < latest.ID.Hex()) {
				copy := ev
				latest = &copy
			}
		}
		if latest == nil {
			continue
		}
		want := trustepoch.Readiness{ServiceID: r.ServiceID, ConsumerID: id, Epoch: uint64(epoch),
			IncidentID: incidentID, EnforcingPubkey: newPubkey}
		if err := trustepoch.VerifyReadiness(*latest, want, r.OperatorRoot); err != nil {
			return CutoverReadiness{}, err
		}
		var body trustepoch.Readiness
		if err := json.Unmarshal([]byte(latest.Content), &body); err != nil {
			return CutoverReadiness{}, err
		}
		if body.Waived {
			proof.Waivers[id] = latest.ID.Hex()
		} else {
			ready[id] = true
		}
		proof.EvidenceIDs = append(proof.EvidenceIDs, latest.ID.Hex())
	}
	for _, id := range roster {
		if ready[id] {
			continue
		}
		if proof.Waivers[id] == "" {
			proof.Missing = append(proof.Missing, id)
		}
	}
	return proof, nil
}
