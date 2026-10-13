package db

import (
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestAvailableMigrationsUseFullStem(t *testing.T) {
	files := fstest.MapFS{
		"migrations/000050_alpha.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1")},
		"migrations/000050_beta.up.sql":    &fstest.MapFile{Data: []byte("SELECT 2")},
		"migrations/000050_alpha.down.sql": &fstest.MapFile{Data: []byte("SELECT 3")},
	}
	versions, err := availableMigrations(files)
	require.NoError(t, err)
	require.Equal(t, []string{"000050_alpha", "000050_beta"}, versions)
}

// Historical duplicate prefixes have distinct full-stem database identities.
// Keep these applied names intact, but reject any new collision in CI.
func TestNoNewDuplicateMigrationNumericPrefixes(t *testing.T) {
	legacy := map[string][]string{
		"000020": {"000020_artifact_signature_status", "000020_tool_provisioning"},
		"000050": {"000050_backup_operation_checkpoints", "000050_contextvm_responses", "000050_nostr_publish_outbox", "000050_usage_ledger"},
		"000051": {"000051_deployment_execution_claims", "000051_hiveci_pstf_gate"},
		"000055": {"000055_dns_zone_mesh_visibility", "000055_hiveci_accepted_releases", "000055_managed_instance_health"},
		"000056": {"000056_hiveci_release_registration_evidence", "000056_managed_instance_recovery_pending"},
		"000060": {"000060_contextvm_response_fingerprint", "000060_route_canary"},
		"000062": {"000062_budget_policies", "000062_nostr_event_archive_lifecycle"},
	}
	versions, err := availableMigrations(migrationsFS)
	require.NoError(t, err)
	byPrefix := make(map[string][]string)
	for _, version := range versions {
		parts := strings.SplitN(version, "_", 2)
		require.Len(t, parts, 2, "migration must have a numeric prefix and descriptive stem: %s", version)
		require.Len(t, parts[0], 6, "migration prefix must be six digits: %s", version)
		_, err := strconv.Atoi(parts[0])
		require.NoError(t, err, "migration prefix must be numeric: %s", version)
		byPrefix[parts[0]] = append(byPrefix[parts[0]], version)
	}
	for prefix, stems := range byPrefix {
		if len(stems) > 1 {
			require.Equal(t, legacy[prefix], stems, "new duplicate migration prefix %s", prefix)
		}
	}
}
