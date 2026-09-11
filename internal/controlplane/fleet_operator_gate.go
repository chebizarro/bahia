package controlplane

import (
	"context"
	"errors"
	"slices"
	"strings"
)

const (
	fleetOperatorNotConfiguredError = "fleet operator authorization is not configured"
	fleetOperatorUnauthorizedError  = "requester is not an authorized fleet operator"
)

// FleetOperatorGate authorizes fleet-scoped ContextVM operations against the
// configured global Nostr operator allowlist. An absent or empty allowlist
// deliberately denies every request.
type FleetOperatorGate struct {
	authorizedPubkeys []string
}

func NewFleetOperatorGate(authorizedPubkeys []string) *FleetOperatorGate {
	return &FleetOperatorGate{authorizedPubkeys: append([]string(nil), authorizedPubkeys...)}
}

func (g *FleetOperatorGate) wrap(next ContextVMHandler) ContextVMHandler {
	return func(ctx context.Context, request ContextVMRequest) (any, error) {
		if g == nil || len(g.authorizedPubkeys) == 0 {
			return nil, errors.New(fleetOperatorNotConfiguredError)
		}
		requester := ""
		if request.Event != nil {
			requester = strings.TrimSpace(request.Event.PubKey.Hex())
		}
		if requester == "" || !slices.Contains(g.authorizedPubkeys, requester) {
			return nil, errors.New(fleetOperatorUnauthorizedError)
		}
		return next(ctx, request)
	}
}
