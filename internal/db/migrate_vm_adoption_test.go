package db

import (
	"strings"
	"testing"
)

func TestMeasuredAdoptionRollbackFencesBeforeCheckingEvidence(t *testing.T) {
	data, err := migrationsFS.ReadFile("migrations/000067_vm_measured_adoption.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	lock, check, drop := strings.Index(sql, "LOCK TABLE"), strings.Index(sql, "IF EXISTS"), strings.Index(sql, "DROP TABLE")
	if lock < 0 || check < lock || drop < check {
		t.Fatal("rollback must lock before checking before dropping")
	}
	for _, table := range []string{"vm_adoption_storage", "vm_operation_approvals", "vm_operations", "persistent_vm_deployments"} {
		if !strings.Contains(sql[lock:check], table) {
			t.Fatalf("missing admission fence for %s", table)
		}
	}
}
