package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestConnectOptionalDatabaseRedactsCredentialRepresentationsFromLogs(t *testing.T) {
	const secret = `LOG-SECRET-SENTINEL:/@?&%\"<>`
	cfg := &config.Config{DB: config.DBConfig{
		Host: "db.internal", Port: 5432, User: "bahia", Password: secret,
		Name: "bahia", SSLMode: "require",
	}}
	originalConnect := dbConnect
	dbConnect = func(context.Context, config.DBConfig, *zap.Logger) (*pgxpool.Pool, error) {
		return nil, fmt.Errorf("dial db.internal failed raw=%s encoded=%s", secret, url.QueryEscape(secret))
	}
	t.Cleanup(func() { dbConnect = originalConnect })
	core, observed := observer.New(zap.WarnLevel)

	pool, available := connectOptionalDatabase(context.Background(), cfg, zap.New(core), nil)
	if pool != nil || available {
		t.Fatalf("connectOptionalDatabase() = (%v, %v), want unavailable", pool, available)
	}
	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("observed log entries = %d, want 1", len(entries))
	}
	contextFields := entries[0].ContextMap()
	errorText, _ := contextFields["error"].(string)
	for _, variant := range []string{secret, url.QueryEscape(secret)} {
		if strings.Contains(errorText, variant) {
			t.Fatalf("database warning leaked credential representation %q: %s", variant, errorText)
		}
	}
	if !strings.Contains(errorText, "dial db.internal failed") {
		t.Fatalf("database warning lost useful diagnostics: %s", errorText)
	}
}
