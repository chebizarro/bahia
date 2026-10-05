package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Audit B-31/B-32: payments and security are canonical cp-state domains, so a
// daemon with no reachable Postgres must still wire them (publish first, read
// from the local event store) and say so in its health report. A missing
// database must neither fail the checks nor mark the domains unavailable.
func TestDBLessDaemonWiresPaymentsAndSecurityCanonicalFirst(t *testing.T) {
	app, err := New(unreachableDatabaseConfig(t, "full"))
	require.NoError(t, err, "the daemon must boot without Postgres")
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	require.Nil(t, app.DB)

	checks := map[string]HealthCheck{}
	for _, check := range app.Health.Readiness().Checks {
		checks[check.Name] = check
	}

	payments, ok := checks["payments"]
	require.True(t, ok, "the payment service must report its wiring")
	require.Equal(t, HealthStatusPass, payments.Status)
	require.Equal(t, "false", payments.Details["sql_index"])
	require.NotEqual(t, "unavailable", payments.Details["availability"], "payments must be available without a database: %s", payments.Message)

	security, ok := checks["security_scanner"]
	require.True(t, ok, "the security scanner must report its wiring")
	require.Equal(t, HealthStatusPass, security.Status)
	require.Equal(t, "false", security.Details["sql_index"])
}
