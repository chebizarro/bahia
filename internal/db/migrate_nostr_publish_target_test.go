package db

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// migrationStatements returns the SQL statements of an embedded migration,
// upper-cased with comments stripped and whitespace collapsed.
func migrationStatements(t *testing.T, name string) []string {
	t.Helper()
	data, err := migrationsFS.ReadFile("migrations/" + name)
	require.NoError(t, err)
	var body strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		body.WriteString(line)
		body.WriteString(" ")
	}
	var statements []string
	for _, stmt := range strings.Split(body.String(), ";") {
		stmt = strings.Join(strings.Fields(strings.ToUpper(stmt)), " ")
		if stmt != "" {
			statements = append(statements, stmt)
		}
	}
	return statements
}

var pendingOnly = regexp.MustCompile(`\bWHERE\b.*\bPUBLISH_STATE = 'PENDING'`)

// nostr_events is ~19GB in production and 000071 runs at startup, so it must
// stay metadata-only (docs/designs/nostr-event-store-lifecycle.md): constraints
// NOT VALID, no index builds (those are CONCURRENTLY and out of band), no
// column rewrites, and no UPDATE outside the pending outbox served by the
// pending partial index. The Postgres round trip is
// TestNostrPublishTargetMigrationRoundTrip (integration tag).
func TestNostrPublishTargetMigrationIsStartupSafe(t *testing.T) {
	check := func(t *testing.T, statements []string) {
		t.Helper()
		for _, stmt := range statements {
			if strings.Contains(stmt, "ADD CONSTRAINT") {
				require.Contains(t, stmt, "NOT VALID", "constraint must not validate the table at startup: %s", stmt)
			}
			require.NotRegexp(t, `\b(CREATE|DROP)( UNIQUE)? INDEX\b|\bREINDEX\b`, stmt, "no index build or drop on nostr_events in this migration: %s", stmt)
			require.NotContains(t, stmt, "ALTER COLUMN", "no column rewrite or NOT NULL scan: %s", stmt)
			require.NotContains(t, stmt, "DELETE FROM", stmt)
			require.NotContains(t, stmt, "VALIDATE CONSTRAINT", "validation belongs to the online maintenance command: %s", stmt)
			if strings.HasPrefix(stmt, "UPDATE ") {
				require.Contains(t, stmt, " WHERE ", "unscoped UPDATE: %s", stmt)
			}
		}
	}

	up := migrationStatements(t, "000071_nostr_publish_target.up.sql")
	check(t, up)
	var sawColumn, sawConstraint bool
	for _, stmt := range up {
		if strings.HasPrefix(stmt, "UPDATE ") {
			require.Regexp(t, pendingOnly, stmt, "startup UPDATEs must be confined to the pending outbox: %s", stmt)
		}
		if strings.Contains(stmt, "ADD COLUMN") {
			require.Contains(t, stmt, "ADD COLUMN IF NOT EXISTS PUBLISH_TARGET TEXT NOT NULL DEFAULT ''", "constant default keeps ADD COLUMN metadata-only")
			sawColumn = true
		}
		if strings.Contains(stmt, "ADD CONSTRAINT NOSTR_EVENTS_PUBLISH_STATE_CHECK") {
			require.Contains(t, stmt, "'NOT_APPLICABLE', 'PENDING', 'PUBLISHED', 'FAILED'")
			sawConstraint = true
		}
	}
	require.True(t, sawColumn)
	require.True(t, sawConstraint)

	down := migrationStatements(t, "000071_nostr_publish_target.down.sql")
	check(t, down)
	firstAlter := -1
	for i, stmt := range down {
		if strings.HasPrefix(stmt, "ALTER TABLE") && firstAlter < 0 {
			firstAlter = i
		}
		if strings.HasPrefix(stmt, "UPDATE ") {
			require.Contains(t, stmt, "WHERE PUBLISH_STATE = ", "rollback UPDATEs are confined to one publish state: %s", stmt)
			require.True(t, firstAlter < 0, "row updates must run before ALTER TABLE takes ACCESS EXCLUSIVE: %s", stmt)
		}
	}
	require.Contains(t, strings.Join(down, ";"), "CHECK (PUBLISH_STATE IN ('NOT_APPLICABLE', 'PENDING', 'PUBLISHED')) NOT VALID")
	require.Contains(t, strings.Join(down, ";"), "DROP COLUMN IF EXISTS PUBLISH_TARGET")
}
