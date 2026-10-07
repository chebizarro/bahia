package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type staticPolicyView []domain.DeploymentPolicy

func (v staticPolicyView) ListSecurityPolicies(context.Context) ([]domain.DeploymentPolicy, error) {
	return append([]domain.DeploymentPolicy(nil), v...), nil
}

// A policy that exists only in SQL gets its canonical cp-state published by
// the one-time backfill; one the local store already retains is left alone;
// the marker makes the backfill run once, and a failed publish leaves it
// unset so the next start retries.
func TestBackfillCanonicalPoliciesPublishesSQLOnlyPolicies(t *testing.T) {
	ctx := context.Background()
	repo := newMockPolicyRepo()
	retained := &domain.DeploymentPolicy{ID: uuid.New(), Name: "already-canonical", Enabled: true}
	sqlOnly := &domain.DeploymentPolicy{ID: uuid.New(), Name: "sql-only", Enabled: true}
	disabled := &domain.DeploymentPolicy{ID: uuid.New(), Name: "sql-only-disabled", Enabled: false}
	for _, p := range []*domain.DeploymentPolicy{retained, sqlOnly, disabled} {
		require.NoError(t, repo.Create(ctx, p))
	}
	svc := NewPolicyService(repo, nil, nil, zap.NewNop())
	svc.SetCanonicalPolicyView(staticPolicyView{*retained})
	marker := &memoryBackfillMarker{}

	var published []uuid.UUID
	failing := errors.New("outbox refused")
	publish := func(_ context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
		require.False(t, deleted)
		require.NotEqual(t, retained.ID, policy.ID, "a policy the canonical view holds is not republished")
		if policy.ID == disabled.ID {
			return failing
		}
		published = append(published, policy.ID)
		return nil
	}
	// The first run fails on one policy: nothing is marked done.
	err := svc.BackfillCanonicalPolicies(ctx, marker, publish)
	require.ErrorIs(t, err, failing)
	done, _ := marker.GetControlRecord("bootstrap", policyCanonicalBackfillMarker)
	require.Empty(t, done, "a failed backfill is retried on the next start")

	published = nil
	require.NoError(t, svc.BackfillCanonicalPolicies(ctx, marker, func(_ context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
		require.False(t, deleted)
		require.NotEqual(t, retained.ID, policy.ID)
		published = append(published, policy.ID)
		return nil
	}))
	require.ElementsMatch(t, []uuid.UUID{sqlOnly.ID, disabled.ID}, published, "only SQL-only policies are published, enabled or not")
	done, _ = marker.GetControlRecord("bootstrap", policyCanonicalBackfillMarker)
	require.Equal(t, "1", string(done))

	published = nil
	require.NoError(t, svc.BackfillCanonicalPolicies(ctx, marker, func(context.Context, *domain.DeploymentPolicy, bool) error {
		t.Fatal("a completed backfill publishes nothing")
		return nil
	}))

	// Without a SQL repository or a canonical view there is nothing to do.
	require.NoError(t, NewPolicyService(nil, nil, nil, zap.NewNop()).BackfillCanonicalPolicies(ctx, &memoryBackfillMarker{}, publish))
}
