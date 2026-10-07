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

var indexBuildOrDrop = regexp.MustCompile(`\b(CREATE|DROP)( UNIQUE)? INDEX\b|\bREINDEX\b`)

// requireStartupSafe enforces the startup-migration rules shared by 000071 and
// 000072 (docs/architecture/postgres-event-store-lifecycle.md): constraints NOT VALID,
// no index builds (those are CONCURRENTLY and out of band), no column
// rewrites, no deletes, no validation (the online maintenance command does
// it) and no unscoped UPDATE. droppableIndexes names retired indexes a
// migration may drop (a metadata-only DROP INDEX IF EXISTS, never a build).
func requireStartupSafe(t *testing.T, statements []string, droppableIndexes ...string) {
	t.Helper()
	for _, stmt := range statements {
		if strings.Contains(stmt, "ADD CONSTRAINT") {
			require.Contains(t, stmt, "NOT VALID", "constraint must not validate the table at startup: %s", stmt)
		}
		if indexBuildOrDrop.MatchString(stmt) {
			allowed := false
			for _, index := range droppableIndexes {
				allowed = allowed || stmt == "DROP INDEX IF EXISTS "+strings.ToUpper(index)
			}
			require.True(t, allowed, "no index build (or unlisted drop) in a startup migration: %s", stmt)
		}
		require.NotContains(t, stmt, "CONCURRENTLY", "concurrent index DDL cannot run in the migration transaction: %s", stmt)
		require.NotContains(t, stmt, "ALTER COLUMN", "no column rewrite or NOT NULL scan: %s", stmt)
		require.NotContains(t, stmt, "DELETE FROM", stmt)
		require.NotContains(t, stmt, "VALIDATE CONSTRAINT", "validation belongs to the online maintenance command: %s", stmt)
		if strings.HasPrefix(stmt, "UPDATE ") {
			require.Contains(t, stmt, " WHERE ", "unscoped UPDATE: %s", stmt)
		}
	}
}

// nostr_events is ~19GB in production and 000071 runs at startup, so it must
// stay metadata-only (docs/architecture/postgres-event-store-lifecycle.md): constraints
// NOT VALID, no index builds (those are CONCURRENTLY and out of band), no
// column rewrites, and no UPDATE outside the pending outbox served by the
// pending partial index. The Postgres round trip is
// TestNostrPublishTargetMigrationRoundTrip (integration tag).
func TestNostrPublishTargetMigrationIsStartupSafe(t *testing.T) {
	check := func(t *testing.T, statements []string) {
		t.Helper()
		requireStartupSafe(t, statements)
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

// 000072 retires the Security failed_retryable publish state at startup with
// the same rules: the narrowed checks are NOT VALID (ensure-indexes converts
// leftover security_scan_runs rows and validates both out of band), the only
// UPDATE touches the retired publication rows through the retry partial index
// before that index is dropped, and nothing is built. The Postgres round trip
// is TestSecurityRetireFailedRetryableMigrationRoundTrip (integration tag).
func TestSecurityRetireFailedRetryableMigrationIsStartupSafe(t *testing.T) {
	const retryIndex = "idx_security_observable_publications_retry"
	up := migrationStatements(t, "000072_security_retire_failed_retryable.up.sql")
	requireStartupSafe(t, up, retryIndex)
	var sawUpdate, sawDrop bool
	constraints := map[string]bool{}
	for i, stmt := range up {
		require.NotContains(t, stmt, "NOSTR_EVENTS", "000072 must not touch nostr_events")
		require.NotContains(t, stmt, "FAILED_RETRYABLE', 'FAILED_TERMINAL", "the narrowed check must not keep failed_retryable: %s", stmt)
		switch {
		case strings.HasPrefix(stmt, "UPDATE "):
			require.True(t, strings.HasPrefix(stmt, "UPDATE SECURITY_OBSERVABLE_PUBLICATIONS "), "security_scan_runs has no publish_state index; convert it out of band: %s", stmt)
			require.True(t, strings.HasSuffix(stmt, "WHERE PUBLISH_STATE = 'FAILED_RETRYABLE'"), "the UPDATE must match the retry index predicate exactly: %s", stmt)
			require.False(t, sawDrop, "the retired rows must be converted while the retry index still serves them")
			sawUpdate = true
		case strings.HasPrefix(stmt, "DROP INDEX"):
			sawDrop = true
		case strings.Contains(stmt, "ADD CONSTRAINT"):
			require.Contains(t, stmt, "CHECK (PUBLISH_STATE IN ('PENDING', 'PUBLISHED', 'FAILED_TERMINAL')) NOT VALID")
			for _, name := range []string{"SECURITY_OBSERVABLE_PUBLICATIONS_PUBLISH_STATE_CHECK", "SECURITY_SCAN_RUNS_PUBLISH_STATE_CHECK"} {
				if strings.Contains(stmt, "ADD CONSTRAINT "+name+" ") {
					require.Contains(t, up[i-1], "DROP CONSTRAINT IF EXISTS "+name, "each narrowed check replaces the 000044 one")
					constraints[name] = true
				}
			}
		}
	}
	require.True(t, sawUpdate)
	require.True(t, sawDrop)
	require.Len(t, constraints, 2)

	down := migrationStatements(t, "000072_security_retire_failed_retryable.down.sql")
	requireStartupSafe(t, down)
	joined := strings.Join(down, ";")
	require.Equal(t, 2, strings.Count(joined, "CHECK (PUBLISH_STATE IN ('PENDING', 'PUBLISHED', 'FAILED_RETRYABLE', 'FAILED_TERMINAL')) NOT VALID"))
	for _, stmt := range down {
		require.False(t, strings.HasPrefix(stmt, "UPDATE "), "the rollback only widens the checks: %s", stmt)
	}
}
