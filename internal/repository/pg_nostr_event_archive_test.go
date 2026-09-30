package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnlineIndexStatementsAreConcurrent(t *testing.T) {
	statements := NostrEventArchiveOnlineIndexStatements()
	require.Len(t, statements, 6)
	for _, statement := range statements {
		require.Contains(t, statement, "CREATE INDEX CONCURRENTLY IF NOT EXISTS")
		require.NotContains(t, strings.ToUpper(statement), "DROP ")
	}
	require.Contains(t, statements[0], "publish_state <> 'pending'")
	require.Contains(t, statements[5], nostrPublishFailedIndex)
	require.Contains(t, statements[5], "WHERE publish_state = 'failed'")
	// Archival readiness does not wait on the metrics-only failed-row index.
	require.Len(t, nostrEventArchiveRequiredIndexes, 5)
	require.NotContains(t, nostrEventArchiveRequiredIndexes, nostrPublishFailedIndex)
}
