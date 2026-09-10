package db

import (
	"strings"
	"testing"
)

func TestContextVMResponseFingerprintMigrationIsEmbedded(t *testing.T) {
	tests := []struct {
		file     string
		required []string
	}{
		{
			file: "migrations/000060_contextvm_response_fingerprint.up.sql",
			required: []string{
				"ADD COLUMN request_fingerprint TEXT NULL",
				"request_fingerprint IS NULL",
			},
		},
		{
			file:     "migrations/000060_contextvm_response_fingerprint.down.sql",
			required: []string{"DROP COLUMN request_fingerprint"},
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
					t.Fatalf("%s does not contain %q", test.file, required)
				}
			}
		})
	}
}
