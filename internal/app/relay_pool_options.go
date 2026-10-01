package app

import (
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
)

// closedRetryBudgetOption applies nostr.closed_retry_budget to a daemon relay
// pool. 0 (unset) keeps the pool's default budget.
func closedRetryBudgetOption(nostr config.NostrConfig) nostrAdapter.RelayPoolOption {
	if nostr.ClosedRetryBudget <= 0 {
		return func(*nostrAdapter.RelayPool) {}
	}
	return nostrAdapter.WithRetryableClosedBudget(nostr.ClosedRetryBudget)
}
