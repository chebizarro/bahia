package telemetry

import (
	"context"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fleetHealthWorkerRegistration interface {
	GetByPubKey(context.Context, string) (*domain.Worker, error)
}

type fleetHealthAgentRegistration interface {
	IsRegisteredAgent(context.Context, string) (bool, error)
}

// FleetHealthRegistrationTrust resolves publisher authority from Bahia's
// existing service identity, worker records, and live managed-agent records.
// Other service identities are existing Soul Factory controller, runtime, and
// operator-assistant registrations supplied by app wiring, not a new policy
// allowlist. Worker and agent lookups are deliberately performed per event.
func FleetHealthRegistrationTrust(servicePubkey string, workers fleetHealthWorkerRegistration, agents fleetHealthAgentRegistration, serviceRegistrations []string) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, pubkey string) (bool, error) {
		if strings.TrimSpace(pubkey) == "" {
			return false, nil
		}
		if servicePubkey != "" && strings.EqualFold(pubkey, servicePubkey) {
			return true, nil
		}
		for _, registered := range serviceRegistrations {
			if registered != "" && strings.EqualFold(pubkey, registered) {
				return true, nil
			}
		}
		if workers != nil {
			worker, err := workers.GetByPubKey(ctx, pubkey)
			if err != nil || worker != nil {
				return worker != nil, err
			}
		}
		if agents != nil {
			return agents.IsRegisteredAgent(ctx, pubkey)
		}
		return false, nil
	}
}
