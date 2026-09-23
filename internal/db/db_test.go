package db

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestDBConfigDSNRoundTripsThroughPGX(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			cfg := config.DBConfig{
				Host: host, Port: 5432, User: "bahia+test@example.org",
				Password: `space +:@/?#%\'`, Name: "wave3+test", SSLMode: "disable",
			}
			parsed, err := pgxpool.ParseConfig(cfg.DSN())
			if err != nil {
				t.Fatalf("parse generated DSN: %v", cfg.RedactError(err))
			}
			conn := parsed.ConnConfig
			if conn.Host != strings.Trim(cfg.Host, "[]") || int(conn.Port) != cfg.Port || conn.User != cfg.User || conn.Password != cfg.Password || conn.Database != cfg.Name {
				t.Fatal("pgx changed a generated DSN field during parsing")
			}
			if conn.TLSConfig != nil {
				t.Fatal("sslmode=disable unexpectedly enabled TLS")
			}
		})
	}
}

func TestConnectRedactsCredentialRepresentationsFromParseErrors(t *testing.T) {
	const secret = `DB-SECRET-SENTINEL:/@?&%\"<>`
	cfg := config.DBConfig{
		Host: "db.internal", Port: 5432, User: "bahia", Password: secret,
		Name: "bahia", SSLMode: "require",
	}
	originalParse := parsePoolConfig
	parsePoolConfig = func(dsn string) (*pgxpool.Config, error) {
		return nil, fmt.Errorf(
			"invalid database endpoint raw=%s query=%s path=%s userinfo=%s html=%s base64=%s dsn=%s",
			secret,
			url.QueryEscape(secret),
			url.PathEscape(secret),
			strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:"),
			html.EscapeString(secret),
			base64.StdEncoding.EncodeToString([]byte(secret)),
			dsn,
		)
	}
	t.Cleanup(func() { parsePoolConfig = originalParse })

	_, err := Connect(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("Connect() error = nil, want parse failure")
	}
	message := err.Error()
	for _, variant := range []string{
		secret,
		url.QueryEscape(secret),
		url.PathEscape(secret),
		strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:"),
		html.EscapeString(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
	} {
		if strings.Contains(message, variant) {
			t.Fatalf("Connect() error leaked credential representation %q: %s", variant, message)
		}
	}
	if !strings.Contains(message, "invalid database endpoint") || !strings.Contains(message, "db.internal") {
		t.Fatalf("Connect() error lost useful diagnostics: %s", message)
	}
}
