package app

import (
	"context"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// The runner is woken by warm-start and relay OK delivery. It never polls SQL
// or reconstructs a signed intent from an SQL-only row.
type virtualizationRecoveryRunner struct {
	handler   *controlplane.VirtualizationIntentHandler
	processor *controlplane.IntentProcessor
	wake      chan struct{}
	logger    *zap.Logger
}

func (*virtualizationRecoveryRunner) Name() string { return "virtualization-receipts" }

func (r *virtualizationRecoveryRunner) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *virtualizationRecoveryRunner) Run(ctx context.Context) error {
	r.Notify()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			recoverCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := r.handler.Recover(recoverCtx, r.processor.ProcessRelayIntent)
			cancel()
			if err != nil {
				r.logger.Warn("virtualization receipt recovery requires operator reconciliation", zap.Error(err))
			}
		}
	}
}

func virtualizationStateEvent(ev nostr.Event) bool {
	if ev.Kind != nostr.Kind(kinds.CASControlState) {
		return false
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "domain" && tag[1] == kinds.VirtualizationDomain {
			return true
		}
	}
	return false
}

func canonicalProjectorWarmStartDomains() []string {
	all := nostrAdapter.CPStateDomains()
	domains := make([]string, 0, len(all))
	for _, name := range all {
		if name != kinds.VirtualizationDomain {
			domains = append(domains, name)
		}
	}
	return domains
}
