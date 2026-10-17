package identityrecovery

import (
	"context"
	"fmt"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

// AttestAcknowledgment publishes an operator-authorized post-cut receipt for
// a CLI verification or a consumer that cannot hold its own signing key.
// Component-owned receipts must instead come from their configured identities.
func (e *Engine) AttestAcknowledgment(ctx context.Context, consumerID string) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	if consumerID != "cli" && consumerID != "web" && consumerID != "pinned-runtimes" && consumerID != "loom" && consumerID != "hiveci" && consumerID != "soulfactory" {
		return fmt.Errorf("consumer does not use operator acknowledgment authority")
	}
	s, err := e.Status()
	if err != nil {
		return err
	}
	if !s.CutComplete() || s.Epoch <= 0 || s.NewPubkey == "" {
		return fmt.Errorf("trust cutover is not complete")
	}
	reader, ok := e.Acks.(*AcknowledgmentReader)
	if !ok {
		return fmt.Errorf("production acknowledgment reader required")
	}
	authority, operatorAttested, err := reader.authority(consumerID, s.NewPubkey)
	if err != nil || authority != e.Config.OperatorPubkey.Hex() {
		return fmt.Errorf("operator acknowledgment authority is not pinned")
	}
	if _, err := reader.Missing(ctx, s.Epoch, e.Config.ConsumerRoster); err != nil {
		return err
	}
	var resolved trustepoch.Resolution
	for _, ev := range s.Records {
		if tag(ev, "t") != kinds.CPStateTopicTrustEpoch || tag(ev, "d") != "trust-epoch:"+e.Config.ServiceID {
			continue
		}
		body, parseErr := trustepoch.Parse(ev, e.Config.ServiceID, e.Config.OperatorPubkey.Hex())
		if parseErr != nil {
			return parseErr
		}
		if body.Epoch != uint64(s.Epoch) || !body.Activated || body.IncidentID != s.IncidentID || body.NewPubkey != s.NewPubkey {
			continue
		}
		if resolved.Record != nil && resolved.EventID != ev.ID.Hex() {
			return fmt.Errorf("conflicting activated epoch records")
		}
		resolved = trustepoch.Resolution{ServiceID: e.Config.ServiceID, Pubkey: body.NewPubkey,
			Epoch: body.Epoch, Record: &body, EventID: ev.ID.Hex()}
	}
	if resolved.Record == nil {
		return fmt.Errorf("signed activated epoch record unavailable")
	}
	attestedBy := ""
	if operatorAttested {
		attestedBy = authority
	}
	ack, err := trustepoch.NewAcknowledgmentForAuthority(resolved, consumerID, e.nextTimestamp(&s), attestedBy)
	if err != nil {
		return err
	}
	if err := e.Operator.SignEvent(ctx, &ack); err != nil {
		return fmt.Errorf("operator acknowledgment signing failed")
	}
	if err := trustepoch.VerifyAcknowledgmentAuthority(ack, resolved, consumerID, authority, operatorAttested); err != nil {
		return err
	}
	if err := e.Ledger.Save(s); err != nil {
		return err
	}
	return e.Source.Publish(ctx, ack)
}
