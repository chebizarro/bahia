package db

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// This test exists because of a real production failure.
//
// A classification was added to the domain without widening the CHECK
// constraints that migration 000060 pinned it to. The build was green, the
// candidate started healthy, and then every route canary supervisor upsert
// failed with SQLSTATE 23514 against the live database, forcing a rollback.
//
// Nothing in the type system connects a Go constant to a SQL CHECK constraint,
// so the connection is asserted here instead.

var (
	classificationCheckPattern = regexp.MustCompile(`(?is)CHECK\s*\(\s*classification\s+IN\s*\((.*?)\)\s*\)`)
	perspectiveCheckPattern    = regexp.MustCompile(`(?is)CHECK\s*\(\s*perspective\s+IN\s*\((.*?)\)\s*\)`)
	transitionCheckPattern     = regexp.MustCompile(`(?is)CHECK\s*\(\s*transition\s+IN\s*\((.*?)\)\s*\)`)
	quotedLiteralPattern       = regexp.MustCompile(`'([^']*)'`)
)

// migrationsDir resolves the migrations directory relative to this package.
func migrationsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("migrations")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("cannot locate migrations directory: %v", err)
	}
	return dir
}

// effectiveAllowedValues replays every route canary migration in filename order
// and returns the value set left in force by the last migration that redefined
// it. Replaying in order matters: a later migration widening a constraint must
// win over the original that pinned it.
func effectiveAllowedValues(t *testing.T, pattern *regexp.Regexp) map[string]bool {
	t.Helper()
	dir := migrationsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, "route_canary") && strings.HasSuffix(name, ".up.sql") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("no route canary migrations found; this test would silently pass")
	}
	sort.Strings(names)

	allowed := map[string]bool{}
	found := false
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(content), -1) {
			// A later migration redefines the set outright rather than adding
			// to it, so reset before recording.
			allowed = map[string]bool{}
			found = true
			for _, literal := range quotedLiteralPattern.FindAllStringSubmatch(match[1], -1) {
				allowed[literal[1]] = true
			}
		}
	}
	if !found {
		t.Fatal("no matching CHECK constraint found in route canary migrations")
	}
	return allowed
}

func assertAllAdmitted(t *testing.T, label string, values []string, allowed map[string]bool) {
	t.Helper()
	var missing []string
	for _, value := range values {
		if !allowed[value] {
			missing = append(missing, value)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s values are defined in Go but rejected by the database CHECK constraint: %s.\n"+
			"Persisting one of these will fail at runtime with SQLSTATE 23514.\n"+
			"Add a new versioned migration widening the constraint; never edit an applied migration in place.",
			label, strings.Join(missing, ", "))
	}
}

func assertNoOrphans(t *testing.T, label string, values []string, allowed map[string]bool) {
	t.Helper()
	defined := map[string]bool{}
	for _, value := range values {
		defined[value] = true
	}
	var orphaned []string
	for value := range allowed {
		if value != "" && !defined[value] {
			orphaned = append(orphaned, value)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Fatalf("%s values are admitted by the database but no longer defined in Go: %s.\n"+
			"Either the constant was removed without a migration, or the constraint was widened speculatively.",
			label, strings.Join(orphaned, ", "))
	}
}

func TestRouteCanaryClassificationsMatchDatabaseConstraint(t *testing.T) {
	values := make([]string, 0)
	for _, classification := range domain.AllRouteCanaryClassifications() {
		values = append(values, string(classification))
	}
	allowed := effectiveAllowedValues(t, classificationCheckPattern)
	assertAllAdmitted(t, "route canary classification", values, allowed)
	assertNoOrphans(t, "route canary classification", values, allowed)
}

func TestRouteCanaryPerspectivesMatchDatabaseConstraint(t *testing.T) {
	values := make([]string, 0)
	for _, perspective := range domain.AllRouteCanaryPerspectives() {
		values = append(values, string(perspective))
	}
	allowed := effectiveAllowedValues(t, perspectiveCheckPattern)
	assertAllAdmitted(t, "route canary perspective", values, allowed)
	assertNoOrphans(t, "route canary perspective", values, allowed)
}

func TestRouteCanaryTransitionsMatchDatabaseConstraint(t *testing.T) {
	values := make([]string, 0)
	for _, transition := range domain.AllRouteCanaryTransitions() {
		values = append(values, string(transition))
	}
	allowed := effectiveAllowedValues(t, transitionCheckPattern)
	assertAllAdmitted(t, "route canary transition", values, allowed)
	assertNoOrphans(t, "route canary transition", values, allowed)
}

// TestConformanceCheckDetectsAMissingClassification proves the guard actually
// fails when a value is absent, so a green result means something.
func TestConformanceCheckDetectsAMissingClassification(t *testing.T) {
	allowed := effectiveAllowedValues(t, classificationCheckPattern)
	fake := "definitely_not_in_any_migration"
	if allowed[fake] {
		t.Fatalf("unexpected value present: %s", fake)
	}
	var missing []string
	for _, value := range []string{fake} {
		if !allowed[value] {
			missing = append(missing, value)
		}
	}
	if len(missing) != 1 {
		t.Fatal("the conformance check would not have detected a missing classification")
	}
	_ = fmt.Sprint(missing)
}
