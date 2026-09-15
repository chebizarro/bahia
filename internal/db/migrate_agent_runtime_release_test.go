package db

import (
	"strings"
	"testing"
)

func TestAgentRuntimeReleaseMigrationUpgradeAndDowngradeAreEmbedded(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/000063_agent_runtime_releases.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile("migrations/000063_agent_runtime_releases.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"CREATE TABLE agent_runtime_sources", "CREATE TABLE agent_runtime_releases", "CREATE TABLE agent_service_runtime_release_bindings", "previous_binding_id", "UNIQUE (org_id, agent_id, service_id, release_channel, release_id)"} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("upgrade missing %q", required)
		}
	}
	last := -1
	for _, required := range []string{"DROP TABLE IF EXISTS agent_service_runtime_release_bindings", "DROP TABLE IF EXISTS agent_runtime_releases", "DROP TABLE IF EXISTS agent_runtime_sources"} {
		index := strings.Index(string(down), required)
		if index < 0 {
			t.Fatalf("downgrade missing %q", required)
		}
		if index <= last {
			t.Fatalf("downgrade dependency order is unsafe at %q", required)
		}
		last = index
	}
}

// TestAgentRuntimeReleaseBindingEventIdempotencyMigrationIsEmbedded verifies
// migration 000064 replaces the erroneous release-level uniqueness with
// event-based idempotency plus concurrent linear-history enforcement.
func TestAgentRuntimeReleaseBindingEventIdempotencyMigrationIsEmbedded(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/000064_agent_runtime_release_binding_event_idempotency.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile("migrations/000064_agent_runtime_release_binding_event_idempotency.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ARRAY['org_id','agent_id','service_id','release_channel','release_id']",
		"DROP CONSTRAINT",
		"agent_service_runtime_release_bindings_chain_head_idx",
		"agent_service_runtime_release_bindings_chain_next_idx",
		"WHERE previous_binding_id IS NULL",
		"WHERE previous_binding_id IS NOT NULL",
	} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("upgrade missing %q", required)
		}
	}
	last := -1
	for _, required := range []string{
		"DROP INDEX IF EXISTS agent_service_runtime_release_bindings_chain_next_idx",
		"DROP INDEX IF EXISTS agent_service_runtime_release_bindings_chain_head_idx",
		"ADD CONSTRAINT agent_service_runtime_release_bindings_release_uniq_key",
		"UNIQUE (org_id, agent_id, service_id, release_channel, release_id)",
	} {
		index := strings.Index(string(down), required)
		if index < 0 {
			t.Fatalf("downgrade missing %q", required)
		}
		if index <= last {
			t.Fatalf("downgrade dependency order is unsafe at %q", required)
		}
		last = index
	}
}
