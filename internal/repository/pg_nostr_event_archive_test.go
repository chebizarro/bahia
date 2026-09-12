package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnlineIndexStatementsAreConcurrent(t *testing.T) {
	statements := NostrEventArchiveOnlineIndexStatements()
	require.Len(t, statements, 5)
	for _, statement := range statements {
		require.Contains(t, statement, "CREATE INDEX CONCURRENTLY IF NOT EXISTS")
		require.NotContains(t, strings.ToUpper(statement), "DROP ")
	}
	require.Contains(t, statements[0], "publish_state <> 'pending'")
}
