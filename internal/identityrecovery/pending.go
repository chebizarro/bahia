package identityrecovery

import (
	"errors"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func (e *Engine) validatePending(s State, id string, ev nostr.Event) error {
	if ev.ID.Hex() != id || !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("invalid signed pending recovery event")
	}
	if ev.Kind == nostr.Kind(kinds.CASControlState) {
		if ev.PubKey != e.Config.OperatorPubkey {
			return errors.New("pending recovery record is not operator-signed")
		}
		topic, d := tag(ev, "t"), tag(ev, "d")
		switch topic {
		case kinds.CPStateTopicIdentityIncident:
			if d != "identity-incident:"+s.IncidentID || !validRecord(ev, e.Config.OperatorPubkey, topic, d, kinds.CPStateFamilyIdentityIncident) {
				return errors.New("pending incident record outside scope")
			}
		case kinds.CPStateTopicIdentityRecoveryReceipt:
			if !strings.HasPrefix(d, "identity-incident:"+s.IncidentID+":phase:") || tag(ev, "incident") != s.IncidentID || !validRecord(ev, e.Config.OperatorPubkey, topic, d, kinds.CPStateFamilyIdentityRecoveryReceipt) {
				return errors.New("pending recovery receipt outside scope")
			}
		case kinds.CPStateTopicTrustEpoch:
			if d != "trust-epoch:"+e.Config.ServiceID || !validRecord(ev, e.Config.OperatorPubkey, topic, d, kinds.CPStateFamilyTrustEpoch) {
				return errors.New("pending trust epoch outside scope")
			}
		default:
			return errors.New("unrecognized pending recovery topic")
		}
		return nil
	}
	if ev.Kind != nostr.KindDeletion || ev.PubKey != e.Config.OldPubkey || ev.Content != "" {
		return errors.New("pending event is neither operator record nor scoped deletion")
	}
	targets := map[string]nostr.Kind{}
	for _, item := range s.Observed {
		targets[item.ID] = nostr.Kind(item.Kind)
	}
	referenced := map[string]struct{}{}
	kindsNeeded := map[nostr.Kind]struct{}{}
	declared := map[nostr.Kind]struct{}{}
	for _, t := range ev.Tags {
		if len(t) != 2 || strings.Contains(strings.ToLower(t[1]), "nsec1") {
			return errors.New("unsafe pending deletion tag")
		}
		switch t[0] {
		case "e":
			if _, dup := referenced[t[1]]; dup {
				return errors.New("duplicate pending deletion target")
			}
			kind, ok := targets[t[1]]
			if !ok {
				return errors.New("pending deletion outside observed inventory")
			}
			referenced[t[1]] = struct{}{}
			kindsNeeded[kind] = struct{}{}
		case "k":
			v, err := strconv.Atoi(t[1])
			if err != nil || strconv.Itoa(v) != t[1] {
				return errors.New("invalid pending deletion kind")
			}
			declared[nostr.Kind(v)] = struct{}{}
		default:
			return errors.New("pending deletion tag not permitted")
		}
	}
	if len(referenced) == 0 || len(referenced) > 500 || len(kindsNeeded) != len(declared) {
		return errors.New("pending deletion scope invalid")
	}
	for k := range kindsNeeded {
		if _, ok := declared[k]; !ok {
			return errors.New("pending deletion kind mismatch")
		}
	}
	return nil
}
