package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNostrEventArchiveMigrationIsMetadataOnly(t *testing.T) {
	data, err := migrationsFS.ReadFile("migrations/000062_nostr_event_archive_lifecycle.up.sql")
	require.NoError(t, err)
	text := strings.ToUpper(string(data))
	require.NotContains(t, text, "CREATE INDEX")
	require.NotContains(t, text, "UPDATE NOSTR_EVENTS")
	require.NotContains(t, text, "DELETE FROM NOSTR_EVENTS")
	require.Contains(t, text, "ADD COLUMN IF NOT EXISTS ARCHIVE_BATCH_ID")
	require.Contains(t, text, "NOT VALID")
}
