package db

import (
	"strings"
	"testing"
)

func TestEmbeddedDNSZoneAuthoritativeMigrationUpAndDown(t *testing.T) {
	tests := []struct {
		file     string
		required []string
	}{
		{file: "migrations/000058_dns_zone_authoritative.up.sql", required: []string{"ADD COLUMN authoritative BOOLEAN NOT NULL DEFAULT FALSE"}},
		{file: "migrations/000058_dns_zone_authoritative.down.sql", required: []string{"DROP COLUMN authoritative"}},
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
