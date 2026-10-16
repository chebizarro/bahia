package identityrecovery

import (
	"errors"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/deletionguard"
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
	targets := map[nostr.ID]nostr.Kind{}
	for _, item := range s.Observed {
		targetID, err := nostr.IDFromHex(item.ID)
		if err != nil {
			return errors.New("invalid observed inventory id")
		}
		targets[targetID] = nostr.Kind(item.Kind)
	}
	return deletionguard.CheckSigned(ev, e.Config.OldPubkey, targets, nil)
}
