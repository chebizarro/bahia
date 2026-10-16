package identityrecovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/trustepoch"
)

// AttestReadiness publishes an explicit operator verification or waiver for
// one consumer. It does not advance recovery; cutover re-reads the signed
// record from every controlled relay before activating trust.
func (e *Engine) AttestReadiness(ctx context.Context, consumerID string, evidenceIDs []string, waiverReason string) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	if !trustepoch.Registered(consumerID) {
		return fmt.Errorf("unknown trust-epoch consumer")
	}
	s, err := e.Status()
	if err != nil {
		return err
	}
	next, ok := s.Next()
	if !ok || next != CutActivateTrust || s.NewPubkey == "" {
		return fmt.Errorf("readiness may be attested only after provisioning and before cutover")
	}
	head, exists, err := e.epochHead(ctx)
	if err != nil {
		return err
	}
	epoch := int64(1)
	if exists {
		epoch = head.Epoch + 1
	}
	if s.Epoch != 0 && s.Epoch != epoch {
		return fmt.Errorf("trust epoch changed during readiness attestation")
	}
	value := trustepoch.Readiness{ServiceID: e.Config.ServiceID, ConsumerID: consumerID,
		Epoch: uint64(epoch), IncidentID: s.IncidentID, EnforcingPubkey: s.NewPubkey,
		Waived: waiverReason != "", ReasonCode: strings.TrimSpace(waiverReason), EvidenceIDs: evidenceIDs}
	ev, err := trustepoch.NewReadiness(value, e.nextTimestamp(&s))
	if err != nil {
		return err
	}
	if err := e.Operator.SignEvent(ctx, &ev); err != nil {
		return fmt.Errorf("operator readiness signing failed")
	}
	if err := trustepoch.VerifyReadiness(ev, value, e.Config.OperatorPubkey.Hex()); err != nil {
		return err
	}
	if err := e.Ledger.Save(s); err != nil {
		return err
	}
	return e.Source.Publish(ctx, ev)
}
