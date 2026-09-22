package reconcile

import (
	"context"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPlaneRecurringDriftAndDisabledConvergence(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "enabled"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			r, repo, client, run := planeReconcileFixture(t)
			if disabled {
				run.plane.Desired.State = domain.ExecutionPlaneDisabled
				repo.plane.Desired.State = domain.ExecutionPlaneDisabled
			}
			require.NoError(t, r.rotate(t.Context(), run))
			drift := client.state
			drift.SessionID = run.session
			drift.PackageDigest = "sha256:" + strings.Repeat("d", 64)
			require.NoError(t, r.accept(t.Context(), run, drift))
			require.Len(t, client.applyRequests, 1)
			first := client.applyRequests[0].OperationID
			drift.Sequence++
			require.NoError(t, r.accept(t.Context(), run, drift))
			require.Len(t, client.applyRequests, 1, "coalesce in-flight repair")
			matching := client.state
			matching.SessionID, matching.Sequence = run.session, drift.Sequence+1
			matching.State = run.plane.Desired.State
			require.NoError(t, r.accept(t.Context(), run, matching))
			drift.Sequence = matching.Sequence + 10
			require.NoError(t, r.accept(t.Context(), run, drift))
			require.Len(t, client.applyRequests, 2, "same-generation drift must repair again")
			require.NotEqual(t, first, client.applyRequests[1].OperationID, "new drift needs a new remote idempotency key")
			require.Empty(t, run.verified.Capabilities)
		})
	}
}

func TestPlaneProbeTimeoutRetractsAndFreshEvidenceRecovers(t *testing.T) {
	r, _, client, run := planeReconcileFixture(t)
	verifyPlaneFixture(t, r, client, run)
	client.probeError = context.DeadlineExceeded
	require.ErrorIs(t, r.probe(t.Context(), run, client.state.Sequence), context.DeadlineExceeded)
	require.Empty(t, run.verified.Capabilities)
	require.Nil(t, run.confirmed)
	client.probeError = nil
	require.NoError(t, r.probe(t.Context(), run, client.state.Sequence))
	require.Empty(t, run.verified.Capabilities)
	require.NoError(t, r.accept(t.Context(), run, client.state))
	require.Len(t, run.verified.Capabilities, 1)
}
