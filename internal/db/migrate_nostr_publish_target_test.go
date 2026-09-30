package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Postgres round trip is TestNostrPublishTargetMigrationRoundTrip
// (integration tag); this guards the shape without a database.
func TestNostrPublishTargetMigrationShape(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/000071_nostr_publish_target.up.sql")
	require.NoError(t, err)
	upText := strings.ToUpper(string(up))
	require.Contains(t, upText, "ADD COLUMN IF NOT EXISTS PUBLISH_TARGET TEXT NOT NULL DEFAULT ''")
	require.Contains(t, upText, "'NOT_APPLICABLE', 'PENDING', 'PUBLISHED', 'FAILED'")
	require.Contains(t, upText, "ON NOSTR_EVENTS(PUBLISH_TARGET, RECEIVED_AT, ID)")
	require.NotContains(t, upText, "DELETE FROM NOSTR_EVENTS")

	down, err := migrationsFS.ReadFile("migrations/000071_nostr_publish_target.down.sql")
	require.NoError(t, err)
	downText := strings.ToUpper(string(down))
	require.Contains(t, downText, "DROP COLUMN IF EXISTS PUBLISH_TARGET")
	require.Contains(t, downText, "CHECK (PUBLISH_STATE IN ('NOT_APPLICABLE', 'PENDING', 'PUBLISHED'))")
	require.Contains(t, downText, "ON NOSTR_EVENTS(RECEIVED_AT, ID)")
	require.NotContains(t, downText, "DELETE FROM NOSTR_EVENTS")
}
