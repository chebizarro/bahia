package db

import (
	"os"
	"strings"
	"testing"
)

func TestHiveCIPolicySeedReconcilesCanonicalReleaseWorkflow(t *testing.T) {
	script, err := os.ReadFile("../../scripts/seed_hiveci_pipeline_policy.sql")
	if err != nil {
		t.Fatalf("read Hive-CI policy seed script: %v", err)
	}
	sql := string(script)
	for _, required := range []string{
		"WITH resolved_target AS",
		"reconciled AS (",
		"UPDATE hiveci_pipeline_policies p",
		"metadata = target.metadata",
		"p.workflow_path = '.gitea/workflows/release.yml'",
		"COALESCE(p.branch_pattern, '') = ''",
		"WHERE NOT EXISTS (SELECT 1 FROM reconciled)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("seed script is missing reconciliation clause %q", required)
		}
	}
	if strings.Contains(sql, ".github/workflows/hive-ci-build.yml") {
		t.Fatal("seed script still checks the stale workflow path")
	}
	if got := strings.Count(sql, "'.gitea/workflows/release.yml'"); got < 3 {
		t.Fatalf("canonical release workflow occurrences=%d, want update/insert/guard coverage", got)
	}
}
