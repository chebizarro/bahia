package db

import (
	"strings"
	"testing"
)

func TestEmbeddedRuntimeMigrationsContainReleaseRegistrationAndPromotionSchema(t *testing.T) {
	tests := []struct {
		file     string
		required []string
	}{
		{
			file: "migrations/000056_hiveci_release_registration_evidence.up.sql",
			required: []string{
				"policy_snapshot JSONB NOT NULL",
				"workflow_run_signed_event JSONB NOT NULL",
				"worker_admission_evidence JSONB NOT NULL",
				"rollback_compatibility JSONB NOT NULL",
				"health_readiness_contracts JSONB NOT NULL",
			},
		},
		{
			file: "migrations/000057_release_promotion_idempotency.up.sql",
			required: []string{
				"CREATE UNIQUE INDEX deployment_intents_release_promotion_idempotency_uq",
				"(metadata->>'promotion_requester')",
				"(metadata->>'promotion_idempotency_key')",
				"WHERE metadata->>'release_promotion' = 'true'",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			sql, err := migrationsFS.ReadFile(test.file)
			if err != nil {
				t.Fatalf("runtime migration is not embedded: %v", err)
			}
			for _, required := range test.required {
				if !strings.Contains(string(sql), required) {
					t.Errorf("%s missing %q", test.file, required)
				}
			}
		})
	}
}
