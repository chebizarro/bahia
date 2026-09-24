//go:build integration

package repository

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPackageAuthorizationPostgresSingleUseAndDurableClaims(t *testing.T) {
	pool, _ := vmPostgres(t)
	r := NewPgPackageControlPlaneRepository(pool)
	now := time.Now().UTC()
	a := PackageApproval{ID: uuid.New(), Requester: "requester", Approver: "approver", Method: "package/publish", PlanHash: "hash", EventID: "approval-event", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	require.NoError(t, r.CreatePackageApproval(t.Context(), a))
	for _, mismatch := range []string{"requester", "method", "hash", "revoked"} {
		requester, method, hash, allowed := a.Requester, a.Method, a.PlanHash, []string{a.Approver}
		switch mismatch {
		case "requester":
			requester = "other"
		case "method":
			method = "package/promote"
		case "hash":
			hash = "changed"
		case "revoked":
			allowed = nil
		}
		_, err := r.ConsumePackageApproval(t.Context(), a.ID, requester, method, hash, allowed)
		require.ErrorIs(t, err, ErrPackageApprovalInvalid)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := r.ConsumePackageApproval(t.Context(), a.ID, a.Requester, a.Method, a.PlanHash, []string{a.Approver})
			if err == nil {
				wins.Add(1)
			} else {
				require.ErrorIs(t, err, ErrPackageApprovalInvalid)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
	c := PackageRequestClaim{Requester: a.Requester, Method: a.Method, Token: "key", EventID: "request-event", Fingerprint: "request-hash"}
	_, fresh, err := r.ClaimPackageRequest(t.Context(), c)
	require.NoError(t, err)
	require.True(t, fresh)
	require.NoError(t, r.CompletePackageRequest(t.Context(), c.EventID))
	restarted := NewPgPackageControlPlaneRepository(pool)
	c.EventID = "resigned-event"
	replay, fresh, err := restarted.ClaimPackageRequest(t.Context(), c)
	require.NoError(t, err)
	require.False(t, fresh)
	require.True(t, replay.Completed)
	require.Equal(t, "request-event", replay.EventID)
	c.Fingerprint = "changed"
	_, _, err = restarted.ClaimPackageRequest(t.Context(), c)
	require.Error(t, err)
}
