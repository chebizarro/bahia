package migrations_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCorrectiveOrgBackfillPopulatesOrgID(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping migration integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire postgres connection: %v", err)
	}
	defer conn.Release()

	schema := "test_org_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SET search_path TO public")
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search path: %v", err)
	}

	_, err = conn.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE services (id UUID PRIMARY KEY, org_id UUID);
		CREATE TABLE environments (id UUID PRIMARY KEY, org_id UUID);
	`)
	if err != nil {
		t.Fatalf("create upgrade fixture: %v", err)
	}

	orgID := uuid.New()
	serviceID := uuid.New()
	environmentID := uuid.New()
	for statement, args := range map[string][]any{
		"INSERT INTO organizations (id) VALUES ($1)":              {orgID},
		"INSERT INTO services (id, org_id) VALUES ($1, NULL)":     {serviceID},
		"INSERT INTO environments (id, org_id) VALUES ($1, NULL)": {environmentID},
	} {
		if _, err := conn.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed upgrade fixture: %v", err)
		}
	}

	migrationSQL, err := os.ReadFile("000045_core_resource_org_backfill.up.sql")
	if err != nil {
		t.Fatalf("read corrective migration: %v", err)
	}
	if _, err := conn.Exec(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("execute corrective migration: %v", err)
	}

	for table, id := range map[string]uuid.UUID{
		"services":     serviceID,
		"environments": environmentID,
	} {
		var got uuid.UUID
		query := fmt.Sprintf("SELECT org_id FROM %s WHERE id = $1", table)
		if err := conn.QueryRow(ctx, query, id).Scan(&got); err != nil {
			t.Fatalf("read %s org_id: %v", table, err)
		}
		if got != orgID {
			t.Fatalf("%s org_id = %s, want %s", table, got, orgID)
		}
	}
}
