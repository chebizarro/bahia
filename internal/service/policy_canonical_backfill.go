package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// policyCanonicalBackfillMarker is the local-store control record that
// records the one-time SQL-era policy backfill as done.
const policyCanonicalBackfillMarker = "policy-canonical-v1"

// PolicyCanonicalPublisher publishes one policy's canonical cp-state record
// (controlplane.PolicyStatePublisher has this shape).
type PolicyCanonicalPublisher func(ctx context.Context, policy *domain.DeploymentPolicy, deleted bool) error

// BackfillCanonicalPolicies publishes, once per local store (marker), the
// cp-state record of every policy that exists only in the SQL repository.
// Security scan schedules and scan-time policy evaluation derive from the
// retained policy cp-state alone (SecurityPolicyView), and policy
// cp-state is otherwise published only when a policy is mutated through the
// intent handler, so a pre-inversion policy that was never touched since
// would have no schedules and no gate until an operator edited it
// A policy the canonical view already holds is left alone: a
// republish would only move its coordinate forward. A failed publish leaves
// the marker unset so the next start retries the remainder.
func (s *PolicyService) BackfillCanonicalPolicies(ctx context.Context, marker F74aBackfillMarker, publish PolicyCanonicalPublisher) error {
	if s == nil || s.policies == nil || s.canonicalPolicies == nil || marker == nil || publish == nil {
		return nil
	}
	if done, err := marker.GetControlRecord("bootstrap", policyCanonicalBackfillMarker); err != nil || string(done) == "1" {
		return err
	}
	retained, err := s.canonicalPolicies.ListSecurityPolicies(ctx)
	if err != nil {
		return fmt.Errorf("listing canonical policies for backfill: %w", err)
	}
	known := make(map[uuid.UUID]struct{}, len(retained))
	for _, policy := range retained {
		known[policy.ID] = struct{}{}
	}
	indexed, err := s.policies.List(ctx, false)
	if err != nil {
		return fmt.Errorf("listing SQL policies for backfill: %w", err)
	}
	sort.Slice(indexed, func(i, j int) bool { return indexed[i].ID.String() < indexed[j].ID.String() })
	published := 0
	for i := range indexed {
		if _, ok := known[indexed[i].ID]; ok {
			continue
		}
		if err := publish(ctx, &indexed[i], false); err != nil {
			return fmt.Errorf("backfill policy %s: %w", indexed[i].ID, err)
		}
		published++
	}
	if published > 0 {
		s.logger.Info("published SQL-era policies as canonical cp-state", zap.Int("policies", published))
	}
	return marker.PutControlRecord("bootstrap", policyCanonicalBackfillMarker, []byte("1"))
}
